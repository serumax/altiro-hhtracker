package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// TrackingInput contains event metadata only, never prompt text. ConfirmedHuman
// and Root are adapter assertions, not authentication of a tool caller.
type TrackingInput struct {
	Action         string `json:"action"`
	Project        string `json:"project"`
	Cwd            string `json:"cwd"`
	Client         string `json:"client"`
	Chat           string `json:"chat"`
	EventID        string `json:"event_id"`
	EventType      string `json:"event_type"`
	ConfirmedHuman bool   `json:"confirmed_human"`
	Root           bool   `json:"root"`
	ClaimToken     string `json:"claim_token,omitempty"`
}

// TrackingResult shares project context, not a human session or chat-specific
// timer. TaskIDs are existing open entries; checking never opens or closes one.
type TrackingResult struct {
	ContextID      string  `json:"context_id"`
	Project        string  `json:"project"`
	Initialized    bool    `json:"initialized"`
	Pending        string  `json:"pending,omitempty"`
	Ask            bool    `json:"ask"`
	ClaimToken     string  `json:"claim_token,omitempty"`
	ClaimExpiresAt int64   `json:"claim_expires_at,omitempty"`
	LastHumanAt    int64   `json:"last_human_at,omitempty"`
	Replay         bool    `json:"replay"`
	TaskIDs        []int64 `json:"task_ids"`
}

const trackingClaimTTL = 5 * time.Minute

// trackProject is shared by MCP and local client event adapters. Only confirmed
// root human_prompt events advance the human clock or own questions. It never
// writes entries or responses. now is adapter-supplied receipt time, not bot time.
func trackProject(db *sql.DB, input TrackingInput, now time.Time, reminder time.Duration) (TrackingResult, error) {
	if input.Action == "" {
		input.Action = "check"
	}
	if input.Action != "check" && input.Action != "initialize" && input.Action != "resolve" {
		return TrackingResult{}, fmt.Errorf("action must be check, initialize or resolve")
	}
	if reminder <= 0 {
		return TrackingResult{}, fmt.Errorf("reminder must be positive")
	}
	for _, identity := range []string{input.Client, input.Chat, input.EventID} {
		if strings.TrimSpace(identity) == "" || len(identity) > 512 {
			return TrackingResult{}, fmt.Errorf("client, chat and event_id are required and bounded")
		}
	}
	if len(input.Project) > 256 || len(input.Cwd) > 4096 {
		return TrackingResult{}, fmt.Errorf("project or cwd exceeds metadata limit")
	}
	human := input.Root && input.ConfirmedHuman && input.EventType == "human_prompt"
	if input.Action != "check" && !human {
		return TrackingResult{}, fmt.Errorf("initialization and resolution require a confirmed root human event")
	}
	input.Project = strings.TrimSpace(input.Project)
	marker := false
	cwd := ""
	if input.Cwd != "" {
		var err error
		cwd, err = filepath.Abs(input.Cwd)
		if err != nil {
			return TrackingResult{}, err
		}
		if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
			cwd = resolved
		}
		if root, err := mainCheckoutRoot(cwd); err == nil {
			cwd = root
		}
		if file, ok := findProjectFile(cwd); ok && file.Proyecto != "" {
			input.Project = file.Proyecto
			marker = true
		}
	}
	if input.Project == "" && cwd == "" {
		return TrackingResult{}, fmt.Errorf("provide project or cwd to identify tracking context")
	}
	tx, err := db.Begin()
	if err != nil {
		return TrackingResult{}, err
	}
	defer tx.Rollback()
	// Take SQLite's writer lock before reading shared context. This prevents
	// concurrent processes from upgrading stale read snapshots during claims.
	if _, err := tx.Exec(`UPDATE tracking_contexts SET scope=scope WHERE 0`); err != nil {
		return TrackingResult{}, err
	}
	bound := ""
	if cwd != "" {
		err := tx.QueryRow(`SELECT scope FROM tracking_workspaces WHERE cwd=?`, cwd).Scan(&bound)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return TrackingResult{}, err
		}
	}
	scope := bound
	if input.Project != "" {
		scope = "project:" + input.Project
	}
	if scope == "" {
		scope = "workspace:" + cwd
	}
	if _, err := tx.Exec(`INSERT INTO tracking_contexts (scope,project) VALUES (?,?) ON CONFLICT(scope) DO NOTHING`, scope, input.Project); err != nil {
		return TrackingResult{}, err
	}
	state, err := readTrackingContext(tx, scope)
	if err != nil {
		return TrackingResult{}, err
	}
	// Claims from a provisional workspace may be used to confirm its canonical
	// project. An expired or displaced claim can never resolve a newer question.
	claimState := state
	if bound != "" && bound != scope {
		prior, err := readTrackingContext(tx, bound)
		if err != nil {
			return TrackingResult{}, err
		}
		if !prior.Initialized {
			claimState = prior
			if strings.HasPrefix(bound, "workspace:") && prior.LastHumanAt > state.LastHumanAt {
				state.LastHumanAt = prior.LastHumanAt
			}
		}
	}
	var recordedScope string
	err = tx.QueryRow(`SELECT scope FROM tracking_events WHERE client=? AND chat=? AND event_id=? AND action=?`, input.Client, input.Chat, input.EventID, input.Action).Scan(&recordedScope)
	replay := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return TrackingResult{}, err
	}
	if replay && recordedScope != scope {
		return TrackingResult{}, fmt.Errorf("event identity already belongs to another project")
	}
	owner := input.Client + "\x00" + input.Chat
	if !replay {
		if input.Action == "resolve" || (input.Action == "initialize" && (input.ClaimToken != "" || claimState.claimToken != "")) {
			if input.ClaimToken == "" || input.ClaimToken != claimState.claimToken || owner != claimState.claimOwner || now.UnixMilli() >= claimState.claimExpires {
				return TrackingResult{}, fmt.Errorf("question claim is missing, expired or belongs to another chat; check again")
			}
		}
		if input.Action == "initialize" {
			if input.Project == "" {
				return TrackingResult{}, fmt.Errorf("initialization requires a confirmed project name")
			}
			state.Initialized = true
			state.Pending = ""
			state.clearClaim()
		} else if input.Action == "resolve" {
			if !state.Initialized || state.Pending != "continuity" {
				return TrackingResult{}, fmt.Errorf("only initialized continuity questions can be resolved; initialize a missing project")
			}
			state.Pending = ""
			state.clearClaim()
		} else if marker {
			state.Initialized = true
			if state.Pending == "initialize" {
				state.Pending = ""
				state.clearClaim()
			}
		}
		if !state.Initialized {
			state.Pending = "initialize"
		}
		if human {
			if input.Action == "check" && state.Initialized && state.Pending == "" && state.LastHumanAt != 0 && now.UnixMilli()-state.LastHumanAt >= reminder.Milliseconds() {
				state.Pending = "continuity"
			}
			if now.UnixMilli() > state.LastHumanAt {
				state.LastHumanAt = now.UnixMilli()
			}
		}
		if _, err := tx.Exec(`INSERT INTO tracking_events (client,chat,event_id,action,scope) VALUES (?,?,?,?,?)`, input.Client, input.Chat, input.EventID, input.Action, scope); err != nil {
			return TrackingResult{}, err
		}
	}
	state.Replay = replay
	state.Ask = false
	if human && !replay && state.Pending != "" && now.UnixMilli() >= state.claimExpires {
		token := make([]byte, 16)
		if _, err := rand.Read(token); err != nil {
			return TrackingResult{}, err
		}
		state.claimToken = hex.EncodeToString(token)
		state.claimOwner = owner
		state.claimExpires = now.Add(trackingClaimTTL).UnixMilli()
		state.Ask = true
	}
	if _, err := tx.Exec(`UPDATE tracking_contexts SET initialized=?,last_human_at=?,pending=?,claim_owner=?,claim_token=?,claim_expires_at=? WHERE scope=?`, state.Initialized, nullableHumanTime(state.LastHumanAt), state.Pending, state.claimOwner, state.claimToken, state.claimExpires, scope); err != nil {
		return TrackingResult{}, err
	}
	if cwd != "" && (state.Initialized || bound == "") {
		if _, err := tx.Exec(`INSERT INTO tracking_workspaces (cwd,scope) VALUES (?,?) ON CONFLICT(cwd) DO UPDATE SET scope=excluded.scope`, cwd, scope); err != nil {
			return TrackingResult{}, err
		}
		if bound != "" && bound != scope && strings.HasPrefix(bound, "workspace:") {
			if _, err := tx.Exec(`UPDATE tracking_events SET scope=? WHERE scope=?`, scope, bound); err != nil {
				return TrackingResult{}, err
			}
			if _, err := tx.Exec(`DELETE FROM tracking_contexts WHERE scope=?`, bound); err != nil {
				return TrackingResult{}, err
			}
		}
	}
	state.ContextID = scope
	state.TaskIDs = []int64{}
	if state.Initialized {
		rows, err := tx.Query(`SELECT id FROM entries WHERE proyecto=? AND estado='abierto' ORDER BY id`, state.Project)
		if err != nil {
			return TrackingResult{}, err
		}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return TrackingResult{}, err
			}
			state.TaskIDs = append(state.TaskIDs, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return TrackingResult{}, err
		}
	}
	// The owning human/root chat can recover its token after a lost response or
	// restart, without asking again. Observers never receive another chat's token.
	if human && state.claimOwner == owner && now.UnixMilli() < state.claimExpires {
		state.ClaimToken = state.claimToken
		state.ClaimExpiresAt = state.claimExpires
	}
	if err := tx.Commit(); err != nil {
		return TrackingResult{}, err
	}
	return state.TrackingResult, nil
}

type trackingContext struct {
	TrackingResult
	claimOwner   string
	claimToken   string
	claimExpires int64
}

func (state *trackingContext) clearClaim() {
	state.claimOwner, state.claimToken, state.claimExpires = "", "", 0
}

func readTrackingContext(tx *sql.Tx, scope string) (trackingContext, error) {
	var state trackingContext
	var last sql.NullInt64
	err := tx.QueryRow(`SELECT project,initialized,last_human_at,pending,claim_owner,claim_token,claim_expires_at FROM tracking_contexts WHERE scope=?`, scope).Scan(&state.Project, &state.Initialized, &last, &state.Pending, &state.claimOwner, &state.claimToken, &state.claimExpires)
	if last.Valid {
		state.LastHumanAt = last.Int64
	}
	return state, err
}

func nullableHumanTime(stamp int64) any {
	if stamp == 0 {
		return nil
	}
	return stamp
}

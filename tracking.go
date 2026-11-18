package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
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
	// Worked is the user's answer to the continuity question: true keeps the
	// hours of the gap; false closes open entries at the last activity and
	// reopens them when the user came back. Required to resolve.
	Worked *bool `json:"worked,omitempty"`
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
		literalCwd := cwd
		if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
			cwd = resolved
		}
		if root, err := mainCheckoutRoot(cwd); err == nil {
			cwd = root
		}
		// Keep the canonical primary-root binding, but match metric resolution:
		// the primary marker wins, then the original literal worktree is searched.
		if content, err := os.ReadFile(filepath.Join(cwd, projectFileName)); err == nil {
			if file := parseProjectFile(content); file.Proyecto != "" {
				input.Project = file.Proyecto
				marker = true
			}
		}
		if !marker {
			if file, ok := findProjectFile(literalCwd); ok && file.Proyecto != "" {
				input.Project = file.Proyecto
				marker = true
			}
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
			// A workspace claim may bind a folder, not resolve an existing
			// project's continuity question owned by another chat.
			if !state.Initialized {
				state.Pending = ""
				state.clearClaim()
			}
			state.Initialized = true
		} else if input.Action == "resolve" {
			if !state.Initialized || state.Pending != "continuity" {
				return TrackingResult{}, fmt.Errorf("only initialized continuity questions can be resolved; initialize a missing project")
			}
			if input.Worked == nil {
				return TrackingResult{}, fmt.Errorf("resolve requires worked=true or worked=false from the user's answer")
			}
			if !*input.Worked && state.gapStarted != 0 && state.gapEnded != 0 {
				if err := splitOpenEntriesAtGap(tx, state.Project, time.UnixMilli(state.gapStarted), time.UnixMilli(state.gapEnded)); err != nil {
					return TrackingResult{}, fmt.Errorf("close the gap: %w", err)
				}
			}
			state.Pending = ""
			state.gapStarted, state.gapEnded = 0, 0
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
			// A gap only matters when hours are running: without an open entry
			// there is nothing to keep or close, so a new chat does not ask.
			if input.Action == "check" && state.Initialized && state.Pending == "" && state.LastHumanAt != 0 && now.UnixMilli()-state.LastHumanAt >= reminder.Milliseconds() {
				open, err := hasOpenEntries(tx, state.Project)
				if err != nil {
					return TrackingResult{}, err
				}
				if open {
					state.Pending = "continuity"
					state.gapStarted, state.gapEnded = state.LastHumanAt, now.UnixMilli()
				}
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
	if _, err := tx.Exec(`UPDATE tracking_contexts SET initialized=?,last_human_at=?,pending=?,claim_owner=?,claim_token=?,claim_expires_at=?,gap_started_at=?,gap_ended_at=? WHERE scope=?`, state.Initialized, nullableHumanTime(state.LastHumanAt), state.Pending, state.claimOwner, state.claimToken, state.claimExpires, nullableHumanTime(state.gapStarted), nullableHumanTime(state.gapEnded), scope); err != nil {
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
	// gapStarted is the last activity before a gap; gapEnded is when the user
	// came back. They are kept until the continuity question is answered.
	gapStarted int64
	gapEnded   int64
}

func hasOpenEntries(tx *sql.Tx, project string) (bool, error) {
	var count int
	err := tx.QueryRow(`SELECT count(*) FROM entries WHERE proyecto=? AND estado='abierto'`, project).Scan(&count)
	return count > 0, err
}

// splitOpenEntriesAtGap applies the user's "I did not work during the gap":
// each open entry of the project closes at the last activity and a new entry
// with the same task opens when the user came back.
func splitOpenEntriesAtGap(tx *sql.Tx, project string, gapStart, gapEnd time.Time) error {
	rows, err := tx.Query(`SELECT id, descripcion, inicio FROM entries WHERE proyecto=? AND estado='abierto' ORDER BY id`, project)
	if err != nil {
		return err
	}
	type open struct {
		id          int64
		descripcion string
		inicio      string
	}
	var entries []open
	for rows.Next() {
		var e open
		if err := rows.Scan(&e.id, &e.descripcion, &e.inicio); err != nil {
			rows.Close()
			return err
		}
		entries = append(entries, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	resumed := gapEnd.Local().Format(tsLayout)
	for _, e := range entries {
		start, err := parseTS(e.inicio)
		if err != nil {
			return fmt.Errorf("entry #%d: invalid start: %w", e.id, err)
		}
		// An entry opened inside the gap has no worked time before it: it
		// simply starts again when the user came back.
		if !start.Before(gapStart) {
			if _, err := tx.Exec(`UPDATE entries SET inicio=? WHERE id=? AND estado='abierto'`, resumed, e.id); err != nil {
				return err
			}
			continue
		}
		hours, err := elapsedHours(start, gapStart)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE entries SET estado='cerrado', fin=?, duracion_hh=?, resumen=? WHERE id=? AND estado='abierto'`,
			gapStart.Local().Format(tsLayout), hours, "Cerrado a la última actividad: el usuario confirmó que no trabajó durante la pausa.", e.id); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO entries (descripcion, proyecto, inicio, estado) VALUES (?, ?, ?, 'abierto')`, e.descripcion, project, resumed); err != nil {
			return err
		}
	}
	return nil
}

func (state *trackingContext) clearClaim() {
	state.claimOwner, state.claimToken, state.claimExpires = "", "", 0
}

func readTrackingContext(tx *sql.Tx, scope string) (trackingContext, error) {
	var state trackingContext
	var last, gapStarted, gapEnded sql.NullInt64
	err := tx.QueryRow(`SELECT project,initialized,last_human_at,pending,claim_owner,claim_token,claim_expires_at,gap_started_at,gap_ended_at FROM tracking_contexts WHERE scope=?`, scope).Scan(&state.Project, &state.Initialized, &last, &state.Pending, &state.claimOwner, &state.claimToken, &state.claimExpires, &gapStarted, &gapEnded)
	if last.Valid {
		state.LastHumanAt = last.Int64
	}
	state.gapStarted, state.gapEnded = gapStarted.Int64, gapEnded.Int64
	return state, err
}

func nullableHumanTime(stamp int64) any {
	if stamp == 0 {
		return nil
	}
	return stamp
}

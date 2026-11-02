package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func humanEvent(project, chat, event string) TrackingInput {
	return TrackingInput{Project: project, Client: "test", Chat: chat, EventID: event, EventType: "human_prompt", ConfirmedHuman: true, Root: true}
}

func trackAt(t *testing.T, db *sql.DB, input TrackingInput, now time.Time) TrackingResult {
	t.Helper()
	result, err := trackProject(db, input, now, 2*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func initializeAt(t *testing.T, db *sql.DB, project string, now time.Time) TrackingResult {
	t.Helper()
	input := humanEvent(project, "first-chat", "init-"+project)
	input.Action = "initialize"
	result := trackAt(t, db, input, now)
	if !result.Initialized || result.Pending != "" {
		t.Fatalf("initialization: %+v", result)
	}
	return result
}

func TestTrackingInitializedProjectSharedAcrossChats(t *testing.T) {
	db := openTestDB(t)
	now := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	initial := initializeAt(t, db, "project", now)
	id, _, _ := insertOpen(db, "task", "project")
	for i := 0; i < 4; i++ {
		result := trackAt(t, db, humanEvent("project", fmt.Sprintf("chat-%d", i), fmt.Sprintf("event-%d", i)), now.Add(time.Duration(i+1)*time.Minute))
		if result.ContextID != initial.ContextID || !result.Initialized || result.Ask || result.Pending != "" || len(result.TaskIDs) != 1 || result.TaskIDs[0] != id {
			t.Fatalf("chat repeats initialization or loses tasks: %+v", result)
		}
	}
	other := trackAt(t, db, humanEvent("other-project", "chat", "new"), now)
	if other.Initialized || !other.Ask || other.Pending != "initialize" {
		t.Fatalf("new project should ask: %+v", other)
	}
}

func TestTrackingWorkspaceInitializationPreservesProjectContinuityClaim(t *testing.T) {
	db := openTestDB(t)
	cfg := loadConfig(t.TempDir())
	initializeAt(t, db, "canonical", time.Now().Add(-3*time.Hour))
	call := func(args map[string]any) TrackingResult {
		t.Helper()
		args["client"], args["event_type"] = "test", "human_prompt"
		args["confirmed_human"], args["root"] = true, true
		result, err := hhTrackingHandler(db, cfg)(context.Background(), callReq(args))
		if err != nil {
			t.Fatal(err)
		}
		var state TrackingResult
		if err := json.Unmarshal([]byte(textOf(t, result)), &state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	continuity := call(map[string]any{"project": "canonical", "chat": "owner", "event_id": "return"})
	if !continuity.Ask || continuity.Pending != "continuity" || continuity.ClaimToken == "" {
		t.Fatalf("missing owner question: %+v", continuity)
	}
	cwd := t.TempDir()
	provisional := call(map[string]any{"cwd": cwd, "chat": "new-chat", "event_id": "workspace"})
	if !provisional.Ask || provisional.Pending != "initialize" || provisional.ClaimToken == "" {
		t.Fatalf("missing workspace question: %+v", provisional)
	}
	joined := call(map[string]any{
		"action": "initialize", "project": "canonical", "cwd": cwd,
		"chat": "new-chat", "event_id": "join", "claim_token": provisional.ClaimToken,
	})
	if !joined.Initialized || joined.Pending != "continuity" || joined.Ask || joined.ClaimToken != "" {
		t.Fatalf("workspace initialization changed canonical question: %+v", joined)
	}
	var pending, token, owner string
	if err := db.QueryRow(`SELECT pending,claim_token,claim_owner FROM tracking_contexts WHERE scope=?`, continuity.ContextID).Scan(&pending, &token, &owner); err != nil {
		t.Fatal(err)
	}
	if pending != "continuity" || token != continuity.ClaimToken || owner != "test\x00owner" {
		t.Fatalf("persisted owner changed: pending=%q token=%q owner=%q", pending, token, owner)
	}
	resolved := call(map[string]any{
		"action": "resolve", "project": "canonical", "chat": "owner",
		"event_id": "answer", "claim_token": continuity.ClaimToken,
	})
	if resolved.Pending != "" || resolved.Ask {
		t.Fatalf("original owner cannot resolve: %+v", resolved)
	}
	reused := call(map[string]any{"cwd": cwd, "chat": "new-chat", "event_id": "continue"})
	if reused.ContextID != continuity.ContextID || !reused.Initialized || reused.Ask || reused.Pending != "" {
		t.Fatalf("workspace binding or initialized behavior changed: %+v", reused)
	}
}

func TestTrackingGapBoundaryAndPendingSurvivesActivity(t *testing.T) {
	for _, minutes := range []int{119, 120} {
		t.Run(fmt.Sprint(minutes), func(t *testing.T) {
			db := openTestDB(t)
			now := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
			initializeAt(t, db, "project", now)
			result := trackAt(t, db, humanEvent("project", "chat", "return"), now.Add(time.Duration(minutes)*time.Minute))
			if result.Ask != (minutes == 120) || (result.Pending != "") != (minutes == 120) {
				t.Fatalf("boundary: %+v", result)
			}
			if result.LastHumanAt != now.Add(time.Duration(minutes)*time.Minute).UnixMilli() {
				t.Fatal("human activity not advanced after comparison")
			}
			if minutes == 120 {
				next := trackAt(t, db, humanEvent("project", "chat", "next"), now.Add(121*time.Minute))
				if next.Pending != "continuity" || next.Ask {
					t.Fatalf("pending was cleared or repeated: %+v", next)
				}
				resolve := humanEvent("project", "chat", "resolve")
				resolve.Action = "resolve"
				resolve.ClaimToken = result.ClaimToken
				resolved := trackAt(t, db, resolve, now.Add(122*time.Minute))
				if resolved.Pending != "" || resolved.Ask {
					t.Fatalf("explicit resolution: %+v", resolved)
				}
			}
		})
	}
}

func TestTrackingBotUnknownAndSubagentCannotAdvanceOrResolve(t *testing.T) {
	db := openTestDB(t)
	now := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	initial := initializeAt(t, db, "project", now)
	for i, input := range []TrackingInput{
		{EventType: "tool", Root: true, ConfirmedHuman: true},
		{EventType: "human_prompt", Root: true},
		{EventType: "human_prompt", ConfirmedHuman: true},
		{EventType: "stop", Root: true, ConfirmedHuman: true},
	} {
		input.Project, input.Client, input.Chat, input.EventID = "project", "test", "bot", fmt.Sprint(i)
		result := trackAt(t, db, input, now.Add(3*time.Hour))
		if result.LastHumanAt != initial.LastHumanAt || result.Pending != "" || result.Ask {
			t.Fatalf("unconfirmed activity treated as human: %+v", result)
		}
		input.Action = "resolve"
		input.EventID += "resolve"
		if _, err := trackProject(db, input, now.Add(3*time.Hour), 2*time.Hour); err == nil {
			t.Fatal("unconfirmed caller resolved tracking")
		}
	}
	returned := trackAt(t, db, humanEvent("project", "chat", "human-return"), now.Add(3*time.Hour))
	if !returned.Ask || returned.Pending != "continuity" {
		t.Fatalf("bots concealed human gap: %+v", returned)
	}
}

func TestTrackingReplayCannotAdvanceOrAskAgain(t *testing.T) {
	db := openTestDB(t)
	now := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	input := humanEvent("project", "chat", "one")
	first := trackAt(t, db, input, now)
	if !first.Ask || first.ClaimToken == "" {
		t.Fatalf("missing initial question: %+v", first)
	}
	replay := trackAt(t, db, input, now.Add(3*time.Hour))
	if !replay.Replay || replay.Ask || replay.LastHumanAt != first.LastHumanAt {
		t.Fatalf("replay changed tracking: %+v", replay)
	}
	next := trackAt(t, db, humanEvent("project", "chat", "two"), now.Add(3*time.Hour))
	if !next.Ask || next.Pending != "initialize" || next.ClaimToken == first.ClaimToken {
		t.Fatalf("expired claim not renewed: %+v", next)
	}
	stale := humanEvent("project", "chat", "stale")
	stale.Action = "initialize"
	stale.ClaimToken = first.ClaimToken
	if _, err := trackProject(db, stale, now.Add(3*time.Hour), 2*time.Hour); err == nil {
		t.Fatal("stale claim initialized context")
	}
}

func TestTrackingConcurrentClaimsAcrossProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hh.db")
	db1, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db1.Close()
	db2, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	now := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	initializeAt(t, db1, "project", now)
	results := make(chan TrackingResult, 8)
	failures := make(chan error, 8)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			db := db1
			if i%2 == 1 {
				db = db2
			}
			result, err := trackProject(db, humanEvent("project", fmt.Sprint(i), fmt.Sprint(i)), now.Add(2*time.Hour), 2*time.Hour)
			if err != nil {
				failures <- err
			}
			results <- result
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	asks := 0
	for result := range results {
		if result.Ask {
			asks++
		}
	}
	if asks != 1 {
		t.Fatalf("question owners=%d, want 1", asks)
	}
}

func TestTrackingRestartAndWorkspaceInitialization(t *testing.T) {
	cwd := t.TempDir()
	path := filepath.Join(t.TempDir(), "hh.db")
	db, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	unknown := humanEvent("", "chat", "unknown")
	unknown.Cwd = cwd
	first := trackAt(t, db, unknown, now)
	if first.Initialized || !first.Ask {
		t.Fatalf("uninitialized workspace: %+v", first)
	}
	init := humanEvent("canonical", "chat", "init")
	init.Cwd = cwd
	init.Action = "initialize"
	init.ClaimToken = first.ClaimToken
	initialized := trackAt(t, db, init, now.Add(time.Minute))
	db.Close()
	db, err = openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	returned := humanEvent("", "another-chat", "return")
	returned.Cwd = cwd
	shared := trackAt(t, db, returned, now.Add(2*time.Minute))
	if shared.ContextID != initialized.ContextID || !shared.Initialized || shared.Project != "canonical" || shared.Ask {
		t.Fatalf("restart lost workspace binding: %+v", shared)
	}
	returned.EventID = "gap"
	gap := trackAt(t, db, returned, now.Add(3*time.Hour))
	if !gap.Ask || gap.Pending != "continuity" {
		t.Fatalf("gap lost: %+v", gap)
	}
	db.Close()
	db, err = openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	returned.EventID = "pending"
	pending := trackAt(t, db, returned, now.Add(3*time.Hour+time.Minute))
	if pending.Ask || pending.Pending != "continuity" || pending.ClaimToken != gap.ClaimToken {
		t.Fatalf("pending/claim lost on restart: %+v", pending)
	}
}

func TestTrackingCanonicalMarkerInitializesWithoutGenericQuestion(t *testing.T) {
	db := openTestDB(t)
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, projectFileName), []byte("---\nproyecto: canonical\n---\n"), 0644); err != nil {
		t.Fatal(err)
	}
	input := humanEvent("wrong-name", "chat", "first")
	input.Cwd = cwd
	result := trackAt(t, db, input, time.Now())
	if result.Project != "canonical" || !result.Initialized || result.Ask {
		t.Fatalf("marker not reused: %+v", result)
	}
}

func TestTrackingReadyProjectWithNoTasksDoesNotAsk(t *testing.T) {
	db := openTestDB(t)
	now := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	initializeAt(t, db, "project", now)
	result := trackAt(t, db, humanEvent("project", "new-chat", "one"), now.Add(time.Minute))
	if result.Ask || result.Pending != "" || len(result.TaskIDs) != 0 {
		t.Fatalf("empty ready project asked generically: %+v", result)
	}
}

func TestTrackingOtherProjectActivityCannotMaskGap(t *testing.T) {
	db := openTestDB(t)
	now := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	initializeAt(t, db, "a", now)
	initializeAt(t, db, "b", now)
	trackAt(t, db, humanEvent("b", "chat", "active"), now.Add(119*time.Minute))
	result := trackAt(t, db, humanEvent("a", "chat", "return"), now.Add(120*time.Minute))
	if !result.Ask || result.Pending != "continuity" {
		t.Fatalf("other project hid gap: %+v", result)
	}
}

func TestTrackingExpiredClaimTransfersWithoutChangingHours(t *testing.T) {
	db := openTestDB(t)
	now := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	initializeAt(t, db, "project", now)
	id, _, _ := insertOpen(db, "human work", "project")
	before, _ := getEntry(db, id)
	first := trackAt(t, db, humanEvent("project", "first", "gap"), now.Add(2*time.Hour))
	observer := humanEvent("project", "second", "observer")
	observer.Root = false
	pending := trackAt(t, db, observer, now.Add(121*time.Minute))
	if pending.Pending != "continuity" || pending.Ask || pending.ClaimToken != "" || pending.LastHumanAt != first.LastHumanAt {
		t.Fatalf("observer changed pending: %+v", pending)
	}
	claimed := trackAt(t, db, humanEvent("project", "second", "claim"), now.Add(125*time.Minute))
	if !claimed.Ask || claimed.ClaimToken == first.ClaimToken {
		t.Fatalf("claim did not transfer at expiry: %+v", claimed)
	}
	stale := humanEvent("project", "first", "old-answer")
	stale.Action = "resolve"
	stale.ClaimToken = first.ClaimToken
	if _, err := trackProject(db, stale, now.Add(126*time.Minute), 2*time.Hour); err == nil {
		t.Fatal("old owner resolved new claim")
	}
	answer := humanEvent("project", "second", "answer")
	answer.Action = "resolve"
	answer.ClaimToken = claimed.ClaimToken
	resolved := trackAt(t, db, answer, now.Add(126*time.Minute))
	if resolved.Pending != "" {
		t.Fatalf("pending not resolved: %+v", resolved)
	}
	after, _ := getEntry(db, id)
	if after != before {
		t.Fatalf("continuity resolution altered human time: %+v", after)
	}
}

func TestTrackingLegacyHistoryAndTechnicalMetaRemainUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Exec(`CREATE TABLE entries (id INTEGER PRIMARY KEY,descripcion TEXT NOT NULL,proyecto TEXT NOT NULL,inicio TEXT NOT NULL,fin TEXT,duracion_hh REAL,resumen TEXT,estado TEXT NOT NULL); INSERT INTO entries VALUES (1,'legacy','project','2026-08-01T09:00:00','2026-08-01T10:00:00',1,'old','cerrado')`)
	if err != nil {
		t.Fatal(err)
	}
	legacy.Close()
	db, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	before, _ := getEntry(db, 1)
	now := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	initializeAt(t, db, "project", now)
	if err := setMeta(db, "last_tool_call_at", now.Add(119*time.Minute).Local().Format(tsLayout)); err != nil {
		t.Fatal(err)
	}
	result := trackAt(t, db, humanEvent("project", "chat", "return"), now.Add(2*time.Hour))
	if !result.Ask {
		t.Fatal("legacy technical activity reset human clock")
	}
	after, _ := getEntry(db, 1)
	if after != before {
		t.Fatal("legacy migration changed hours")
	}
}

func TestTrackingInvalidMetadataAndReplayBindingCannotMutate(t *testing.T) {
	db := openTestDB(t)
	now := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	initializeAt(t, db, "project", now)
	input := humanEvent("project", "chat", "event")
	first := trackAt(t, db, input, now.Add(time.Minute))
	input.Project = "another-project"
	if _, err := trackProject(db, input, now.Add(3*time.Hour), 2*time.Hour); err == nil {
		t.Fatal("replay changed project binding")
	}
	input = humanEvent("project", "chat", "")
	if _, err := trackProject(db, input, now.Add(3*time.Hour), 2*time.Hour); err == nil {
		t.Fatal("missing event identity accepted")
	}
	input.EventID = "after-errors"
	result := trackAt(t, db, input, now.Add(2*time.Minute))
	if result.Pending != "" || result.LastHumanAt <= first.LastHumanAt {
		t.Fatalf("invalid input affected tracking: %+v", result)
	}
	var count int
	db.QueryRow("SELECT count(*) FROM tracking_contexts").Scan(&count)
	if count != 1 {
		t.Fatalf("failed replay left context behind: %d", count)
	}
}

func TestTrackingMarkerConfirmationPreservesPreviousHumanTime(t *testing.T) {
	db := openTestDB(t)
	cwd := t.TempDir()
	now := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	input := humanEvent("", "chat", "initial")
	input.Cwd = cwd
	trackAt(t, db, input, now)
	if err := os.WriteFile(filepath.Join(cwd, projectFileName), []byte("---\nproyecto: canonical\n---\n"), 0644); err != nil {
		t.Fatal(err)
	}
	input.EventID = "return"
	result := trackAt(t, db, input, now.Add(3*time.Hour))
	if !result.Initialized || !result.Ask || result.Pending != "continuity" {
		t.Fatalf("marker confirmation concealed previous human gap: %+v", result)
	}
}

func TestTrackingLinkedWorktreeMarkerMatchesMetricResolution(t *testing.T) {
	for _, primaryMarker := range []bool{false, true} {
		t.Run(fmt.Sprint(primaryMarker), func(t *testing.T) {
			mainRoot, worktree := setupWorktree(t, "tracking-marker")
			db := openTestDB(t)
			if err := os.WriteFile(filepath.Join(worktree, projectFileName), []byte("---\nproyecto: worktree-only\n---\n"), 0644); err != nil {
				t.Fatal(err)
			}
			expected := "worktree-only"
			if primaryMarker {
				if err := os.WriteFile(filepath.Join(mainRoot, projectFileName), []byte("---\nproyecto: primary\n---\n"), 0644); err != nil {
					t.Fatal(err)
				}
				expected = "primary"
			}
			input := humanEvent("", "chat", "first")
			input.Cwd = worktree
			result := trackAt(t, db, input, time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC))
			if !result.Initialized || result.Ask || result.Project != expected || result.Project != computeResolvedProyecto(worktree) {
				t.Fatalf("tracking and metrics disagree: %+v metric=%s", result, computeResolvedProyecto(worktree))
			}
			var bound string
			if err := db.QueryRow("SELECT cwd FROM tracking_workspaces WHERE scope=?", result.ContextID).Scan(&bound); err != nil {
				t.Fatal(err)
			}
			if bound != mainRoot {
				t.Fatalf("workspace binding lost primary root: %s want %s", bound, mainRoot)
			}
			// Another chat in the primary checkout reuses the initialized binding even
			// when its only project marker was supplied by the linked worktree.
			input.Chat, input.EventID, input.Cwd = "other-chat", "second", mainRoot
			reused := trackAt(t, db, input, time.Date(2026, 10, 13, 9, 1, 0, 0, time.UTC))
			if reused.ContextID != result.ContextID || !reused.Initialized || reused.Ask {
				t.Fatalf("primary binding not reused: %+v", reused)
			}
		})
	}
}

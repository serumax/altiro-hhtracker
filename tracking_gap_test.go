package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// openAt inserts an open entry that started at a known local time.
func openAt(t *testing.T, db *sql.DB, descripcion, proyecto string, start time.Time) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO entries (descripcion, proyecto, inicio, estado) VALUES (?, ?, ?, 'abierto')`, descripcion, proyecto, start.Local().Format(tsLayout))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func resolveAt(t *testing.T, db *sql.DB, project, token string, worked *bool, now time.Time) (TrackingResult, error) {
	t.Helper()
	input := humanEvent(project, "chat", "answer")
	input.Action = "resolve"
	input.ClaimToken = token
	input.Worked = worked
	return trackProject(db, input, now, 2*time.Hour)
}

func boolPtr(v bool) *bool { return &v }

// The day in the example: work from 09:00, last activity 11:30, back at 16:30.
var (
	gapDay      = time.Date(2026, 11, 18, 9, 0, 0, 0, time.Local)
	gapLast     = gapDay.Add(150 * time.Minute) // 11:30
	gapReturned = gapDay.Add(450 * time.Minute) // 16:30
)

func startGapDay(t *testing.T, db *sql.DB) (entryID int64, question TrackingResult) {
	t.Helper()
	initializeAt(t, db, "project", gapDay)
	entryID = openAt(t, db, "task", "project", gapDay)
	trackAt(t, db, humanEvent("project", "chat", "morning"), gapDay.Add(90*time.Minute))
	trackAt(t, db, humanEvent("project", "chat", "last"), gapLast)
	question = trackAt(t, db, humanEvent("project", "chat", "back"), gapReturned)
	if !question.Ask || question.Pending != "continuity" || question.ClaimToken == "" {
		t.Fatalf("expected the continuity question: %+v", question)
	}
	return entryID, question
}

func TestContinuityIsNotAskedWithoutAnOpenEntry(t *testing.T) {
	db := openTestDB(t)
	initializeAt(t, db, "project", gapDay)
	result := trackAt(t, db, humanEvent("project", "new-chat", "back"), gapReturned)
	if result.Ask || result.Pending != "" {
		t.Fatalf("a new chat with nothing open asked about the gap: %+v", result)
	}
}

func TestResolveNoClosesAtLastActivityAndReopensAtReturn(t *testing.T) {
	db := openTestDB(t)
	entryID, question := startGapDay(t, db)

	resolved, err := resolveAt(t, db, "project", question.ClaimToken, boolPtr(false), gapReturned.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Pending != "" || resolved.Ask {
		t.Fatalf("question still pending: %+v", resolved)
	}

	closed, err := getEntry(db, entryID)
	if err != nil {
		t.Fatal(err)
	}
	if closed.Estado != "cerrado" || closed.Fin.String != gapLast.Format(tsLayout) || closed.DuracionHH.Float64 != 2.5 {
		t.Fatalf("entry not closed at the last activity (11:30, 2.5 h): %+v", closed)
	}
	if len(resolved.TaskIDs) != 1 || resolved.TaskIDs[0] == entryID {
		t.Fatalf("expected one new open entry: %+v", resolved.TaskIDs)
	}
	reopened, err := getEntry(db, resolved.TaskIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Estado != "abierto" || reopened.Descripcion != "task" || reopened.Proyecto != "project" || reopened.Inicio != gapReturned.Format(tsLayout) {
		t.Fatalf("new entry must start at the return (16:30) with the same task: %+v", reopened)
	}
}

func TestResolveYesKeepsTheEntryOpen(t *testing.T) {
	db := openTestDB(t)
	entryID, question := startGapDay(t, db)

	resolved, err := resolveAt(t, db, "project", question.ClaimToken, boolPtr(true), gapReturned.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	entry, err := getEntry(db, entryID)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Estado != "abierto" || entry.Inicio != gapDay.Format(tsLayout) || len(resolved.TaskIDs) != 1 || resolved.TaskIDs[0] != entryID {
		t.Fatalf("a yes must keep the hours running: %+v %+v", entry, resolved)
	}
}

func TestResolveRequiresTheAnswer(t *testing.T) {
	db := openTestDB(t)
	_, question := startGapDay(t, db)
	if _, err := resolveAt(t, db, "project", question.ClaimToken, nil, gapReturned.Add(time.Minute)); err == nil || !strings.Contains(err.Error(), "worked") {
		t.Fatalf("resolve without worked must fail: %v", err)
	}
}

func TestTrackingToolPassesTheAnswer(t *testing.T) {
	db := openTestDB(t)
	entryID, question := startGapDay(t, db)
	cfg := loadConfig(t.TempDir())
	result, err := hhTrackingHandler(db, cfg)(context.Background(), callReq(map[string]any{
		"action": "resolve", "project": "project", "client": "test", "chat": "chat", "event_id": "answer",
		"event_type": "human_prompt", "confirmed_human": true, "root": true,
		"claim_token": question.ClaimToken, "worked": false,
	}))
	if err != nil {
		t.Fatal(err)
	}
	var state TrackingResult
	if err := json.Unmarshal([]byte(textOf(t, result)), &state); err != nil {
		t.Fatalf("tool error: %s", textOf(t, result))
	}
	if entry, _ := getEntry(db, entryID); entry.Estado != "cerrado" {
		t.Fatalf("the tool did not apply worked=false: %+v", entry)
	}
}

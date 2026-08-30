package main

import (
	"database/sql"
	"testing"
)

func sampleResponse() Response {
	return Response{
		Ts:               "2026-08-30T10:00:00.000000",
		Proyecto:         "hh-tracker",
		Cliente:          "opencode",
		Modelo:           "minimax/MiniMax-M3",
		DuracionS:        sql.NullFloat64{Float64: 12.5, Valid: true},
		TokensIn:         100,
		TokensOut:        200,
		TokensCacheWrite: 20,
		TokensCacheRead:  30,
		CostoUSD:         0.01,
		EntryID:          sql.NullInt64{},
		Source:           "opencode",
		ExternalID:       "msg-1",
	}
}

func TestInsertResponseInsertsOnceThenDedup(t *testing.T) {
	db := openTestDB(t)

	ins, err := insertResponse(db, sampleResponse())
	if err != nil {
		t.Fatalf("insertResponse: %v", err)
	}
	if !ins {
		t.Fatal("first insert: expected inserted=true")
	}

	ins, err = insertResponse(db, sampleResponse())
	if err != nil {
		t.Fatalf("insertResponse 2: %v", err)
	}
	if ins {
		t.Fatal("second insert with same (source,external_id): expected inserted=false")
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM responses`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("expected 1 row, got %d (err %v)", n, err)
	}

	var tin, tout, tcw, tcr int64
	var costo float64
	if err := db.QueryRow(`SELECT tokens_in, tokens_out, tokens_cache_write, tokens_cache_read, costo_usd FROM responses`).
		Scan(&tin, &tout, &tcw, &tcr, &costo); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if tin != 100 || tout != 200 || tcw != 20 || tcr != 30 || costo != 0.01 {
		t.Fatalf("stored fields wrong: in=%d out=%d cw=%d cr=%d costo=%v", tin, tout, tcw, tcr, costo)
	}
}

func TestWatermarkRoundTrip(t *testing.T) {
	db := openTestDB(t)

	wm, err := getWatermark(db, "opencode")
	if err != nil {
		t.Fatalf("getWatermark: %v", err)
	}
	if wm != "" {
		t.Fatalf("absent watermark should be empty, got %q", wm)
	}

	if err := setWatermark(db, "opencode", "123"); err != nil {
		t.Fatalf("setWatermark: %v", err)
	}
	if err := setWatermark(db, "opencode", "456"); err != nil {
		t.Fatalf("setWatermark upsert: %v", err)
	}
	wm, _ = getWatermark(db, "opencode")
	if wm != "456" {
		t.Fatalf("watermark = %q, want 456", wm)
	}
}

func TestMetaRoundTrip(t *testing.T) {
	db := openTestDB(t)

	v, err := getMeta(db, "x")
	if err != nil || v != "" {
		t.Fatalf("absent meta: v=%q err=%v", v, err)
	}
	if err := setMeta(db, "x", "1"); err != nil {
		t.Fatalf("setMeta: %v", err)
	}
	if err := setMeta(db, "x", "2"); err != nil {
		t.Fatalf("setMeta upsert: %v", err)
	}
	v, _ = getMeta(db, "x")
	if v != "2" {
		t.Fatalf("meta = %q, want 2", v)
	}
}

func TestSingleOpenEntryID(t *testing.T) {
	db := openTestDB(t)

	// none open
	id, err := singleOpenEntryID(db)
	if err != nil {
		t.Fatalf("singleOpenEntryID: %v", err)
	}
	if id.Valid {
		t.Fatalf("no open entries: expected invalid, got %v", id.Int64)
	}

	// exactly one
	oid, _, _ := insertOpen(db, "task", "p")
	id, _ = singleOpenEntryID(db)
	if !id.Valid || id.Int64 != oid {
		t.Fatalf("one open entry: expected %d, got %+v", oid, id)
	}

	// two open -> invalid
	insertOpen(db, "task2", "p")
	id, _ = singleOpenEntryID(db)
	if id.Valid {
		t.Fatalf("two open entries: expected invalid, got %v", id.Int64)
	}
}

func TestCostReportAggregates(t *testing.T) {
	db := openTestDB(t)

	rows := []Response{
		{Ts: "2026-08-10T10:00:00.000000", Proyecto: "a", Cliente: "opencode", Modelo: "m1", TokensIn: 10, TokensOut: 20, TokensCacheWrite: 1, TokensCacheRead: 2, CostoUSD: 0.5, DuracionS: sql.NullFloat64{Float64: 3, Valid: true}, Source: "opencode", ExternalID: "1"},
		{Ts: "2026-08-11T10:00:00.000000", Proyecto: "a", Cliente: "opencode", Modelo: "m1", TokensIn: 5, TokensOut: 5, TokensCacheWrite: 0, TokensCacheRead: 4, CostoUSD: 0.25, DuracionS: sql.NullFloat64{Float64: 2, Valid: true}, Source: "opencode", ExternalID: "2"},
		{Ts: "2026-08-12T10:00:00.000000", Proyecto: "b", Cliente: "claude-code", Modelo: "m2", TokensIn: 1, TokensOut: 1, TokensCacheWrite: 1, TokensCacheRead: 1, CostoUSD: 0, Source: "claude-code", ExternalID: "3"},
		{Ts: "2026-07-01T10:00:00.000000", Proyecto: "a", Cliente: "opencode", Modelo: "m1", TokensIn: 999, TokensOut: 999, TokensCacheWrite: 999, TokensCacheRead: 999, CostoUSD: 9, Source: "opencode", ExternalID: "4"},
	}
	for _, r := range rows {
		if _, err := insertResponse(db, r); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	got, err := costReport(db, "2026-08-01", "2026-08-31", "", "")
	if err != nil {
		t.Fatalf("costReport: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 groups, got %d: %+v", len(got), got)
	}
	// order: proyecto ASC, modelo ASC
	if got[0].Proyecto != "a" || got[0].Modelo != "m1" {
		t.Fatalf("group 0 = %+v", got[0])
	}
	if got[0].NumRespuestas != 2 || got[0].TokensIn != 15 || got[0].TokensOut != 25 ||
		got[0].SumCacheWrite != 1 || got[0].SumCacheRead != 6 {
		t.Fatalf("group a/m1 aggregate wrong: %+v", got[0])
	}
	if got[0].CostoUSD != 0.75 || got[0].DuracionS != 5 {
		t.Fatalf("group a/m1 sums wrong: %+v", got[0])
	}
	if got[1].Proyecto != "b" || got[1].Modelo != "m2" {
		t.Fatalf("group 1 = %+v", got[1])
	}

	// modelo filter
	got, _ = costReport(db, "", "", "", "m2")
	if len(got) != 1 || got[0].Modelo != "m2" {
		t.Fatalf("modelo filter: %+v", got)
	}

	// proyecto filter
	got, _ = costReport(db, "", "", "a", "")
	if len(got) != 1 || got[0].Proyecto != "a" || got[0].NumRespuestas != 3 {
		t.Fatalf("proyecto filter: %+v", got)
	}
}

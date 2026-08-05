package main

import (
	"database/sql"
	"strings"
	"testing"
	"time"
)

// ---- fmtDuracion ----------------------------------------------------------

func TestFmtDuracion(t *testing.T) {
	tests := []struct {
		name  string
		input float64
		want  string
	}{
		{"zero", 0, "0 min"},
		{"exactly 1h", 1.0, "1 HH"},
		{"0.99167 rounds to 60 min not 1 HH", 0.99167, "60 min"},
		{"1.99167 is 1 HH 60 min", 1.99167, "1 HH 60 min"},
		{"negative 30min", -0.5, "-30 min"},
		{"1.5h is 1 HH 30 min", 1.5, "1 HH 30 min"},
		{"0.5h is 30 min", 0.5, "30 min"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := fmtDuracion(tc.input)
			if got != tc.want {
				t.Errorf("fmtDuracion(%v) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// ---- Locale maps ----------------------------------------------------------

func TestDiasCortos(t *testing.T) {
	// Monday-first: 0=lun … 6=dom
	expected := map[int]string{
		0: "lun",
		1: "mar",
		2: "mié",
		3: "jue",
		4: "vie",
		5: "sáb",
		6: "dom",
	}
	for idx, want := range expected {
		got := diasCortos[idx]
		if got != want {
			t.Errorf("diasCortos[%d] = %q, want %q", idx, got, want)
		}
	}
}

func TestMesesCortos(t *testing.T) {
	expected := map[int]string{
		1: "ene", 2: "feb", 3: "mar", 4: "abr",
		5: "may", 6: "jun", 7: "jul", 8: "ago",
		9: "sep", 10: "oct", 11: "nov", 12: "dic",
	}
	for idx, want := range expected {
		got := mesesCortos[idx]
		if got != want {
			t.Errorf("mesesCortos[%d] = %q, want %q", idx, got, want)
		}
	}
}

func TestMesesLargos(t *testing.T) {
	expected := map[int]string{
		1: "enero", 2: "febrero", 3: "marzo", 4: "abril",
		5: "mayo", 6: "junio", 7: "julio", 8: "agosto",
		9: "septiembre", 10: "octubre", 11: "noviembre", 12: "diciembre",
	}
	for idx, want := range expected {
		got := mesesLargos[idx]
		if got != want {
			t.Errorf("mesesLargos[%d] = %q, want %q", idx, got, want)
		}
	}
}

// ---- reporte helper -------------------------------------------------------

func nullFloat(v float64) sql.NullFloat64 {
	return sql.NullFloat64{Valid: true, Float64: v}
}

func nullString(s string) sql.NullString {
	return sql.NullString{Valid: true, String: s}
}

func TestReporteEmpty(t *testing.T) {
	got := reporte(nil, "Semana 01/08 – 07/08/2026")
	if !strings.Contains(got, "Sin entradas cerradas") {
		t.Errorf("empty reporte: got %q, want contains 'Sin entradas cerradas'", got)
	}
}

func TestReporteFormatting(t *testing.T) {
	// 2026-08-03 is a Monday
	entries := []Entry{
		{
			Descripcion: "fix bug",
			Proyecto:    "my-project",
			Inicio:      "2026-08-03T09:00:00.000000",
			DuracionHH:  nullFloat(1.5),
			Estado:      "cerrado",
		},
	}
	got := reporte(entries, "Semana 03/08 – 09/08/2026")

	checks := []struct {
		desc string
		sub  string
	}{
		{"📅 prefix", "📅"},
		{"weekday Lun", "Lun"},
		{"time 09:00", "09:00"},
		{"descripcion", "fix bug"},
		{"duration 1 HH 30 min", "1 HH 30 min"},
		{"Por proyecto section", "Por proyecto:"},
		{"Total section", "⏱ Total:"},
	}
	for _, c := range checks {
		if !strings.Contains(got, c.sub) {
			t.Errorf("reporte (%s): want %q in output; got:\n%s", c.desc, c.sub, got)
		}
	}
}

func TestReporteProyectoAlphabetical(t *testing.T) {
	// Two projects: verify alphabetical order in Por proyecto section.
	// Entry order is insertion order (zorro first), but Por proyecto must be alphabetical.
	entries := []Entry{
		{Descripcion: "task B", Proyecto: "zorro", Inicio: "2026-08-03T10:00:00.000000", DuracionHH: nullFloat(1.0), Estado: "cerrado"},
		{Descripcion: "task A", Proyecto: "alfa", Inicio: "2026-08-03T11:00:00.000000", DuracionHH: nullFloat(2.0), Estado: "cerrado"},
	}
	got := reporte(entries, "Test")

	// Locate the "Por proyecto:" section to search within it.
	secIdx := strings.Index(got, "Por proyecto:")
	if secIdx < 0 {
		t.Fatalf("reporte: 'Por proyecto:' section missing; got:\n%s", got)
	}
	section := got[secIdx:]

	idxAlfa := strings.Index(section, "alfa")
	idxZorro := strings.Index(section, "zorro")
	if idxAlfa < 0 || idxZorro < 0 {
		t.Fatalf("reporte: expected both projects in 'Por proyecto:' section; got:\n%s", got)
	}
	if idxAlfa > idxZorro {
		t.Errorf("reporte: expected 'alfa' before 'zorro' in 'Por proyecto:' (alphabetical);\ngot:\n%s", got)
	}
	if !strings.Contains(got, "3 HH") {
		t.Errorf("reporte: expected '3 HH' global total; got:\n%s", got)
	}
}

func TestReporteTitleInOutput(t *testing.T) {
	titulo := "Semana 03/08 – 09/08/2026"
	entries := []Entry{
		{Descripcion: "work", Proyecto: "proj", Inicio: "2026-08-03T08:00:00.000000", DuracionHH: nullFloat(0.5), Estado: "cerrado"},
	}
	got := reporte(entries, titulo)
	if !strings.Contains(got, titulo) {
		t.Errorf("reporte: title %q not found in:\n%s", titulo, got)
	}
}

// ---- Weekday index sanity -------------------------------------------------

func TestWeekdayMondayFirst(t *testing.T) {
	// 2026-08-03 is Monday → index 0
	monday := time.Date(2026, 8, 3, 0, 0, 0, 0, time.Local)
	if idx := (int(monday.Weekday()) + 6) % 7; idx != 0 {
		t.Errorf("Monday weekday index: got %d, want 0", idx)
	}
	// 2026-08-09 is Sunday → index 6
	sunday := time.Date(2026, 8, 9, 0, 0, 0, 0, time.Local)
	if idx := (int(sunday.Weekday()) + 6) % 7; idx != 6 {
		t.Errorf("Sunday weekday index: got %d, want 6", idx)
	}
	// 2026-08-08 is Saturday → index 5
	saturday := time.Date(2026, 8, 8, 0, 0, 0, 0, time.Local)
	if idx := (int(saturday.Weekday()) + 6) % 7; idx != 5 {
		t.Errorf("Saturday weekday index: got %d, want 5", idx)
	}
}

package main

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// tsLayout is the timestamp format used for all stored timestamps.
// It matches Python's naive local ISO format and keeps the first 10 chars
// as a valid DATE() for SQLite range queries.
const tsLayout = "2006-01-02T15:04:05.000000"

// ---- Spanish locale maps --------------------------------------------------
// Monday-first: 0=lun … 6=dom
var diasCortos = map[int]string{
	0: "lun",
	1: "mar",
	2: "mié",
	3: "jue",
	4: "vie",
	5: "sáb",
	6: "dom",
}

// 1-indexed month abbreviations.
var mesesCortos = map[int]string{
	1: "ene", 2: "feb", 3: "mar", 4: "abr",
	5: "may", 6: "jun", 7: "jul", 8: "ago",
	9: "sep", 10: "oct", 11: "nov", 12: "dic",
}

// 1-indexed full month names.
var mesesLargos = map[int]string{
	1: "enero", 2: "febrero", 3: "marzo", 4: "abril",
	5: "mayo", 6: "junio", 7: "julio", 8: "agosto",
	9: "septiembre", 10: "octubre", 11: "noviembre", 12: "diciembre",
}

// ---- fmtDuracion ----------------------------------------------------------

// fmtDuracion converts a duration in fractional hours to a Spanish display string.
// It uses standard 0.5-rounds-up rounding for minutes. It intentionally does NOT
// normalize a 60-minute remainder (0.99167h → "60 min", not "1 HH").
func fmtDuracion(hh float64) string {
	h := int(hh)
	m := int(math.Round((hh - float64(h)) * 60))
	if h == 0 {
		return fmt.Sprintf("%d min", m)
	}
	if m == 0 {
		return fmt.Sprintf("%d HH", h)
	}
	return fmt.Sprintf("%d HH %d min", h, m)
}

// ---- formatCostos --------------------------------------------------------

// fmtSegundos renders a duration in seconds as a compact Spanish-friendly string.
func fmtSegundos(s float64) string {
	total := int(s + 0.5)
	if total < 60 {
		return fmt.Sprintf("%ds", total)
	}
	m := total / 60
	rem := total % 60
	if m < 60 {
		return fmt.Sprintf("%dm %ds", m, rem)
	}
	h := m / 60
	m = m % 60
	return fmt.Sprintf("%dh %dm", h, m)
}

// formatCostos renders the cost report rows as a text table with a TOTAL line.
func formatCostos(rows []CostRow) string {
	if len(rows) == 0 {
		return "Sin respuestas registradas en el rango."
	}

	var sb strings.Builder
	sb.WriteString("💸 Costos por proyecto y modelo\n")
	sb.WriteString(fmt.Sprintf("%-20s %-24s %8s  %-22s %10s  %10s\n",
		"proyecto", "modelo", "nº resp", "tokens (in/out/cache)", "costo USD", "tiempo"))

	var (
		tResp             int64
		tIn, tOut, tCache int64
		tCost, tDur       float64
	)
	for _, r := range rows {
		sb.WriteString(fmt.Sprintf("%-20s %-24s %8d  %-22s %10.4f  %10s\n",
			r.Proyecto, r.Modelo, r.NumRespuestas,
			fmt.Sprintf("%d/%d/%d", r.TokensIn, r.TokensOut, r.TokensCache),
			r.CostoUSD, fmtSegundos(r.DuracionS)))
		tResp += r.NumRespuestas
		tIn += r.TokensIn
		tOut += r.TokensOut
		tCache += r.TokensCache
		tCost += r.CostoUSD
		tDur += r.DuracionS
	}

	sb.WriteString(fmt.Sprintf("%-20s %-24s %8d  %-22s %10.4f  %10s\n",
		"TOTAL", "", tResp,
		fmt.Sprintf("%d/%d/%d", tIn, tOut, tCache),
		tCost, fmtSegundos(tDur)))
	return sb.String()
}

// ---- reporte helper -------------------------------------------------------

// reporte formats a list of closed entries for hh_semana and hh_mes output.
// Entries must already be ordered ASC by inicio (the caller's query is responsible).
// It returns the full formatted string including title, detail, per-project,
// and global total sections.
func reporte(entries []Entry, titulo string) string {
	if len(entries) == 0 {
		return fmt.Sprintf("Sin entradas cerradas para %s.", titulo)
	}

	var sb strings.Builder
	sb.WriteString("📅 " + titulo + "\n")

	// Per-project totals — insertion-order slice keeps global sum deterministic.
	proyTotales := make(map[string]float64)
	var proyOrden []string

	for _, e := range entries {
		t, err := time.ParseInLocation(tsLayout, e.Inicio, time.Local)
		if err != nil {
			// Fallback: try without fractional seconds.
			t, err = time.ParseInLocation("2006-01-02T15:04:05", e.Inicio, time.Local)
			if err != nil {
				// Last resort: emit raw inicio.
				sb.WriteString(fmt.Sprintf("  %s [%s] %s\n", e.Inicio, e.Proyecto, e.Descripcion))
				continue
			}
		}

		// Monday-first weekday index via (Go Sunday=0 + 6) % 7.
		wdIdx := (int(t.Weekday()) + 6) % 7
		wdRaw := diasCortos[wdIdx]
		wd := capitalizeFirst(wdRaw)

		fechaHora := fmt.Sprintf("%s %02d/%02d %02d:%02d",
			wd, t.Day(), int(t.Month()), t.Hour(), t.Minute())

		durStr := ""
		if e.DuracionHH.Valid {
			durStr = " — " + fmtDuracion(e.DuracionHH.Float64)
		}

		sb.WriteString(fmt.Sprintf("%s [%s] %s%s\n",
			fechaHora, e.Proyecto, e.Descripcion, durStr))

		if _, seen := proyTotales[e.Proyecto]; !seen {
			proyOrden = append(proyOrden, e.Proyecto)
		}
		if e.DuracionHH.Valid {
			proyTotales[e.Proyecto] += e.DuracionHH.Float64
		}
	}

	// Por proyecto: alphabetical.
	sortedProys := make([]string, len(proyOrden))
	copy(sortedProys, proyOrden)
	sort.Strings(sortedProys)

	sb.WriteString("Por proyecto:\n")
	for _, p := range sortedProys {
		sb.WriteString(fmt.Sprintf("  %s: %s\n", p, fmtDuracion(proyTotales[p])))
	}

	// Global total: iterate insertion-order slice for determinism.
	var total float64
	for _, p := range proyOrden {
		total += proyTotales[p]
	}
	sb.WriteString(fmt.Sprintf("⏱ Total: %s\n", fmtDuracion(total)))

	return sb.String()
}

// capitalizeFirst uppercases the first rune of s.
// Every first rune in diasCortos is ASCII so a byte-safe approach is fine,
// but using utf8 keeps it correct in all cases.
func capitalizeFirst(s string) string {
	if s == "" {
		return s
	}
	r, size := utf8.DecodeRuneInString(s)
	return strings.ToUpper(string(r)) + s[size:]
}

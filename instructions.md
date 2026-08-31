# hh-tracker — MCP Server Instructions

You have a work-hours tracking tool. Use it automatically according to these rules.

## Session Start

Call `hh_status()` at the beginning of every session. If there are open entries, report them to the user. If there are no open entries, ask: "¿En qué requerimiento vamos a trabajar hoy?" and call `hh_start(descripcion, proyecto)` with the answer.

## Project Name

Before calling `hh_start`, `hh_close` or `hh_manual`, look for a `.hh-tracker.md`
file at the repository root (run `altiro-hhtracker init` once per project to
create it). When it exists, use its `proyecto:` value verbatim as the `proyecto`
argument. This keeps hour entries and the automatic response metrics under the
same name. When there is no such file, fall back to the repository or product
name (e.g. "demo-app", "n8n-demo", "general").

## During Work

- Call `hh_start(descripcion, proyecto)` when a new distinct requirement begins.
- One entry per requirement. Do not open multiple entries for the same task.

## Idle Entries

An open entry left without activity should not run forever. When `hh_status`
shows an open entry and roughly five minutes have passed with no interaction,
ask the user whether to close it.

- The user confirms ("ok", "sí", "dale", "ciérrala") → call `hh_close`.
- The user says anything else, brings up a new requirement, or asks for
  something more → leave the entry open and let the inactivity count restart.
- The user does not answer → do nothing; the server itself closes the entry
  after about ten minutes of total inactivity, as a safety net.

## Closing Entries

Call `hh_close(entry_id, resumen?)` when the user signals the work is done. Trigger words include: "listo", "cerramos", "ok" at the end of a task, "terminamos", "eso era todo". Report: start time, end time, and total duration.

## Manual Registration

Use `hh_manual` when the user reports time that has already passed:

- With explicit times: `hh_manual(descripcion, proyecto, inicio="09:00", fin="10:30")`
- With duration: `hh_manual(descripcion, proyecto, duracion_hh=1.5)`
- If the user gives only HH:MM, assume today's date.

## Reports

| User asks for | Call |
|---|---|
| This week's hours | `hh_semana()` |
| Last week | `hh_semana(offset=-1)` |
| This month | `hh_mes()` |
| Last month | `hh_mes(offset=-1)` |
| A date range | `hh_report(desde="YYYY-MM-DD", hasta="YYYY-MM-DD")` |
| Token / cost of agent responses | `hh_costos(desde?, hasta?, proyecto?, modelo?)` |

## Rules

- Never invent durations or timestamps. Only record what the user explicitly tells you or what the clock confirms.
- Never close an entry without calling `hh_close`. Do not assume an entry is closed.
- If the user changes tasks mid-session, close the current entry first, then open a new one.
- Saving to hh-tracker is bookkeeping — it never replaces the actual answer to the user. Always deliver the answer, then save.
- Times are in the user's local timezone. For Chilean users, that is America/Santiago.

# altiro-hhtracker

Work-hours MCP tracker for AI coding sessions. Tracks time spent on requirements,
generates reports by week, month, or custom date range, and records token and
cost metrics for every agent response.

## Install

```bash
brew install serumax/tap/altiro-hhtracker
```

## Usage

Configure as an MCP server in your AI coding client (Claude Code, OpenCode, etc.):

```json
{
  "mcp": {
    "altiro-hhtracker": {
      "type": "local",
      "command": "altiro-hhtracker"
    }
  }
}
```

`altiro-hhtracker install` autodetects Claude Code and OpenCode and registers the
server for you. The server injects its usage instructions automatically on connect.

## Tools

| Tool | Description |
|---|---|
| `hh_start(descripcion, proyecto)` | Open a time entry |
| `hh_close(entry_id, resumen?)` | Close and report duration |
| `hh_status()` | Show open entries |
| `hh_update(entry_id, ...)` | Edit fields of an entry |
| `hh_delete(entry_id)` | Delete an entry |
| `hh_manual(...)` | Register past time (with times or duration) |
| `hh_report(desde, hasta, proyecto?)` | Report by date range (YYYY-MM-DD) |
| `hh_semana(offset?, proyecto?)` | Weekly report |
| `hh_mes(offset?, proyecto?)` | Monthly report |
| `hh_costos(desde?, hasta?, proyecto?, modelo?)` | Token and cost report for agent responses |

Data is stored locally at `~/.hh-tracker/hh.db`.

## Entry safety and response attribution

Manual entries accept local `HH:MM` or `YYYY-MM-DDTHH:MM:SS[.fraction]`
start/end pairs, or a positive duration in hours alone. Partial pairs, invalid
local timestamps, reversed intervals, negative/non-finite durations and durations
outside the representable range are rejected before writing. Timestamp pairs
calculate elapsed hours; duration-only mode counts backward from the current time.

Updates are atomic: validation and duration recalculation cannot leave partially
changed entries. Empty strings and zero duration mean unchanged. A positive
explicit update duration overrides elapsed time; updating `fin` does not implicitly
close a record. Concurrent close attempts cannot overwrite the first successful
close. Deleting an entry clears its metric links without deleting response metrics.

Start, close, update and manual recording warn about overlapping human-time
intervals, including other projects. Warnings do not modify other entries or prevent
recording; review overlaps manually instead of summing parallel work blindly.

Newly imported response metrics link to an entry only when its canonical project
and response timestamp match exactly one recorded interval. Intervals include their
start and exclude their end, so adjacent tasks are not ambiguous. Missing or
multiple matches remain unlinked; a currently open task never claims unrelated
historical responses. `.hh-tracker.md` takes precedence over OpenCode display names.
Existing history is not relinked automatically.

## Project files

Run once per repository:

```bash
altiro-hhtracker init
```

This writes `.hh-tracker.md` at the repository root (the primary checkout root,
even when run from a linked worktree), prefilled with the project name derived
from the `origin` remote:

```markdown
---
proyecto: demo
alias: [api-demo, web-demo, theme-demo]
---
```

`proyecto` is the canonical name used for both hour entries and response metrics.
`alias` is a note for yourself listing the repositories that belong to the same
project; put a `.hh-tracker.md` with the same `proyecto` in each of them. Commit
the file so every worktree shares it.

## Response metrics

While connected, the server ingests one row per agent response into
`~/.hh-tracker/hh.db`:

- **OpenCode** — read from `~/.local/share/opencode/opencode.db`, with the real
  cost reported by OpenCode.
- **Claude Code** — read from the `~/.claude/projects` transcripts, deduped by
  the API message id. Cost is recorded as `0` (subscription).

Each row carries the timestamp, project, client, model, response duration,
input / output / cache tokens, cost, and the entry that was open at the time.
Query it with `hh_costos`. The first run backfills the full history.

## Idle auto-close

An open entry with no activity for about ten minutes is closed automatically with
the note "cerrada automáticamente por inactividad". The agent is expected to ask
you first, around the five-minute mark. Activity means any ingested response or
any `hh_*` tool call.

## Configuration

Optional `~/.hh-tracker/config.json`, all keys optional. Path overrides must be
absolute (no `~` expansion); the defaults shown are used when the file or a key
is absent.

```json
{
  "idle_close_minutes": 10,
  "ingest_enabled": true,
  "opencode_db_path": "/Users/you/.local/share/opencode/opencode.db",
  "claude_projects_dir": "/Users/you/.claude/projects"
}
```

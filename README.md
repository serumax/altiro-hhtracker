# altiro-hhtracker

Human work-hours MCP tracker by project and task. Tracks time spent on requirements,
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

`altiro-hhtracker install` detects Codex CLI, Claude Code and OpenCode and registers
MCP for detected clients. Codex additionally receives a UserPromptSubmit context
hook. The server injects its tracking instructions on connect. Installation failures
return a nonzero exit and report partial success; successful client changes remain.

## Tools

| Tool | Description |
|---|---|
| `hh_tracking(...)` | Check/initialize shared project context or resolve continuity questions |
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
input / output / cache tokens, cost, and an unambiguous matching project/time entry when available.
Query it with `hh_costos`. The first run backfills the full history.

## Human project tracking

Initialized project context is persistent and shared across its chats. A canonical
`.hh-tracker.md` initializes that identity automatically; a human-confirmed explicit
project name can initialize it through `hh_tracking(action="initialize", ...)`.
A reused chat is not a new human work session. New task requests already supply
the task description, so an initialized project does not ask the generic question
again merely because it has no open task.

`hh_tracking` checks metadata from a confirmed **root human prompt**. At the next
such interaction after at least 120 minutes without one in that project, it requests
continuity clarification. The previous timestamp is checked before advancing it.
Pending clarification survives other messages and restarts until explicitly
resolved with the question owner's claim token. One chat owns a question for five
minutes; abandoned claims can be recovered by a later confirmed human event.
Stable event IDs deduplicate retries, while technical chat/session IDs only identify
the conversation. No prompt text is stored.

Assistant messages, subagent completion, tool calls, Stop and unknown events never
advance the human clock. Root/human flags are provenance assertions from the adapter
or orchestrator, **not an authentication boundary**. The root orchestrator alone
records task start and completion, including verification; delegated agents only
report. Checking or resolving context never opens/closes tasks or changes HH times.

**No human HH entry is closed by inactivity.** Silence may include human thinking,
research or other work. Manual recording and corrections remain available.
`idle_close_minutes` is deprecated and ignored, regardless of its previous value.
The background worker only imports response metrics. Existing HH/history is preserved.

Without a verified client event adapter, the root assistant must call `hh_tracking`
following the injected instructions. The server does not wake inactive chats or
promise delivery of a prompt exactly two hours later. The Codex adapter below can provide
context at submitted prompts, but the root must still verify their human origin.

## Configuration

Optional `~/.hh-tracker/config.json`, all keys optional. Path overrides must be
absolute (no `~` expansion); the defaults shown are used when the file or a key
is absent.

```json
{
  "human_reminder_minutes": 120,
  "ingest_enabled": true,
  "opencode_db_path": "/Users/you/.local/share/opencode/opencode.db",
  "claude_projects_dir": "/Users/you/.claude/projects"
}
```

`human_reminder_minutes` must be positive and representable as a duration; invalid
values fall back to 120. The setting controls clarification on human return, not
a timer or automatic close. Keep the server's local timezone consistent with the
user's timezone for hour-entry timestamps.

## Codex local integration

Installation registers the owned MCP name with `codex mcp add altiro-hhtracker --
<absolute executable>` and atomically merges only the marked UserPromptSubmit
handler into `~/.codex/hooks.json` (or `$CODEX_HOME/hooks.json`). Existing events,
other handlers and unknown fields are retained. Malformed JSON, duplicate keys,
invalid hook collections and symlink configuration files are refused unchanged.
Codex hook preflight precedes its MCP registration; a later file-write failure is
reported as partial, not silently rolled back. Reinstallation is idempotent.
Claude retains its user-scoped registration; OpenCode JSON writes are atomic.

Review and trust the new hook through Codex `/hooks`, then reload the client.
The installer never changes trust or approval preferences. Command scripts must
exist on the local execution host; this installer supports POSIX hook commands.
A custom `CODEX_HOME` must be an existing absolute directory.
[Official hooks documentation](https://learn.chatgpt.com/docs/hooks),
[official MCP setup](https://learn.chatgpt.com/docs/extend/mcp?surface=cli),
[official environment settings](https://learn.chatgpt.com/docs/config-file/environment-variables).

The hook command `altiro-hhtracker hook codex` consumes one JSON object from stdin,
limited to 256 KiB. It validates event metadata and emits Codex developer-context
JSON, without retaining or echoing prompt text, reading transcripts, starting a
worker, or closing tasks. Unknown native provenance stays unconfirmed: UserPromptSubmit
alone is not proof of human work because automatic continuations can create prompts.
The context instructs the actual root orchestrator to verify origin and call
`hh_tracking` using the stable `turn_id:human` event identity. Technical observation
uses `turn_id:observed`; retries do not create new human activity. Explicit
synthetic/subagent events are excluded, and undocumented positive human flags are
not treated as authority. No Stop or SessionEnd closing hook is installed.

This is contextual assistance, **not guaranteed native UI questioning** or a
background timer. Desktop hook delivery, hook trust and the installed runtime still
need a live smoke test; cloud-orchestrated hook execution is outside this local
adapter. Claude/OpenCode retain instruction-driven tracking, not invented hook
support. Codex human-task tracking does not imply Codex token/cost ingestion.

For a safe local check, run `go test ./... -count=1`; the executable integration
test builds into a temporary directory and uses temporary HOME/database/config
paths. It exercises root confirmation, initialization, task start/close, reused
chats, duplicate events and two-hour continuity without sleeping.

### Recovering a lost tracking question

A repeated event does not allocate another question or advance human activity.
The root chat can recover its existing token and pending question after a lost
response, and may relay it if it was not already shown or answered. A question
already shown must not be repeated; another chat cannot recover its owner token.

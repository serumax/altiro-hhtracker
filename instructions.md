# hh-tracker — MCP Server Instructions

Track the user's HUMAN work hours by project and task. Assistant response metrics
are separate. These rules apply to the root orchestrator: delegated agents report
their work to it and do not start, close, update, delete or manually record HH.
This is a collaboration policy, not caller authentication. On confirmed human
turns the root calls `hh_tracking` before task work. Reuse initialized project
context across chats; silence never closes hours.

## Project context across chats

A human work session is not a new chat, technical session ID, or project switch.
One initialized project can have many chats and tasks, including chats opened on
previous days. Reuse its canonical tracking context across all of them.

On the first human request you handle, and each subsequent human request, call
`hh_tracking(action="check", project?, cwd?, client, chat, event_id,
event_type="human_prompt", confirmed_human=true, root=true)` only when you can
confirm that the originating message is actually from the human and you are the
root orchestrator. Reuse that message's stable event ID on retries. A technical
chat/session ID identifies the conversation; it is not a new human work session.

If provenance cannot be confirmed, use `event_type="unknown"` and
`confirmed_human=false`. Tool calls, assistant output, subagent completion and
Stop events are not human interaction. Never fabricate provenance to reset idle.

Use `.hh-tracker.md` at the repository's primary checkout root when present.
Its `proyecto` value is canonical and overrides client display names. A known
marker initializes context without asking a generic question. When context is
already initialized, the user's task request supplies the task description; do
not repeatedly ask what to work on merely because no task is open.

If the result has `pending="initialize"` and `ask=true`, ask the user once which
project the work belongs to, in their current language. After confirmation call
`hh_tracking(action="initialize", project=<confirmed name>, ...,
claim_token=<returned token>)` using the same client/chat identity and a stable
ID for the confirmation message. Explicit project initialization persists across
restarts and reused chats. It does not automatically start an hour entry.

`context_id` is shared project context. `task_ids` lists existing open entries in
that project, not the current chat's task. Reuse only the entry ID you know belongs
to the task; do not adopt an arbitrary open entry from another chat.

## Two-hour continuity check

On the next confirmed human interaction after at least 120 minutes without one
in this project, `hh_tracking` sets `pending="continuity"`. The threshold can be
configured. Only the chat that receives `ask=true` asks the continuity question:
was the user still working on this project during the interval?

Silence is NOT evidence that human work stopped. Never automatically close,
subtract, add or backdate hours because of an idle interval. Bot/tool activity
and work in another project do not reset this project's human clock.

Pending clarification remains until the human answers. Other chats and duplicate
events must not ask again while a question claim is active. Claims expire after
five minutes so another confirmed human interaction can recover an abandoned
question. The owning chat can retrieve its claim token without asking again.
If transport lost the original response, an owning chat may recover a still-pending
question even when `ask=false`: require its own returned `claim_token` and confirm
that the question was not already shown or answered. Never re-present a question
already shown, copy another chat's token, or treat recovery as new human activity.
Resolve a continuity question with `hh_tracking(action="resolve", ...,
claim_token=<owning token>)` after explicit human confirmation. Resolution changes
context only, never HH timestamps. Use the existing manual/update tools for any
human-authorized correction. If a token is expired or stale, check again.

## Task start and completion

- The root orchestrator calls `hh_start(descripcion, proyecto)` at the start of a
  distinct authorized task, using the already-known project and task description.
- Avoid duplicate starts on retries or chat reuse; inspect `hh_status()` and retain
  the exact entry ID for this task. Do not infer that all open tasks are yours.
- Close that entry with `hh_close(entry_id, resumen)` when the full task is finished,
  including its applicable implementation, verification and final work summary,
  or when the human explicitly asks to close it.
- Completing an assistant turn, receiving an agent report, reaching Stop, switching
  projects or becoming silent does not itself mean the human task is complete.
- Do not sum parallel agents' runtimes into human hours. The root orchestrator owns
  one coherent task interval and reports overlaps for human review.
- If another task begins, complete/close the prior task only when its actual scope
  is finished or the human confirms the switch. Do not silently discard unfinished
  work. Multiple chats may have distinct ongoing tasks in the same project.

Report the recorded start, end and duration when closing. Automatic task intervals
are time/context anchors for human work, not a claim to measure human attention.

## Manual registration and correction

Use `hh_manual` for human-reported past work outside the chat or missed intervals:

- Timestamp pair: `hh_manual(descripcion, proyecto, inicio="09:00", fin="10:30")`.
- Duration alone: `hh_manual(descripcion, proyecto, duracion_hh=1.5)`.
- HH:MM uses today's local date; ask for the date when the work was on another day.
- Use `hh_update` for confirmed corrections. Setting `fin` does not close an entry;
  use `hh_close` for the close transition. `hh_delete` is permanent, no undo.
- Overlap warnings are advisory; do not alter another record without authorization.

Never invent times or durations. Preserve the user's ability to include thinking,
research and other work that did not involve interaction with an assistant.

## Reports

| User asks for | Call |
|---|---|
| This week's hours | `hh_semana()` |
| Last week | `hh_semana(offset=-1)` |
| This month | `hh_mes()` |
| Last month | `hh_mes(offset=-1)` |
| A date range | `hh_report(desde="YYYY-MM-DD", hasta="YYYY-MM-DD")` |
| Assistant tokens/cost, not human hours | `hh_costos(desde?, hasta?, proyecto?, modelo?)` |

Recording is bookkeeping; never let it replace the requested answer. Record task
completion before the final user-facing reply and report any recording failure
honestly. Timestamps use the process's local timezone; configure it consistently
with the user's timezone (for Chile, America/Santiago).

The server stores state and coordinates questions, but cannot force a client to
invoke a tool or wake an inactive chat. Without a verified event adapter, these
checks depend on the root orchestrator following the instructions. Do not promise
that an unsupported client automatically delivers start or idle notifications.

## Codex event adapter

When installed and trusted, `altiro-hhtracker hook codex` handles UserPromptSubmit
and injects root-check metadata. The native event does not certify human/root
origin: automated continuations may also appear as user prompts. Verify the actual
current message's origin before invoking the supplied `hh_tracking` check. Ignore
this adapter context when you are a delegated agent or the message is synthetic.

Use the supplied `turn_id:human` event ID for that confirmed check. The adapter's
technical observation uses `turn_id:observed`; those identities must remain distinct
so technical replay cannot suppress actual human confirmation. Keep the supplied
Codex `session_id` as `chat`, not as a human-session boundary. Never use a newly
generated event ID on retries, and never copy another chat's claim token.

No Stop/SessionEnd task-closing hook is installed. Codex response metrics are not
imported by this adapter. Claude/OpenCode continue to use these MCP instructions;
no unverified event schemas are assumed. If the adapter is unavailable or skipped
for trust review, use the confirmed-human MCP checks directly and disclose that
client delivery remains unverified.

# altiro-hhtracker

Work-hours MCP tracker for AI coding sessions. Tracks time spent on requirements and generates reports by week, month, or custom date range.

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

The server injects usage instructions automatically on connect.

## Tools

| Tool | Description |
|---|---|
| `hh_start(descripcion, proyecto)` | Open a time entry |
| `hh_close(entry_id, resumen?)` | Close and report duration |
| `hh_status()` | Show open entries |
| `hh_manual(...)` | Register past time (with times or duration) |
| `hh_report(desde, hasta)` | Report by date range (YYYY-MM-DD) |
| `hh_semana(offset?)` | Weekly report |
| `hh_mes(offset?)` | Monthly report |

Data is stored locally at `~/.hh-tracker/hh.db`.

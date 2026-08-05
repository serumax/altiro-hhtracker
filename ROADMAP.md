# hh-tracker — Roadmap

## Estado actual
- server.py funcional en Python + FastMCP
- SQLite en ~/.hh-tracker/hh.db
- instructions.md definido

## Próxima sesión — Go binary

### 1. Estructura del proyecto Go
```
hh-tracker/
    main.go          ← MCP server + tool handlers
    db.go            ← SQLite schema + queries
    format.go        ← fmt_duracion, date helpers
    instructions.md  ← se embebe en el binario con //go:embed
    go.mod
```

### 2. Dependencias Go
- `github.com/mark3labs/mcp-go` — MCP server
- `github.com/mattn/go-sqlite3` — SQLite driver

### 3. El instructions.md se embebe así
```go
//go:embed instructions.md
var instructions string
// Se inyecta al cliente al iniciar el servidor
```

### 4. GitHub Actions
- Build matrix: darwin/arm64, darwin/amd64, linux/amd64
- Release automático en cada tag

### 5. Homebrew tap
- Repo: tuusuario/homebrew-hh-tracker
- Formula apunta al release de GitHub

## Pendiente definir
- Nombre del binario y organización GitHub
- ¿Multi-usuario o solo local?
- ¿Cloud sync (como Engram) o solo local?

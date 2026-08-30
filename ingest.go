package main

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// deriveProyecto guesses a project name for a working directory. It walks up
// looking for a .git directory. When found it prefers the origin remote's repo
// name, falling back to the basename of the git root. With no .git anywhere it
// returns the basename of dir.
func deriveProyecto(dir string) string {
	if dir == "" {
		return ""
	}
	root := gitRoot(dir)
	if root == "" {
		return filepath.Base(dir)
	}
	if name := originRepoName(filepath.Join(root, ".git", "config")); name != "" {
		return name
	}
	return filepath.Base(root)
}

// gitRoot walks up from dir until it finds a directory containing .git.
func gitRoot(dir string) string {
	cur := dir
	for {
		if fi, err := os.Stat(filepath.Join(cur, ".git")); err == nil {
			_ = fi
			return cur
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return ""
		}
		cur = parent
	}
}

// originRepoName parses a git config file and returns the last path segment of
// the [remote "origin"] url, without a trailing ".git". Returns "" if not found.
func originRepoName(configPath string) string {
	f, err := os.Open(configPath)
	if err != nil {
		return ""
	}
	defer f.Close()

	inOrigin := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			inOrigin = strings.EqualFold(line, `[remote "origin"]`)
			continue
		}
		if !inOrigin {
			continue
		}
		if !strings.HasPrefix(line, "url") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		return repoNameFromURL(strings.TrimSpace(line[eq+1:]))
	}
	return ""
}

// repoNameFromURL turns a git remote URL into its bare repo name.
func repoNameFromURL(url string) string {
	url = strings.TrimSpace(url)
	url = strings.TrimSuffix(url, "/")
	url = strings.TrimSuffix(url, ".git")
	// Split on both ':' (scp-style) and '/'.
	url = strings.ReplaceAll(url, ":", "/")
	seg := strings.Split(url, "/")
	if len(seg) == 0 {
		return ""
	}
	return seg[len(seg)-1]
}

// ---- opencode ingester --------------------------------------------------------

type opencodeMessage struct {
	Role string `json:"role"`
	Path struct {
		Cwd  string `json:"cwd"`
		Root string `json:"root"`
	} `json:"path"`
	Cost   float64 `json:"cost"`
	Tokens struct {
		Input  int64 `json:"input"`
		Output int64 `json:"output"`
		Cache  struct {
			Read  int64 `json:"read"`
			Write int64 `json:"write"`
		} `json:"cache"`
	} `json:"tokens"`
	ModelID    string `json:"modelID"`
	ProviderID string `json:"providerID"`
	Time       struct {
		Created   int64 `json:"created"`
		Completed int64 `json:"completed"`
	} `json:"time"`
	Error json.RawMessage `json:"error"`
}

// ingestOpencode scans an opencode sqlite database for assistant responses and
// records the new ones in the responses table. It is idempotent: a per-source
// watermark plus the UNIQUE(source, external_id) constraint prevent re-import.
func ingestOpencode(db *sql.DB, srcPath string, now time.Time) (int, error) {
	if _, err := os.Stat(srcPath); err != nil {
		return 0, nil
	}

	src, err := sql.Open("sqlite", "file:"+srcPath+"?mode=ro")
	if err != nil {
		return 0, nil
	}
	defer src.Close()

	var watermark int64
	if wm, err := getWatermark(db, "opencode"); err != nil {
		return 0, err
	} else if wm != "" {
		watermark, _ = strconv.ParseInt(wm, 10, 64)
	}

	rows, err := src.Query(
		`SELECT m.id, m.time_created, m.data, s.directory, p.name
		 FROM message m
		 JOIN session s ON s.id = m.session_id
		 LEFT JOIN project p ON p.id = s.project_id
		 WHERE m.time_created > ?
		 ORDER BY m.time_created ASC`, watermark)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	maxSeen := watermark
	inserted := 0
	for rows.Next() {
		var (
			id          string
			timeCreated int64
			data        string
			directory   sql.NullString
			projectName sql.NullString
		)
		if err := rows.Scan(&id, &timeCreated, &data, &directory, &projectName); err != nil {
			return inserted, err
		}
		if timeCreated > maxSeen {
			maxSeen = timeCreated
		}

		var m opencodeMessage
		if err := json.Unmarshal([]byte(data), &m); err != nil {
			continue
		}
		if m.Role != "assistant" {
			continue
		}
		if len(m.Error) > 0 && string(m.Error) != "null" {
			continue
		}
		if m.Time.Completed == 0 {
			continue
		}

		modelo := m.ModelID
		if m.ProviderID != "" {
			modelo = m.ProviderID + "/" + m.ModelID
		}

		proyecto := ""
		if projectName.Valid && projectName.String != "" {
			proyecto = projectName.String
		} else if directory.Valid && directory.String != "" {
			proyecto = deriveProyecto(directory.String)
		} else if m.Path.Cwd != "" {
			proyecto = deriveProyecto(m.Path.Cwd)
		}

		entryID, err := singleOpenEntryID(db)
		if err != nil {
			return inserted, err
		}

		r := Response{
			Ts:               time.UnixMilli(m.Time.Completed).Local().Format(tsLayout),
			Proyecto:         proyecto,
			Cliente:          "opencode",
			Modelo:           modelo,
			DuracionS:        sql.NullFloat64{Float64: float64(m.Time.Completed-m.Time.Created) / 1000.0, Valid: true},
			TokensIn:         m.Tokens.Input,
			TokensOut:        m.Tokens.Output,
			TokensCacheWrite: m.Tokens.Cache.Write,
			TokensCacheRead:  m.Tokens.Cache.Read,
			CostoUSD:         m.Cost,
			EntryID:          entryID,
			Source:           "opencode",
			ExternalID:       id,
		}
		ok, err := insertResponse(db, r)
		if err != nil {
			return inserted, err
		}
		if ok {
			inserted++
		}
	}
	if err := rows.Err(); err != nil {
		return inserted, err
	}

	if maxSeen != watermark {
		if err := setWatermark(db, "opencode", strconv.FormatInt(maxSeen, 10)); err != nil {
			return inserted, err
		}
	}
	return inserted, nil
}

// ---- Claude Code ingester ---------------------------------------------------

type claudeLine struct {
	Type        string `json:"type"`
	UUID        string `json:"uuid"`
	Timestamp   string `json:"timestamp"`
	Cwd         string `json:"cwd"`
	IsSidechain bool   `json:"isSidechain"`
	Message     struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage *struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// ingestClaude scans Claude Code transcript files (*.jsonl) under projectsDir
// for assistant responses. Each file keeps a byte-offset watermark so only the
// bytes appended since the last run are parsed.
func ingestClaude(db *sql.DB, projectsDir string, now time.Time) (int, error) {
	if _, err := os.Stat(projectsDir); err != nil {
		return 0, nil
	}
	files, err := filepath.Glob(filepath.Join(projectsDir, "*", "*.jsonl"))
	if err != nil {
		return 0, err
	}

	total := 0
	for _, path := range files {
		abs, err := filepath.Abs(path)
		if err != nil {
			abs = path
		}
		n, err := ingestClaudeFile(db, abs)
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

func ingestClaudeFile(db *sql.DB, path string) (int, error) {
	source := "claude-code:" + path

	var offset int64
	if wm, err := getWatermark(db, source); err != nil {
		return 0, err
	} else if wm != "" {
		offset, _ = strconv.ParseInt(wm, 10, 64)
	}

	f, err := os.Open(path)
	if err != nil {
		return 0, nil
	}
	defer f.Close()
	if _, err := f.Seek(offset, 0); err != nil {
		return 0, err
	}

	reader := bufio.NewReader(f)
	consumed := offset
	inserted := 0
	var lastUser time.Time
	var haveLastUser bool

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			// Incomplete trailing line: do not advance past it.
			break
		}
		consumed += int64(len(line))

		var cl claudeLine
		if e := json.Unmarshal([]byte(strings.TrimSpace(line)), &cl); e != nil {
			continue
		}

		if cl.Type == "user" && cl.Timestamp != "" {
			if ts, e := time.Parse(time.RFC3339, cl.Timestamp); e == nil {
				lastUser = ts
				haveLastUser = true
			}
		}

		if cl.Type != "assistant" || cl.Message.Model == "" || cl.Message.Usage == nil {
			continue
		}
		if cl.IsSidechain {
			continue
		}
		// Claude Code writes each assistant message several times with distinct
		// line-level uuids but a stable message.id. Dedup on message.id so the
		// UNIQUE(source, external_id) constraint collapses the copies and the
		// first (earliest) occurrence's timestamp and tokens are the ones kept.
		if cl.Message.ID == "" {
			continue
		}

		tsParsed, e := time.Parse(time.RFC3339, cl.Timestamp)
		if e != nil {
			continue
		}

		dur := sql.NullFloat64{}
		if haveLastUser {
			dur = sql.NullFloat64{Float64: tsParsed.Sub(lastUser).Seconds(), Valid: true}
		}

		entryID, err := singleOpenEntryID(db)
		if err != nil {
			return inserted, err
		}

		r := Response{
			Ts:               tsParsed.Local().Format(tsLayout),
			Proyecto:         deriveProyecto(cl.Cwd),
			Cliente:          "claude-code",
			Modelo:           cl.Message.Model,
			DuracionS:        dur,
			TokensIn:         cl.Message.Usage.InputTokens,
			TokensOut:        cl.Message.Usage.OutputTokens,
			TokensCacheWrite: cl.Message.Usage.CacheCreationInputTokens,
			TokensCacheRead:  cl.Message.Usage.CacheReadInputTokens,
			CostoUSD:         0,
			EntryID:          entryID,
			Source:           "claude-code",
			ExternalID:       cl.Message.ID,
		}
		ok, err := insertResponse(db, r)
		if err != nil {
			return inserted, err
		}
		if ok {
			inserted++
		}
	}

	if consumed != offset {
		if err := setWatermark(db, source, strconv.FormatInt(consumed, 10)); err != nil {
			return inserted, err
		}
	}
	return inserted, nil
}

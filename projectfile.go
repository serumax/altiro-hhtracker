package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ProjectFile is the parsed content of a .hh-tracker.md project file.
type ProjectFile struct {
	Proyecto string
	Alias    []string
	Path     string
}

// projectFileName is the per-project marker file that pins the canonical name.
const projectFileName = ".hh-tracker.md"

// resolvedProyectoCache memoizes resolveProyecto results (dir -> proyecto).
var resolvedProyectoCache sync.Map

// parseProjectFile reads the markdown frontmatter block (between a leading
// "---" line and the next "---" line) and extracts proyecto and alias. It never
// errors: missing frontmatter yields the zero value, malformed input is parsed
// best effort.
func parseProjectFile(content []byte) ProjectFile {
	var pf ProjectFile

	lines := strings.Split(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n")

	// The frontmatter must start on the very first line.
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return pf
	}

	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end == -1 {
		return pf
	}

	for i := 1; i < end; i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		switch {
		case strings.HasPrefix(trimmed, "proyecto:"):
			pf.Proyecto = strings.TrimSpace(strings.TrimPrefix(trimmed, "proyecto:"))

		case strings.HasPrefix(trimmed, "alias:"):
			rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "alias:"))
			if rest != "" {
				pf.Alias = parseInlineList(rest)
			} else {
				// YAML block form: subsequent "  - value" lines.
				for j := i + 1; j < end; j++ {
					item := strings.TrimSpace(lines[j])
					if !strings.HasPrefix(item, "-") {
						break
					}
					v := strings.TrimSpace(strings.TrimPrefix(item, "-"))
					if v != "" {
						pf.Alias = append(pf.Alias, v)
					}
				}
			}
		}
	}

	return pf
}

// parseInlineList parses a YAML inline list like "[a, b, c]" into its elements.
func parseInlineList(s string) []string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	var out []string
	for _, part := range strings.Split(s, ",") {
		v := strings.Trim(strings.TrimSpace(part), `"'`)
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

// findProjectFile walks up from startDir to the filesystem root and returns the
// first .hh-tracker.md it finds, parsed, with ok=true. ok=false when none exist.
func findProjectFile(startDir string) (ProjectFile, bool) {
	cur := startDir
	for {
		candidate := filepath.Join(cur, projectFileName)
		if content, err := os.ReadFile(candidate); err == nil {
			pf := parseProjectFile(content)
			pf.Path = candidate
			return pf, true
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return ProjectFile{}, false
		}
		cur = parent
	}
}

// resolveProyecto returns the canonical project name for a directory: the
// proyecto from the nearest .hh-tracker.md when present, otherwise the
// git/basename-derived name. Results are memoized per dir.
func resolveProyecto(dir string) string {
	if dir == "" {
		return ""
	}
	if v, ok := resolvedProyectoCache.Load(dir); ok {
		return v.(string)
	}
	name := deriveProyecto(dir)
	if pf, ok := findProjectFile(dir); ok && pf.Proyecto != "" {
		name = pf.Proyecto
	}
	resolvedProyectoCache.Store(dir, name)
	return name
}

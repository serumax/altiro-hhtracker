package main

import (
	"fmt"
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

// gitMainRoot returns the main checkout root for dir: the parent of git's
// common-dir. From inside a linked worktree this is the primary checkout (where
// a committed .hh-tracker.md lives and where `init` writes one), not the
// worktree directory. ok is false when dir is not inside a git repository.
func gitMainRoot(dir string) (string, bool) {
	out := gitOutput(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if out == "" {
		return "", false
	}
	return filepath.Dir(out), true
}

// mainCheckoutRoot resolves the primary checkout root for dir, erroring when dir
// is not inside a git repository.
func mainCheckoutRoot(dir string) (string, error) {
	root, ok := gitMainRoot(dir)
	if !ok {
		return "", fmt.Errorf("no estás dentro de un repositorio git")
	}
	return root, nil
}

// resolveProyecto returns the canonical project name for a directory. Resolution
// order: (1) proyecto from a .hh-tracker.md at the git main-checkout root — this
// is what makes linked worktrees resolve to the shared canonical name; (2)
// proyecto from a .hh-tracker.md found walking up the literal path; (3) the
// git/basename-derived name. Results are memoized per dir.
func resolveProyecto(dir string) string {
	if dir == "" {
		return ""
	}
	if v, ok := resolvedProyectoCache.Load(dir); ok {
		return v.(string)
	}
	name := computeResolvedProyecto(dir)
	resolvedProyectoCache.Store(dir, name)
	return name
}

func computeResolvedProyecto(dir string) string {
	if root, ok := gitMainRoot(dir); ok {
		if content, err := os.ReadFile(filepath.Join(root, projectFileName)); err == nil {
			if pf := parseProjectFile(content); pf.Proyecto != "" {
				return pf.Proyecto
			}
		}
	}
	if pf, ok := findProjectFile(dir); ok && pf.Proyecto != "" {
		return pf.Proyecto
	}
	return deriveProyecto(dir)
}

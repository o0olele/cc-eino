package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
)

const (
	// globDefaultLimit mirrors globLimits?.maxResults ?? 100 in the TS source.
	globDefaultLimit = 100
)

// vcsDirectories are excluded from searches to avoid VCS metadata noise.
var vcsDirectories = []string{".git", ".svn", ".hg", ".bzr", ".jj", ".sl"}

type GlobToolInput struct {
	Pattern string `json:"pattern" jsonschema_description:"The glob pattern to match files against" jsonschema:"required"`
	Path    string `json:"path" jsonschema_description:"The directory to search in. If not specified, the current working directory will be used. IMPORTANT: Omit this field to use the default directory. DO NOT enter \"undefined\" or \"null\" - simply omit it for the default behavior. Must be a valid directory path if provided."`
}

type GlobToolOutput struct {
	DurationMs float32  `json:"durationMs" jsonschema_description:"Time taken to execute the search in milliseconds"`
	NumFiles   int      `json:"numFiles" jsonschema_description:"Total number of files found"`
	Filenames  []string `json:"filenames" jsonschema_description:"Array of file paths that match the pattern"`
	Truncated  bool     `json:"truncated" jsonschema_description:"Whether results were truncated (limited to 100 files)"`
}

var globToolInputSchema, _ = utils.GoStruct2ParamsOneOf[GlobToolInput]()

type GlobTool struct {
}

func (GlobTool) Name() string {
	return "Glob"
}

func (GlobTool) Description() string {
	return `- Fast file pattern matching tool that works with any codebase size
- Supports glob patterns like "**/*.js" or "src/**/*.ts"
- Returns matching file paths sorted by modification time
- Use this tool when you need to find files by name patterns
- When you are doing an open ended search that may require multiple rounds of globbing and grepping, use the Agent tool instead`
}

func (t GlobTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name:        t.Name(),
		Desc:        t.Description(),
		ParamsOneOf: globToolInputSchema,
	}, nil

}

func (t GlobTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	input := GlobToolInput{}
	err := json.Unmarshal([]byte(argumentsInJSON), &input)
	if err != nil {
		return "", fmt.Errorf("failed to unmarshal arguments: %w", err)
	}

	// Resolve search root.
	searchRoot, _ := ctx.Value("WorkDir").(string)
	if input.Path != "" {
		if !filepath.IsAbs(input.Path) {
			searchRoot = filepath.Join(searchRoot, input.Path)
		} else {
			searchRoot = input.Path
		}
		if info, errStat := os.Stat(searchRoot); errStat != nil {
			return "", fmt.Errorf("Directory does not exist: %w", errStat)
		} else if !info.IsDir() {
			return "", fmt.Errorf("Path is not a directory: %s", searchRoot)
		}
	}

	var start = time.Now()
	var files []string
	var truncated bool

	if rgPath, lookErr := exec.LookPath("rg"); lookErr == nil {
		files, truncated, err = globWithRipgrep(rgPath, input.Pattern, searchRoot, ctx)
	} else {
		files, truncated, err = globWithGoWalk(input.Pattern, searchRoot)
	}
	if err != nil {
		return "", fmt.Errorf("error: %w", err)
	}

	// Convert absolute paths → relative to workDir to save tokens.
	rel := make([]string, 0, len(files))
	for _, f := range files {
		if r, err := filepath.Rel(searchRoot, f); err == nil {
			rel = append(rel, filepath.ToSlash(r))
		} else {
			rel = append(rel, f)
		}
	}

	_ = start // durationMs available if needed by callers

	if len(rel) == 0 {
		return "", fmt.Errorf("No files found")
	}

	var sb strings.Builder
	for _, f := range rel {
		sb.WriteString(f)
		sb.WriteByte('\n')
	}
	if truncated {
		sb.WriteString("(Results are truncated. Consider using a more specific path or pattern.)")
	}

	return sb.String(), nil
}

// ─────────────────────────────────────────────
// Ripgrep backend
// ─────────────────────────────────────────────

// globWithRipgrep runs:
//
//	rg --files --glob <pattern> --sort=modified --no-ignore --hidden [searchDir]
//
// It handles absolute patterns by splitting them into a base directory and a
// relative sub-pattern, mirroring extractGlobBaseDirectory() in the TS source.
func globWithRipgrep(
	rgPath, pattern, searchDir string, ctx context.Context,
) (files []string, truncated bool, err error) {
	// Handle absolute patterns: extract static base dir + relative pattern.
	actualDir := searchDir
	actualPattern := pattern
	if filepath.IsAbs(pattern) {
		base, rel := extractGlobBaseDir(pattern)
		if base != "" {
			actualDir = base
			actualPattern = rel
		}
	}

	args := []string{
		"--files",
		"--glob", actualPattern,
		"--sort=modified", // oldest-first; matches TS behaviour
		"--no-ignore",
		"--hidden",
	}

	cmd := exec.CommandContext(ctx, rgPath, append(args, actualDir)...)
	cmd.Dir = actualDir
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	_ = cmd.Run() // exit 1 = no matches

	paths := splitLines(stdout.String())

	// Convert relative (rg) → absolute.
	abs := make([]string, 0, len(paths))
	for _, p := range paths {
		if filepath.IsAbs(p) {
			abs = append(abs, p)
		} else {
			abs = append(abs, filepath.Join(actualDir, p))
		}
	}

	truncated = len(abs) > globDefaultLimit
	if truncated {
		abs = abs[:globDefaultLimit]
	}
	return abs, truncated, nil
}

// extractGlobBaseDir splits a pattern like "/foo/bar/**/*.ts" into
// baseDir="/foo/bar" and relativePattern="**/*.ts".
// Mirrors extractGlobBaseDirectory() in the TS source.
func extractGlobBaseDir(pattern string) (baseDir, relativePattern string) {
	// Find first glob metacharacter.
	metaIdx := strings.IndexAny(pattern, "*?[{")
	if metaIdx < 0 {
		// Literal path — use its directory.
		return filepath.Dir(pattern), filepath.Base(pattern)
	}

	staticPrefix := pattern[:metaIdx]
	lastSep := strings.LastIndexAny(staticPrefix, "/\\")
	if lastSep < 0 {
		return "", pattern // no separator before glob
	}

	base := staticPrefix[:lastSep]
	if base == "" {
		base = "/"
	}
	return base, pattern[lastSep+1:]
}

// ─────────────────────────────────────────────
// Pure-Go fallback backend
// ─────────────────────────────────────────────

// globWithGoWalk implements glob matching via filepath.WalkDir.
// It supports ** (double-star) patterns by converting them to a two-phase
// match: walk the entire tree and apply doublestar-style matching per entry.
func globWithGoWalk(
	pattern, searchRoot string,
) (files []string, truncated bool, err error) {
	type entry struct {
		abs     string
		modTime time.Time
	}
	var matches []entry

	walkErr := filepath.WalkDir(searchRoot, func(path string, d os.DirEntry, werr error) error {
		if werr != nil {
			return nil
		}
		if d.IsDir() {
			// Skip VCS dirs.
			for _, vcs := range vcsDirectories {
				if d.Name() == vcs {
					return filepath.SkipDir
				}
			}
			return nil
		}

		// Match relative path against pattern using doublestar semantics.
		rel, _ := filepath.Rel(searchRoot, path)
		rel = filepath.ToSlash(rel)

		if matchGlobPattern(pattern, rel) {
			info, _ := d.Info()
			var mt time.Time
			if info != nil {
				mt = info.ModTime()
			}
			matches = append(matches, entry{abs: path, modTime: mt})
		}
		return nil
	})
	if walkErr != nil {
		return nil, false, walkErr
	}

	// Sort oldest-first (ascending modtime) to match rg --sort=modified.
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].modTime.Equal(matches[j].modTime) {
			return matches[i].abs < matches[j].abs
		}
		return matches[i].modTime.Before(matches[j].modTime)
	})

	abs := make([]string, len(matches))
	for i, m := range matches {
		abs[i] = m.abs
	}

	truncated = len(abs) > globDefaultLimit
	if truncated {
		abs = abs[:globDefaultLimit]
	}
	return abs, truncated, nil
}

// matchGlobPattern matches a slash-separated relative path against a glob
// pattern that may contain ** (matches any number of path segments).
//
// Rules:
//   - ** matches zero or more complete path segments.
//   - * matches any characters except '/'.
//   - ? matches any single character except '/'.
//   - All other filepath.Match rules apply per segment.
func matchGlobPattern(pattern, path string) bool {
	// Normalise separators.
	pattern = filepath.ToSlash(pattern)
	path = filepath.ToSlash(path)

	return doublestarMatch(pattern, path)
}

// doublestarMatch is a recursive glob matcher with ** support.
func doublestarMatch(pattern, name string) bool {
	for len(pattern) > 0 {
		// Find next **.
		dIdx := strings.Index(pattern, "**")
		if dIdx < 0 {
			// No more **, use standard match for the rest.
			matched, err := filepath.Match(pattern, name)
			return err == nil && matched
		}

		// Match the part before **.
		pre := pattern[:dIdx]
		rest := pattern[dIdx+2:]

		// Trim leading/trailing slashes from the separator around **.
		if len(rest) > 0 && rest[0] == '/' {
			rest = rest[1:]
		}

		// pre must match a prefix of name.
		if pre != "" {
			pre = strings.TrimRight(pre, "/")
			prefix := pre + "/"
			// The literal prefix must match the name start.
			if !strings.HasPrefix(name, prefix) {
				// Try filepath.Match on the pre segment against the first segment.
				nameParts := strings.SplitN(name, "/", 2)
				matched, err := filepath.Match(pre, nameParts[0])
				if err != nil || !matched {
					return false
				}
				if len(nameParts) == 1 {
					name = ""
				} else {
					name = nameParts[1]
				}
			} else {
				name = name[len(prefix):]
			}
		}

		if rest == "" {
			// ** at end matches everything remaining.
			return true
		}

		// ** can match zero or more segments; try each suffix of name.
		// Match zero segments first.
		if doublestarMatch(rest, name) {
			return true
		}
		parts := strings.Split(name, "/")
		for i := 1; i <= len(parts); i++ {
			suffix := strings.Join(parts[i:], "/")
			if doublestarMatch(rest, suffix) {
				return true
			}
		}
		return false
	}
	return name == ""
}

// splitLines splits s on newlines, discarding empty trailing lines.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(strings.TrimRight(s, "\n"), "\n")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
)

const (
	grepDefaultHeadLimit = 250
	grepMaxColumns       = 500
)

// commonTypeExtensions maps rg --type names to file extensions for the Go fallback.
var commonTypeExtensions = map[string][]string{
	"go":     {".go"},
	"js":     {".js", ".mjs", ".cjs"},
	"ts":     {".ts", ".mts", ".cts"},
	"tsx":    {".tsx"},
	"jsx":    {".jsx"},
	"py":     {".py"},
	"rust":   {".rs"},
	"java":   {".java"},
	"c":      {".c", ".h"},
	"cpp":    {".cpp", ".cc", ".cxx", ".hpp", ".hh"},
	"cs":     {".cs"},
	"rb":     {".rb"},
	"php":    {".php"},
	"swift":  {".swift"},
	"kotlin": {".kt", ".kts"},
	"scala":  {".scala"},
	"sh":     {".sh", ".bash"},
	"yaml":   {".yaml", ".yml"},
	"json":   {".json"},
	"toml":   {".toml"},
	"xml":    {".xml"},
	"html":   {".html", ".htm"},
	"css":    {".css"},
	"md":     {".md", ".markdown"},
	"sql":    {".sql"},
}

type GrepToolInput struct {
	Pattern    string `json:"pattern" jsonschema:"required" jsonschema_description:"The regular expression pattern to search for in file contents"`
	Path       string `json:"path,omitempty" jsonschema_description:"File or directory to search in (rg PATH). Defaults to current working directory."`
	Glob       string `json:"glob,omitempty" jsonschema_description:"Glob pattern to filter files (e.g. \"*.js\", \"**/*.tsx\") - maps to rg --glob"`
	OutputMode string `json:"output_mode,omitempty" jsonschema_description:"Output mode: \"content\" shows matching lines (supports -A/-B/-C context, -n line numbers, head_limit), \"files_with_matches\" shows file paths (supports head_limit), \"count\" shows match counts (supports head_limit). Defaults to \"files_with_matches\"."`
	ContextB   *int   `json:"-B,omitempty" jsonschema_description:"Number of lines to show before each match (rg -B). Requires output_mode: \"content\", ignored otherwise."`
	ContextA   *int   `json:"-A,omitempty" jsonschema_description:"Number of lines to show after each match (rg -A). Requires output_mode: \"content\", ignored otherwise."`
	ContextC   *int   `json:"-C,omitempty" jsonschema_description:"Alias for context."`
	Context    *int   `json:"context,omitempty" jsonschema_description:"Number of lines to show before and after each match (rg -C). Requires output_mode: \"content\", ignored otherwise."`
	LineNums   *bool  `json:"-n,omitempty" jsonschema_description:"Show line numbers in output (rg -n). Requires output_mode: \"content\", ignored otherwise. Defaults to true."`
	CaseInsens bool   `json:"-i,omitempty" jsonschema_description:"Case insensitive search (rg -i)"`
	Type       string `json:"type,omitempty" jsonschema_description:"File type to search (rg --type). Common types: js, py, rust, go, java, etc. More efficient than include for standard file types."`
	HeadLimit  *int   `json:"head_limit,omitempty" jsonschema_description:"Limit output to first N lines/entries, equivalent to \"| head -N\". Works across all output modes. Defaults to 250 when unspecified. Pass 0 for unlimited (use sparingly — large result sets waste context)."`
	Offset     int    `json:"offset,omitempty" jsonschema_description:"Skip first N lines/entries before applying head_limit, equivalent to \"| tail -n +N | head -N\". Works across all output modes. Defaults to 0."`
	Multiline  bool   `json:"multiline,omitempty" jsonschema_description:"Enable multiline mode where . matches newlines and patterns can span lines (rg -U --multiline-dotall). Default: false."`
}

var grepToolInputSchema, _ = utils.GoStruct2ParamsOneOf[GrepToolInput]()

type GrepTool struct{}

func (GrepTool) Name() string { return "Grep" }

func (GrepTool) Description() string {
	return `A powerful search tool built on ripgrep

  Usage:
  - ALWAYS use Grep for search tasks. NEVER invoke ` + "`grep`" + ` or ` + "`rg`" + ` as a Bash command. The Grep tool has been optimized for correct permissions and access.
  - Supports full regex syntax (e.g., "log.*Error", "function\s+\w+")
  - Filter files with glob parameter (e.g., "*.js", "**/*.tsx") or type parameter (e.g., "js", "py", "rust")
  - Output modes: "content" shows matching lines, "files_with_matches" shows only file paths (default), "count" shows match counts
  - Use Agent tool for open-ended searches requiring multiple rounds
  - Pattern syntax: Uses ripgrep (not grep) - literal braces need escaping (use ` + "`interface\\{\\}`" + ` to find ` + "`interface{}`" + ` in Go code)
  - Multiline matching: By default patterns match within single lines only. For cross-line patterns like ` + "`struct \\{[\\s\\S]*?field`" + `, use ` + "`multiline: true`"
}

func (t GrepTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name:        t.Name(),
		Desc:        t.Description(),
		ParamsOneOf: grepToolInputSchema,
	}, nil
}

func (t GrepTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	input := GrepToolInput{}
	if err := json.Unmarshal([]byte(argumentsInJSON), &input); err != nil {
		return "", fmt.Errorf("failed to unmarshal arguments: %w", err)
	}

	// Resolve search root.
	searchRoot, _ := ctx.Value("WorkDir").(string)
	if input.Path != "" {
		if filepath.IsAbs(input.Path) {
			searchRoot = input.Path
		} else {
			searchRoot = filepath.Join(searchRoot, input.Path)
		}
	}

	outputMode := input.OutputMode
	if outputMode == "" {
		outputMode = "files_with_matches"
	}

	showLineNums := true
	if input.LineNums != nil {
		showLineNums = *input.LineNums
	}

	if rgPath, err := exec.LookPath("rg"); err == nil {
		return grepWithRipgrep(ctx, rgPath, input, searchRoot, outputMode, showLineNums)
	}
	return grepWithGo(ctx, input, searchRoot, outputMode, showLineNums)
}

// ─────────────────────────────────────────────
// Ripgrep backend
// ─────────────────────────────────────────────

func grepWithRipgrep(
	ctx context.Context,
	rgPath string,
	input GrepToolInput,
	searchRoot, outputMode string,
	showLineNums bool,
) (string, error) {
	args := []string{"--hidden", "--max-columns", strconv.Itoa(grepMaxColumns)}

	// Exclude VCS directories.
	for _, dir := range vcsDirectories {
		args = append(args, "--glob", "!"+dir)
	}

	if input.Multiline {
		args = append(args, "-U", "--multiline-dotall")
	}
	if input.CaseInsens {
		args = append(args, "-i")
	}

	switch outputMode {
	case "files_with_matches":
		args = append(args, "-l")
	case "count":
		args = append(args, "-c")
	}

	if outputMode == "content" && showLineNums {
		args = append(args, "-n")
	}

	// Context flags for content mode (-C / context take precedence over -B/-A).
	if outputMode == "content" {
		switch {
		case input.Context != nil:
			args = append(args, "-C", strconv.Itoa(*input.Context))
		case input.ContextC != nil:
			args = append(args, "-C", strconv.Itoa(*input.ContextC))
		default:
			if input.ContextB != nil {
				args = append(args, "-B", strconv.Itoa(*input.ContextB))
			}
			if input.ContextA != nil {
				args = append(args, "-A", strconv.Itoa(*input.ContextA))
			}
		}
	}

	// Pattern — use -e if it starts with a dash to avoid flag confusion.
	if strings.HasPrefix(input.Pattern, "-") {
		args = append(args, "-e", input.Pattern)
	} else {
		args = append(args, input.Pattern)
	}

	if input.Type != "" {
		args = append(args, "--type", input.Type)
	}

	// Glob filters.
	if input.Glob != "" {
		for _, g := range parseGlobPatterns(input.Glob) {
			args = append(args, "--glob", g)
		}
	}

	args = append(args, searchRoot)

	cmd := exec.CommandContext(ctx, rgPath, args...)
	cmd.Dir = searchRoot
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	_ = cmd.Run() // exit 1 = no matches, not an error

	lines := splitLines(stdout.String())

	return formatGrepOutput(lines, outputMode, searchRoot, input.HeadLimit, input.Offset)
}

// ─────────────────────────────────────────────
// Pure-Go fallback backend
// ─────────────────────────────────────────────

func grepWithGo(
	ctx context.Context,
	input GrepToolInput,
	searchRoot, outputMode string,
	showLineNums bool,
) (string, error) {
	// Compile regex.
	flags := ""
	if input.CaseInsens {
		flags = "(?i)"
	}
	re, err := regexp.Compile(flags + input.Pattern)
	if err != nil {
		return "", fmt.Errorf("invalid pattern: %w", err)
	}

	// Collect candidate files.
	files, err := collectFiles(searchRoot, input.Glob, input.Type)
	if err != nil {
		return "", err
	}

	switch outputMode {
	case "content":
		return grepGoContent(ctx, files, re, searchRoot, input, showLineNums)
	case "count":
		return grepGoCount(ctx, files, re, searchRoot, input.HeadLimit, input.Offset)
	default: // files_with_matches
		return grepGoFilesWithMatches(ctx, files, re, searchRoot, input.HeadLimit, input.Offset)
	}
}

func collectFiles(searchRoot, globPattern, fileType string) ([]string, error) {
	var exts map[string]struct{}
	if fileType != "" {
		extList, ok := commonTypeExtensions[fileType]
		if ok {
			exts = make(map[string]struct{}, len(extList))
			for _, e := range extList {
				exts[e] = struct{}{}
			}
		}
	}

	type entry struct {
		path    string
		modTime time.Time
	}
	var entries []entry

	err := filepath.WalkDir(searchRoot, func(path string, d os.DirEntry, werr error) error {
		if werr != nil {
			return nil
		}
		if d.IsDir() {
			for _, vcs := range vcsDirectories {
				if d.Name() == vcs {
					return filepath.SkipDir
				}
			}
			return nil
		}

		rel, _ := filepath.Rel(searchRoot, path)
		rel = filepath.ToSlash(rel)

		if globPattern != "" && !matchGlobPattern(globPattern, rel) {
			return nil
		}
		if exts != nil {
			if _, ok := exts[strings.ToLower(filepath.Ext(path))]; !ok {
				return nil
			}
		}

		info, _ := d.Info()
		var mt time.Time
		if info != nil {
			mt = info.ModTime()
		}
		entries = append(entries, entry{path: path, modTime: mt})
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].modTime.Equal(entries[j].modTime) {
			return entries[i].path < entries[j].path
		}
		return entries[i].modTime.Before(entries[j].modTime)
	})

	paths := make([]string, len(entries))
	for i, e := range entries {
		paths[i] = e.path
	}
	return paths, nil
}

func grepGoFilesWithMatches(
	ctx context.Context,
	files []string,
	re *regexp.Regexp,
	searchRoot string,
	headLimit *int,
	offset int,
) (string, error) {
	var matched []string
	for _, f := range files {
		if ctx.Err() != nil {
			break
		}
		if fileHasMatch(f, re) {
			matched = append(matched, f)
		}
	}
	return formatGrepOutput(matched, "files_with_matches", searchRoot, headLimit, offset)
}

func grepGoCount(
	ctx context.Context,
	files []string,
	re *regexp.Regexp,
	searchRoot string,
	headLimit *int,
	offset int,
) (string, error) {
	var lines []string
	for _, f := range files {
		if ctx.Err() != nil {
			break
		}
		count := countMatches(f, re)
		if count > 0 {
			lines = append(lines, fmt.Sprintf("%s:%d", f, count))
		}
	}
	return formatGrepOutput(lines, "count", searchRoot, headLimit, offset)
}

func grepGoContent(
	ctx context.Context,
	files []string,
	re *regexp.Regexp,
	searchRoot string,
	input GrepToolInput,
	showLineNums bool,
) (string, error) {
	ctxLines := 0
	if input.Context != nil {
		ctxLines = *input.Context
	} else if input.ContextC != nil {
		ctxLines = *input.ContextC
	}
	before := ctxLines
	after := ctxLines
	if input.ContextB != nil {
		before = *input.ContextB
	}
	if input.ContextA != nil {
		after = *input.ContextA
	}

	var allLines []string
	for _, f := range files {
		if ctx.Err() != nil {
			break
		}
		lines, err := searchFileContent(f, re, before, after, showLineNums, searchRoot)
		if err != nil {
			continue
		}
		allLines = append(allLines, lines...)
	}
	return formatGrepOutput(allLines, "content", searchRoot, input.HeadLimit, input.Offset)
}

func fileHasMatch(path string, re *regexp.Regexp) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if re.Match(scanner.Bytes()) {
			return true
		}
	}
	return false
}

func countMatches(path string, re *regexp.Regexp) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	count := 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if re.Match(scanner.Bytes()) {
			count++
		}
	}
	return count
}

func searchFileContent(path string, re *regexp.Regexp, before, after int, showLineNums bool, searchRoot string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	relPath, err := filepath.Rel(searchRoot, path)
	if err != nil {
		relPath = path
	}
	relPath = filepath.ToSlash(relPath)

	// Read all lines for context support.
	var rawLines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		rawLines = append(rawLines, scanner.Text())
	}

	// Find matching line indices.
	var matchIdx []int
	for i, l := range rawLines {
		if re.MatchString(l) {
			matchIdx = append(matchIdx, i)
		}
	}
	if len(matchIdx) == 0 {
		return nil, nil
	}

	// Build output with context, deduplicating overlapping ranges.
	var out []string
	printed := make(map[int]bool)
	for _, idx := range matchIdx {
		start := idx - before
		if start < 0 {
			start = 0
		}
		end := idx + after
		if end >= len(rawLines) {
			end = len(rawLines) - 1
		}
		for i := start; i <= end; i++ {
			if printed[i] {
				continue
			}
			printed[i] = true
			line := rawLines[i]
			// Truncate long lines.
			if len(line) > grepMaxColumns {
				line = line[:grepMaxColumns]
			}
			if showLineNums {
				out = append(out, fmt.Sprintf("%s:%d:%s", relPath, i+1, line))
			} else {
				out = append(out, fmt.Sprintf("%s:%s", relPath, line))
			}
		}
	}
	return out, nil
}

// ─────────────────────────────────────────────
// Output formatting
// ─────────────────────────────────────────────

func formatGrepOutput(lines []string, outputMode, searchRoot string, headLimit *int, offset int) (string, error) {
	limited, appliedLimit := applyHeadLimit(lines, headLimit, offset)

	switch outputMode {
	case "content":
		// Relativize absolute paths in content lines (format: /abs/path:rest).
		rel := make([]string, len(limited))
		for i, line := range limited {
			rel[i] = relativizeLine(line, searchRoot)
		}
		result := strings.Join(rel, "\n")
		if appliedLimit > 0 {
			result += fmt.Sprintf("\n\n[Showing results with pagination = limit: %d", appliedLimit)
			if offset > 0 {
				result += fmt.Sprintf(", offset: %d", offset)
			}
			result += "]"
		}
		if result == "" {
			return "No matches found", nil
		}
		return result, nil

	case "count":
		// Relativize and sum totals.
		var totalMatches, fileCount int
		rel := make([]string, len(limited))
		for i, line := range limited {
			rel[i] = relativizeLine(line, searchRoot)
			if idx := strings.LastIndex(line, ":"); idx >= 0 {
				if n, err := strconv.Atoi(strings.TrimSpace(line[idx+1:])); err == nil {
					totalMatches += n
					fileCount++
				}
			}
		}
		content := strings.Join(rel, "\n")
		var sb strings.Builder
		if content != "" {
			sb.WriteString(content)
		}
		plural := func(n int, s string) string {
			if n == 1 {
				return s
			}
			return s + "s"
		}
		sb.WriteString(fmt.Sprintf("\n\nFound %d total %s across %d %s.",
			totalMatches, plural(totalMatches, "occurrence"),
			fileCount, plural(fileCount, "file"),
		))
		if appliedLimit > 0 {
			sb.WriteString(fmt.Sprintf(" with pagination = limit: %d", appliedLimit))
			if offset > 0 {
				sb.WriteString(fmt.Sprintf(", offset: %d", offset))
			}
		}
		if sb.Len() == 0 {
			return "No matches found", nil
		}
		return sb.String(), nil

	default: // files_with_matches
		if len(limited) == 0 {
			return "No files found", nil
		}
		rel := make([]string, len(limited))
		for i, line := range limited {
			if r, err := filepath.Rel(searchRoot, line); err == nil {
				rel[i] = filepath.ToSlash(r)
			} else {
				rel[i] = line
			}
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Found %d %s", len(rel), func() string {
			if len(rel) == 1 {
				return "file"
			}
			return "files"
		}()))
		if appliedLimit > 0 {
			sb.WriteString(fmt.Sprintf(" limit: %d", appliedLimit))
			if offset > 0 {
				sb.WriteString(fmt.Sprintf(", offset: %d", offset))
			}
		}
		sb.WriteByte('\n')
		sb.WriteString(strings.Join(rel, "\n"))
		return sb.String(), nil
	}
}

// applyHeadLimit slices items with offset and limit. Returns the sliced items
// and the effective limit if truncation occurred (0 means no truncation).
func applyHeadLimit(items []string, headLimit *int, offset int) ([]string, int) {
	if offset >= len(items) {
		return nil, 0
	}
	items = items[offset:]

	// headLimit == nil → default; headLimit == 0 → unlimited.
	if headLimit != nil && *headLimit == 0 {
		return items, 0
	}
	limit := grepDefaultHeadLimit
	if headLimit != nil {
		limit = *headLimit
	}
	if len(items) <= limit {
		return items, 0
	}
	return items[:limit], limit
}

// relativizeLine converts an absolute path prefix in a ripgrep output line
// (format "/abs/path:rest") to a path relative to searchRoot.
func relativizeLine(line, searchRoot string) string {
	colonIdx := strings.Index(line, ":")
	if colonIdx <= 0 {
		return line
	}
	filePart := line[:colonIdx]
	rest := line[colonIdx:]
	if r, err := filepath.Rel(searchRoot, filePart); err == nil {
		return filepath.ToSlash(r) + rest
	}
	return line
}

// parseGlobPatterns splits a glob string on whitespace, preserving brace expressions.
func parseGlobPatterns(glob string) []string {
	var out []string
	for _, raw := range strings.Fields(glob) {
		if strings.Contains(raw, "{") && strings.Contains(raw, "}") {
			out = append(out, raw)
		} else {
			for _, part := range strings.Split(raw, ",") {
				if p := strings.TrimSpace(part); p != "" {
					out = append(out, p)
				}
			}
		}
	}
	return out
}

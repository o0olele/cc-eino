package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
)

type Skill struct {
	Name        string
	Description string
	Body        string
	Path        string
}

type SkillLoader struct {
	skills map[string]*Skill
}

type LoadResult struct {
	Loaded  []string
	Skipped []string
	Errors  []error
}

func (r *LoadResult) String() string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Loaded %d skills", len(r.Loaded)))
	if len(r.Skipped) > 0 {
		sb.WriteString(fmt.Sprintf(", skipped %d", len(r.Skipped)))
	}
	if len(r.Errors) > 0 {
		sb.WriteString(fmt.Sprintf(", %d errors", len(r.Errors)))
	}
	return sb.String()
}

func NewSkillLoader(dir string) (*SkillLoader, *LoadResult) {
	sl := &SkillLoader{skills: make(map[string]*Skill)}
	result := &LoadResult{}

	if dir == "" {
		return sl, result
	}

	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return sl, result
	}

	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("walk error at %s: %w", path, err))
			return nil
		}
		if d.IsDir() || d.Name() != "SKILL.md" {
			return nil
		}

		skill, err := parseSkillFile(path)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("parse %s: %w", path, err))
			return nil
		}

		if skill.Name == "" {
			result.Skipped = append(result.Skipped, path)
			return nil
		}

		if _, exists := sl.skills[skill.Name]; exists {
			result.Errors = append(result.Errors, fmt.Errorf("duplicate skill name %q at %s", skill.Name, path))
			return nil
		}

		sl.skills[skill.Name] = skill
		result.Loaded = append(result.Loaded, skill.Name)
		return nil
	})

	return sl, result
}

func parseSkillFile(path string) (*Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	skill := &Skill{
		Path: path,
	}

	if !strings.HasPrefix(text, "---\n") {
		skill.Name = filepath.Base(filepath.Dir(path))
		skill.Body = strings.TrimSpace(text)
		return skill, nil
	}

	meta, body, err := parseFrontmatter(text[4:])
	if err != nil {
		return nil, err
	}

	skill.Name = meta["name"]
	if skill.Name == "" {
		skill.Name = filepath.Base(filepath.Dir(path))
	}
	skill.Description = meta["description"]
	skill.Body = body

	return skill, nil
}

func parseFrontmatter(text string) (map[string]string, string, error) {
	meta := make(map[string]string)
	var bodyLines []string
	inFront := true
	scanner := bufio.NewScanner(strings.NewReader(text))

	for scanner.Scan() {
		line := scanner.Text()
		if inFront {
			if line == "---" {
				inFront = false
				continue
			}
			if idx := strings.Index(line, ":"); idx >= 0 {
				key := strings.TrimSpace(line[:idx])
				val := strings.TrimSpace(line[idx+1:])
				meta[key] = val
			}
		} else {
			bodyLines = append(bodyLines, line)
		}
	}

	if inFront {
		return nil, "", fmt.Errorf("missing closing --- in frontmatter")
	}

	return meta, strings.TrimSpace(strings.Join(bodyLines, "\n")), scanner.Err()
}

func (sl *SkillLoader) Has(name string) bool {
	_, ok := sl.skills[name]
	return ok
}

func (sl *SkillLoader) Names() []string {
	names := make([]string, 0, len(sl.skills))
	for name := range sl.skills {
		names = append(names, name)
	}
	return names
}

func (sl *SkillLoader) Descriptions() string {
	if len(sl.skills) == 0 {
		return "(no skills)"
	}
	var lines []string
	for name, s := range sl.skills {
		desc := s.Description
		if desc == "" {
			desc = "-"
		}
		lines = append(lines, fmt.Sprintf("  - %s: %s", name, desc))
	}
	return strings.Join(lines, "\n")
}

func (sl *SkillLoader) Load(name string) string {
	s, ok := sl.skills[name]
	if !ok {
		return fmt.Sprintf("Error: Unknown skill %q. Available: %s", name, strings.Join(sl.Names(), ", "))
	}
	return fmt.Sprintf("<skill name=%q>\n%s\n</skill>", name, s.Body)
}

type SkillToolInput struct {
	Name string `json:"name" jsonschema:"required" jsonschema_description:"Skill name to load"`
}

type SkillTool struct {
	sl *SkillLoader
}

func NewSkillTool(sl *SkillLoader) *SkillTool {
	return &SkillTool{sl: sl}
}

func (t *SkillTool) Name() string {
	return "Skill"
}

func (t *SkillTool) Description() string {
	return fmt.Sprintf("Load specialized knowledge by name. Available skills:\n%s", t.sl.Descriptions())
}

var skillToolInputSchema, _ = utils.GoStruct2ParamsOneOf[SkillToolInput]()

func (t *SkillTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name:        t.Name(),
		Desc:        t.Description(),
		ParamsOneOf: skillToolInputSchema,
	}, nil
}

func (t *SkillTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	var input SkillToolInput
	if err := json.Unmarshal([]byte(argumentsInJSON), &input); err != nil {
		return "", err
	}
	return t.sl.Load(input.Name), nil
}

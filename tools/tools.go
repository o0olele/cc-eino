package tools

import (
	"context"

	"github.com/cloudwego/eino/schema"
)

// Registry holds all registered tools and provides lookup by name.
type Registry struct {
	Infos    []*schema.ToolInfo
	handlers map[string]func(context.Context, string) (string, error)
}

// Run dispatches a tool call by name and returns its result.
func (r *Registry) Run(ctx context.Context, name, arguments string) (string, error) {
	fn, ok := r.handlers[name]
	if !ok {
		return "", &UnknownToolError{Name: name}
	}
	return fn(ctx, arguments)
}

// UnknownToolError is returned when a tool name is not found in the registry.
type UnknownToolError struct {
	Name string
}

func (e *UnknownToolError) Error() string {
	return "unknown tool: " + e.Name
}

// entry is an internal helper for building the registry.
type entry struct {
	name string
	info func(context.Context) (*schema.ToolInfo, error)
	run  func(context.Context, string) (string, error)
}

// NewRegistry builds a Registry from all available tools.
func NewRegistry(ctx context.Context) (*Registry, error) {
	glob := GlobTool{}
	grep := GrepTool{}
	bash := BashTool{}

	entries := []entry{
		{
			name: glob.Name(),
			info: glob.Info,
			run:  func(c context.Context, args string) (string, error) { return glob.InvokableRun(c, args) },
		},
		{
			name: grep.Name(),
			info: grep.Info,
			run:  func(c context.Context, args string) (string, error) { return grep.InvokableRun(c, args) },
		},
		{
			name: bash.Name(),
			info: bash.Info,
			run:  func(c context.Context, args string) (string, error) { return bash.InvokableRun(c, args) },
		},
	}

	r := &Registry{
		handlers: make(map[string]func(context.Context, string) (string, error), len(entries)),
	}
	for _, e := range entries {
		info, err := e.info(ctx)
		if err != nil {
			return nil, err
		}
		r.Infos = append(r.Infos, info)
		r.handlers[e.name] = e.run
	}
	return r, nil
}

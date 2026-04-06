package tools

import (
	"context"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// Registry holds all registered tools and provides lookup by name.
type Registry struct {
	Infos    []*schema.ToolInfo
	entries  []entry
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
	tool tool.BaseTool
	info *schema.ToolInfo
	run  func(context.Context, string) (string, error)
}

// NewRegistry builds a Registry from all available tools.
func NewRegistry(ctx context.Context) (*Registry, error) {

	r := &Registry{
		handlers: make(map[string]func(context.Context, string) (string, error)),
	}

	r.Register(ctx, GrepTool{}, func(ctx context.Context, s string) (string, error) {
		var tool GrepTool
		return tool.InvokableRun(ctx, s)
	})

	r.Register(ctx, BashTool{}, func(ctx context.Context, s string) (string, error) {
		var tool BashTool
		return tool.InvokableRun(ctx, s)
	})

	r.Register(ctx, GlobTool{}, func(ctx context.Context, s string) (string, error) {
		var tool GlobTool
		return tool.InvokableRun(ctx, s)
	})

	return r, nil
}

func (r *Registry) Register(ctx context.Context, tool tool.BaseTool, f func(context.Context, string) (string, error)) {
	info, err := tool.Info(ctx)
	if err != nil {
		panic(err)
	}
	r.Infos = append(r.Infos, info)
	r.handlers[info.Name] = f
	r.entries = append(r.entries, entry{
		name: info.Name,
		tool: tool,
		info: info,
		run:  f,
	})
}

func (r *Registry) CollectTools(filter func(info *schema.ToolInfo) bool) []tool.BaseTool {
	var tools []tool.BaseTool
	for _, e := range r.entries {
		if filter(e.info) {
			tools = append(tools, e.tool)
		}
	}
	return tools
}

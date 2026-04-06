package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
)

type TodoItem struct {
	Id      string `json:"id" jsonschema:"required" jsonschema_description:"The id of the todo"`
	Content string `json:"content" jsonschema:"required" jsonschema_description:"The content of the todo"`
	Status  string `json:"status" jsonschema:"enum=pending,enum=in_progress,enum=completed,required" jsonschema_description:"The status of the todo"`
}

type TodoManager struct {
	items           []TodoItem
	roundsSinceTodo int32
}

func NewTodoManager() *TodoManager {
	return &TodoManager{}
}

func (t *TodoManager) HasInProgress() bool {
	for _, item := range t.items {
		if item.Status == "in_progress" {
			return true
		}
	}
	return false
}

func (t *TodoManager) Update(items []TodoItem) (string, error) {
	inProgress := 0
	for i, item := range items {
		if strings.TrimSpace(item.Content) == "" {
			return "", fmt.Errorf("item %d: content required", i)
		}
		switch item.Status {
		case "pending", "in_progress", "completed":
		default:
			return "", fmt.Errorf("item %d: invalid status %q", i, item.Status)
		}
		if item.Status == "in_progress" {
			inProgress++
		}
	}
	if len(items) > 20 {
		return "", fmt.Errorf("max 20 todos")
	}
	if inProgress > 1 {
		return "", fmt.Errorf("only one in_progress allowed")
	}
	t.items = items
	return t.Render(), nil
}

func (t *TodoManager) Render() string {
	if len(t.items) == 0 {
		return "No todos."
	}
	var sb strings.Builder
	for _, item := range t.items {
		mark := map[string]string{
			"completed":   "[x]",
			"in_progress": "[>]",
			"pending":     "[ ]",
		}[item.Status]
		if mark == "" {
			mark = "[?]"
		}
		suffix := ""
		if item.Status == "in_progress" {
			suffix = " <- "
		}
		sb.WriteString(fmt.Sprintf("%s %s%s\n", mark, item.Content, suffix))
	}
	done := 0
	for _, item := range t.items {
		if item.Status == "completed" {
			done++
		}
	}
	sb.WriteString(fmt.Sprintf("\n(%d/%d completed)", done, len(t.items)))
	return sb.String()
}

func (t *TodoManager) HasOpenItems() bool {
	for _, item := range t.items {
		if item.Status != "completed" {
			return true
		}
	}
	return false
}

type TodoToolInput struct {
	Items []TodoItem `json:"items" jsonschema:"required" jsonschema_description:"The list of todos"`
}

type TodoTool struct {
	todoManager *TodoManager
}

func NewTodoTool(todoManager *TodoManager) *TodoTool {
	return &TodoTool{todoManager: todoManager}
}

func (t *TodoTool) Name() string { return "Todo" }

func (t *TodoTool) Description() string {
	return "Update task list. Track progress on multi-step tasks."
}

var todoToolInputSchema, _ = utils.GoStruct2ParamsOneOf[TodoToolInput]()

func (t *TodoTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name:        t.Name(),
		Desc:        t.Description(),
		ParamsOneOf: todoToolInputSchema,
	}, nil
}

func (t *TodoTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {

	input := TodoToolInput{}

	errTodo := json.Unmarshal([]byte(argumentsInJSON), &input)
	if errTodo != nil {
		return "", errTodo
	}

	fmt.Println(input.Items)

	return t.todoManager.Update(input.Items)
}

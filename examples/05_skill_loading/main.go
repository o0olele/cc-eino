package main

import (
	"bufio"
	"cc/tools"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	localbk "github.com/cloudwego/eino-ext/adk/backend/local"
	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

const (
	baseURL = "https://openrouter.ai/api/v1"
	modelID = "qwen/qwen3.6-plus:free"
)

func newModel() (model.ToolCallingChatModel, error) {
	ctx := context.Background()
	return openai.NewChatModel(ctx, &openai.ChatModelConfig{
		APIKey:  os.Getenv("OPENAI_API_KEY"),
		Model:   modelID,
		BaseURL: baseURL,
	})
}

func main() {
	workDir, _ := os.Getwd()

	ctx := context.Background()
	cm, err := newModel()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Failed to init model:", err)
		os.Exit(1)
	}

	toolRegistry, err := tools.NewRegistry(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "Failed to init tool registry:", err)
		os.Exit(1)
	}

	todoMgr := NewTodoManager()
	toolRegistry.Register(ctx, NewTodoTool(todoMgr), func(ctx context.Context, args string) (string, error) {
		input := TodoToolInput{}

		errTodo := json.Unmarshal([]byte(args), &input)
		if errTodo != nil {
			return "", errTodo
		}

		return todoMgr.Update(input.Items)
	})

	skillLoader, result := NewSkillLoader(workDir)
	println(result.String())
	println(skillLoader.Descriptions())

	toolRegistry.Register(ctx, NewSkillTool(skillLoader), func(ctx context.Context, args string) (string, error) {
		input := SkillToolInput{}
		err = json.Unmarshal([]byte(args), &input)
		if err != nil {
			return "", err
		}

		return skillLoader.Load(input.Name), nil
	})

	backend, err := localbk.NewBackend(ctx, &localbk.Config{})
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	sysPrompt := fmt.Sprintf(`You are a coding agent at %s. 
Use the todo tool to plan multi-step tasks. Mark in_progress before starting, completed when done.
Use the agent tool to delegate exploration or subtasks.
Prefer tools over prose.Use load_skill for specialized knowledge.
Skills: %s`, workDir, skillLoader.Descriptions())

	history := []*schema.Message{
		schema.SystemMessage(sysPrompt),
	}

	app := NewEngine().
		SetWorkDir(workDir).
		SetChatModel(cm).
		SetTools(toolRegistry).
		SetBackend(backend).
		SetTodoManager(todoMgr).
		SetHistory(history)

	toolRegistry.Register(ctx, NewAgentTool(app), func(ctx context.Context, args string) (string, error) {
		return NewAgentTool(app).InvokableRun(ctx, args)
	})

	scanner := bufio.NewScanner(os.Stdin)

	fmt.Print("\033[36ms_full >> \033[0m")
	for scanner.Scan() {
		query := strings.TrimSpace(scanner.Text())

		switch query {
		case "", "q", "exit":
			return
		default:
			history = append(history, schema.UserMessage(query))
			app.SetHistory(history)
			app.AgentLoop(ctx, &history)
		}

		fmt.Println()
		fmt.Print("\033[36ms_full >> \033[0m")
	}
}

// Delegate: read all .go files in sfull dir and summarize what each one does

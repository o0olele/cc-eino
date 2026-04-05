package main

import (
	"bufio"
	"cc/tools"
	"context"
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

	backend, err := localbk.NewBackend(ctx, &localbk.Config{})
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	sysPrompt := fmt.Sprintf(`You are a coding agent at %s. Use tools to solve tasks.
Prefer task_create/task_update/task_list for multi-step work. Use TodoWrite for short checklists.
Use task for subagent delegation. Use load_skill for specialized knowledge.`, workDir)

	app := NewEngine().
		SetWorkDir(workDir).
		SetChatModel(cm).
		SetTools(toolRegistry).
		SetBackend(backend)

	history := []*schema.Message{
		schema.SystemMessage(sysPrompt),
	}

	scanner := bufio.NewScanner(os.Stdin)

	fmt.Print("\033[36ms_full >> \033[0m")
	for scanner.Scan() {
		query := strings.TrimSpace(scanner.Text())

		switch query {
		case "", "q", "exit":
			return
		default:
			history = append(history, schema.UserMessage(query))
			app.AgentLoop(ctx, &history)
		}

		fmt.Println()
		fmt.Print("\033[36ms_full >> \033[0m")
	}
}

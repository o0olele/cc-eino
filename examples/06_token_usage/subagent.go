package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
)

type AgentToolInput struct {
	Prompt string `json:"prompt" jsonschema:"required" jsonschema_description:"The prompt for the subagent"`
}

type AgentTool struct {
	engine *Engine
}

func NewAgentTool(engine *Engine) *AgentTool {
	return &AgentTool{engine: engine}
}

func (t *AgentTool) Name() string {
	return "Agent"
}

func (t *AgentTool) Description() string {
	return "Spawn a subagent with fresh context. It shares the filesystem but not conversation history."
}

var agentToolInputSchema, _ = utils.GoStruct2ParamsOneOf[AgentToolInput]()

func (t *AgentTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name:        t.Name(),
		Desc:        t.Description(),
		ParamsOneOf: agentToolInputSchema,
	}, nil
}

func (t *AgentTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {

	var input AgentToolInput
	if err := json.Unmarshal([]byte(argumentsInJSON), &input); err != nil {
		return "", err
	}

	subHistory := []*schema.Message{
		schema.SystemMessage(fmt.Sprintf("You are a subagent. Your task: %s", input.Prompt)),
	}

	// You can choose main agent's history
	// subHistory := append(subHistory, t.engine.history...)

	subEngine := NewEngine().
		SetIsSubAgent(true).
		SetWorkDir(t.engine.workDir).
		SetTools(t.engine.tools).
		SetBackend(t.engine.backend).
		SetTodoManager(NewTodoManager()).
		SetChatModel(t.engine.cm).
		SetHistory(subHistory)

	subEngine.AgentLoop(ctx, &subHistory)

	// 注意 同一轮的多个tool会并行执行
	// fmt.Println("Subagent history start:")
	for _, msg := range subHistory {
		fmt.Println(msg)
	}
	// fmt.Println("Subagent history end")

	if len(subHistory) > 0 {
		lastMsg := subHistory[len(subHistory)-1]
		return lastMsg.Content, nil
	}

	return "Subagent completed with no output.", nil
}

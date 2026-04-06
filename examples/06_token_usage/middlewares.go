package main

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type safeToolMiddleware struct {
	*adk.BaseChatModelAgentMiddleware
}

func (m *safeToolMiddleware) WrapInvokableToolCall(
	_ context.Context,
	endpoint adk.InvokableToolCallEndpoint,
	_ *adk.ToolContext,
) (adk.InvokableToolCallEndpoint, error) {
	return func(ctx context.Context, args string, opts ...tool.Option) (string, error) {
		result, err := endpoint(ctx, args, opts...)
		if err != nil {
			return fmt.Sprintf("[tool error] %v", err), nil
		}
		return result, nil
	}, nil
}

type todoWarningMiddleware struct {
	*adk.BaseChatModelAgentMiddleware
	todoManager *TodoManager
}

func newTodoWarningMiddleware(tm *TodoManager) *todoWarningMiddleware {
	return &todoWarningMiddleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		todoManager:                  tm,
	}
}

func (m *todoWarningMiddleware) BeforeModelRewriteState(
	ctx context.Context,
	state *adk.ChatModelAgentState,
	mc *adk.ModelContext,
) (context.Context, *adk.ChatModelAgentState, error) {
	if m.todoManager.HasInProgress() && m.todoManager.roundsSinceTodo > 3 {
		warning := fmt.Sprintf("[system note] %d rounds without updating todo. Please check and update the todo list.", m.todoManager.roundsSinceTodo)
		state.Messages = append(state.Messages, schema.UserMessage(warning))
	}
	return ctx, state, nil
}

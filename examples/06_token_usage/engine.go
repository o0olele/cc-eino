package main

import (
	"bytes"
	"cc/tools"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"

	localbk "github.com/cloudwego/eino-ext/adk/backend/local"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/prebuilt/deep"
	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type TokenCounter struct {
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
}

func (t *TokenCounter) Add(prompt, completion, total int64) {
	atomic.AddInt64(&t.PromptTokens, int64(prompt))
	atomic.AddInt64(&t.CompletionTokens, int64(completion))
	atomic.AddInt64(&t.TotalTokens, int64(total))
}

func (t *TokenCounter) String() string {
	return fmt.Sprintf("Tokens - Prompt: %d, Completion: %d, Total: %d",
		atomic.LoadInt64(&t.PromptTokens),
		atomic.LoadInt64(&t.CompletionTokens),
		atomic.LoadInt64(&t.TotalTokens))
}

func NewTokenCallbackHandler(counter *TokenCounter) callbacks.Handler {
	return callbacks.NewHandlerBuilder().
		OnEndFn(func(ctx context.Context, info *callbacks.RunInfo, output callbacks.CallbackOutput) context.Context {
			if info != nil && info.Component == components.ComponentOfChatModel {
				mo := model.ConvCallbackOutput(output)
				if mo != nil && mo.TokenUsage != nil {
					counter.Add(
						int64(mo.TokenUsage.PromptTokens),
						int64(mo.TokenUsage.CompletionTokens),
						int64(mo.TokenUsage.TotalTokens),
					)
				}
			}
			return ctx
		}).
		OnEndWithStreamOutputFn(func(ctx context.Context, info *callbacks.RunInfo, output *schema.StreamReader[callbacks.CallbackOutput]) context.Context {
			defer output.Close()
			if info != nil && info.Component == components.ComponentOfChatModel {
				var lastUsage *schema.TokenUsage
				for {
					chunk, err := output.Recv()
					if err != nil {
						break
					}
					mo := model.ConvCallbackOutput(chunk)
					if mo != nil && mo.Message != nil && mo.Message.ResponseMeta != nil && mo.Message.ResponseMeta.Usage != nil {
						lastUsage = mo.Message.ResponseMeta.Usage
					}
				}
				if lastUsage != nil {
					counter.Add(
						int64(lastUsage.PromptTokens),
						int64(lastUsage.CompletionTokens),
						int64(lastUsage.TotalTokens),
					)
				}
			}
			return ctx
		}).
		Build()
}

type Engine struct {
	workDir      string
	cm           model.ToolCallingChatModel
	tools        *tools.Registry
	backend      *localbk.Local
	todo         *TodoManager
	isSubAgent   bool
	history      []*schema.Message
	tokenCounter *TokenCounter
}

func NewEngine() *Engine {
	return &Engine{}
}

func (e *Engine) SetWorkDir(workDir string) *Engine {
	e.workDir = workDir
	return e
}

func (e *Engine) SetChatModel(cm model.ToolCallingChatModel) *Engine {
	e.cm = cm
	return e
}

func (e *Engine) SetTools(toolRegistry *tools.Registry) *Engine {
	e.tools = toolRegistry
	return e
}

func (e *Engine) SetBackend(backend *localbk.Local) *Engine {
	e.backend = backend
	return e
}
func (e *Engine) SetTodoManager(todoMgr *TodoManager) *Engine {
	e.todo = todoMgr
	return e
}

func (e *Engine) SetIsSubAgent(isSubAgent bool) *Engine {
	e.isSubAgent = isSubAgent
	return e
}

func (e *Engine) SetHistory(history []*schema.Message) *Engine {
	e.history = history
	return e
}

func (e *Engine) SetTokenCounter(counter *TokenCounter) *Engine {
	e.tokenCounter = counter
	return e
}

func (e *Engine) GetTokenUsage() string {
	if e.tokenCounter == nil {
		return "(no token counter)"
	}
	return e.tokenCounter.String()
}

func (e *Engine) GetValidTools() []tool.BaseTool {
	return e.tools.CollectTools(func(info *schema.ToolInfo) bool {
		switch info.Name {
		case "Agent":
			// 子智能体不允许调用 Agent 工具
			if e.isSubAgent {
				return false
			}
		case "Glob", "Grep":
			return false
		}
		return true
	})
}

func (e *Engine) AgentLoop(ctx context.Context, history *[]*schema.Message) {
	agent, err := deep.New(ctx, &deep.Config{
		Name:         "tool agent",
		Description:  "tool agent with filesystem access via LocalBackend.",
		ChatModel:    e.cm,
		Backend:      e.backend,
		MaxIteration: 50,
		ModelRetryConfig: &adk.ModelRetryConfig{
			MaxRetries: 5,
			IsRetryAble: func(_ context.Context, err error) bool {
				return strings.Contains(err.Error(), "429") ||
					strings.Contains(err.Error(), "Too Many Requests") ||
					strings.Contains(err.Error(), "qpm limit")
			},
		},
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: e.GetValidTools(),
			},
		},
		Handlers: []adk.ChatModelAgentMiddleware{
			&safeToolMiddleware{},
			newTodoWarningMiddleware(e.todo),
		},
		WithoutGeneralSubAgent: true,
	})
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:           agent,
		EnableStreaming: true,
	})

	var runOpts []adk.AgentRunOption
	if e.tokenCounter != nil {
		runOpts = append(runOpts, adk.WithCallbacks(NewTokenCallbackHandler(e.tokenCounter)))
	}

	events := runner.Run(ctx, *history, runOpts...)
	toolCall, content, err := printAndCollectAssistantFromEvents(events)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if e.todo.HasInProgress() && toolCall != nil {
		for _, tc := range toolCall.ToolCalls {
			if tc.Function.Name == "Todo" {
				e.todo.roundsSinceTodo = 0
			} else {
				e.todo.roundsSinceTodo++
			}
		}
	}

	assistantMsg := schema.AssistantMessage(content, nil)
	*history = append(*history, assistantMsg)
}

func printAndCollectAssistantFromEvents(events *adk.AsyncIterator[*adk.AgentEvent]) (*schema.Message, string, error) {
	var sb strings.Builder
	var toolCall *schema.Message

	for {
		event, ok := events.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			return nil, "", event.Err
		}

		if event.Output != nil && event.Output.MessageOutput != nil {
			mv := event.Output.MessageOutput
			if mv.Role == schema.Tool {
				content := drainToolResult(mv)
				fmt.Printf("[tool result] %s\n", truncate(content, 200))
				continue
			}

			if mv.Role != schema.Assistant && mv.Role != "" {
				continue
			}

			if mv.IsStreaming {
				mv.MessageStream.SetAutomaticClose()
				var accumulatedToolCalls []*schema.Message
				for {
					frame, err := mv.MessageStream.Recv()
					if errors.Is(err, io.EOF) {
						break
					}
					if err != nil {
						return nil, "", err
					}
					if frame != nil {
						if frame.Content != "" {
							sb.WriteString(frame.Content)
							_, _ = fmt.Fprint(os.Stdout, frame.Content)
						}
						// 累积 ToolCalls
						if len(frame.ToolCalls) > 0 {
							accumulatedToolCalls = append(accumulatedToolCalls, frame)
						}
					}
				}
				msgToolCalls, err := schema.ConcatMessages(accumulatedToolCalls)
				if err == nil {
					// 流结束后打印完整的 ToolCalls
					for _, tc := range msgToolCalls.ToolCalls {
						if tc.Function.Name != "" && tc.Function.Arguments != "" {
							fmt.Printf("\n[tool call] %s(%s)\n", tc.Function.Name, tc.Function.Arguments)
						}
					}
					toolCall = msgToolCalls
				}

				_, _ = fmt.Fprintln(os.Stdout)
				continue
			}

			if mv.Message != nil {
				sb.WriteString(mv.Message.Content)
				_, _ = fmt.Fprintln(os.Stdout, mv.Message.Content)
				for _, tc := range mv.Message.ToolCalls {
					fmt.Printf("[tool call] %s(%s)\n", tc.Function.Name, tc.Function.Arguments)
				}
			}
		}
	}

	return toolCall, sb.String(), nil
}

func drainToolResult(mo *adk.MessageVariant) string {
	if mo.IsStreaming && mo.MessageStream != nil {
		var sb strings.Builder
		for {
			chunk, err := mo.MessageStream.Recv()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				break
			}
			if chunk != nil && chunk.Content != "" {
				sb.WriteString(chunk.Content)
			}
		}
		return sb.String()
	}
	if mo.Message != nil {
		return mo.Message.Content
	}
	return ""
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	var result bytes.Buffer
	if err := json.Compact(&result, []byte(s)); err == nil {
		s = result.String()
	}
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

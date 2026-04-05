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

	localbk "github.com/cloudwego/eino-ext/adk/backend/local"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/prebuilt/deep"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type Engine struct {
	workDir string
	cm      model.ToolCallingChatModel
	tools   *tools.Registry
	backend *localbk.Local
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

func (e *Engine) AgentLoop(ctx context.Context, history *[]*schema.Message) {

	agent, err := deep.New(ctx, &deep.Config{
		Name:           "tool agent",
		Description:    "tool agent with filesystem access via LocalBackend.",
		ChatModel:      e.cm,
		Backend:        e.backend,
		StreamingShell: e.backend,
		MaxIteration:   50,
		ModelRetryConfig: &adk.ModelRetryConfig{
			MaxRetries: 5,
			IsRetryAble: func(_ context.Context, err error) bool {
				// 429 限流错误可重试
				return strings.Contains(err.Error(), "429") ||
					strings.Contains(err.Error(), "Too Many Requests") ||
					strings.Contains(err.Error(), "qpm limit")
			},
		},
	})
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:           agent,
		EnableStreaming: true,
	})

	events := runner.Run(ctx, *history)
	content, err := printAndCollectAssistantFromEvents(events)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	assistantMsg := schema.AssistantMessage(content, nil)
	*history = append(*history, assistantMsg)
}

func printAndCollectAssistantFromEvents(events *adk.AsyncIterator[*adk.AgentEvent]) (string, error) {
	var sb strings.Builder

	for {
		event, ok := events.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			return "", event.Err
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
						return "", err
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

	return sb.String(), nil
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

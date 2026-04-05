package main

import (
	"cc/tools"
	"context"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type Engine struct {
	workDir string
	cm      model.ToolCallingChatModel
	tools   *tools.Registry
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

func (e *Engine) AgentLoop(ctx context.Context, history *[]*schema.Message) {
	for {
		resp, err := generateWithRetry(ctx, e.cm, *history)
		if err != nil {
			fmt.Println("Generate error:", err)
			return
		}
		*history = append(*history, resp)

		if len(resp.ToolCalls) == 0 {
			fmt.Println(resp.Content)
			break
		}

		for _, tc := range resp.ToolCalls {
			result, err := e.tools.Run(ctx, tc.Function.Name, tc.Function.Arguments)
			if err != nil {
				fmt.Printf("Tool %s error: %v\n", tc.Function.Name, err)
				continue
			}
			fmt.Printf("Tool %s result: %s\n", tc.Function.Name, result)
			*history = append(*history, schema.ToolMessage(result, tc.ID))
		}
	}
}

// generateWithRetry calls cm.Generate with exponential backoff on 429 errors.
func generateWithRetry(ctx context.Context, cm model.ToolCallingChatModel, messages []*schema.Message) (*schema.Message, error) {
	const maxRetries = 5
	const baseDelay = time.Second

	for attempt := range maxRetries {
		resp, err := cm.Generate(ctx, messages)
		if err == nil {
			return resp, nil
		}
		if !strings.Contains(err.Error(), "429") || attempt == maxRetries-1 {
			return nil, err
		}
		delay := time.Duration(math.Pow(2, float64(attempt))) * baseDelay
		fmt.Fprintf(os.Stderr, "\n[rate limited, retrying in %s]\n[Assistant] ", delay)
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	panic("unreachable")
}

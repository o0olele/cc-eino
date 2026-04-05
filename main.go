package main

import (
	"bufio"
	"cc/tools"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"time"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func main() {
	var instruction string
	flag.StringVar(&instruction, "instruction", "You are a helpful assistant.", "")
	flag.Parse()

	ctx := context.Background()

	registry, err := tools.NewRegistry(ctx)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	cm, err := newChatModel(ctx)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	cm, _ = cm.WithTools(registry.Infos)

	fmt.Println("Chat session started. Type 'exit' or 'quit' to end.")
	scanner := bufio.NewScanner(os.Stdin)

	messages := []*schema.Message{
		schema.SystemMessage(instruction),
	}
	for {
		fmt.Print("\n[User] ")
		if !scanner.Scan() {
			break
		}

		query := strings.TrimSpace(scanner.Text())
		if query == "" {
			continue
		}

		if query == "exit" || query == "quit" {
			break
		}

		messages = append(messages, schema.UserMessage(query))

		fmt.Print("[Assistant] ")

		// Agentic loop: keep calling the model until it stops issuing tool calls.
		for {
			stream, err := streamWithRetry(ctx, cm, messages)
			if err != nil {
				fmt.Fprintf(os.Stderr, "\nError: %v\n", err)
				break
			}

			var textContent string
			var toolCallFrames []*schema.Message
			for {
				frame, err := stream.Recv()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					fmt.Fprintf(os.Stderr, "\nStream error: %v\n", err)
					break
				}
				if frame == nil {
					continue
				}
				if frame.Content != "" {
					textContent += frame.Content
					fmt.Print(frame.Content)
				}
				if len(frame.ToolCalls) > 0 {
					toolCallFrames = append(toolCallFrames, frame)
				}
			}
			stream.Close()

			if len(toolCallFrames) == 0 {
				// No tool calls — we have the final answer.
				messages = append(messages, schema.AssistantMessage(textContent, nil))
				break
			}

			// Concatenate streaming tool-call chunks into a single message.
			assistantMsg, err := schema.ConcatMessages(toolCallFrames)
			if err != nil {
				fmt.Fprintf(os.Stderr, "\nError concatenating tool calls: %v\n", err)
				break
			}
			messages = append(messages, assistantMsg)

			// Execute each tool call and append its result.
			for _, tc := range assistantMsg.ToolCalls {
				result, err := registry.Run(ctx, tc.Function.Name, tc.Function.Arguments)
				if err != nil {
					result = fmt.Sprintf("error: %v", err)
				}
				fmt.Printf("\n[Tool: %s] %s\n[Assistant] ", tc.Function.Name, result)
				messages = append(messages, schema.ToolMessage(result, tc.ID))
			}
			// Continue loop — ask model for next response.
		}

		fmt.Println()
	}
}

// streamWithRetry calls cm.Stream with exponential backoff on 429 errors.
func streamWithRetry(ctx context.Context, cm model.ToolCallingChatModel, messages []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
	const maxRetries = 5
	const baseDelay = time.Second

	for attempt := range maxRetries {
		stream, err := cm.Stream(ctx, messages)
		if err == nil {
			return stream, nil
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

func newChatModel(ctx context.Context) (model.ToolCallingChatModel, error) {
	return openai.NewChatModel(ctx, &openai.ChatModelConfig{
		APIKey:  "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",
		Model:   "qwen/qwen3.6-plus:free",
		BaseURL: "https://openrouter.ai/api/v1",
	})
}

package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
)

const (
	bashDefaultTimeoutMs = 120_000 // 2 minutes
	bashMaxTimeoutMs     = 600_000 // 10 minutes
	bashMaxOutputBytes   = 100_000 // ~100 KB per stream before truncation
)

type BashToolInput struct {
	Command     string `json:"command" jsonschema:"required" jsonschema_description:"The command to execute"`
	Timeout     *int   `json:"timeout,omitempty" jsonschema_description:"Optional timeout in milliseconds (max 600000). By default, commands timeout after 120000ms (2 minutes)."`
	Description string `json:"description,omitempty" jsonschema_description:"Clear, concise description of what this command does in active voice. Never use words like \"complex\" or \"risk\" in the description - just describe what it does.\n\nFor simple commands (git, npm, standard CLI tools), keep it brief (5-10 words):\n- ls → \"List files in current directory\"\n- git status → \"Show working tree status\"\n\nFor commands that are harder to parse at a glance (piped commands, obscure flags, etc.), add enough context to clarify what it does."`
}

var bashToolInputSchema, _ = utils.GoStruct2ParamsOneOf[BashToolInput]()

type BashTool struct{}

func (BashTool) Name() string { return "Bash" }

func (BashTool) Description() string {
	return `Executes a given bash command and returns its output.

The working directory persists between commands, but shell state does not. The shell environment is initialized from the user's profile (bash or zsh).

IMPORTANT: Avoid using this tool to run ` + "`find`" + `, ` + "`grep`" + `, ` + "`cat`" + `, ` + "`head`" + `, ` + "`tail`" + `, ` + "`sed`" + `, ` + "`awk`" + `, or ` + "`echo`" + ` commands, unless explicitly instructed or after you have verified that a dedicated tool cannot accomplish your task. Instead, use the appropriate dedicated tool as this will provide a much better experience for the user:

 - File search: Use Glob (NOT find or ls)
 - Content search: Use Grep (NOT grep or rg)
 - Reserve using the Bash exclusively for system commands and terminal operations that require shell execution.

# Instructions
 - If your command will create new directories or files, first use this tool to run ` + "`ls`" + ` to verify the parent directory exists and is the correct location.
 - Always quote file paths that contain spaces with double quotes in your command (e.g., cd "path with spaces/file.txt")
 - Try to maintain your current working directory throughout the session by using absolute paths and avoiding usage of ` + "`cd`" + `. You may use ` + "`cd`" + ` if the User explicitly requests it.
 - You may specify an optional timeout in milliseconds (up to 600000ms / 10 minutes). By default, your command will timeout after 120000ms (2 minutes).
 - When issuing multiple commands:
  - If the commands are independent and can run in parallel, make multiple Bash tool calls in a single message.
  - If the commands depend on each other and must run sequentially, use a single Bash call with '&&' to chain them together.
  - Use ';' only when you need to run commands sequentially but don't care if earlier commands fail.
  - DO NOT use newlines to separate commands (newlines are ok in quoted strings).
 - For git commands:
  - Prefer to create a new commit rather than amending an existing commit.
  - Before running destructive operations (e.g., git reset --hard, git push --force, git checkout --), consider whether there is a safer alternative that achieves the same goal. Only use destructive operations when they are truly the best approach.
  - Never skip hooks (--no-verify) or bypass signing (--no-gpg-sign, -c commit.gpgsign=false) unless the user has explicitly asked for it. If a hook fails, investigate and fix the underlying issue.`
}

func (t BashTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name:        t.Name(),
		Desc:        t.Description(),
		ParamsOneOf: bashToolInputSchema,
	}, nil
}

func (t BashTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	input := BashToolInput{}
	if err := json.Unmarshal([]byte(argumentsInJSON), &input); err != nil {
		return "", fmt.Errorf("failed to unmarshal arguments: %w", err)
	}

	if strings.TrimSpace(input.Command) == "" {
		return "", fmt.Errorf("command must not be empty")
	}

	// Resolve timeout.
	timeoutMs := bashDefaultTimeoutMs
	if input.Timeout != nil && *input.Timeout > 0 {
		timeoutMs = *input.Timeout
		if timeoutMs > bashMaxTimeoutMs {
			timeoutMs = bashMaxTimeoutMs
		}
	}

	cmdCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "bash", "-c", input.Command)

	// Inherit working directory from context if set.
	if workDir, ok := ctx.Value("WorkDir").(string); ok && workDir != "" {
		cmd.Dir = workDir
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	runErr := cmd.Run()

	stdout := truncateBashOutput(stdoutBuf.String())
	stderr := truncateBashOutput(stderrBuf.String())

	interrupted := cmdCtx.Err() == context.DeadlineExceeded
	exitCode := 0
	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else if !interrupted {
			// Command could not be started at all.
			return "", runErr
		}
	}

	return formatBashOutput(stdout, stderr, exitCode, interrupted), nil
}

// truncateBashOutput caps output at bashMaxOutputBytes and appends a notice.
func truncateBashOutput(s string) string {
	if len(s) <= bashMaxOutputBytes {
		return s
	}
	return s[:bashMaxOutputBytes] + fmt.Sprintf("\n... (output truncated, %d bytes omitted)", len(s)-bashMaxOutputBytes)
}

// formatBashOutput builds the string returned to the model.
func formatBashOutput(stdout, stderr string, exitCode int, interrupted bool) string {
	var sb strings.Builder

	if stdout != "" {
		sb.WriteString(strings.TrimRight(stdout, "\n"))
		sb.WriteByte('\n')
	}

	if stderr != "" {
		if sb.Len() > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(strings.TrimRight(stderr, "\n"))
		sb.WriteByte('\n')
	}

	if interrupted {
		if sb.Len() > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString("Command timed out and was interrupted.\n")
	} else if exitCode != 0 {
		if sb.Len() > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(fmt.Sprintf("Exit code %d\n", exitCode))
	}

	if sb.Len() == 0 {
		return "(no output)"
	}
	return sb.String()
}

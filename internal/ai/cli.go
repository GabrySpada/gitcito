package ai

// Model calls served by an agent CLI the user already has installed and signed
// in — `claude`, `gemini`, `codex` (src/main/aiCli.ts). It is the answer to "I
// pay for a subscription, why do I also need an API key": the binary
// authenticates with its own stored session and Gitcito never sees a
// credential.
//
// Three rules this module does not bend:
//
//   - The binary is always launched with an argv array, never a shell. Prompts
//     contain diffs, branch names and file contents — anything that reaches a
//     shell reaches it as code.
//   - Only a binary the user explicitly configured as an account is ever run.
//     Detection lists what is available; it never invokes anything.
//   - Nothing about this is more private than an API key. The prompt still
//     leaves the machine to the same vendor, under the user's own account.

import (
	"context"
	"errors"
	"io/fs"
	"math"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

// How long a single CLI answer may take before it is killed.
const cliTimeout = 180 * time.Second

// Cap on captured stdout, so a runaway CLI cannot exhaust memory.
const cliMaxBuffer = 8 * 1024 * 1024

type cliSpec struct {
	// Default executable name, looked up on PATH.
	bin   string
	label string
	// Argv for one non-interactive prompt.
	args func(model string) []string
	// Models worth offering before the user types their own.
	models []string
}

// CliBinaries is CLI_BINARIES, in order.
var CliBinaries = []string{"claude", "gemini", "codex"}

var clis = map[string]cliSpec{
	"claude": {
		bin:   "claude",
		label: "Claude Code",
		// `--output-format json` gives a parseable envelope with token usage.
		args: func(model string) []string {
			a := []string{"-p", "--output-format", "json"}
			if model != "" {
				a = append(a, "--model", model)
			}
			return a
		},
		models: []string{"haiku", "sonnet", "opus"},
	},
	"gemini": {
		bin:   "gemini",
		label: "Gemini CLI",
		args: func(model string) []string {
			if model != "" {
				return []string{"-m", model}
			}
			return []string{}
		},
		models: []string{"gemini-2.5-flash", "gemini-2.5-pro"},
	},
	"codex": {
		bin:   "codex",
		label: "Codex CLI",
		args: func(model string) []string {
			a := []string{"exec"}
			if model != "" {
				a = append(a, "--model", model)
			}
			return append(a, "-")
		},
		models: []string{},
	},
}

func specFor(binary string) cliSpec {
	if s, ok := clis[binary]; ok {
		return s
	}
	return clis["claude"]
}

// CliModels are the models offered for a CLI before the user types one.
func CliModels(binary string) []string { return append([]string{}, specFor(binary).models...) }

// CommandResult is what one CLI run produced.
type CommandResult struct {
	Stdout, Stderr string
	// Code is the exit status of a process that ran to completion.
	Code int
	// NotFound: the executable does not exist (Node's ENOENT).
	NotFound bool
	// StartFailed: it could not be started for another reason.
	StartFailed bool
	// TimedOut: killed after the timeout.
	TimedOut bool
	// Signaled: killed by a signal it did not get from us.
	Signaled bool
	// Overflow: output passed the buffer cap and the process was killed.
	Overflow bool
}

// CommandRunner runs a binary with an argv array — never a shell — feeding
// stdin and capturing output. Fakes replace it in tests.
type CommandRunner interface {
	Run(ctx context.Context, file string, args []string, stdin string, timeout time.Duration, maxBuffer int) (CommandResult, error)
}

// ExecRunner runs real processes through gitx.Exec: inherited environment,
// process-group kill on timeout or cancel, output capped at maxBuffer.
type ExecRunner struct{}

// Run implements CommandRunner.
func (ExecRunner) Run(ctx context.Context, file string, args []string, stdin string, timeout time.Duration, maxBuffer int) (CommandResult, error) {
	res, err := gitx.Exec(ctx, "", file, args, gitx.RunOpts{
		PlainEnv:  true,
		Stdin:     []byte(stdin),
		Timeout:   timeout,
		MaxBuffer: maxBuffer,
	})
	out := CommandResult{Stdout: res.Stdout, Stderr: res.Stderr, Code: res.ExitCode}
	if err == nil {
		return out, nil
	}
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	var ee *gitx.ExecError
	if !errors.As(err, &ee) {
		return out, err
	}
	out.Stdout, out.Stderr = ee.Stdout, ee.Stderr
	switch {
	case !ee.Started():
		if errors.Is(ee.Err, exec.ErrNotFound) || errors.Is(ee.Err, fs.ErrNotExist) {
			out.NotFound = true
		} else {
			out.StartFailed = true
		}
	case errors.Is(ee.Err, gitx.ErrMaxBuffer):
		out.Overflow = true
	case ee.Killed:
		out.TimedOut = true
	case ee.Signal != "":
		out.Signaled = true
	default:
		out.Code = ee.Code
	}
	return out, nil
}

// runCLI is aiCli.ts's run(): the exit status as Node's execFile callback
// reports it, or the two failures that read better as their own message.
func runCLI(ctx context.Context, runner CommandRunner, file string, args []string, input string, timeout time.Duration) (code int, stdout, stderr string, err error) {
	res, err := runner.Run(ctx, file, args, input, timeout, cliMaxBuffer)
	if err != nil {
		return 0, "", "", err
	}
	switch {
	case res.NotFound:
		return 0, "", "", errors.New("`" + file + "` was not found. Install it, or set an explicit path on the account.")
	case res.TimedOut:
		secs := strconv.Itoa(int(math.Round(timeout.Seconds())))
		return 0, "", "", errors.New("`" + file + "` did not answer within " + secs + "s.")
	case res.Signaled:
		// Node reports `code: null` for a signal, and `null ?? 0` is 0: a CLI
		// killed from outside reads as success with whatever it printed.
		return 0, res.Stdout, res.Stderr, nil
	case res.Overflow, res.StartFailed:
		// Node's code is then a string ('ERR_CHILD_PROCESS_STDIO_MAXBUFFER',
		// 'EACCES'), which the TS maps to 1.
		return 1, res.Stdout, res.Stderr, nil
	}
	return res.Code, res.Stdout, res.Stderr, nil
}

// FlattenMessages flattens a chat into one prompt.
//
// These CLIs take a single string, so roles become labelled sections. System
// text goes first and is marked as instructions, which is the closest thing to
// a system prompt that survives the round trip.
func FlattenMessages(messages []ChatMessage) string {
	var system []string
	var parts []string
	for _, m := range messages {
		if m.Role == "system" {
			system = append(system, m.Content)
		}
	}
	if len(system) > 0 {
		parts = append(parts, "Instructions:\n"+strings.Join(system, "\n\n"))
	}
	for _, m := range messages {
		switch m.Role {
		case "system":
		case "assistant":
			parts = append(parts, "Your previous reply:\n"+m.Content)
		default:
			parts = append(parts, m.Content)
		}
	}
	return strings.Join(parts, "\n\n")
}

// parseClaudeReply reads the `claude -p --output-format json` envelope. Older
// versions, or a CLI that ignored the flag, print plain text; falling back
// beats failing the whole feature over an output-format difference.
func parseClaudeReply(stdout string) (ModelReply, bool) {
	v, err := jsonParse(stdout)
	if err != nil {
		return ModelReply{}, false
	}
	env := asObj(v)
	result, ok := env.V("result").(string)
	if !ok {
		return ModelReply{}, false
	}
	usage := asObj(env.V("usage"))
	num := func(k string) float64 {
		f, _ := usage.V(k).(float64)
		return f
	}
	in, out := num("input_tokens"), num("output_tokens")
	return ModelReply{Text: result, Usage: TokenUsage{PromptTokens: in, CompletionTokens: out, TotalTokens: in + out}}, true
}

// runCliModel is one answer from a locally installed, already signed-in
// agent CLI.
func (t *transport) runCliModel(ctx context.Context, cfg Config, messages []ChatMessage) (ModelReply, error) {
	binary := cfg.cliBinary()
	spec := specFor(binary)
	file := jsTrim(cfg.CliPath)
	if file == "" {
		file = spec.bin
	}
	model := jsTrim(cfg.Model)

	code, stdout, stderr, err := runCLI(ctx, t.runner, file, spec.args(model), FlattenMessages(messages), cliTimeout)
	if err != nil {
		return ModelReply{}, err
	}
	if code != 0 {
		detail := stderr
		if detail == "" {
			detail = stdout
		}
		detail = sliceUTF16(jsTrim(detail), 300)
		if detail != "" {
			detail = ": " + detail
		}
		return ModelReply{}, errors.New("`" + spec.bin + "` exited with code " + strconv.Itoa(code) + detail)
	}
	if binary == "claude" {
		if parsed, ok := parseClaudeReply(jsTrim(stdout)); ok {
			return parsed, nil
		}
	}
	return ModelReply{Text: jsTrim(stdout)}, nil
}

// DetectedCli is one agent CLI found on the machine.
type DetectedCli struct {
	Binary string `json:"binary"`
	Label  string `json:"label"`
	Path   string `json:"path"`
}

var newlineRE = regexp.MustCompile(`\r?\n`)

// detectCliBinaries lists the agent CLIs on PATH. It resolves the executable's
// location and nothing more, so listing has no side effect on the user's
// session or usage.
func detectCliBinaries(ctx context.Context, runner CommandRunner, goos string) []DetectedCli {
	lookup := "which"
	if goos == "windows" {
		lookup = "where"
	}
	found := []DetectedCli{}
	for _, binary := range CliBinaries {
		spec := clis[binary]
		code, stdout, _, err := runCLI(ctx, runner, lookup, []string{spec.bin}, "", 5*time.Second)
		if err != nil {
			// Not installed, or the lookup tool is missing: simply not offered.
			continue
		}
		path := jsTrim(newlineRE.Split(stdout, 2)[0])
		if code == 0 && path != "" {
			found = append(found, DetectedCli{Binary: binary, Label: spec.label, Path: path})
		}
	}
	return found
}

func defaultGOOS() string { return runtime.GOOS }

//lint:file-ignore ST1005 user-facing messages are kept byte-identical to src/main/ssh.ts

// Package ssh makes the system's SSH machinery inspectable — the port of
// src/main/ssh.ts.
//
// SSH keys are the auth path Gitcito never owned but always depended on: a
// `git@` remote authenticates through the system ssh, and
// GIT_TERMINAL_PROMPT=0 does nothing for it. All this package does is show
// which keys exist, whether the agent holds them, and whether the host accepts
// them.
//
// Hard rule: **a private key is never read, never displayed, never
// transmitted.** Only public halves and fingerprints leave this package. And a
// passphrase is a secret: it is never logged, never put in an error, and never
// put on a command line (it reaches ssh-add and ssh-keygen through an askpass
// helper reading an environment variable).
package ssh

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"

	"github.com/GabrySpada/gitcito/internal/gitx"
	"github.com/GabrySpada/gitcito/internal/shared"
)

// EnvSSHDir redirects the key directory — honoured only in --shot mode.
const EnvSSHDir = "GITCITO_SSH_DIR"

// Keys inspects and manages the user's SSH keys.
type Keys struct {
	shot   bool
	goos   string
	getenv func(string) string
}

// New builds Keys. shot is the --shot flag: only then may GITCITO_SSH_DIR
// redirect the key directory.
func New(shot bool) *Keys {
	return &Keys{shot: shot, goos: runtime.GOOS, getenv: os.Getenv}
}

// Dir is where the keys live. Normally `~/.ssh`, and only ever that.
//
// The screenshot harness needs a deterministic, publishable set of keys instead
// of whoever ran it — a documentation shot must not carry a real fingerprint or
// the email in a key comment. It gets one by pointing this at a throwaway
// directory, and the override is honoured **only** in `--shot` mode, the same
// flag that enables the capture bridge. A normal run cannot be redirected by an
// environment variable.
func (k *Keys) Dir() string {
	if k.shot {
		if override := k.getenv(EnvSSHDir); override != "" {
			return override
		}
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".ssh")
}

// testHosts are the hosts we know an SSH endpoint for, for the connection test.
var testHosts = map[string]string{
	"github":    "git@github.com",
	"gitlab":    "git@gitlab.com",
	"bitbucket": "git@bitbucket.org",
	"azure":     "git@ssh.dev.azure.com",
}

// noPrompt is run()'s environment: no tty here, so anything that would prompt
// must fail instead of hanging.
var noPrompt = []string{"DISPLAY=", "SSH_ASKPASS=", "GIT_TERMINAL_PROMPT=0"}

// run executes cmd and returns what it printed, failure or not — the TS
// run(), whose catch kept `e.stdout` and `e.stderr ?? e.message`.
// A missing binary prints nothing (Node's spawn error carried empty output).
func run(ctx context.Context, cmd string, args []string, env []string) (stdout, stderr string) {
	res, err := gitx.Exec(ctx, "", cmd, args, gitx.RunOpts{PlainEnv: true, Env: env})
	if err == nil {
		return res.Stdout, res.Stderr
	}
	var ee *gitx.ExecError
	if errors.As(err, &ee) {
		return ee.Stdout, ee.Stderr
	}
	return "", err.Error()
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// readUTF8 is readFile(path, 'utf-8'): invalid sequences become U+FFFD.
func readUTF8(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var d shared.UTF8Stream
	return d.Decode(b) + d.Flush(), nil
}

// fsError words a filesystem failure the way Node's fs did:
// "ENOTDIR: not a directory, scandir '/x'".
func fsError(op, path string, err error) error {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		if code, text, ok := errnoText(errno); ok {
			return fmt.Errorf("%s: %s, %s '%s'", code, text, op, path)
		}
	}
	return err
}

func errnoText(e syscall.Errno) (code, text string, ok bool) {
	switch e {
	case syscall.ENOENT:
		return "ENOENT", "no such file or directory", true
	case syscall.ENOTDIR:
		return "ENOTDIR", "not a directory", true
	case syscall.EACCES:
		return "EACCES", "permission denied", true
	case syscall.EPERM:
		return "EPERM", "operation not permitted", true
	case syscall.EEXIST:
		return "EEXIST", "file already exists", true
	}
	return "", "", false
}

var agentDownRe = regexp.MustCompile(`(?i)could not open a connection|error connecting`)

// Status lists the public keys in the directory, which of them the agent
// holds, and whether there is an agent at all.
func (k *Keys) Status(ctx context.Context) (shared.SshStatus, error) {
	dir := k.Dir()
	if !exists(dir) {
		return shared.SshStatus{Dir: dir, Keys: []shared.SshKey{}, AgentRunning: false}, nil
	}

	agentOut, agentErr := run(ctx, "ssh-add", []string{"-l"}, noPrompt)
	loaded := map[string]bool{}
	for _, fp := range shared.AgentFingerprints(agentOut) {
		loaded[fp] = true
	}
	// "The agent has no identities" is a running agent with nothing in it; only
	// a connection failure means there is no agent to talk to.
	agentRunning := !agentDownRe.MatchString(agentErr + agentOut)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return shared.SshStatus{}, fsError("scandir", dir, unwrapPath(err))
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	keys := []shared.SshKey{}
	for _, file := range shared.PublicKeyFiles(names) {
		path := filepath.Join(dir, file)
		out, _ := run(ctx, "ssh-keygen", []string{"-lf", path}, noPrompt)
		info := shared.ParseKeygenLine(out)
		if info == nil {
			continue
		}
		publicKey, err := readUTF8(path)
		if err != nil {
			publicKey = ""
		}
		keys = append(keys, shared.SshKey{
			File:        file,
			Path:        path,
			Type:        info.Type,
			Bits:        info.Bits,
			Fingerprint: info.Fingerprint,
			Comment:     info.Comment,
			PublicKey:   shared.JSTrim(publicKey),
			InAgent:     loaded[info.Fingerprint],
			HasPrivate:  exists(filepath.Join(dir, shared.PrivateKeyName(file))),
		})
	}
	return shared.SshStatus{Dir: dir, Keys: keys, AgentRunning: agentRunning}, nil
}

var unsafeNameRe = regexp.MustCompile(`[^A-Za-z0-9_.@-]`)

// Generate creates an ed25519 key and returns the public key's path. It
// refuses to touch an existing file — overwriting a private key destroys
// access to every host that trusts it, and no dialog makes that recoverable.
func (k *Keys) Generate(ctx context.Context, name, comment, passphrase string) (string, error) {
	safe := unsafeNameRe.ReplaceAllString(shared.JSTrim(name), "")
	if safe == "" {
		return "", errors.New("A file name is required.")
	}
	dir := k.Dir()
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return "", fsError("mkdir", dir, unwrapPath(err))
	}
	_ = os.Chmod(dir, 0o700)
	target := filepath.Join(dir, safe)
	if exists(target) || exists(target+".pub") {
		return "", fmt.Errorf("A key called \"%s\" already exists. Choose another name.", safe)
	}
	args := []string{"-t", "ed25519", "-f", target, "-C", shared.JSTrim(comment)}
	var stderr string
	if passphrase == "" {
		// -N "" means "no passphrase", which is what the user asked for when
		// they leave the field blank.
		_, stderr = run(ctx, "ssh-keygen", append(args, "-N", ""), noPrompt)
	} else {
		// TS passed the passphrase as `-N <passphrase>`, where any process can
		// read it off the process list. ssh-keygen also takes it from an
		// askpass helper when stdin is not a tty (both prompts, "enter" and
		// "again", get the same answer), so it goes the way ssh-add's does.
		err := withAskpass(func(env []string) {
			_, stderr = run(ctx, "ssh-keygen", args, k.askpassEnv(env, passphrase))
		})
		if err != nil {
			return "", err
		}
	}
	if !exists(target + ".pub") {
		if msg := shared.JSTrim(stderr); msg != "" {
			return "", errors.New(msg)
		}
		return "", errors.New("ssh-keygen failed.")
	}
	return target + ".pub", nil
}

// unwrapPath strips os.PathError so fsError sees the errno.
func unwrapPath(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

// AddToAgent loads a key into the running agent. It returns an empty string
// on success, else the reason.
func (k *Keys) AddToAgent(ctx context.Context, publicKeyPath, passphrase string) (string, error) {
	privatePath := shared.PrivateKeyName(publicKeyPath)
	// The path reaches ssh-add as a bare argument; a leading dash would make it
	// an option. Key paths from ssh:status are absolute, so this never refuses
	// a real one.
	if strings.HasPrefix(privatePath, "-") {
		return "The private key for this entry is not on this machine.", nil
	}
	if !exists(privatePath) {
		return "The private key for this entry is not on this machine.", nil
	}
	// macOS can persist the passphrase in the login keychain, which is the
	// whole reason "add to agent" survives a reboot there.
	args := []string{privatePath}
	if k.goos == "darwin" {
		args = []string{"--apple-use-keychain", privatePath}
	}

	if passphrase == "" {
		stdout, stderr := run(ctx, "ssh-add", args, noPrompt)
		return readAddResult(stdout + stderr), nil
	}

	// ssh-add takes a passphrase from the tty or from an askpass helper — never
	// from stdin — and there is no tty here. The helper is a throwaway script
	// that echoes an environment variable: the passphrase never reaches disk,
	// and never reaches argv where any other process could read it off the
	// process list.
	var stdout, stderr string
	err := withAskpass(func(helper []string) {
		stdout, stderr = run(ctx, "ssh-add", args, k.askpassEnv(helper, passphrase))
	})
	if err != nil {
		return "", err
	}
	return readAddResult(stdout + stderr), nil
}

// askpassScript prints the passphrase it finds in the environment.
const askpassScript = "#!/bin/sh\nprintf %s \"$GITCITO_SSH_PASSPHRASE\"\n"

// withAskpass writes the helper into a fresh private temp dir, hands fn the
// SSH_ASKPASS assignment, and removes the dir afterwards whatever happened.
func withAskpass(fn func(env []string)) error {
	dir, err := os.MkdirTemp("", "gitcito-askpass-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	helper := filepath.Join(dir, "askpass.sh")
	if err := os.WriteFile(helper, []byte(askpassScript), 0o700); err != nil {
		return err
	}
	fn([]string{"SSH_ASKPASS=" + helper})
	return nil
}

// askpassEnv completes the helper's environment with the passphrase. OpenSSH
// 8.4+ honours the helper without a DISPLAY; older builds insist on one, so
// both are set.
func (k *Keys) askpassEnv(helper []string, passphrase string) []string {
	display := k.getenv("DISPLAY")
	if display == "" {
		display = ":0"
	}
	return append(append([]string{}, helper...),
		"SSH_ASKPASS_REQUIRE=force", "DISPLAY="+display, "GITCITO_SSH_PASSPHRASE="+passphrase)
}

var identityAddedRe = regexp.MustCompile(`(?i)identity added`)

// readAddResult: ssh-add says "Identity added: …" on success and prints
// nothing at all on some platforms; anything else is the failure worth showing.
func readAddResult(output string) string {
	if identityAddedRe.MatchString(output) || shared.JSTrim(output) == "" {
		return ""
	}
	return shared.JSTrim(output)
}

// Test runs `ssh -T git@host`: does this machine authenticate to that host at all?
func (k *Keys) Test(ctx context.Context, host string) shared.SshTest {
	target, ok := testHosts[host]
	if !ok {
		return shared.SshTest{Result: "unknown", Output: "Unknown host: " + host}
	}
	stdout, stderr := run(ctx, "ssh", []string{
		"-T",
		// A first-time host would otherwise block on the fingerprint question
		// with no tty to answer it.
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		target,
	}, noPrompt)
	output := shared.JSTrim(stdout + stderr)
	return shared.SshTest{Result: shared.ReadSshTest(output), Output: output}
}

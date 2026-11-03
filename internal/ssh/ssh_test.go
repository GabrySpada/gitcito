package ssh

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/GabrySpada/gitcito/internal/fakebin"
)

// Every test here runs against a temp directory (shot mode + GITCITO_SSH_DIR,
// or HOME pointed at a temp dir) and fake or throwaway binaries: the real
// ~/.ssh, the user's agent and any real host are never touched.

type sshCase struct {
	Name     string             `json:"name"`
	Platform string             `json:"platform"`
	Shot     bool               `json:"shot"`
	Env      map[string]string  `json:"env"`
	Files    map[string]*string `json:"files"`
	Bins     []string           `json:"bins"`
	Replies  []fakebin.Reply    `json:"replies"`
	Call     struct {
		Method string   `json:"method"`
		Args   []string `json:"args"`
	} `json:"call"`
	Result         json.RawMessage `json:"result"`
	Error          *string         `json:"error"`
	Calls          json.RawMessage `json:"calls"`
	AskpassRemoved bool            `json:"askpassRemoved"`
	ModeOf         string          `json:"modeOf"`
	Mode           *int            `json:"mode"`
}

func layFiles(t *testing.T, root string, files map[string]*string) {
	t.Helper()
	for rel, content := range files {
		full := filepath.Join(root, rel)
		if content == nil {
			if err := os.MkdirAll(full, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(*content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// Replays testdata/ssh.json, recorded from src/main/ssh.ts by
// test/port/tools.test.ts against the same fake ssh, ssh-add and ssh-keygen.
func TestSSHGolden(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("session leadership is read from /proc")
	}
	raw, err := os.ReadFile(filepath.Join("testdata", "ssh.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []sshCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("no cases")
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			root := fakebin.TempRoot(t)
			layFiles(t, root, c.Files)
			f := fakebin.Install(t, root, c.Bins, fakebin.Expand(t, c.Replies, root))
			f.Use(t)
			t.Setenv(EnvSSHDir, "")
			for k, v := range fakebin.Expand(t, c.Env, root) {
				t.Setenv(k, v)
			}
			keys := New(c.Shot)
			if c.Platform == "darwin" {
				keys.goos = "darwin"
			}
			s := NewService(keys)
			args := fakebin.Expand(t, c.Call.Args, root)
			ctx := context.Background()
			var result any
			var callErr error
			switch c.Call.Method {
			case "status":
				result, callErr = s.Status(ctx)
			case "generate":
				result, callErr = s.Generate(ctx, args[0], args[1], args[2])
			case "addToAgent":
				result, callErr = s.AddToAgent(ctx, args[0], args[1])
			case "test":
				result, callErr = s.Test(ctx, args[0])
			}
			if c.Error != nil {
				if callErr == nil {
					t.Fatalf("no error, want %q", *c.Error)
				}
				if got := strings.ReplaceAll(callErr.Error(), root, "<ROOT>"); got != *c.Error {
					t.Fatalf("error %q, want %q", got, *c.Error)
				}
			} else {
				if callErr != nil {
					t.Fatal(callErr)
				}
				fakebin.SameJSON(t, "result", result, c.Result, root)
			}

			calls := f.Calls(t)
			removed := true
			for i, call := range calls {
				if p := call.Env["SSH_ASKPASS"]; p != "" {
					if _, err := os.Stat(filepath.Dir(p)); err == nil {
						removed = false
					}
					if !strings.Contains(p, "gitcito-askpass-") || filepath.Base(p) != "askpass.sh" {
						t.Errorf("unexpected helper path %q", p)
					}
					calls[i].Env["SSH_ASKPASS"] = "<ASKPASS>"
				}
			}
			fakebin.SameJSON(t, "calls", calls, c.Calls, root)
			if removed != c.AskpassRemoved {
				t.Errorf("askpass removed = %v, want %v", removed, c.AskpassRemoved)
			}
			if c.ModeOf != "" {
				st, err := os.Stat(fakebin.Expand(t, c.ModeOf, root))
				if err != nil {
					t.Fatal(err)
				}
				if c.Mode == nil || int(st.Mode().Perm()) != *c.Mode {
					t.Errorf("mode %o, want %v", st.Mode().Perm(), c.Mode)
				}
			}
		})
	}
}

// The sentinel: a passphrase reaches ssh-keygen and ssh-add only through the
// askpass helper. It is never on a command line, never in a result or an
// error, and the helper holding it is gone afterwards.
func TestPassphraseNeverLeaks(t *testing.T) {
	const secret = "s3ntinel-PASS phrase;$(x)"
	root := fakebin.TempRoot(t)
	dir := filepath.Join(root, "ssh")
	priv := "PRIVATE\n"
	layFiles(t, root, map[string]*string{"ssh/id_ed25519": &priv, "ssh/id_ed25519.pub": &priv})
	f := fakebin.Install(t, root, []string{"ssh-keygen", "ssh-add"}, []fakebin.Reply{
		{Argv: []string{"ssh-keygen", "-t", "ed25519", "-f", filepath.Join(dir, "ok"), "-C", "me"}, Create: []string{filepath.Join(dir, "ok.pub")}},
		{Argv: []string{"ssh-keygen", "-t", "ed25519", "-f", filepath.Join(dir, "bad"), "-C", "me"}, Code: 1, Stderr: "Saving key failed\n"},
		{Argv: []string{"ssh-add", filepath.Join(dir, "id_ed25519")}, Code: 1, Stdout: "out\n", Stderr: "Bad passphrase\n"},
	})
	f.Use(t)
	t.Setenv(EnvSSHDir, dir)
	s := NewService(New(true))
	ctx := context.Background()

	var seen []string
	pub, err := s.Generate(ctx, "ok", "me", secret)
	if err != nil || pub != filepath.Join(dir, "ok.pub") {
		t.Fatalf("generate: %q %v", pub, err)
	}
	seen = append(seen, pub)
	if _, err := s.Generate(ctx, "bad", "me", secret); err == nil || err.Error() != "Saving key failed" {
		t.Fatalf("generate failure: %v", err)
	} else {
		seen = append(seen, err.Error())
	}
	msg, err := s.AddToAgent(ctx, filepath.Join(dir, "id_ed25519.pub"), secret)
	if err != nil || msg != "out\nBad passphrase" {
		t.Fatalf("add: %q %v", msg, err)
	}
	seen = append(seen, msg)
	if _, err := s.AddToAgent(ctx, filepath.Join(dir, "missing.pub"), secret); err != nil {
		t.Fatal(err)
	}

	calls := f.Calls(t)
	if len(calls) != 3 {
		t.Fatalf("calls: %+v", calls)
	}
	for _, c := range calls {
		for _, a := range c.Argv {
			if strings.Contains(a, "s3ntinel") {
				t.Fatalf("passphrase on the command line: %q", c.Argv)
			}
		}
		if c.Askpass == nil || *c.Askpass != secret {
			t.Fatalf("askpass delivered %v", c.Askpass)
		}
		if c.AskpassScript == nil || strings.Contains(*c.AskpassScript, "s3ntinel") {
			t.Fatal("the helper script must read the passphrase from the environment, not hold it")
		}
		if c.Env["GITCITO_SSH_PASSPHRASE"] != secret || c.Env["SSH_ASKPASS_REQUIRE"] != "force" {
			t.Fatalf("env %v", c.Env)
		}
		if _, err := os.Stat(filepath.Dir(c.Env["SSH_ASKPASS"])); !os.IsNotExist(err) {
			t.Fatalf("helper left behind: %v", err)
		}
	}
	for _, s := range seen {
		if strings.Contains(s, "s3ntinel") {
			t.Fatalf("passphrase in a result: %q", s)
		}
	}
}

// A normal run cannot be redirected: without --shot, GITCITO_SSH_DIR is ignored.
func TestSSHDirOverrideOnlyInShotMode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(EnvSSHDir, filepath.Join(t.TempDir(), "elsewhere"))
	if got := New(false).Dir(); got != filepath.Join(home, ".ssh") {
		t.Fatalf("normal run: %q", got)
	}
	if got := New(true).Dir(); got != os.Getenv(EnvSSHDir) {
		t.Fatalf("shot run: %q", got)
	}
	t.Setenv(EnvSSHDir, "")
	if got := New(true).Dir(); got != filepath.Join(home, ".ssh") {
		t.Fatalf("shot run without override: %q", got)
	}
}

// Real OpenSSH, local only: a key generated with a passphrase through the
// askpass helper really is protected by that passphrase, its status parses,
// and a throwaway agent (never the user's) accepts it with the same helper.
func TestWithRealOpenSSH(t *testing.T) {
	for _, bin := range []string{"ssh-keygen", "ssh-add", "ssh-agent"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not installed", bin)
		}
	}
	if runtime.GOOS == "windows" {
		t.Skip("the askpass helper is a shell script")
	}
	root := fakebin.TempRoot(t)
	dir := filepath.Join(root, "ssh")
	fakebin.Clear(t)
	t.Setenv(EnvSSHDir, dir)
	t.Setenv("HOME", filepath.Join(root, "home"))
	// No agent yet: a socket path that does not exist.
	t.Setenv("SSH_AUTH_SOCK", filepath.Join(root, "no-agent"))
	s := NewService(New(true))
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const pass = "correct horse battery staple"
	pub, err := s.Generate(ctx, "id_test", "test@gitcito.invalid", pass)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("ssh-keygen", "-y", "-P", pass, "-f", filepath.Join(dir, "id_test")).CombinedOutput(); err != nil {
		t.Fatalf("the passphrase does not open the key: %v %s", err, out)
	}
	if err := exec.Command("ssh-keygen", "-y", "-P", "", "-f", filepath.Join(dir, "id_test")).Run(); err == nil {
		t.Fatal("the key is not protected")
	}
	nopass, err := s.Generate(ctx, "id_open", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("ssh-keygen", "-y", "-P", "", "-f", filepath.Join(dir, "id_open")).Run(); err != nil {
		t.Fatalf("a blank passphrase should mean none: %v", err)
	}
	if st, _ := os.Stat(dir); st.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %o", st.Mode().Perm())
	}

	status, err := s.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.AgentRunning || len(status.Keys) != 2 {
		t.Fatalf("status without agent: %+v", status)
	}
	k := status.Keys[1]
	if k.Path != pub || k.Type != "ed25519" || k.Bits != 256 || k.Comment != "test@gitcito.invalid" ||
		!strings.HasPrefix(k.Fingerprint, "SHA256:") || !k.HasPrivate || !strings.HasPrefix(k.PublicKey, "ssh-ed25519 ") {
		t.Fatalf("key %+v", k)
	}
	if status.Keys[0].Path != nopass {
		t.Fatalf("order %+v", status.Keys)
	}

	// A throwaway agent on a temp socket.
	sock := filepath.Join(root, "agent.sock")
	agent := exec.Command("ssh-agent", "-D", "-a", sock)
	if err := agent.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = agent.Process.Kill()
		_ = agent.Wait()
	})
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Setenv("SSH_AUTH_SOCK", sock)

	if msg, err := s.AddToAgent(ctx, pub, pass); err != nil || msg != "" {
		t.Fatalf("add with passphrase: %q %v", msg, err)
	}
	if msg, err := s.AddToAgent(ctx, nopass, ""); err != nil || msg != "" {
		t.Fatalf("add without passphrase: %q %v", msg, err)
	}
	status, err = s.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !status.AgentRunning || !status.Keys[0].InAgent || !status.Keys[1].InAgent {
		t.Fatalf("status with agent: %+v", status)
	}
}

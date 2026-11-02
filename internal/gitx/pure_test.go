package gitx

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestTrimJS(t *testing.T) {
	tests := []struct{ in, want string }{
		{"  a b  ", "a b"},
		{"\t\n\v\f\r x \r\n", "x"},
		{"\ufeffbom\ufeff", "bom"},             // JS trims the BOM, strings.TrimSpace does not
		{"\u0085nel\u0085", "\u0085nel\u0085"}, // JS keeps NEL, strings.TrimSpace does not
		{"\u00a0\u2003\u3000x\u2028", "x"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := TrimJS(tt.in); got != tt.want {
			t.Errorf("TrimJS(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	if got := TrimEndJS("  x \n"); got != "  x" {
		t.Errorf("TrimEndJS = %q", got)
	}
}

func TestUTF16(t *testing.T) {
	tests := []struct {
		in    string
		n     int
		slice string
		len   int
	}{
		{"abc", 2, "ab", 3},
		{"héllo", 2, "hé", 5},
		{"a😀b", 2, "a", 4}, // would cut the surrogate pair: dropped whole
		{"a😀b", 3, "a😀", 4},
		{"abc", 10, "abc", 3},
		{"abc", 0, "", 3},
	}
	for _, tt := range tests {
		if got := SliceUTF16(tt.in, tt.n); got != tt.slice {
			t.Errorf("SliceUTF16(%q,%d) = %q, want %q", tt.in, tt.n, got, tt.slice)
		}
		if got := UTF16Len(tt.in); got != tt.len {
			t.Errorf("UTF16Len(%q) = %d, want %d", tt.in, got, tt.len)
		}
	}
}

func TestMergeEnv(t *testing.T) {
	base := []string{"A=1", "B=2", "PATH=/bin"}
	got := MergeEnv(base, "B=3", "C=4", "junk")
	want := []string{"A=1", "B=3", "PATH=/bin", "C=4"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MergeEnv = %v, want %v", got, want)
	}
	if v := EnvValue(got, "B"); v != "3" {
		t.Errorf("EnvValue(B) = %q", v)
	}
	if v := EnvValue(got, "missing"); v != "" {
		t.Errorf("EnvValue(missing) = %q", v)
	}
	// Base is not modified.
	if base[1] != "B=2" {
		t.Error("MergeEnv mutated its input")
	}
}

func TestNoPromptEnv(t *testing.T) {
	t.Setenv("GIT_TERMINAL_PROMPT", "1")
	env := NoPromptEnv()
	n := 0
	for _, kv := range env {
		if strings.HasPrefix(kv, "GIT_TERMINAL_PROMPT=") {
			n++
		}
	}
	if n != 1 || EnvValue(env, "GIT_TERMINAL_PROMPT") != "0" {
		t.Fatalf("GIT_TERMINAL_PROMPT entries=%d value=%q", n, EnvValue(env, "GIT_TERMINAL_PROMPT"))
	}
	// Read at call time, like `{ ...process.env }`.
	t.Setenv("GITCITO_PROBE", "late")
	if EnvValue(NoPromptEnv(), "GITCITO_PROBE") != "late" {
		t.Error("NoPromptEnv does not see a variable set after startup")
	}
}

func TestRedactCredentials(t *testing.T) {
	tests := []struct{ in, want string }{
		{"fatal: unable to access 'https://oauth2:ghp_SECRET@github.com/a/b.git/'",
			"fatal: unable to access 'https://***@github.com/a/b.git/'"},
		{"https://user@host/x", "https://***@host/x"},
		{"https://:tok@dev.azure.com/o/p", "https://***@dev.azure.com/o/p"},
		{"two https://a:b@x/ and http://c:d@y/", "two https://***@x/ and http://***@y/"},
		{"no creds https://github.com/a/b", "no creds https://github.com/a/b"},
		{"ssh git@github.com:a/b", "ssh git@github.com:a/b"},
		{"https://***@github.com/x", "https://***@github.com/x"}, // idempotent
	}
	for _, tt := range tests {
		if got := RedactCredentials(tt.in); got != tt.want {
			t.Errorf("RedactCredentials(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestStripAnsi(t *testing.T) {
	tests := []struct{ in, want string }{
		{"\x1b[31mred\x1b[0m", "red"},
		{"\x1b[1;32mok\x1b[39;49m done", "ok done"},
		{"\x1b]0;title\x07text", "text"},
		{"\x1b]8;;http://x\x1b\\link\x1b]8;;\x1b\\", "link"},
		{"\x1b[?25lhidden\x1b[?25h", "hidden"},
		{"\x1bMx", "x"},
		{"plain", "plain"},
	}
	for _, tt := range tests {
		if got := StripAnsi(tt.in); got != tt.want {
			t.Errorf("StripAnsi(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestIsGitLockError(t *testing.T) {
	tests := []struct {
		msg  string
		want bool
	}{
		{"fatal: Unable to create '/r/.git/index.lock': File exists.", true},
		{"error: cannot lock ref 'refs/remotes/o/x': Unable to create '/r/.git/refs/remotes/o/x.lock': File exists.", true},
		{"Unable to create \"/r/.git/packed-refs.lock\": file EXISTS", true},
		{"fatal: not a git repository", false},
	}
	for _, tt := range tests {
		if got := IsGitLockError(errors.New(tt.msg)); got != tt.want {
			t.Errorf("IsGitLockError(%q) = %v", tt.msg, got)
		}
	}
	if IsGitLockError(nil) {
		t.Error("nil is not a lock error")
	}
}

func TestWithLockRetry(t *testing.T) {
	lockErr := errors.New("fatal: Unable to create '/r/.git/index.lock': File exists.")
	other := errors.New("boom")
	tests := []struct {
		name      string
		failures  []error
		wantCalls int
		wantErr   error
	}{
		{"success", nil, 1, nil},
		{"retries then succeeds", []error{lockErr, lockErr}, 3, nil},
		{"other error is not retried", []error{other}, 1, other},
		{"gives up after five", []error{lockErr, lockErr, lockErr, lockErr, lockErr, lockErr}, 5, lockErr},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			err := withLockRetry(context.Background(), func(context.Context) error {
				calls++
				if calls <= len(tt.failures) {
					return tt.failures[calls-1]
				}
				return nil
			}, time.Millisecond)
			if calls != tt.wantCalls || !errors.Is(err, tt.wantErr) && err != tt.wantErr {
				t.Fatalf("calls=%d err=%v, want calls=%d err=%v", calls, err, tt.wantCalls, tt.wantErr)
			}
		})
	}
	t.Run("cancelled context stops waiting", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		calls := 0
		err := withLockRetry(ctx, func(context.Context) error {
			calls++
			cancel()
			return lockErr
		}, time.Hour)
		if calls != 1 || err != lockErr {
			t.Fatalf("calls=%d err=%v", calls, err)
		}
	})
}

func TestLRU(t *testing.T) {
	size := func(s string) int { return len(s) }

	t.Run("evicts least recently used by count", func(t *testing.T) {
		c := NewLRU(2, 1000, size)
		c.Set("a", "1")
		c.Set("b", "2")
		if _, ok := c.Get("a"); !ok { // a is now most recent
			t.Fatal("a missing")
		}
		c.Set("c", "3")
		if _, ok := c.Get("b"); ok {
			t.Error("b should have been evicted")
		}
		for _, k := range []string{"a", "c"} {
			if _, ok := c.Get(k); !ok {
				t.Errorf("%s evicted", k)
			}
		}
	})
	t.Run("evicts by bytes", func(t *testing.T) {
		c := NewLRU(100, 10, size)
		c.Set("a", "1234")
		c.Set("b", "1234")
		c.Set("c", "1234") // 12 > 10: a goes
		if _, ok := c.Get("a"); ok || c.Len() != 2 || c.Bytes() != 8 {
			t.Fatalf("len=%d bytes=%d", c.Len(), c.Bytes())
		}
	})
	t.Run("refuses an entry over half the budget", func(t *testing.T) {
		c := NewLRU(100, 10, size)
		c.Set("big", "123456")
		if c.Len() != 0 {
			t.Fatal("oversized entry cached")
		}
		c.Set("k", "12")
		c.Set("k", "123456") // too big: the old value stays, as in TS
		if v, _ := c.Get("k"); v != "12" {
			t.Fatalf("k = %q", v)
		}
	})
	t.Run("replacing a key updates bytes", func(t *testing.T) {
		c := NewLRU(100, 100, size)
		c.Set("k", "12")
		c.Set("k", "1234")
		if c.Len() != 1 || c.Bytes() != 4 {
			t.Fatalf("len=%d bytes=%d", c.Len(), c.Bytes())
		}
	})
	t.Run("memo", func(t *testing.T) {
		c := NewLRU(10, 100, size)
		calls := 0
		fetch := func() (string, error) { calls++; return "v", nil }
		for i := 0; i < 3; i++ {
			if v, err := Memo(c, "k", fetch); v != "v" || err != nil {
				t.Fatal(v, err)
			}
		}
		if calls != 1 {
			t.Fatalf("fetch called %d times", calls)
		}
		boom := errors.New("boom")
		if _, err := Memo(c, "e", func() (string, error) { return "", boom }); err != boom {
			t.Fatal(err)
		}
		if _, ok := c.Get("e"); ok {
			t.Fatal("error result cached")
		}
	})
	if StrBytes("ab😀") != 8 {
		t.Errorf("StrBytes = %d", StrBytes("ab😀"))
	}
}

func TestParseCoAuthors(t *testing.T) {
	tests := []struct {
		raw  string
		want []CommitAuthor
	}{
		{"", []CommitAuthor{}},
		{"Ada Lovelace <ada@example.com>", []CommitAuthor{{"Ada Lovelace", "ada@example.com"}}},
		{"A <a@x>\x1d B  < b@y > \x1d\x1d", []CommitAuthor{{"A", "a@x"}, {"B", "b@y"}}},
		{"just a name", []CommitAuthor{{"just a name", ""}}},
		{"<only@mail>", []CommitAuthor{{"", "only@mail"}}},
	}
	for _, tt := range tests {
		if got := ParseCoAuthors(tt.raw); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("ParseCoAuthors(%q) = %#v, want %#v", tt.raw, got, tt.want)
		}
	}
}

func TestMapSignature(t *testing.T) {
	want := map[string]string{"G": "good", "U": "unverified", "E": "unverified", "X": "expired", "Y": "expired",
		"B": "bad", "R": "bad", "N": "none", "": "none", " G\n": "good", "?": "none"}
	for in, w := range want {
		if got := MapSignature(in); got != w {
			t.Errorf("MapSignature(%q) = %q, want %q", in, got, w)
		}
	}
}

func TestMapStatusCode(t *testing.T) {
	for in, w := range map[string]string{"A": "A", "D": "D", "R": "R", "C": "C", "U": "U", "?": "?", "M": "M", "T": "M", "": "M", " ": "M"} {
		if got := MapStatusCode(in); got != w {
			t.Errorf("MapStatusCode(%q) = %q, want %q", in, got, w)
		}
	}
}

func TestParseStashSubject(t *testing.T) {
	str := func(s string) *string { return &s }
	tests := []struct {
		in   string
		want StashSubject
	}{
		{"WIP on main: abc123 subject", StashSubject{str("main"), "abc123 subject"}},
		{"On feature/x: my message", StashSubject{str("feature/x"), "my message"}},
		{"On : nothing", StashSubject{nil, "nothing"}},
		{"On main: ", StashSubject{str("main"), "On main: "}},
		{"autostash", StashSubject{nil, "autostash"}},
	}
	for _, tt := range tests {
		got := ParseStashSubject(tt.in)
		if got.Message != tt.want.Message || (got.Branch == nil) != (tt.want.Branch == nil) ||
			(got.Branch != nil && *got.Branch != *tt.want.Branch) {
			t.Errorf("ParseStashSubject(%q) = %v/%q", tt.in, got.Branch, got.Message)
		}
	}
}

func TestFileMime(t *testing.T) {
	tests := map[string]string{
		"a/b/photo.PNG": "image/png", "x.svg": "image/svg+xml", "doc.pdf": "application/pdf",
		"v.mov": "video/quicktime", "s.xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		"Makefile": "application/octet-stream", "png": "image/png", // no dot: the name is its own "extension"
		"archive.tar.gz": "application/octet-stream", "trailing.": "application/octet-stream",
	}
	for in, w := range tests {
		if got := FileMime(in); got != w {
			t.Errorf("FileMime(%q) = %q, want %q", in, got, w)
		}
	}
}

func TestParseTrack(t *testing.T) {
	tests := map[string]Track{
		"[ahead 3, behind 2]": {3, 2}, "ahead 10": {10, 0}, "behind 1": {0, 1}, "": {0, 0}, "gone": {0, 0},
	}
	for in, w := range tests {
		if got := ParseTrack(in); got != w {
			t.Errorf("ParseTrack(%q) = %v, want %v", in, got, w)
		}
	}
}

func TestSmallPureHelpers(t *testing.T) {
	if got := EncodeSubtreeKey("vendor/lib/x"); got != "vendor%2Flib%2Fx" {
		t.Errorf("EncodeSubtreeKey = %q", got)
	}
	if got := DecodeSubtreeKey("vendor%2Flib%2Fx"); got != "vendor/lib/x" {
		t.Errorf("DecodeSubtreeKey = %q", got)
	}
	if got := PrePurgeKey(1700000000000); got != "gitcito.prepurge.at1700000000000" {
		t.Errorf("PrePurgeKey = %q", got)
	}
	ts := time.Date(2026, 3, 4, 5, 6, 7, 0, time.Local)
	if got := BackupStamp(ts); got != "20260304-050607" {
		t.Errorf("BackupStamp = %q", got)
	}
	if got := ParseRerereResolved("Auto-merging a\nResolved 'src/a.ts' using previous resolution.\nResolved 'b c' using previous resolution.\n"); !reflect.DeepEqual(got, []string{"src/a.ts", "b c"}) {
		t.Errorf("ParseRerereResolved = %q", got)
	}
	if got := ParseRerereResolved("nothing"); got == nil || len(got) != 0 {
		t.Errorf("ParseRerereResolved(none) = %#v", got)
	}
	if got := SplitLines("a\n\nb\n"); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("SplitLines = %q", got)
	}
	if got := SplitTrimmedLines(" a \n \n b"); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("SplitTrimmedLines = %q", got)
	}
	if len(KnownHooks) != 14 || KnownHooks[0] != "applypatch-msg" || KnownHooks[13] != "pre-auto-gc" {
		t.Errorf("KnownHooks = %v", KnownHooks)
	}
	if FileTooLargePrefix != "GITCITO_FILE_TOO_LARGE:" || SEP != "\x1f" || REC != "\x1e" || CreationFactor != "--creation-factor=85" {
		t.Error("a marker constant drifted from TS")
	}
}

func TestContiguousRuns(t *testing.T) {
	tests := []struct {
		in   []int
		want [][2]int
	}{
		{nil, [][2]int{}},
		{[]int{5}, [][2]int{{5, 5}}},
		{[]int{3, 1, 2, 2, 7, 8, 10}, [][2]int{{1, 3}, {7, 8}, {10, 10}}},
	}
	for _, tt := range tests {
		if got := ContiguousRuns(tt.in); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("ContiguousRuns(%v) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestMergeArgs(t *testing.T) {
	tests := []struct {
		o    MergeOptions
		want []string
	}{
		{MergeOptions{}, []string{}},
		{MergeOptions{NoFf: true}, []string{"--no-ff"}},
		{MergeOptions{NoFf: true, FfOnly: true}, []string{"--ff-only"}},
		{MergeOptions{Squash: true, NoCommit: true}, []string{"--squash", "--no-commit"}},
		{MergeOptions{Strategy: "ort"}, []string{}},
		{MergeOptions{Strategy: "subtree", Favour: "theirs"}, []string{"-s", "subtree", "-X", "theirs"}},
		{MergeOptions{IgnoreSpace: "eol"}, []string{"-X", "ignore-space-at-eol"}},
		{MergeOptions{IgnoreSpace: "all"}, []string{"-X", "ignore-space-all"}},
	}
	for _, tt := range tests {
		if got := MergeArgs(tt.o); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("MergeArgs(%+v) = %q, want %q", tt.o, got, tt.want)
		}
	}
}

func TestHostFromURL(t *testing.T) {
	tests := map[string]string{
		"https://github.com/a/b.git":          HostGitHub,
		"https://GitHub.Enterprise.corp/a/b":  HostGitHub,
		"ssh://git@gitlab.com/a/b.git":        HostGitLab,
		"https://user@bitbucket.org/a/b.git":  HostBitbucket,
		"https://dev.azure.com/o/p/_git/r":    HostAzure,
		"https://o.visualstudio.com/p/_git/r": HostAzure,
		"git@github.com:a/b.git":              "",
		"https://example.com/a.git":           "",
		"/local/path":                         "",
		"":                                    "",
	}
	for in, w := range tests {
		if got := HostFromURL(in); got != w {
			t.Errorf("HostFromURL(%q) = %q, want %q", in, got, w)
		}
	}
}

func TestAuthedCloneURL(t *testing.T) {
	tests := []struct {
		url, host, token, want string
	}{
		{"https://github.com/a/b.git", HostGitHub, "ghp_x", "https://oauth2:ghp_x@github.com/a/b.git"},
		{"https://gitlab.com/a/b.git", HostGitLab, " glpat \n", "https://oauth2:glpat@gitlab.com/a/b.git"},
		{"https://bitbucket.org/a/b.git", HostBitbucket, "me:app:pw", "https://me:app%3Apw@bitbucket.org/a/b.git"},
		{"https://bitbucket.org/a/b.git", HostBitbucket, "tok", "https://x-token-auth:tok@bitbucket.org/a/b.git"},
		{"https://dev.azure.com/o/p/_git/r", HostAzure, "pat", "https://:pat@dev.azure.com/o/p/_git/r"},
		{"https://old@GitHub.com:443", HostGitHub, "t", "https://oauth2:t@github.com/"},
		{"https://github.com:8443/a?x=1#f", HostGitHub, "t", "https://oauth2:t@github.com:8443/a?x=1#f"},
		{"https://github.com/a", HostGitHub, "a/b@c", "https://oauth2:a%2Fb%40c@github.com/a"},
		{"HTTPS://github.com/a", HostGitHub, "t", "https://oauth2:t@github.com/a"},
		{"http://github.com/a", HostGitHub, "t", "http://github.com/a"},     // not https
		{"https://github.com/a", HostGitHub, "   ", "https://github.com/a"}, // blank token
		{"https://github.com/a", "", "t", "https://github.com/a"},           // unknown host
		{"https://github.com/a", "gitea", "t", "https://github.com/a"},
		{"https://", HostGitHub, "t", "https://"}, // unparseable for a URL parser
	}
	for _, tt := range tests {
		if got := AuthedCloneURL(tt.url, tt.host, tt.token); got != tt.want {
			t.Errorf("AuthedCloneURL(%q, %q, %q) = %q, want %q", tt.url, tt.host, tt.token, got, tt.want)
		}
	}
}

func TestCheckUnsafe(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		allow []string
		want  string // "" = allowed
	}{
		{"plain read", []string{"log", "--oneline", "-5"}, nil, ""},
		{"config read", []string{"config", "--get", "core.hooksPath"}, nil, ""},
		{"inline editor", []string{"-c", "core.editor=true", "merge", "--continue"}, nil,
			"Configuring core.editor is not permitted without enabling allowUnsafeEditor"},
		{"inline editor allowed", []string{"-c", "core.editor=true", "merge", "--continue"}, []string{"allowUnsafeEditor"}, ""},
		{"sequence editor and core editor", []string{"-c", "sequence.editor=cp x", "-c", "core.editor=true", "rebase", "-i", "main"},
			[]string{"allowUnsafeEditor"}, ""},
		{"config write hooksPath", []string{"config", "core.hooksPath", ".githooks"}, nil,
			"Configuring core.hooksPath is not permitted without enabling allowUnsafeHooksPath"},
		{"config set verb", []string{"config", "set", "core.sshCommand", "ssh -i x"}, nil,
			"Configuring core.sshCommand is not permitted without enabling allowUnsafeSshCommand"},
		{"config unset is a write", []string{"config", "--unset", "core.pager"}, nil,
			"Configuring core.pager is not permitted without enabling allowUnsafePager"},
		{"config get one positional is a read", []string{"config", "core.pager"}, nil, ""},
		{"subsection credential helper", []string{"config", "credential.https://x.helper", "store"}, nil,
			"Configuring credential.helper is not permitted without enabling allowUnsafeCredentialHelper"},
		{"alias anywhere in key", []string{"-c", "x.alias.y=1", "status"}, nil,
			"Configuring alias is not permitted without enabling allowUnsafeAlias"},
		{"diff textconv", []string{"config", "diff.pdf.textconv", "pdftotext"}, nil,
			"Configuring diff.textconv is not permitted without enabling allowUnsafeDiffTextConv"},
		{"plain config write is fine", []string{"config", "user.name", "x"}, nil, ""},
		{"upload-pack", []string{"fetch", "--upload-pack=evil", "origin"}, nil,
			"Use of --upload-pack or --receive-pack is not permitted without enabling allowUnsafePack"},
		{"clone -u", []string{"clone", "-u", "evil", "url", "dir"}, nil,
			`Use of clone with option /^-\w*u/ is not permitted without enabling allowUnsafePack`},
		{"push --exec", []string{"push", "--exec=evil"}, nil,
			"Use of push with option --exec is not permitted without enabling allowUnsafePack"},
		{"template", []string{"init", "--template=/x"}, nil,
			"Use of --template is not permitted without enabling allowUnsafeTemplateDir"},
		{"-u on a non-clone task is fine", []string{"push", "-u", "origin", "main"}, nil, ""},
		{"commit -c is a commit ref, not config", []string{"commit", "-c", "HEAD"}, nil, ""},
		{"after -- everything is a path", []string{"log", "--", "--upload-pack"}, nil, ""},
		{"config-env", []string{"--config-env=core.editor=ED", "status"}, nil,
			"Configuring core.editor is not permitted without enabling allowUnsafeEditor"},
		{"'=' at index 0 is no key", []string{"-c", "=x", "status"}, nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckUnsafe(tt.args, tt.allow...)
			got := ""
			if err != nil {
				got = err.Error()
			}
			if got != tt.want {
				t.Fatalf("CheckUnsafe(%q) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

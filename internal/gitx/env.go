package gitx

import (
	"os"
	"runtime"
	"strings"
)

// NoPromptEnv is the process environment with git's interactive credential
// prompt disabled, for network ops run from the app.
//
// The app has no controlling TTY, so a prompt fails with the opaque macOS error
// "could not read Password … Device not configured". With this set, a missing
// credential surfaces immediately as a clear "could not read Username/Password"
// (terminal prompts disabled) error instead of hanging on /dev/tty.
//
// It reads the environment at call time, so a PATH restored by FixPath is
// seen by every later spawn — exactly like `{ ...process.env }` in TS.
func NoPromptEnv() []string {
	return MergeEnv(os.Environ(), "GIT_TERMINAL_PROMPT=0")
}

// MergeEnv returns base with each "KEY=VALUE" override applied, the way an
// object spread `{ ...base, ...overrides }` does: a key already present is
// replaced in place, a new one is appended. Keys compare case-insensitively on
// Windows, where the environment itself is case-insensitive. An override with
// no '=' is ignored.
func MergeEnv(base []string, overrides ...string) []string {
	out := make([]string, 0, len(base)+len(overrides))
	index := make(map[string]int, len(base)+len(overrides))
	put := func(kv string) {
		eq := strings.IndexByte(kv, '=')
		// Windows keeps per-drive cwd entries like "=C:=C:\\" — a leading '='
		// is part of the name, not the separator.
		if eq == 0 {
			if j := strings.IndexByte(kv[1:], '='); j >= 0 {
				eq = j + 1
			}
		}
		if eq < 0 {
			return
		}
		key := envKey(kv[:eq])
		if i, ok := index[key]; ok {
			out[i] = kv
			return
		}
		index[key] = len(out)
		out = append(out, kv)
	}
	for _, kv := range base {
		put(kv)
	}
	for _, kv := range overrides {
		put(kv)
	}
	return out
}

func envKey(k string) string {
	if runtime.GOOS == "windows" {
		return strings.ToUpper(k)
	}
	return k
}

// EnvValue reads key from an environment slice ("" when absent), with the
// same case rule as MergeEnv.
func EnvValue(env []string, key string) string {
	want := envKey(key)
	val := ""
	for _, kv := range env {
		eq := strings.IndexByte(kv, '=')
		if eq <= 0 {
			continue
		}
		if envKey(kv[:eq]) == want {
			val = kv[eq+1:] // last one wins, as it does for the OS
		}
	}
	return val
}

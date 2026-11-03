package launch

import (
	"net/netip"
	"regexp"
	"strconv"
	"strings"

	"github.com/GabrySpada/gitcito/internal/shared"
)

// ─── serverReadyAction ───────────────────────────────────────────────────────
// VS Code watches the debug terminal for a pattern and reacts once the server
// announces itself. We support the browser-opening actions: `openExternally`,
// and `debugWithChrome` / `debugWithEdge` degrade to opening the URL in the
// default browser (we can't attach a debugger). `startDebugging` is skipped.

var (
	reAnsi           = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
	reReadyAction    = regexp.MustCompile(`^(openexternally|debugwithchrome|debugwithedge)$`)
	reHTTPURL        = regexp.MustCompile(`(?i)^https?://`)
	reTrailingPunct  = regexp.MustCompile(`[).,]+$`)
	reLoopbackPrefix = regexp.MustCompile(`(?i)^https?://(localhost|127\.0\.0\.1|\[::1\]|0\.0\.0\.0)(:|/|$)`)
)

// tapBuffer is how much recent output a watcher keeps, so a match can span
// chunks.
const tapBuffer = 8192

// serverReadyTap returns the output watcher for a config's serverReadyAction,
// or nil when it has none we act on. open receives the URL (http(s) only).
func (h host) serverReadyTap(config LaunchConfig, folder string, open func(string)) func(string) {
	sra, ok := obj(config["serverReadyAction"])
	if !ok {
		return nil
	}
	pattern, ok := sra["pattern"].(string)
	if !ok || pattern == "" {
		return nil
	}
	action := "openexternally"
	if sra["action"] != nil {
		action = lower(sra["action"])
	}
	if !reReadyAction.MatchString(action) {
		return nil
	}
	// A JS pattern RE2 cannot express (lookaround, backreferences) is treated
	// like the invalid pattern `new RegExp` would have thrown on.
	re, err := shared.JSRegexp(pattern, false)
	if err != nil {
		return nil
	}
	uriFormat := "%s"
	if truthy(sra["uriFormat"]) {
		uriFormat = h.subAll(sra["uriFormat"], folder)
	}
	buf := ""
	fired := false
	return func(chunk string) {
		if fired {
			return
		}
		// Strip ANSI escapes so the pattern matches what the user reads, not the
		// color codes around it; keep a rolling tail so a match can span chunks.
		buf = shared.TailUTF16(buf+reAnsi.ReplaceAllLiteralString(chunk, ""), tapBuffer)
		m := re.FindStringSubmatchIndex(buf)
		if m == nil {
			return
		}
		fired = true
		value := buf[m[0]:m[1]]
		if len(m) >= 4 && m[2] >= 0 {
			value = buf[m[2]:m[3]]
		}
		url := shared.JSReplaceFirstString(uriFormat, "%s", value)
		if reHTTPURL.MatchString(url) && open != nil {
			open(url)
		}
	}
}

// devToolPatterns are the dev tools that announce themselves on stdout as a
// page you can open.
//
// The rule for being in this table is narrow: the tool must *serve a web UI on
// loopback* and *print its address*. That is why Flutter is here and the Node
// inspector is not — Node prints a `ws://` endpoint for a debugger to attach
// to, and the Chrome DevTools front end it pairs with lives behind a
// `devtools://` URL that no embedded view may load.
//
// Order matters only in that the first match wins; the patterns are specific
// enough not to overlap.
var devToolPatterns = []struct {
	name string
	re   *regexp.Regexp
}{
	// `flutter run`: "The Flutter DevTools debugger and profiler … is available at: <url>"
	{"Flutter DevTools", shared.MustJSRegexp(`flutter\s+devtools[^\n]*?(?:available at|at):?\s*(https?:\/\/\S+)`, true)},
	// `dart devtools`: "Serving DevTools at <url>"
	{"Dart DevTools", shared.MustJSRegexp(`serving devtools at\s*(https?:\/\/\S+)`, true)},
	// `npx @vue/devtools`: "Vue Devtools … listening on http://localhost:8098"
	{"Vue DevTools", shared.MustJSRegexp(`vue\s*devtools[^\n]*?(?:listening on|at)\s*(https?:\/\/\S+)`, true)},
	// `prisma studio`: "Prisma Studio is up on <url>"
	{"Prisma Studio", shared.MustJSRegexp(`prisma studio is up on\s*(https?:\/\/\S+)`, true)},
	// `drizzle-kit studio`: "Drizzle Studio is up and running on <url>"
	{"Drizzle Studio", shared.MustJSRegexp(`drizzle studio is up[^\n]*?on\s*(https?:\/\/\S+)`, true)},
	// webpack-bundle-analyzer: "Webpack Bundle Analyzer is started at <url>"
	{"Bundle Analyzer", shared.MustJSRegexp(`bundle analyzer is started at\s*(https?:\/\/\S+)`, true)},
	// Last: anything else that says DevTools and an address. A named tool must
	// be matched by its own line above, or it would be labelled by this one.
	{"DevTools", shared.MustJSRegexp(`devtools[^\n]*?(?:available at|listening on|at):?\s*(https?:\/\/\S+)`, true)},
}

// ExtractDevToolsURL finds the address a dev tool announced in text and the
// name of whatever announced it; nil when there is none or it is not served by
// this machine.
func ExtractDevToolsURL(text string) *DevToolsFound {
	for _, p := range devToolPatterns {
		m := p.re.FindStringSubmatch(text)
		if m == nil {
			continue
		}
		// Trailing punctuation from a sentence-shaped log line is not part of it.
		url := reTrailingPunct.ReplaceAllLiteralString(m[1], "")
		if !reLoopbackPrefix.MatchString(url) {
			continue
		}
		return &DevToolsFound{URL: url, Name: p.name}
	}
	return nil
}

// devToolsTap watches a session's output for a dev tool's address. The URL
// changes when the app restarts, so this keeps reporting rather than firing
// once like serverReadyAction.
func devToolsTap(report func(DevToolsFound)) func(string) {
	buf, last := "", ""
	return func(chunk string) {
		buf = shared.TailUTF16(buf+reAnsi.ReplaceAllLiteralString(chunk, ""), tapBuffer)
		found := ExtractDevToolsURL(buf)
		if found == nil || found.URL == last {
			return
		}
		last = found.URL
		buf = ""
		report(*found)
	}
}

// ─── loopback check (src/main/index.ts isLoopbackUrl) ────────────────────────

// IsLoopbackURL reports whether raw is an http(s) URL served by this machine:
// localhost, 127.0.0.1, ::1 or 0.0.0.0. It is the gate in front of opening a
// dev tool's page, so it follows the WHATWG URL parser where hosts can be
// spelled more than one way (case, percent-encoding, IPv4 shorthand like
// 127.1 or 0x7f.0.0.1, IPv6 forms). A host the WHATWG parser would map through
// IDNA (non-ASCII) is refused rather than guessed at — failing closed.
func IsLoopbackURL(raw string) bool {
	host, ok := whatwgHTTPHost(raw)
	if !ok {
		return false
	}
	return host == "localhost" || host == "127.0.0.1" || host == "::1" || host == "0.0.0.0"
}

// whatwgHTTPHost extracts and canonicalises the host of an http(s) URL the way
// `new URL(raw).hostname` does (brackets removed). ok is false for anything
// that is not http(s) or that the WHATWG parser would reject.
func whatwgHTTPHost(raw string) (string, bool) {
	// Leading/trailing C0 controls and spaces are stripped; tabs and newlines
	// anywhere are removed.
	s := strings.TrimFunc(raw, func(r rune) bool { return r <= 0x20 })
	s = strings.NewReplacer("\t", "", "\n", "", "\r", "").Replace(s)
	colon := strings.IndexByte(s, ':')
	if colon <= 0 {
		return "", false
	}
	scheme := strings.ToLower(s[:colon])
	if scheme != "http" && scheme != "https" {
		return "", false
	}
	rest := s[colon+1:]
	// Special schemes skip any run of slashes and backslashes before the host.
	rest = strings.TrimLeft(rest, `/\`)
	end := strings.IndexAny(rest, `/\?#`)
	if end >= 0 {
		rest = rest[:end]
	}
	if at := strings.LastIndexByte(rest, '@'); at >= 0 {
		rest = rest[at+1:]
	}
	hostPart, port := rest, ""
	if strings.HasPrefix(rest, "[") {
		close := strings.IndexByte(rest, ']')
		if close < 0 {
			return "", false
		}
		hostPart, port = rest[:close+1], rest[close+1:]
		if port != "" {
			if port[0] != ':' {
				return "", false
			}
			port = port[1:]
		}
	} else if i := strings.LastIndexByte(rest, ':'); i >= 0 {
		hostPart, port = rest[:i], rest[i+1:]
	}
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 0 || n > 65535 || strings.ContainsAny(port, "+-") {
			return "", false
		}
	}
	if hostPart == "" {
		return "", false
	}
	if strings.HasPrefix(hostPart, "[") {
		addr, err := netip.ParseAddr(hostPart[1 : len(hostPart)-1])
		if err != nil || !addr.Is6() || addr.Zone() != "" {
			return "", false
		}
		return addr.String(), true
	}
	host, ok := percentDecode(hostPart)
	if !ok {
		return "", false
	}
	for _, r := range host {
		if r >= 0x80 {
			return "", false // would need IDNA; refuse rather than guess
		}
	}
	host = strings.ToLower(host)
	if strings.ContainsAny(host, "\x00\t\n\r #/:<>?@[\\]^|%") || host == "" {
		return "", false
	}
	if ends, ok := endsInNumber(host); ends {
		if !ok {
			return "", false
		}
		return parseWhatwgIPv4(host)
	}
	return host, true
}

func percentDecode(s string) (string, bool) {
	if !strings.Contains(s, "%") {
		return s, true
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+3], 16, 8); err == nil {
				b.WriteByte(byte(v))
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String(), true
}

// endsInNumber is the WHATWG "ends in a number" check: the last label (after
// dropping one trailing empty label) is all digits, or a 0x hex number.
func endsInNumber(host string) (ends bool, ok bool) {
	parts := strings.Split(host, ".")
	if parts[len(parts)-1] == "" {
		if len(parts) == 1 {
			return false, true
		}
		parts = parts[:len(parts)-1]
	}
	last := parts[len(parts)-1]
	if last != "" && strings.Trim(last, "0123456789") == "" {
		return true, true
	}
	_, isNum := ipv4Number(last)
	return isNum, true
}

// ipv4Number parses one IPv4 part: decimal, 0-prefixed octal or 0x hex.
func ipv4Number(p string) (uint64, bool) {
	if p == "" {
		return 0, false
	}
	base := 10
	switch {
	case len(p) >= 2 && (p[:2] == "0x" || p[:2] == "0X"):
		p, base = p[2:], 16
		if p == "" {
			return 0, true
		}
	case len(p) >= 2 && p[0] == '0':
		p, base = p[1:], 8
	}
	v, err := strconv.ParseUint(p, base, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func parseWhatwgIPv4(host string) (string, bool) {
	parts := strings.Split(host, ".")
	if parts[len(parts)-1] == "" && len(parts) > 1 {
		parts = parts[:len(parts)-1]
	}
	if len(parts) > 4 {
		return "", false
	}
	nums := make([]uint64, len(parts))
	for i, p := range parts {
		v, ok := ipv4Number(p)
		if !ok {
			return "", false
		}
		nums[i] = v
	}
	for _, v := range nums[:len(nums)-1] {
		if v > 255 {
			return "", false
		}
	}
	last := nums[len(nums)-1]
	if last >= 1<<(8*(5-len(nums))) {
		return "", false
	}
	ipv4 := last
	for i, v := range nums[:len(nums)-1] {
		ipv4 += v << (8 * (3 - i))
	}
	return strconv.FormatUint(ipv4>>24, 10) + "." + strconv.FormatUint(ipv4>>16&255, 10) + "." +
		strconv.FormatUint(ipv4>>8&255, 10) + "." + strconv.FormatUint(ipv4&255, 10), true
}

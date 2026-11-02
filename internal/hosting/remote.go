package hosting

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

// ParsedRemote is a hosting remote broken into the parts the APIs address.
type ParsedRemote struct {
	Provider string `json:"provider"` // github | azure | gitlab | bitbucket
	Owner    string `json:"owner"`    // github owner / azure organization / gitlab namespace
	Project  string `json:"project"`  // azure project ('' elsewhere)
	Repo     string `json:"repo"`
}

// The provider names (RepoHost / HostingProvider in src/shared/types.ts).
const (
	ProviderGitHub    = "github"
	ProviderAzure     = "azure"
	ProviderGitLab    = "gitlab"
	ProviderBitbucket = "bitbucket"
)

// jsDot is JavaScript's `.` without the `s` flag: anything but a line
// terminator. Go's `.` only excludes '\n'.
const jsDot = `[^\n\r\x{2028}\x{2029}]`

var (
	reGitHub     = regexp.MustCompile(`github\.com[/:]([^/]+)/([^/]+?)(\.git)?$`)
	reAzure      = regexp.MustCompile(`dev\.azure\.com/([^/]+)/([^/]+)/_git/([^/]+?)(\.git)?$`)
	reAzureSSH   = regexp.MustCompile(`ssh\.dev\.azure\.com[/:]v3/([^/]+)/([^/]+)/(` + jsDot + `+?)(\.git)?$`)
	reVSTS       = regexp.MustCompile(`([^/@:]+)\.visualstudio\.com/([^/]+)/_git/([^/]+?)(\.git)?$`)
	reGitLab     = regexp.MustCompile(`gitlab\.com[/:](` + jsDot + `+?)(?:\.git)?$`)
	reBitbucket  = regexp.MustCompile(`bitbucket\.org[/:]([^/]+)/(` + jsDot + `+?)(?:\.git)?$`)
	errMalformed = errors.New("URI malformed")
)

// ParseRemoteURL recognises GitHub, Azure DevOps, GitLab and Bitbucket remotes
// (https and ssh). It returns nil for any other host. The error is the
// `URIError: URI malformed` TS threw when an Azure project or repository
// segment carries a broken percent-escape.
func ParseRemoteURL(url string) (*ParsedRemote, error) {
	if m := reGitHub.FindStringSubmatch(url); m != nil {
		return &ParsedRemote{Provider: ProviderGitHub, Owner: m[1], Repo: m[2]}, nil
	}
	if m := reAzure.FindStringSubmatch(url); m != nil {
		return azureRemote(m[1], m[2], m[3], true)
	}
	if m := reAzureSSH.FindStringSubmatch(url); m != nil {
		return azureRemote(m[1], m[2], m[3], false)
	}
	if m := reVSTS.FindStringSubmatch(url); m != nil {
		return azureRemote(m[1], m[2], m[3], true)
	}
	// GitLab: namespace may be multi-level (group/subgroup/repo) — owner holds the
	// full namespace path, repo the last segment.
	if m := reGitLab.FindStringSubmatch(url); m != nil {
		full := m[1]
		if i := strings.LastIndex(full, "/"); i > 0 {
			return &ParsedRemote{Provider: ProviderGitLab, Owner: full[:i], Repo: full[i+1:]}, nil
		}
	}
	if m := reBitbucket.FindStringSubmatch(url); m != nil {
		return &ParsedRemote{Provider: ProviderBitbucket, Owner: m[1], Repo: m[2]}, nil
	}
	return nil, nil
}

func azureRemote(org, project, repo string, decode bool) (*ParsedRemote, error) {
	if decode {
		var err error
		if project, err = decodeURIComponent(project); err != nil {
			return nil, err
		}
		if repo, err = decodeURIComponent(repo); err != nil {
			return nil, err
		}
	}
	return &ParsedRemote{Provider: ProviderAzure, Owner: org, Project: project, Repo: repo}, nil
}

// encodeURIComponent escapes everything but A–Z a–z 0–9 - _ . ! ~ * ' ( ),
// UTF-8 encoded, upper-case hex — exactly the JavaScript builtin.
func encodeURIComponent(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			strings.IndexByte("-_.!~*'()", c) >= 0 {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&15])
	}
	return b.String()
}

func unhex(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// decodeURIComponent is the JavaScript builtin: every %XX is decoded (reserved
// characters included), '+' stays '+', and a malformed escape or an invalid
// UTF-8 sequence is `URIError: URI malformed`.
func decodeURIComponent(s string) (string, error) {
	if !strings.Contains(s, "%") {
		return s, nil
	}
	byteAt := func(i int) (byte, bool) {
		if i+2 >= len(s) || s[i] != '%' {
			return 0, false
		}
		hi, ok1 := unhex(s[i+1])
		lo, ok2 := unhex(s[i+2])
		if !ok1 || !ok2 {
			return 0, false
		}
		return hi<<4 | lo, true
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] != '%' {
			b.WriteByte(s[i])
			i++
			continue
		}
		lead, ok := byteAt(i)
		if !ok {
			return "", errMalformed
		}
		i += 3
		if lead < 0x80 {
			b.WriteByte(lead)
			continue
		}
		n := 0
		switch {
		case lead&0xE0 == 0xC0:
			n = 2
		case lead&0xF0 == 0xE0:
			n = 3
		case lead&0xF8 == 0xF0:
			n = 4
		default:
			return "", errMalformed
		}
		seq := []byte{lead}
		for k := 1; k < n; k++ {
			if i >= len(s) {
				return "", errMalformed
			}
			c, ok := byteAt(i)
			if !ok || c&0xC0 != 0x80 {
				return "", errMalformed
			}
			seq = append(seq, c)
			i += 3
		}
		r, size := utf8.DecodeRune(seq)
		if r == utf8.RuneError || size != n {
			return "", errMalformed
		}
		b.Write(seq)
	}
	return b.String(), nil
}

// webURL is the subset of a WHATWG URL record this package reads: what
// `new URL(x)` yields for protocol, host and pathname.
type webURL struct {
	protocol string // "https:"
	host     string // hostname plus a non-default port
	pathname string
}

var (
	schemeRe = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9+.\-]*):`)
	special  = map[string]string{"http": "80", "https": "443", "ws": "80", "wss": "443", "ftp": "21", "file": ""}
)

// parseWebURL approximates `new URL(raw)` for the URLs a remote can hold. It
// follows the WHATWG parser for the common shapes — scheme required, special
// schemes lower-case their host and drop the default port, backslashes count
// as slashes, the path is percent-encoded and dot segments are resolved — and
// reports failure (TS's `catch`) where `new URL` would throw. IDNA hosts and
// IPv6 normalisation are not reproduced.
func parseWebURL(raw string) (webURL, bool) {
	s := strings.Trim(raw, "\x00\x01\x02\x03\x04\x05\x06\x07\x08\x09\x0a\x0b\x0c\x0d\x0e\x0f"+
		"\x10\x11\x12\x13\x14\x15\x16\x17\x18\x19\x1a\x1b\x1c\x1d\x1e\x1f ")
	s = strings.NewReplacer("\t", "", "\n", "", "\r", "").Replace(s)
	m := schemeRe.FindStringSubmatch(s)
	if m == nil {
		return webURL{}, false
	}
	scheme := strings.ToLower(m[1])
	rest := s[len(m[0]):]
	defPort, isSpecial := special[scheme]
	if isSpecial {
		rest = strings.ReplaceAll(rest, `\`, "/")
	}
	if !strings.HasPrefix(rest, "//") {
		if isSpecial && scheme != "file" {
			rest = "//" + strings.TrimLeft(rest, "/")
		} else {
			// An opaque or path-only URL: no host.
			return webURL{protocol: scheme + ":", pathname: cutQueryFragment(rest)}, true
		}
	}
	if isSpecial {
		rest = strings.TrimLeft(rest, "/")
	} else {
		rest = rest[2:]
	}
	end := strings.IndexAny(rest, "/?#")
	authority, tail := rest, ""
	if end >= 0 {
		authority, tail = rest[:end], rest[end:]
	}
	if at := strings.LastIndex(authority, "@"); at >= 0 {
		authority = authority[at+1:]
	}
	host, port := authority, ""
	if i := strings.LastIndex(authority, ":"); i >= 0 && !strings.HasSuffix(authority, "]") {
		host, port = authority[:i], authority[i+1:]
	}
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n > 65535 || strings.ContainsAny(port, "+-") {
			return webURL{}, false
		}
		port = strconv.Itoa(n)
	}
	if isSpecial {
		if host == "" {
			return webURL{}, false
		}
		decoded, err := decodeURIComponent(host)
		if err != nil {
			decoded = host
		}
		host = strings.ToLower(decoded)
		if strings.ContainsAny(host, " #%/:<>?@[\\]^|") && !strings.HasPrefix(host, "[") {
			return webURL{}, false
		}
		if port == defPort {
			port = ""
		}
	}
	if port != "" {
		host += ":" + port
	}
	path := cutQueryFragment(tail)
	if isSpecial {
		path = normalizePath(path)
	}
	return webURL{protocol: scheme + ":", host: host, pathname: encodePath(path)}, true
}

func cutQueryFragment(s string) string {
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		return s[:i]
	}
	return s
}

// normalizePath resolves "." and ".." segments (also their %2e spellings) and
// gives an empty special-scheme path its "/".
func normalizePath(p string) string {
	if p == "" {
		return "/"
	}
	segs := strings.Split(p[1:], "/")
	out := []string{}
	for i, seg := range segs {
		last := i == len(segs)-1
		switch strings.ToLower(seg) {
		case ".", "%2e":
			if last {
				out = append(out, "")
			}
		case "..", ".%2e", "%2e.", "%2e%2e":
			if len(out) > 0 {
				out = out[:len(out)-1]
			}
			if last {
				out = append(out, "")
			}
		default:
			out = append(out, seg)
		}
	}
	return "/" + strings.Join(out, "/")
}

// encodePath percent-encodes the WHATWG path percent-encode set (C0 controls,
// space, " # < > ? ` { } and everything above ASCII), leaving existing escapes.
func encodePath(p string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		c := p[i]
		if c <= 0x20 || c >= 0x7F || strings.IndexByte("\"#<>?`{}", c) >= 0 {
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&15])
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// urlHost is `new URL(raw).host`, or "" where the constructor would throw.
func urlHost(raw string) string {
	u, ok := parseWebURL(raw)
	if !ok {
		return ""
	}
	return u.host
}

// Web (not API) addresses: these end up in links the user opens, so they stay
// the real hosts even when the API endpoints are redirected (tests).
const (
	webGitHub    = "https://github.com"
	webGitLab    = "https://gitlab.com"
	webBitbucket = "https://bitbucket.org"
	webAzure     = "https://dev.azure.com"
)

// createPullRequestURL is the provider's "open a pull request" page for
// source → target, or "" for an unrecognised remote.
func createPullRequestURL(remoteURL, source, target string) (string, error) {
	p, err := ParseRemoteURL(remoteURL)
	if err != nil || p == nil {
		return "", err
	}
	switch p.Provider {
	case ProviderGitHub:
		return webGitHub + "/" + p.Owner + "/" + p.Repo + "/compare/" + target + "..." + source + "?expand=1", nil
	case ProviderGitLab:
		return webGitLab + "/" + p.Owner + "/" + p.Repo + "/-/merge_requests/new?merge_request%5Bsource_branch%5D=" +
			encodeURIComponent(source) + "&merge_request%5Btarget_branch%5D=" + encodeURIComponent(target), nil
	case ProviderBitbucket:
		return webBitbucket + "/" + p.Owner + "/" + p.Repo + "/pull-requests/new?source=" + encodeURIComponent(source) +
			"&dest=" + encodeURIComponent(target), nil
	}
	return webAzure + "/" + p.Owner + "/" + encodeURIComponent(p.Project) + "/_git/" + encodeURIComponent(p.Repo) +
		"/pullrequestcreate?sourceRef=" + encodeURIComponent(source) + "&targetRef=" + encodeURIComponent(target), nil
}

// ProviderBaseURL is the URL a provider's credential is filed under when there
// is no repository in hand (Settings, the clone browser). Azure DevOps keys per
// organization, since each one needs its own token. An unknown provider has
// none ("").
func ProviderBaseURL(provider, org string) string {
	switch provider {
	case ProviderGitHub:
		return webGitHub
	case ProviderGitLab:
		return webGitLab
	case ProviderBitbucket:
		return webBitbucket
	case ProviderAzure:
		return webAzure + "/" + encodeURIComponent(gitx.TrimJS(org))
	}
	return ""
}

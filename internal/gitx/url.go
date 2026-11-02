package gitx

import (
	"net/url"
	"regexp"
	"strings"
)

// Hosting providers a personal access token can authenticate (RepoHost).
const (
	HostGitHub    = "github"
	HostGitLab    = "gitlab"
	HostBitbucket = "bitbucket"
	HostAzure     = "azure"
)

// HostFromURL maps a remote URL's hostname to the provider whose PAT can
// authenticate it; "" when it is none of them, or not a parseable URL (e.g.
// scp-style ssh `git@github.com:a/b`, which has no host to a URL parser).
func HostFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Opaque != "" {
		return ""
	}
	h := strings.ToLower(u.Hostname())
	switch {
	case strings.Contains(h, "github"):
		return HostGitHub
	case strings.Contains(h, "gitlab"):
		return HostGitLab
	case strings.Contains(h, "bitbucket"):
		return HostBitbucket
	case strings.Contains(h, "azure") || strings.HasSuffix(h, "visualstudio.com"):
		return HostAzure
	}
	return ""
}

var httpsPrefixRe = regexp.MustCompile(`(?i)^https://`)

// AuthedCloneURL injects credentials into an https clone URL so private repos
// can be cloned (and pushed/pulled) non-interactively. Anything that is not
// https, an empty token, or an unknown host returns the URL untouched.
//
// The result is serialized the way the WHATWG URL parser (`new URL(...)
// .toString()`) does for the common shapes: lower-cased host, default port
// dropped, empty path becomes "/", userinfo percent-encoded with the userinfo
// set. Exotic inputs (dot segments, IDNA hosts, embedded tabs) are not
// normalised the way a browser would.
func AuthedCloneURL(raw, host, token string) string {
	t := TrimJS(token)
	if t == "" || !httpsPrefixRe.MatchString(raw) {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return raw
	}
	var user, pass string
	switch host {
	case HostGitHub, HostGitLab:
		user, pass = "oauth2", t
	case HostBitbucket:
		// token stored as username:app_password
		if i := strings.IndexByte(t, ':'); i >= 0 {
			user, pass = t[:i], t[i+1:]
		} else {
			user, pass = "x-token-auth", t
		}
	case HostAzure:
		user, pass = "", t
	default:
		return raw
	}
	return serializeHTTPS(u, user, pass)
}

func serializeHTTPS(u *url.URL, user, pass string) string {
	var b strings.Builder
	b.WriteString("https://")
	if user != "" || pass != "" {
		b.WriteString(percentEncode(user, userinfoSet))
		if pass != "" {
			b.WriteByte(':')
			b.WriteString(percentEncode(pass, userinfoSet))
		}
		b.WriteByte('@')
	}
	hostname := u.Hostname()
	if strings.Contains(hostname, ":") {
		b.WriteString("[" + strings.ToLower(hostname) + "]")
	} else {
		b.WriteString(strings.ToLower(hostname))
	}
	if p := u.Port(); p != "" && p != "443" {
		b.WriteString(":" + p)
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	b.WriteString(percentEncode(strings.ReplaceAll(path, `\`, "/"), pathSet))
	if u.ForceQuery || u.RawQuery != "" {
		b.WriteString("?" + percentEncode(u.RawQuery, specialQuerySet))
	}
	if u.Fragment != "" || u.RawFragment != "" {
		b.WriteString("#" + percentEncode(u.EscapedFragment(), fragmentSet))
	}
	return b.String()
}

// WHATWG percent-encode sets (beyond C0 controls and non-ASCII, which every
// set encodes). '%' is never encoded: an existing escape stays as is.
const (
	fragmentSet     = " \"<>`"
	pathSet         = " \"<>`#?{}"
	specialQuerySet = " \"#<>'"
	userinfoSet     = " \"#<>`?{}/:;=@[\\]^|"
)

func percentEncode(s, set string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c > 0x7e || strings.IndexByte(set, c) >= 0 {
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&15])
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

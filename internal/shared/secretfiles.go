package shared

import "regexp"

// Heuristics for spotting secret-bearing files by path. Mirrors
// src/shared/secretFiles.ts, which the renderer keeps using for masking; the
// Go side needs it for repository chat and the wiki, which must never read a
// credential file into a model prompt.

var secretFilePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(^|/)\.env(\.[\w.-]+)?$`), // .env, .env.local, .env.production…
	regexp.MustCompile(`(?i)(^|/)\.envrc$`),
	regexp.MustCompile(`(?i)\.(pem|key|p12|pfx|keystore|jks)$`),
	regexp.MustCompile(`(?i)(^|/)id_(rsa|dsa|ecdsa|ed25519)$`),
	regexp.MustCompile(`(?i)(^|/)\.npmrc$`),
	regexp.MustCompile(`(?i)(^|/)\.pgpass$`),
	// Apple signing material. A .mobileprovision embeds the team's certificates,
	// and a .p8 is an App Store Connect key — both are committed by accident far
	// more often than on purpose. A .cer is deliberately public, so it is absent.
	regexp.MustCompile(`(?i)\.(mobileprovision|provisionprofile|p8)$`),
	regexp.MustCompile(`(?i)(^|/)\.netrc$`),
	regexp.MustCompile(`(?i)(^|/)credentials(\.json)?$`),
	regexp.MustCompile(`(?i)(^|/)secrets?\.(ya?ml|json|toml|ini)$`),
}

// Committed-by-design templates that merely share the .env shape — never masked
// or guarded, since they hold placeholders, not real secrets.
var secretFileAllow = regexp.MustCompile(`(?i)(^|/)\.env\.(example|sample|template|dist|defaults?)$`)

// IsSecretFile reports whether a path looks like it holds credentials.
func IsSecretFile(path string) bool {
	if secretFileAllow.MatchString(path) {
		return false
	}
	for _, re := range secretFilePatterns {
		if re.MatchString(path) {
			return true
		}
	}
	return false
}

// Package secure is the port of src/main/secureBundle.ts (the .gitcito
// format) and src/main/secureShare.ts (the `secure:*` channels).
//
// Pure pack/unpack logic for .gitcito secure bundles — a password-encrypted set
// of sections shared out-of-band. A section is either a repo's files (typically
// .env and friends, re-materialised at the same relative paths in another
// checkout), the global vault's key/value secrets, a workspace's tab structure,
// or a repo's git notes. bundle.go has no I/O so it stays unit-testable;
// dialogs, fs and git live in share.go.
//
// Envelope (plaintext JSON): format marker, version, project label, a per-section
// summary (repo folder/remote for import-time matching, or vault entry count) —
// everything the import UI needs before the password. Payload: JSON
// `{ sections: [...] }`, encrypted with AES-256-GCM under a scrypt-derived key.
// GCM authenticates, so a wrong password and a tampered file both fail the tag.
//
// v1 (legacy single-repo) bundles carried `{ files: [...] }` and no sections; we
// still read them, upgrading to a single repo section on unpack.
//
// Compatibility: v4 (Electron) and v5 (this port) users exchange these files,
// so the output is byte-identical to the TS for the same salt, IV and clock —
// testdata/vectors/secure-bundle.json pins it, and the TS opens what Go writes
// (testdata/vectors/secure-go-produced/).
package secure

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/GabrySpada/gitcito/internal/vault"
	"golang.org/x/crypto/scrypt"
)

// BundleFormat is the envelope's format marker.
const BundleFormat = "gitcito-secure-bundle"

// BundleVersion is the newest version this build reads and writes. v2: repo +
// vault sections. v3 added workspace-structure and git-notes sections; a bundle
// is stamped v3 only when it carries one, so older builds refuse it cleanly as
// unsupported-version instead of misreading it.
const BundleVersion = 3

const baseVersion = 2

// The scrypt parameters, fixed: decrypt never reads them from the file.
const (
	scryptN      = 32768
	scryptR      = 8
	scryptP      = 1
	scryptKeyLen = 32
	saltLen      = 16
	ivLen        = 12
)

// NotesRef is the one notes namespace bundles carry: the user-facing commit
// notes. The app's own refs (e.g. refs/notes/gitcito-ci) stay machine-local on
// purpose.
const NotesRef = "refs/notes/commits"

// The section kinds.
const (
	KindRepo      = "repo"
	KindVault     = "vault"
	KindWorkspace = "workspace"
	KindNotes     = "notes"
)

// BundleFile is one repo file inside a bundle.
type BundleFile struct {
	Path       string
	Content    []byte
	Executable bool
}

// BundleVaultEntry is a portable vault secret (no id/updatedAt).
type BundleVaultEntry = vault.BundleEntry

// BundleNote is one git note, pinned to the commit it annotates.
type BundleNote struct {
	Sha  string `json:"sha"`
	Body string `json:"body"`
}

// Section is a section of a bundle, in memory (raw bytes, secret values). Repo
// files land at relative paths in a target repo; vault entries merge into the
// global vault; a workspace section recreates tab structure; a notes section
// applies git notes into a matched repo. Which fields are meaningful depends on
// Kind, as in the TS union.
type Section struct {
	Kind string
	// repo
	Project string
	// repo, notes
	Folder string
	// repo, notes. Nil when absent; "" when the bundle carried an empty string.
	Remote *string
	Files  []BundleFile
	// vault
	Entries []BundleVaultEntry
	// workspace. Tabs are the renderer's objects, kept verbatim (key order
	// included) as parsed JSON values.
	Name string
	Tabs []any
	// notes
	Ref   string
	Notes []BundleNote
}

// BundleError is BundleError: Code is the SecureShareError the renderer gets.
type BundleError struct{ Code string }

func (e *BundleError) Error() string { return e.Code }

// The BundleError codes.
const (
	ErrInvalid            = "invalid"
	ErrBadPassword        = "bad-password"
	ErrUnsupportedVersion = "unsupported-version"
	ErrReadFailed         = "read-failed"
	ErrWriteFailed        = "write-failed"
)

func bundleErr(code string) error { return &BundleError{Code: code} }

// errCode is errCode: a BundleError's code, anything else is read-failed.
func errCode(err error) string {
	var be *BundleError
	if errors.As(err, &be) {
		return be.Code
	}
	return ErrReadFailed
}

var driveLetter = regexp.MustCompile(`^[a-zA-Z]:`)

// IsSafeRelPath is isSafeRelPath: true for a relative path that cannot escape
// the repo it is written into — no absolute paths, drive letters, `.`/`..`
// segments, or empty segments.
func IsSafeRelPath(p string) bool {
	if p == "" || utf16Len(p) > 1024 {
		return false
	}
	norm := strings.ReplaceAll(p, `\`, "/")
	if strings.HasPrefix(norm, "/") || driveLetter.MatchString(norm) {
		return false
	}
	for _, seg := range strings.Split(norm, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

func deriveKey(password string, salt []byte) ([]byte, error) {
	return scrypt.Key([]byte(password), salt, scryptN, scryptR, scryptP, scryptKeyLen)
}

func remoteFields(o jsObject, remote *string) jsObject {
	if remote != nil && *remote != "" {
		o = append(o, jsField{"remote", *remote})
	}
	return o
}

// repoCount is `t.kind === 'repo' ? 1 : t.repos.length` over the tabs. A tab
// shape that would make the TS throw returns an error; one whose `.length` is
// not a number yields NaN, which JSON writes as null — exactly what JS did.
func repoCount(tabs []any) (float64, error) {
	n := 0.0
	for _, t := range tabs {
		obj, isObj := t.(jsObject)
		if t == nil {
			return 0, errors.New("tab is null: cannot read its kind")
		}
		if isObj && obj.get("kind") == KindRepo {
			n++
			continue
		}
		var repos any
		if isObj {
			repos = obj.get("repos")
		}
		switch r := repos.(type) {
		case nil:
			return 0, errors.New("tab has no repos: cannot read their length")
		case []any:
			n += float64(len(r))
		case string:
			n += float64(utf16Len(r))
		case jsObject:
			if l, ok := jsNum(r.get("length")); ok {
				n += l
			} else {
				n = nan()
			}
		default:
			n = nan()
		}
	}
	return n, nil
}

func nan() float64 { return math.NaN() }

func summarise(s Section) (jsObject, error) {
	switch s.Kind {
	case KindVault:
		return jsObject{{"kind", KindVault}, {"entryCount", len(s.Entries)}}, nil
	case KindWorkspace:
		rc, err := repoCount(s.Tabs)
		if err != nil {
			return nil, err
		}
		return jsObject{{"kind", KindWorkspace}, {"name", s.Name}, {"tabCount", len(s.Tabs)}, {"repoCount", rc}}, nil
	case KindNotes:
		o := remoteFields(jsObject{{"kind", KindNotes}, {"folder", s.Folder}}, s.Remote)
		return append(o, jsField{"ref", s.Ref}, jsField{"noteCount", len(s.Notes)}), nil
	}
	o := remoteFields(jsObject{{"kind", KindRepo}, {"project", s.Project}, {"folder", s.Folder}}, s.Remote)
	return append(o, jsField{"fileCount", len(s.Files)}), nil
}

func noteField(o jsObject, note *string) jsObject {
	if note != nil && *note != "" {
		o = append(o, jsField{"note", *note})
	}
	return o
}

func encodeSection(s Section) jsObject {
	switch s.Kind {
	case KindVault:
		entries := make([]any, 0, len(s.Entries))
		for _, e := range s.Entries {
			entries = append(entries, noteField(jsObject{{"key", e.Key}, {"value", e.Value}}, e.Note))
		}
		return jsObject{{"kind", KindVault}, {"entries", entries}}
	case KindWorkspace:
		return jsObject{{"kind", KindWorkspace}, {"name", s.Name}, {"tabs", tabsValue(s.Tabs)}}
	case KindNotes:
		notes := make([]any, 0, len(s.Notes))
		for _, n := range s.Notes {
			notes = append(notes, jsObject{{"sha", n.Sha}, {"body", n.Body}})
		}
		o := remoteFields(jsObject{{"kind", KindNotes}, {"folder", s.Folder}}, s.Remote)
		return append(o, jsField{"ref", s.Ref}, jsField{"notes", notes})
	}
	files := make([]any, 0, len(s.Files))
	for _, f := range s.Files {
		fo := jsObject{{"path", strings.ReplaceAll(f.Path, `\`, "/")}, {"content", base64.StdEncoding.EncodeToString(f.Content)}}
		if f.Executable {
			fo = append(fo, jsField{"executable", true})
		}
		files = append(files, fo)
	}
	o := remoteFields(jsObject{{"kind", KindRepo}, {"project", s.Project}, {"folder", s.Folder}}, s.Remote)
	return append(o, jsField{"files", files})
}

func tabsValue(tabs []any) []any {
	if tabs == nil {
		return []any{}
	}
	return tabs
}

// packParams are the random and clock inputs of a pack, injectable so vectors
// can pin them.
type packParams struct {
	salt, iv  []byte
	createdAt int64
}

func randomPackParams() (packParams, error) {
	p := packParams{salt: make([]byte, saltLen), iv: make([]byte, ivLen), createdAt: time.Now().UnixMilli()}
	if _, err := rand.Read(p.salt); err != nil {
		return p, err
	}
	if _, err := rand.Read(p.iv); err != nil {
		return p, err
	}
	return p, nil
}

// PackSections is packSections: sections into a `.gitcito` bundle string.
// project is the envelope label shown before unlock (a repo name, or e.g.
// "Workspace" for a multi-repo bundle).
func PackSections(project string, sections []Section, password string) (string, error) {
	p, err := randomPackParams()
	if err != nil {
		return "", err
	}
	return packWith(project, sections, password, p)
}

// PackBundle is packBundle, the back-compat convenience: a single repo's files.
func PackBundle(project string, files []BundleFile, password string) (string, error) {
	return PackSections(project, []Section{{Kind: KindRepo, Project: project, Folder: project, Files: files}}, password)
}

func packWith(project string, sections []Section, password string, p packParams) (string, error) {
	key, err := deriveKey(password, p.salt)
	if err != nil {
		return "", err
	}
	encoded := make([]any, 0, len(sections))
	for _, s := range sections {
		encoded = append(encoded, encodeSection(s))
	}
	plain := stringifyJS(jsObject{{"sections", encoded}}, "")
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCMWithNonceSize(block, len(p.iv))
	if err != nil {
		return "", err
	}
	sealed := gcm.Seal(nil, p.iv, plain, nil)
	enc, tag := sealed[:len(sealed)-gcm.Overhead()], sealed[len(sealed)-gcm.Overhead():]

	fileCount := 0
	summaries := make([]any, 0, len(sections))
	// Stamp the lowest version that can express this bundle, so a v2-only build
	// still opens bundles that carry nothing new.
	version := baseVersion
	for _, s := range sections {
		if s.Kind == KindRepo {
			fileCount += len(s.Files)
		}
		if s.Kind != KindRepo && s.Kind != KindVault {
			version = BundleVersion
		}
		sum, err := summarise(s)
		if err != nil {
			return "", err
		}
		summaries = append(summaries, sum)
	}
	bundle := jsObject{
		{"format", BundleFormat},
		{"version", version},
		{"project", project},
		{"createdAt", p.createdAt},
		{"fileCount", fileCount},
		{"sections", summaries},
		{"kdf", jsObject{
			{"algo", "scrypt"}, {"N", scryptN}, {"r", scryptR}, {"p", scryptP},
			{"salt", base64.StdEncoding.EncodeToString(p.salt)},
		}},
		{"cipher", jsObject{
			{"algo", "aes-256-gcm"},
			{"iv", base64.StdEncoding.EncodeToString(p.iv)},
			{"tag", base64.StdEncoding.EncodeToString(tag)},
		}},
		{"payload", base64.StdEncoding.EncodeToString(enc)},
	}
	return string(stringifyJS(bundle, "  ")), nil
}

// BundleHeader is SecureBundleHeader: the plaintext envelope, readable
// without the password. Sections are the envelope's summaries verbatim (or one
// synthesised repo summary for a v1 bundle).
type BundleHeader struct {
	Project   string  `json:"project"`
	CreatedAt float64 `json:"createdAt"`
	FileCount float64 `json:"fileCount"`
	Version   float64 `json:"version"`
	Sections  []any   `json:"sections"`
}

// ReadBundleHeader is readBundleHeader: parse the plaintext envelope without
// decrypting. Returns nil when the text is not a gitcito bundle at all; a
// BundleError (unsupported-version) for a bundle from a newer version.
func ReadBundleHeader(text string) (*BundleHeader, error) {
	parsed, err := parseJS([]byte(text))
	if err != nil {
		return nil, nil
	}
	raw, ok := parsed.(jsObject)
	if !ok || raw.get("format") != BundleFormat {
		return nil, nil
	}
	version, ok := jsNum(raw.get("version"))
	if !ok || version > BundleVersion {
		return nil, bundleErr(ErrUnsupportedVersion)
	}
	h := &BundleHeader{Project: "unknown", Version: version}
	if s, ok := raw.get("project").(string); ok {
		h.Project = s
	}
	if n, ok := jsNum(raw.get("createdAt")); ok {
		h.CreatedAt = n
	}
	if n, ok := jsNum(raw.get("fileCount")); ok {
		h.FileCount = n
	}
	if arr, ok := raw.get("sections").([]any); ok && version >= 2 {
		h.Sections = arr
	} else {
		// Synthesise a single repo tab so the import UI is uniform across versions.
		h.Sections = []any{jsObject{
			{"kind", KindRepo}, {"project", h.Project}, {"folder", h.Project}, {"fileCount", h.FileCount},
		}}
	}
	return h, nil
}

// typeError stands for a JS TypeError the TS let escape (anything that is not
// a BundleError): the channels report it as read-failed.
type typeError struct{ msg string }

func (e *typeError) Error() string { return e.msg }

// prop is `obj?.key` on a parsed value: only objects have properties here
// (strings and numbers have none the format reads).
func prop(v any, key string) any {
	if o, ok := v.(jsObject); ok {
		return o.get(key)
	}
	return nil
}

// bufferFrom is Buffer.from(v, 'base64') for a truthy property value: strings
// decode forgivingly, anything else made Node throw a TypeError.
func bufferFrom(v any) ([]byte, error) {
	s, ok := v.(string)
	if !ok {
		return nil, &typeError{"The first argument must be of type string"}
	}
	return nodeBase64(s), nil
}

// newGCM is createDecipheriv('aes-256-gcm', key, iv) + setAuthTag(tag): Node
// accepts any non-empty IV and tags of 4, 8 or 12–16 bytes.
func newGCM(key, iv []byte, tagLen int) (cipher.AEAD, error) {
	if len(iv) == 0 {
		return nil, &typeError{"Invalid initialization vector"}
	}
	if !(tagLen == 4 || tagLen == 8 || tagLen >= 12 && tagLen <= 16) {
		return nil, &typeError{"Invalid authentication tag length"}
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	switch {
	case tagLen == 16:
		return cipher.NewGCMWithNonceSize(block, len(iv))
	case len(iv) == 12 && tagLen >= 12:
		return cipher.NewGCMWithTagSize(block, tagLen)
	}
	// A short tag with a non-standard IV (or a 4/8-byte tag): Go's GCM cannot
	// verify it, so the bundle reads as undecryptable. Gitcito never wrote one.
	return nil, bundleErr(ErrBadPassword)
}

func decrypt(text, password string) (float64, any, error) {
	head, err := ReadBundleHeader(text)
	if err != nil {
		return 0, nil, err
	}
	if head == nil {
		return 0, nil, bundleErr(ErrInvalid)
	}
	parsed, _ := parseJS([]byte(text))
	raw := parsed.(jsObject)
	kdf, ciph := raw.get("kdf"), raw.get("cipher")
	saltV, ivV, tagV, payloadV := prop(kdf, "salt"), prop(ciph, "iv"), prop(ciph, "tag"), raw.get("payload")
	if !jsTruthy(saltV) || !jsTruthy(ivV) || !jsTruthy(tagV) || !jsTruthy(payloadV) {
		return 0, nil, bundleErr(ErrInvalid)
	}
	salt, err := bufferFrom(saltV)
	if err != nil {
		return 0, nil, err
	}
	key, err := deriveKey(password, salt)
	if err != nil {
		return 0, nil, err
	}
	iv, err := bufferFrom(ivV)
	if err != nil {
		return 0, nil, err
	}
	tag, err := bufferFrom(tagV)
	if err != nil {
		return 0, nil, err
	}
	gcm, err := newGCM(key, iv, len(tag))
	if err != nil {
		return 0, nil, err
	}
	payload, err := bufferFrom(payloadV)
	if err != nil {
		// Inside the TS try block: reads as a wrong password.
		return 0, nil, bundleErr(ErrBadPassword)
	}
	plain, err := gcm.Open(nil, iv, append(append([]byte{}, payload...), tag...), nil)
	if err != nil {
		return 0, nil, bundleErr(ErrBadPassword)
	}
	obj, err := parseJS([]byte(decodeUTF8(string(plain))))
	if err != nil {
		return 0, nil, bundleErr(ErrInvalid)
	}
	return head.Version, obj, nil
}

// objProp is `obj.key` where obj may be any parsed value: null throws in JS.
func objProp(obj any, key string) (any, error) {
	if obj == nil {
		return nil, &typeError{fmt.Sprintf("Cannot read properties of null (reading '%s')", key)}
	}
	return prop(obj, key), nil
}

func decodeFiles(files any) ([]BundleFile, error) {
	arr, ok := files.([]any)
	if !ok {
		return nil, bundleErr(ErrInvalid)
	}
	out := []BundleFile{}
	for _, f := range arr {
		path, ok1 := prop(f, "path").(string)
		content, ok2 := prop(f, "content").(string)
		if !ok1 || !ok2 {
			continue
		}
		out = append(out, BundleFile{Path: path, Content: nodeBase64(content), Executable: jsTruthy(prop(f, "executable"))})
	}
	return out, nil
}

func strProp(v any, key, def string) string {
	if s, ok := prop(v, key).(string); ok {
		return s
	}
	return def
}

func optStrProp(v any, key string) *string {
	if s, ok := prop(v, key).(string); ok {
		return &s
	}
	return nil
}

// UnpackSections is unpackSections: decrypt a bundle to its sections. A v1
// bundle (`{ files }`) upgrades to a single repo section keyed by the envelope
// project name. A BundleError carries the code; any other error is read-failed.
func UnpackSections(text, password string) ([]Section, error) {
	version, obj, err := decrypt(text, password)
	if err != nil {
		return nil, err
	}
	if version >= 2 {
		rawSections, err := objProp(obj, "sections")
		if err != nil {
			return nil, err
		}
		arr, ok := rawSections.([]any)
		if !ok {
			return nil, bundleErr(ErrInvalid)
		}
		return decodeSections(arr)
	}
	// v1: single repo of files.
	header, _ := ReadBundleHeader(text)
	project := "unknown"
	if header != nil {
		project = header.Project
	}
	rawFiles, err := objProp(obj, "files")
	if err != nil {
		return nil, err
	}
	files, err := decodeFiles(rawFiles)
	if err != nil {
		return nil, err
	}
	return []Section{{Kind: KindRepo, Project: project, Folder: project, Files: files}}, nil
}

// decodeSections reads the payload's section list.
func decodeSections(arr []any) ([]Section, error) {
	// Explicit dispatch by kind; a kind this build does not know is skipped
	// rather than misread (the version stamp keeps truly old builds out).
	out := []Section{}
	for _, s := range arr {
		if s == nil {
			return nil, &typeError{"Cannot read properties of null (reading 'kind')"}
		}
		switch prop(s, "kind") {
		case KindVault:
			entries, _ := prop(s, "entries").([]any)
			sec := Section{Kind: KindVault, Entries: []BundleVaultEntry{}}
			for _, e := range entries {
				k, ok1 := prop(e, "key").(string)
				v, ok2 := prop(e, "value").(string)
				if !ok1 || !ok2 {
					continue
				}
				sec.Entries = append(sec.Entries, BundleVaultEntry{Key: k, Value: v, Note: optStrProp(e, "note")})
			}
			out = append(out, sec)
		case KindRepo:
			project := strProp(s, "project", "unknown")
			files, err := decodeFiles(prop(s, "files"))
			if err != nil {
				return nil, err
			}
			out = append(out, Section{
				Kind: KindRepo, Project: project, Folder: strProp(s, "folder", project),
				Remote: optStrProp(s, "remote"), Files: files,
			})
		case KindWorkspace:
			tabs, _ := prop(s, "tabs").([]any)
			kept := []any{}
			for _, t := range tabs {
				if !jsTruthy(t) {
					continue
				}
				var ok bool
				if prop(t, "kind") == KindRepo {
					ok = jsTruthy(prop(t, "repo"))
				} else {
					_, isArr := prop(t, "repos").([]any)
					ok = prop(t, "kind") == "group" && isArr
				}
				if ok {
					kept = append(kept, t)
				}
			}
			out = append(out, Section{Kind: KindWorkspace, Name: strProp(s, "name", "Workspace"), Tabs: kept})
		case KindNotes:
			notes, _ := prop(s, "notes").([]any)
			sec := Section{
				Kind: KindNotes, Folder: strProp(s, "folder", "unknown"), Remote: optStrProp(s, "remote"),
				Ref: strProp(s, "ref", NotesRef), Notes: []BundleNote{},
			}
			for _, n := range notes {
				sha, ok1 := prop(n, "sha").(string)
				body, ok2 := prop(n, "body").(string)
				if ok1 && ok2 {
					sec.Notes = append(sec.Notes, BundleNote{Sha: sha, Body: body})
				}
			}
			out = append(out, sec)
		}
		// any other kind: skipped on purpose
	}
	return out, nil
}

// UnpackBundle is unpackBundle, the back-compat flat file list (repo sections only).
func UnpackBundle(text, password string) ([]BundleFile, error) {
	sections, err := UnpackSections(text, password)
	if err != nil {
		return nil, err
	}
	out := []BundleFile{}
	for _, s := range sections {
		if s.Kind == KindRepo {
			out = append(out, s.Files...)
		}
	}
	return out, nil
}

// parseRaw parses a value that arrived from the renderer as raw JSON into the
// ordered tree (nil when absent).
func parseRaw(raw json.RawMessage) (any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	return parseJS(raw)
}

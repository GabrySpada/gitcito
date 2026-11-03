// Package textconv is src/main/textconv.ts and src/main/textconvCli.ts: the
// converter behind Gitcito's bundled git textconv driver
// (resources/cli/gitcito-textconv), which turns a document into text git can
// diff.
//
// The formats it understands are the ones the app previews: Word and Excel
// (read here with archive/zip and encoding/xml, matching mammoth's raw-text
// extraction and SheetJS's sheet_to_csv), JSON and Apple's String Catalogs
// (key-sorted), and UTF-16 `.strings` files. PDF is deliberately absent — the
// app has no text extractor to reuse, so that suggestion still points at
// pdftotext.
package textconv

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf16"

	"github.com/GabrySpada/gitcito/internal/repoconfig"
)

// JSONToStable is the stable JSON rendering: keys sorted recursively (by
// UTF-16 code unit, as Array#sort does), two-space indent, trailing newline.
// Two orderings of one document produce byte-identical text, so a diff shows
// only what changed. Invalid JSON is an error, so git shows the raw file
// instead of a lie.
func JSONToStable(text string) (string, error) {
	v, err := parseJSON(text)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	stringifyIndent(&b, v, "")
	b.WriteByte('\n')
	return b.String(), nil
}

// StringsToText decodes a `.strings` localization file.
//
// Git calls these binary and shows no diff at all: Xcode wrote them as UTF-16
// for most of its life, and UTF-16 is full of NUL bytes. The encoding is
// per-file — recent Xcode writes UTF-8 — so the BOM is the only reliable
// teller. Assuming UTF-16 outright would turn every modern file into mojibake,
// which is worse than the binary marker git shows with no converter at all.
func StringsToText(buf []byte) (string, error) {
	var text string
	switch {
	case len(buf) >= 2 && buf[0] == 0xff && buf[1] == 0xfe:
		text = decodeUTF16(buf[2:], false)
	case len(buf) >= 2 && buf[0] == 0xfe && buf[1] == 0xff:
		if (len(buf)-2)%2 != 0 {
			return "", errors.New("gitcito-textconv: truncated UTF-16 .strings")
		}
		text = decodeUTF16(buf[2:], true)
	default:
		text = strings.TrimPrefix(repoconfig.DecodeUTF8(buf), "\ufeff")
	}
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return text, nil
}

// decodeUTF16 is Buffer#toString('utf16le') (after a byte swap for BE): a
// trailing odd byte is dropped and a lone surrogate becomes U+FFFD once the
// text is written out as UTF-8.
func decodeUTF16(b []byte, bigEndian bool) string {
	units := make([]uint16, len(b)/2)
	for i := range units {
		if bigEndian {
			units[i] = uint16(b[2*i])<<8 | uint16(b[2*i+1])
		} else {
			units[i] = uint16(b[2*i+1])<<8 | uint16(b[2*i])
		}
	}
	return string(utf16.Decode(units))
}

// extname is Node's path.extname: the last `.suffix` of the base name, ""
// for a dotfile or a name without one.
func extname(p string) string {
	seps := "/"
	if runtime.GOOS == "windows" {
		seps = `/\`
	}
	base := strings.TrimRight(p, seps)
	if i := strings.LastIndexAny(base, seps); i >= 0 {
		base = base[i+1:]
	}
	dot := strings.LastIndex(base, ".")
	if dot <= 0 || strings.Trim(base, ".") == "" {
		return ""
	}
	return base[dot:]
}

// ConvertFile converts one file to diffable text, dispatching on its
// extension.
func ConvertFile(path string) (string, error) {
	ext := strings.ToLower(extname(path))
	switch ext {
	case ".json", ".xcstrings":
		// A String Catalog is JSON with an Xcode extension, so it gets the same
		// key-sorted rendering rather than a format of its own.
		b, err := os.ReadFile(path)
		if err != nil {
			return "", nodeReadError(path, err)
		}
		return JSONToStable(repoconfig.DecodeUTF8(b))
	case ".strings":
		b, err := os.ReadFile(path)
		if err != nil {
			return "", nodeReadError(path, err)
		}
		return StringsToText(b)
	case ".docx":
		b, err := os.ReadFile(path)
		if err != nil {
			return "", nodeReadError(path, err)
		}
		return DocxToText(b)
	case ".xlsx", ".xls":
		b, err := os.ReadFile(path)
		if err != nil {
			return "", nodeReadError(path, err)
		}
		return XlsxToCsv(b)
	}
	what := ext
	if what == "" {
		what = path
	}
	return "", fmt.Errorf("gitcito-textconv: unsupported file type '%s' (supported: .docx, .xlsx, .xls, .json, .xcstrings, .strings)", what)
}

// textErr builds an error whose text is the one the TS threw (mammoth's,
// SheetJS's, V8's) — capitalised, which is why it does not go through
// errors.New with a literal (staticcheck ST1005).
func textErr(msg string) error { return errors.New(msg) }

// nodeReadError words a failed read the way Node's readFile did.
func nodeReadError(path string, err error) error {
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("ENOENT: no such file or directory, open '%s'", path)
	}
	if errors.Is(err, os.ErrPermission) {
		return fmt.Errorf("EACCES: permission denied, open '%s'", path)
	}
	return err
}

// RunCLI is textconvCli.ts, the headless `gitcito-textconv <file>` entry git
// runs as a textconv filter: one argument, a path to (a temp copy of) the
// file; the text goes to stdout. It returns the process exit code: 2 for a
// usage error, 1 when the conversion fails (the message on stderr), 0 on
// success. args are the arguments after the program name.
func RunCLI(args []string, stdout, stderr io.Writer) int {
	file := ""
	if len(args) > 0 {
		file = args[0]
	}
	// A leading dash is refused rather than read as a path: git never passes
	// one, and it keeps an option-looking argument from being taken for a file.
	if file == "" || strings.HasPrefix(file, "-") {
		_, _ = io.WriteString(stderr, "usage: gitcito-textconv <file>\n")
		return 2
	}
	out, err := ConvertFile(file)
	if err != nil {
		_, _ = io.WriteString(stderr, err.Error()+"\n")
		return 1
	}
	if _, err := io.WriteString(stdout, out); err != nil {
		return 1
	}
	return 0
}

// BundledPath is the textconv shim Gitcito ships
// (resources/cli/gitcito-textconv): next to the executable when packaged
// (`cli/` beside it, or `Resources/cli` in a macOS bundle), else in the source
// checkout this file was built from — which is where tests find it.
func BundledPath() string {
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		for _, p := range []string{
			filepath.Join(dir, "cli", "gitcito-textconv"),
			filepath.Join(dir, "..", "Resources", "cli", "gitcito-textconv"),
		} {
			if _, err := os.Stat(p); err == nil {
				return filepath.Clean(p)
			}
		}
	}
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "resources", "cli", "gitcito-textconv")
}

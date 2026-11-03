package textconv

// Excel → CSV, one block per sheet labeled `## <name>` so a diff says which
// sheet moved — xlsxToCsv over SheetJS (0.18): XLSX.read with its default
// options, then utils.sheet_to_csv. Cells print as SheetJS formats them: the
// cached value of a formula, numbers through the cell's number format
// (ssf.go), booleans as TRUE/FALSE, errors by their text. The sheet's range is
// its <dimension>, else the cells it holds; every row of it prints, blank ones
// included, and a field is quoted when it holds a comma, a quote or a newline.
//
// Only the Office Open XML workbook (a zip: .xlsx/.xlsm, whatever the
// extension says) is read. SheetJS also parsed the legacy binary .xls (BIFF)
// format, SpreadsheetML 2003 and CSV-as-.xls; those are refused here.

import (
	"archive/zip"
	"bytes"
	"errors"
	"math"
	"regexp"
	"strconv"
	"strings"
)

type xlsxCell struct {
	t string // n | s | b | e
	n float64
	s string
	b bool
	w string // the formatted text, when SheetJS precomputed it
}

type xlsxSheet struct {
	name  string
	cells map[[2]int]*xlsxCell // [row, col] → cell
	ref   *xlsxRange
}

type xlsxRange struct{ sr, sc, er, ec int }

const relBase = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/"

// XlsxToCsv renders every sheet of a workbook as a labeled CSV block, ending
// with a newline.
func XlsxToCsv(buf []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(buf), int64(len(buf)))
	if err != nil {
		if bytes.HasPrefix(buf, []byte{0xD0, 0xCF, 0x11, 0xE0}) {
			return "", errors.New("gitcito-textconv: legacy binary .xls workbooks are not supported")
		}
		return "", err
	}
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[strings.TrimPrefix(f.Name, "/")] = f
	}
	read := func(name string) (*xnode, error) {
		f, ok := files[strings.TrimPrefix(name, "/")]
		if !ok {
			return nil, nil
		}
		data, err := readZipFile(f)
		if err != nil {
			return nil, err
		}
		return parseXML(data)
	}

	workbookPath := "xl/workbook.xml"
	if rels, err := docxRelationships(files, "_rels/.rels"); err == nil {
		for _, t := range rels[relBase+"officeDocument"] {
			if p := strings.TrimPrefix(t, "/"); files[p] != nil {
				workbookPath = p
				break
			}
		}
	}
	wb, err := read(workbookPath)
	if err != nil {
		return "", err
	}
	if wb == nil {
		return "", textErr("Could not find workbook")
	}
	dir := ""
	if i := strings.LastIndex(workbookPath, "/"); i >= 0 {
		dir = workbookPath[:i]
	}
	relsPath := joinZipPath(dir, "_rels", workbookPath[len(dir):]+".rels")
	relsPath = strings.Replace(relsPath, "_rels//", "_rels/", 1)
	idTargets, typeTargets, err := relationshipMaps(files, relsPath)
	if err != nil {
		return "", err
	}
	resolve := func(target string) string {
		if strings.HasPrefix(target, "/") {
			return strings.TrimPrefix(target, "/")
		}
		return joinZipPath(dir, target)
	}

	var sst []string
	sstPath := joinZipPath(dir, "sharedStrings.xml")
	if t := typeTargets[relBase+"sharedStrings"]; len(t) > 0 {
		sstPath = resolve(t[0])
	}
	if n, err := read(sstPath); err != nil {
		return "", err
	} else if n != nil {
		for _, si := range n.children {
			if !si.isText && (localName(si.name) == "si" || localName(si.name) == "sstItem") {
				sst = append(sst, richText(si))
			}
		}
	}

	numFmts := map[int]string{}
	var cellXfs []int
	stylesPath := joinZipPath(dir, "styles.xml")
	if t := typeTargets[relBase+"styles"]; len(t) > 0 {
		stylesPath = resolve(t[0])
	}
	if n, err := read(stylesPath); err != nil {
		return "", err
	} else if n != nil {
		for _, c := range n.children {
			switch localName(c.name) {
			case "numFmts":
				for _, f := range c.children {
					if localName(f.name) == "numFmt" {
						if id, err := strconv.Atoi(f.attrs["numFmtId"]); err == nil {
							numFmts[id] = f.attrs["formatCode"]
						}
					}
				}
			case "cellXfs":
				for _, xf := range c.children {
					if localName(xf.name) == "xf" {
						id, err := strconv.Atoi(xf.attrs["numFmtId"])
						if err != nil {
							id = 0
						}
						cellXfs = append(cellXfs, id)
					}
				}
			}
		}
	}

	var blocks []string
	sheetsNode := findChild(wb, "sheets")
	if sheetsNode != nil {
		for _, sh := range sheetsNode.children {
			if sh.isText || localName(sh.name) != "sheet" {
				continue
			}
			name := sh.attrs["name"]
			target := idTargets[sh.attrs["r:id"]]
			path := joinZipPath(dir, "worksheets", "sheet"+sh.attrs["sheetId"]+".xml")
			if target != "" {
				path = resolve(target)
			}
			ws, err := read(path)
			if err != nil {
				return "", err
			}
			sheet := &xlsxSheet{name: name, cells: map[[2]int]*xlsxCell{}}
			if ws != nil {
				if err := parseSheet(ws, sheet, sst, numFmts, cellXfs); err != nil {
					return "", err
				}
			}
			blocks = append(blocks, "## "+name+"\n"+sheetToCSV(sheet))
		}
	}
	body := strings.Join(blocks, "\n")
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return body, nil
}

func localName(name string) string {
	if i := strings.LastIndexAny(name, ":}"); i >= 0 {
		return name[i+1:]
	}
	return name
}

func findChild(n *xnode, local string) *xnode {
	for _, c := range n.children {
		if !c.isText && localName(c.name) == local {
			return c
		}
	}
	return nil
}

func relationshipMaps(files map[string]*zip.File, name string) (map[string]string, map[string][]string, error) {
	byID, byType := map[string]string{}, map[string][]string{}
	f, ok := files[name]
	if !ok {
		return byID, byType, nil
	}
	data, err := readZipFile(f)
	if err != nil {
		return nil, nil, err
	}
	root, err := parseXML(data)
	if err != nil {
		return nil, nil, err
	}
	for _, c := range root.children {
		if !c.isText && localName(c.name) == "Relationship" {
			byID[c.attrs["Id"]] = c.attrs["Target"]
			byType[c.attrs["Type"]] = append(byType[c.attrs["Type"]], c.attrs["Target"])
		}
	}
	return byID, byType, nil
}

// richText is a shared/inline string's text: a plain <t>, or the <t> of every
// rich-text run, with phonetic runs (<rPh>) left out — SheetJS's parse_si.
func richText(si *xnode) string {
	var b strings.Builder
	for _, c := range si.children {
		if c.isText {
			continue
		}
		switch localName(c.name) {
		case "t":
			b.WriteString(unescapeOOXML(nodeText(c)))
		case "r":
			if t := findChild(c, "t"); t != nil {
				b.WriteString(unescapeOOXML(nodeText(t)))
			}
		}
	}
	return b.String()
}

func nodeText(n *xnode) string {
	var b strings.Builder
	for _, c := range n.children {
		if c.isText {
			b.WriteString(c.text)
		}
	}
	return b.String()
}

// unescapeOOXML decodes the `_xHHHH_` escapes OOXML uses for characters XML
// cannot carry (the entities are already decoded by the XML reader).
func unescapeOOXML(s string) string {
	if !strings.Contains(s, "_x") && !strings.Contains(s, "_X") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if i+6 < len(s) && s[i] == '_' && (s[i+1] == 'x' || s[i+1] == 'X') && s[i+6] == '_' {
			if n, err := strconv.ParseUint(s[i+2:i+6], 16, 16); err == nil {
				b.WriteRune(rune(n))
				i += 6
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// decodeCell is a cell reference (`B12`) as zero-based row and column.
func decodeCell(ref string) (row, col int, ok bool) {
	i := 0
	for i < len(ref) && ref[i] >= 'A' && ref[i] <= 'Z' {
		col = col*26 + int(ref[i]-'A'+1)
		i++
	}
	if i == 0 {
		return 0, 0, false
	}
	r, err := strconv.Atoi(strings.TrimPrefix(ref[i:], "$"))
	if err != nil || r < 1 {
		return 0, 0, false
	}
	return r - 1, col - 1, true
}

func decodeRange(ref string) (*xlsxRange, bool) {
	ref = strings.ReplaceAll(ref, "$", "")
	a, b, found := strings.Cut(ref, ":")
	if !found {
		b = a
	}
	sr, sc, ok1 := decodeCell(a)
	er, ec, ok2 := decodeCell(b)
	if !ok1 || !ok2 || sr > er || sc > ec {
		return nil, false
	}
	return &xlsxRange{sr, sc, er, ec}, true
}

// parseFloatJS is parseFloat: the longest numeric prefix, NaN without one.
func parseFloatJS(s string) float64 {
	m := floatPrefixRe.FindString(strings.TrimLeft(s, " \t\n\r\v\f"))
	if m == "" {
		return math.NaN()
	}
	if strings.HasSuffix(m, "Infinity") {
		if strings.HasPrefix(m, "-") {
			return math.Inf(-1)
		}
		return math.Inf(1)
	}
	f, _ := strconv.ParseFloat(m, 64) // ErrRange: ±Inf or 0, as JS rounds
	return f
}

var floatPrefixRe = regexp.MustCompile(`^[+-]?(?:Infinity|(?:\d+\.?\d*|\.\d+)(?:[eE][+-]?\d+)?)`)

func parseSheet(ws *xnode, sheet *xlsxSheet, sst []string, numFmts map[int]string, cellXfs []int) error {
	guess := xlsxRange{2000000, 2000000, 0, 0}
	if dim := findChild(ws, "dimension"); dim != nil {
		if r, ok := decodeRange(dim.attrs["ref"]); ok {
			sheet.ref = r
		}
	}
	data := findChild(ws, "sheetData")
	if data == nil {
		return nil
	}
	rowNum := 0
	for _, row := range data.children {
		if row.isText || localName(row.name) != "row" {
			continue
		}
		if r, err := strconv.Atoi(row.attrs["r"]); err == nil {
			rowNum = r
		} else {
			rowNum++
		}
		guess.sr = min(guess.sr, rowNum-1)
		guess.er = max(guess.er, rowNum-1)
		col := -1
		for _, c := range row.children {
			if c.isText || localName(c.name) != "c" {
				continue
			}
			if r, cc, ok := decodeCell(c.attrs["r"]); ok {
				_ = r
				col = cc
			} else {
				col++
			}
			cell, inRange := readCell(c, sst, numFmts, cellXfs)
			if !inRange {
				continue
			}
			guess.sc = min(guess.sc, col)
			guess.ec = max(guess.ec, col)
			if cell != nil {
				sheet.cells[[2]int{rowNum - 1, col}] = cell
			}
		}
	}
	if sheet.ref == nil && guess.ec >= guess.sc && guess.er >= guess.sr {
		sheet.ref = &guess
	}
	return nil
}

// readCell is one <c>: its value by type, then its formatted text. inRange
// says whether the cell still widens the guessed range: SheetJS counts a cell
// it later drops for an empty value, but not one with no type, value or
// formula.
func readCell(c *xnode, sst []string, numFmts map[int]string, cellXfs []int) (*xlsxCell, bool) {
	var v *string
	if n := findChild(c, "v"); n != nil {
		if t := nodeText(n); t != "" {
			v = &t
		}
	}
	hasFormula := false
	if f := findChild(c, "f"); f != nil {
		hasFormula = nodeText(f) != "" || f.attrs["si"] != ""
	}
	t, hasType := c.attrs["t"]
	if !hasType && v == nil {
		if !hasFormula {
			return nil, false
		}
		// SheetJS sets the value to the number 0 and then drops it, because
		// `0 == ""` holds in JavaScript: a formula without a cached value
		// prints as an empty field.
		return nil, true
	} else if !hasType {
		t = "n"
	}
	cell := &xlsxCell{}
	switch t {
	case "n":
		if v == nil || *v == "" {
			return nil, true
		}
		cell.t, cell.n = "n", parseFloatJS(*v)
	case "s":
		if v == nil {
			return nil, true
		}
		idx, err := strconv.Atoi(*v)
		cell.t = "s"
		if err == nil && idx >= 0 && idx < len(sst) {
			cell.s = sst[idx]
		}
	case "str":
		cell.t = "s"
		if v != nil {
			cell.s = unescapeOOXML(*v)
		}
	case "inlineStr":
		cell.t = "s"
		if is := findChild(c, "is"); is != nil {
			cell.s = richText(is)
		}
	case "b":
		cell.t = "b"
		cell.b = v != nil && (*v == "1" || strings.EqualFold(*v, "true"))
	case "e":
		cell.t = "e"
		if v != nil {
			cell.w = *v
		}
		return cell, true
	case "d":
		cell.t = "n"
		if v != nil {
			cell.n = isoToSerial(*v)
		}
	default:
		cell.t = "s"
		if v != nil {
			cell.s = *v
		}
	}
	fmtID := 0
	if s, err := strconv.Atoi(c.attrs["s"]); err == nil && s >= 0 && s < len(cellXfs) {
		fmtID = cellXfs[s]
	}
	cell.w = formatCell(cell, fmtID, numFmts)
	return cell, true
}

// formatCell is SheetJS's safe_format for a parsed cell.
func formatCell(c *xlsxCell, fmtID int, numFmts map[int]string) string {
	if fmtID == 0 {
		switch c.t {
		case "n":
			return generalNumber(c.n)
		case "b":
			if c.b {
				return "TRUE"
			}
			return "FALSE"
		}
		return c.s
	}
	format, ok := numFmts[fmtID]
	if !ok {
		format = builtinFormat(fmtID)
	}
	switch c.t {
	case "n":
		return ssfFormat(format, c.n)
	case "b":
		if c.b {
			return "TRUE"
		}
		return "FALSE"
	}
	return ssfFormatText(format, c.s)
}

// sheetToCSV is utils.sheet_to_csv with its defaults.
func sheetToCSV(s *xlsxSheet) string {
	if s.ref == nil {
		return ""
	}
	var rows []string
	for r := s.ref.sr; r <= s.ref.er; r++ {
		fields := make([]string, 0, s.ref.ec-s.ref.sc+1)
		for c := s.ref.sc; c <= s.ref.ec; c++ {
			cell := s.cells[[2]int{r, c}]
			if cell == nil {
				fields = append(fields, "")
				continue
			}
			txt := cell.w
			if strings.ContainsAny(txt, ",\n\"") {
				txt = `"` + strings.ReplaceAll(txt, `"`, `""`) + `"`
			}
			// SheetJS quotes a lone "ID" so the file is not taken for SYLK.
			if txt == "ID" {
				txt = `"ID"`
			}
			fields = append(fields, txt)
		}
		rows = append(rows, strings.Join(fields, ","))
	}
	return strings.Join(rows, "\n")
}

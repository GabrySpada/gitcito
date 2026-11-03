package textconv

// Word → text, as mammoth.extractRawText (mammoth 1.x) produced it: the main
// document part's body, every paragraph's text followed by a blank line, tabs
// as tabs, everything else (breaks, images, bookmarks, note references)
// contributing nothing. The reader follows mammoth's body-reader for the
// elements that carry text — runs, hyperlinks, insertions, smart tags,
// structured document tags, VML text boxes (whose paragraphs land after the
// paragraph that anchors them), tables with merged cells dropped, deleted
// paragraphs merged into the next — and ignores the same things it ignores:
// deleted text, unknown elements, DrawingML text boxes.

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"strconv"
	"strings"
)

// The namespace URIs mammoth maps to the prefixes its reader matches on.
var docxNamespaces = map[string]string{
	"http://schemas.openxmlformats.org/wordprocessingml/2006/main":           "w",
	"http://schemas.openxmlformats.org/officeDocument/2006/relationships":    "r",
	"http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing": "wp",
	"http://schemas.openxmlformats.org/drawingml/2006/main":                  "a",
	"http://schemas.openxmlformats.org/drawingml/2006/picture":               "pic",
	"http://purl.oclc.org/ooxml/wordprocessingml/main":                       "w",
	"http://purl.oclc.org/ooxml/officeDocument/relationships":                "r",
	"http://purl.oclc.org/ooxml/drawingml/wordprocessingDrawing":             "wp",
	"http://purl.oclc.org/ooxml/drawingml/main":                              "a",
	"http://purl.oclc.org/ooxml/drawingml/picture":                           "pic",
	"http://schemas.openxmlformats.org/package/2006/content-types":           "content-types",
	"http://schemas.openxmlformats.org/package/2006/relationships":           "relationships",
	"http://schemas.openxmlformats.org/markup-compatibility/2006":            "mc",
	"urn:schemas-microsoft-com:vml":                                          "v",
	"urn:schemas-microsoft-com:office:word":                                  "office-word",
	"http://schemas.microsoft.com/office/word/2010/wordml":                   "wordml",
}

// xnode is an XML element or text node, named the way mammoth names them
// (`w:p`, or `{uri}local` for an unmapped namespace).
type xnode struct {
	name     string
	attrs    map[string]string
	children []*xnode
	text     string
	isText   bool
}

func (n *xnode) first(name string) *xnode {
	for _, c := range n.children {
		if !c.isText && c.name == name {
			return c
		}
	}
	return nil
}

func (n *xnode) firstOrEmpty(name string) *xnode {
	if c := n.first(name); c != nil {
		return c
	}
	return &xnode{attrs: map[string]string{}}
}

// textContent is mammoth's Element#text(): the single text child, "" when
// there is none, and an error for anything else.
func (n *xnode) textContent() (string, error) {
	if len(n.children) == 0 {
		return "", nil
	}
	if len(n.children) != 1 || !n.children[0].isText {
		return "", textErr("Not implemented")
	}
	return n.children[0].text, nil
}

func xmlName(n xml.Name) string {
	if n.Space == "" {
		return n.Local
	}
	if p, ok := docxNamespaces[n.Space]; ok {
		return p + ":" + n.Local
	}
	return "{" + n.Space + "}" + n.Local
}

// parseXML reads a document into an xnode tree, with mc:AlternateContent
// collapsed to its fallback the way mammoth's office-xml-reader does.
func parseXML(data []byte) (*xnode, error) {
	data = bytes.TrimPrefix(data, []byte("\xEF\xBB\xBF"))
	dec := xml.NewDecoder(bytes.NewReader(data))
	var stack []*xnode
	var root *xnode
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n := &xnode{name: xmlName(t.Name), attrs: map[string]string{}}
			for _, a := range t.Attr {
				if a.Name.Space == "xmlns" || a.Name.Local == "xmlns" && a.Name.Space == "" {
					continue
				}
				n.attrs[xmlName(a.Name)] = a.Value
			}
			if len(stack) > 0 {
				p := stack[len(stack)-1]
				p.children = append(p.children, n)
			} else if root == nil {
				root = n
			}
			stack = append(stack, n)
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) > 0 {
				p := stack[len(stack)-1]
				p.children = append(p.children, &xnode{isText: true, text: string(t)})
			}
		}
	}
	if root == nil {
		return nil, errors.New("invalid XML: no root element")
	}
	return collapseAlternateContent(root)[0], nil
}

func collapseAlternateContent(n *xnode) []*xnode {
	if n.isText {
		return []*xnode{n}
	}
	if n.name == "mc:AlternateContent" {
		return n.firstOrEmpty("mc:Fallback").children
	}
	var kids []*xnode
	for _, c := range n.children {
		kids = append(kids, collapseAlternateContent(c)...)
	}
	n.children = kids
	return []*xnode{n}
}

// The document model, reduced to what raw text needs.
type delem struct {
	kind     string // text | tab | paragraph | table | tableRow | tableCell | checkbox | other
	text     string
	children []*delem
	colSpan  int
	vMerge   *bool
}

// readResult is mammoth's ReadResult: the elements, plus the "extra" ones (a
// text box's paragraphs) that surface after the enclosing paragraph.
type readResult struct {
	value, extra []*delem
}

type bodyReader struct {
	deletedParagraphContents []*xnode
}

func (r *bodyReader) readElements(nodes []*xnode) (readResult, error) {
	var out readResult
	for _, n := range nodes {
		res, err := r.readElement(n)
		if err != nil {
			return readResult{}, err
		}
		out.value = append(out.value, res.value...)
		out.extra = append(out.extra, res.extra...)
	}
	return out, nil
}

func (r *bodyReader) readChildren(n *xnode) (readResult, error) {
	return r.readElements(n.children)
}

func one(e *delem) readResult { return readResult{value: []*delem{e}} }

// wrap reads n's children into one element of kind, carrying extras up.
func (r *bodyReader) wrap(n *xnode, kind string) (readResult, error) {
	res, err := r.readChildren(n)
	if err != nil {
		return readResult{}, err
	}
	return readResult{value: []*delem{{kind: kind, children: res.value}}, extra: res.extra}, nil
}

func (r *bodyReader) readElement(n *xnode) (readResult, error) {
	if n.isText {
		return readResult{}, nil
	}
	switch n.name {
	case "w:p":
		if n.firstOrEmpty("w:pPr").firstOrEmpty("w:rPr").first("w:del") != nil {
			r.deletedParagraphContents = append(r.deletedParagraphContents, n.children...)
			return readResult{}, nil
		}
		children := n.children
		if len(r.deletedParagraphContents) > 0 {
			children = append(append([]*xnode{}, r.deletedParagraphContents...), children...)
			r.deletedParagraphContents = nil
		}
		res, err := r.readElements(children)
		if err != nil {
			return readResult{}, err
		}
		// insertExtra: a text box's paragraphs follow the paragraph holding it.
		return readResult{value: append([]*delem{{kind: "paragraph", children: res.value}}, res.extra...)}, nil
	case "w:r", "w:hyperlink":
		return r.wrap(n, "other")
	case "w:t":
		text, err := n.textContent()
		if err != nil {
			return readResult{}, err
		}
		return one(&delem{kind: "text", text: text}), nil
	case "w:tab":
		return one(&delem{kind: "tab"}), nil
	case "w:noBreakHyphen":
		return one(&delem{kind: "text", text: "\u2011"}), nil
	case "w:softHyphen":
		return one(&delem{kind: "text", text: "\u00ad"}), nil
	case "w:tbl":
		return r.readTable(n)
	case "w:tr":
		if n.firstOrEmpty("w:trPr").first("w:del") != nil {
			return readResult{}, nil
		}
		return r.wrap(n, "tableRow")
	case "w:tc":
		res, err := r.wrap(n, "tableCell")
		if err != nil {
			return readResult{}, err
		}
		props := n.firstOrEmpty("w:tcPr")
		cell := res.value[0]
		cell.colSpan = 1
		if span := props.firstOrEmpty("w:gridSpan").attrs["w:val"]; span != "" {
			cell.colSpan = parseIntJS(span)
		}
		if vm := props.first("w:vMerge"); vm != nil {
			val := vm.attrs["w:val"]
			merged := val == "continue" || val == ""
			cell.vMerge = &merged
		}
		return res, nil
	case "w:sdt":
		res, err := r.readChildren(n.firstOrEmpty("w:sdtContent"))
		if err != nil {
			return readResult{}, err
		}
		if n.firstOrEmpty("w:sdtPr").first("wordml:checkbox") != nil {
			// The checkbox replaces the first non-empty text of the content
			// (the ☐/☒ glyph); with no such text it replaces all of it.
			replaced := false
			var visit func(es []*delem)
			visit = func(es []*delem) {
				for _, e := range es {
					visit(e.children)
					if !replaced && e.kind == "text" && e.text != "" {
						e.kind, e.text, replaced = "checkbox", "", true
					}
				}
			}
			visit(res.value)
			if !replaced {
				return readResult{value: []*delem{{kind: "checkbox"}}, extra: res.extra}, nil
			}
		}
		return res, nil
	case "mc:AlternateContent":
		return r.readChildren(n.firstOrEmpty("mc:Fallback"))
	case "w:ins", "w:object", "w:smartTag", "w:drawing", "v:roundrect", "v:shape", "v:textbox",
		"w:txbxContent", "v:group", "v:rect":
		return r.readChildren(n)
	case "w:pict":
		// toExtra: the text box's content leaves the run it sits in.
		res, err := r.readChildren(n)
		if err != nil {
			return readResult{}, err
		}
		return readResult{extra: append(res.extra, res.value...)}, nil
	}
	// Breaks, bookmarks, fields, note references, images and every unknown or
	// ignored element: no text.
	return readResult{}, nil
}

// parseIntJS is parseInt(s, 10) where only the value's use as a span matters;
// NaN (no leading digits) is taken as 1.
func parseIntJS(s string) int {
	s = strings.TrimLeft(s, " \t\n\r")
	end := 0
	if end < len(s) && (s[end] == '-' || s[end] == '+') {
		end++
	}
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	n, err := strconv.Atoi(s[:end])
	if err != nil {
		return 1
	}
	return n
}

func (r *bodyReader) readTable(n *xnode) (readResult, error) {
	res, err := r.readChildren(n)
	if err != nil {
		return readResult{}, err
	}
	rows := res.value
	// calculateRowSpans: drop the continuation cells of vertical merges, but
	// only in a table made purely of rows of cells.
	regular := true
	for _, row := range rows {
		if row.kind != "tableRow" {
			regular = false
			break
		}
		for _, c := range row.children {
			if c.kind != "tableCell" {
				regular = false
			}
		}
	}
	if regular {
		columns := map[int]bool{}
		for _, row := range rows {
			idx := 0
			for _, c := range row.children {
				merged := c.vMerge != nil && *c.vMerge
				if merged && columns[idx] {
					c.kind = "dropped"
				} else {
					columns[idx] = true
				}
				idx += c.colSpan
			}
		}
		for _, row := range rows {
			kept := row.children[:0]
			for _, c := range row.children {
				if c.kind != "dropped" {
					kept = append(kept, c)
				}
			}
			row.children = kept
		}
	}
	return readResult{value: []*delem{{kind: "table", children: rows}}, extra: res.extra}, nil
}

func rawText(e *delem, b *strings.Builder) {
	switch e.kind {
	case "text":
		b.WriteString(e.text)
		return
	case "tab":
		b.WriteByte('\t')
		return
	}
	for _, c := range e.children {
		rawText(c, b)
	}
	if e.kind == "paragraph" {
		b.WriteString("\n\n")
	}
}

// docxRelationships reads a .rels part into id → target and type → targets.
func docxRelationships(files map[string]*zip.File, name string) (map[string][]string, error) {
	byType := map[string][]string{}
	f, ok := files[name]
	if !ok {
		return byType, nil
	}
	data, err := readZipFile(f)
	if err != nil {
		return nil, err
	}
	root, err := parseXML(data)
	if err != nil {
		return nil, err
	}
	for _, c := range root.children {
		if !c.isText && c.name == "relationships:Relationship" {
			byType[c.attrs["Type"]] = append(byType[c.attrs["Type"]], c.attrs["Target"])
		}
	}
	return byType, nil
}

func readZipFile(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

// joinZipPath is mammoth's zipfile.joinPath: empty parts dropped, an absolute
// part restarts the path.
func joinZipPath(parts ...string) string {
	var relevant []string
	for _, p := range parts {
		if p == "" {
			continue
		}
		if strings.HasPrefix(p, "/") {
			relevant = []string{p}
		} else {
			relevant = append(relevant, p)
		}
	}
	return strings.Join(relevant, "/")
}

// DocxToText extracts a Word document's raw text, ending with a newline.
func DocxToText(buf []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(buf), int64(len(buf)))
	if err != nil {
		return "", err
	}
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[f.Name] = f
	}
	rels, err := docxRelationships(files, "_rels/.rels")
	if err != nil {
		return "", err
	}
	main := "word/document.xml"
	for _, target := range rels["http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument"] {
		if p := strings.TrimPrefix(joinZipPath("", target), "/"); files[p] != nil {
			main = p
			break
		}
	}
	f, ok := files[main]
	if !ok {
		return "", textErr("Could not find main document part. Are you sure this is a valid .docx file?")
	}
	data, err := readZipFile(f)
	if err != nil {
		return "", err
	}
	root, err := parseXML(data)
	if err != nil {
		return "", err
	}
	body := root.first("w:body")
	if body == nil {
		return "", textErr("Could not find the body element: are you sure this is a docx file?")
	}
	reader := &bodyReader{}
	res, err := reader.readChildren(body)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, e := range res.value {
		rawText(e, &b)
	}
	text := b.String()
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return text, nil
}

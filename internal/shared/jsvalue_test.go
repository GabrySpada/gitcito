package shared

import (
	"encoding/json"
	"testing"
)

func TestJSTruthy(t *testing.T) {
	cases := []struct {
		in   any
		want bool
	}{
		{nil, false}, {false, false}, {true, true}, {"", false}, {"0", true},
		{json.Number("0"), false}, {json.Number("-0"), false}, {json.Number("0.0"), false},
		{json.Number("1e-3"), true}, {0.0, false}, {2.5, true}, {0, false}, {1, true},
		{Object{}, true}, {[]any{}, true},
	}
	for _, c := range cases {
		if got := JSTruthy(c.in); got != c.want {
			t.Errorf("JSTruthy(%#v) = %v", c.in, got)
		}
	}
}

func TestJSTrim(t *testing.T) {
	cases := map[string]string{
		"  a  ":            "a",
		"\ufeffa\u00a0":    "a",
		"\u2028a\u3000\t":  "a",
		"\u0085a\u0085":    "\u0085a\u0085", // NEL is not JS whitespace
		"":                 "",
		"\u200ba\u200b":    "\u200ba\u200b", // zero-width space is not either
		"\u2000\u200aa \n": "a",
	}
	for in, want := range cases {
		if got := JSTrim(in); got != want {
			t.Errorf("JSTrim(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDecodeJSONIsJSONParse(t *testing.T) {
	for _, bad := range []string{"", "{", `{"a":1} x`, `{"a":1}{}`, "\ufeff{}", "undefined"} {
		if _, err := DecodeJSON([]byte(bad)); err == nil {
			t.Errorf("DecodeJSON(%q) should fail", bad)
		}
	}
	v, err := DecodeJSON([]byte(" {\"n\": 12345678901234567890, \"f\": 1.5} \n"))
	if err != nil {
		t.Fatal(err)
	}
	m := v.(Object)
	if m["n"] != json.Number("12345678901234567890") || m["f"] != json.Number("1.5") {
		t.Errorf("numbers = %#v", m)
	}
}

func TestMarshalIndentIsStringify(t *testing.T) {
	got, err := MarshalIndent(Object{"a": "<b>&", "n": []any{}, "o": Object{}, "x": []any{json.Number("1"), nil}})
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"a\": \"<b>&\",\n  \"n\": [],\n  \"o\": {},\n  \"x\": [\n    1,\n    null\n  ]\n}"
	if string(got) != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

func TestJSKeyAndEquality(t *testing.T) {
	cases := map[string]any{"x": "x", "true": true, "3": json.Number("3"), "3.5": 3.5, "1,,b": []any{1, nil, "b"}, "null": nil}
	for want, in := range cases {
		if got := jsKey(in); got != want {
			t.Errorf("jsKey(%#v) = %q, want %q", in, got, want)
		}
	}
	if keyOf(Object{}, "id") != "undefined" {
		t.Error("missing property should key as undefined")
	}
	if !jsStrictEqual(json.Number("1.0"), 1.0) || jsStrictEqual("1", json.Number("1")) || jsStrictEqual(Object{}, Object{}) {
		t.Error("jsStrictEqual")
	}
}

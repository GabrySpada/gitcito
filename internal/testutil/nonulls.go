package testutil

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

var rawMessageType = reflect.TypeFor[json.RawMessage]()

// NoNulls fails the test if v would cross the bridge with a JSON `null` where
// TypeScript expects an array or an object map (brief §5.2: nil slices return
// `[]`, never `null`).
//
// It marshals v (a marshal error fails the test), then walks it by reflection
// and reports every nil slice or map that would serialize as `null`: fields
// tagged `omitempty` are exempt (absent stays `undefined`), as are nil
// pointers (`T | null` in TS) and json.RawMessage (TS `unknown`). Paths are
// reported JSON-style, e.g. `$.staged[2].hunks`.
func NoNulls(t testing.TB, v any) {
	t.Helper()
	if _, err := json.Marshal(v); err != nil {
		t.Fatalf("NoNulls: value does not marshal: %v", err)
	}
	var problems []string
	walkNulls(reflect.ValueOf(v), "$", &problems, map[uintptr]bool{})
	if len(problems) > 0 {
		t.Errorf("NoNulls: %d value(s) would serialize as null:\n  %s", len(problems), strings.Join(problems, "\n  "))
	}
}

// NullPaths is NoNulls' finding as data, for tests of code that builds payloads.
func NullPaths(v any) []string {
	var problems []string
	walkNulls(reflect.ValueOf(v), "$", &problems, map[uintptr]bool{})
	return problems
}

func walkNulls(v reflect.Value, path string, problems *[]string, seen map[uintptr]bool) {
	if !v.IsValid() {
		return
	}
	switch v.Kind() {
	case reflect.Interface:
		if !v.IsNil() {
			walkNulls(v.Elem(), path, problems, seen)
		}
	case reflect.Pointer:
		if v.IsNil() {
			return
		}
		if seen[v.Pointer()] {
			return
		}
		seen[v.Pointer()] = true
		walkNulls(v.Elem(), path, problems, seen)
	case reflect.Slice:
		if v.Type() == rawMessageType {
			return
		}
		if v.IsNil() {
			*problems = append(*problems, path+" is a nil "+v.Type().String())
			return
		}
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return // []byte: a base64 string
		}
		for i := 0; i < v.Len(); i++ {
			walkNulls(v.Index(i), fmt.Sprintf("%s[%d]", path, i), problems, seen)
		}
	case reflect.Array:
		for i := 0; i < v.Len(); i++ {
			walkNulls(v.Index(i), fmt.Sprintf("%s[%d]", path, i), problems, seen)
		}
	case reflect.Map:
		if v.IsNil() {
			*problems = append(*problems, path+" is a nil "+v.Type().String())
			return
		}
		iter := v.MapRange()
		for iter.Next() {
			walkNulls(iter.Value(), fmt.Sprintf("%s[%v]", path, iter.Key()), problems, seen)
		}
	case reflect.Struct:
		walkStruct(v, path, problems, seen)
	}
}

func walkStruct(v reflect.Value, path string, problems *[]string, seen map[uintptr]bool) {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		fv := v.Field(i)
		if f.Anonymous && name == "" {
			// Embedded struct: its fields are promoted into this object.
			ft := f.Type
			if ft.Kind() == reflect.Pointer {
				if fv.IsNil() {
					continue
				}
				fv = fv.Elem()
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				walkStruct(fv, path, problems, seen)
				continue
			}
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		omitempty := strings.Contains(","+opts+",", ",omitempty,") || strings.Contains(","+opts+",", ",omitzero,")
		if omitempty && isEmptyForJSON(fv) {
			continue
		}
		walkNulls(fv, path+"."+name, problems, seen)
	}
}

func isEmptyForJSON(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Slice, reflect.Map, reflect.String, reflect.Array:
		return v.Len() == 0
	case reflect.Pointer, reflect.Interface:
		return v.IsNil()
	}
	return v.IsZero()
}

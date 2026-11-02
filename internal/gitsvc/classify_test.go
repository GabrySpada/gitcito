package gitsvc

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

// Every exported method on *GitService is bound to the page, and the lock has
// to know whether it reads or writes. An unclassified method fails here.
func TestEveryGitServiceMethodIsClassified(t *testing.T) {
	if bad := unclassifiedMethods(reflect.TypeFor[*GitService]()); len(bad) > 0 {
		t.Fatalf("unclassified GitService methods (add each to readMethods, appLevelMethods or writeMethods in classify.go):\n  %s",
			strings.Join(bad, "\n  "))
	}
}

// fakeService proves the check actually bites.
type fakeService struct{}

func (fakeService) Log(context.Context, string) error                 { return nil } // read
func (fakeService) Commit(context.Context, string) error              { return nil } // write
func (fakeService) Clone(context.Context, string) error               { return nil } // app-level
func (fakeService) ServiceStartup(context.Context) error              { return nil } // wails-internal
func (fakeService) DefinitelyNotAGitMethod(context.Context) error     { return nil }
func (fakeService) FileDataURL(context.Context, string, string) error { return nil } // wrong casing
func (fakeService) FileDataUrl(context.Context, string, string) error { return nil }

func TestUnclassifiedMethodsCatchesStrays(t *testing.T) {
	got := unclassifiedMethods(reflect.TypeFor[fakeService]())
	want := []string{"DefinitelyNotAGitMethod (definitelyNotAGitMethod)", "FileDataURL (fileDataURL)"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unclassified = %q, want %q", got, want)
	}
}

func TestClassificationTables(t *testing.T) {
	if n := len(readMethods); n != 112 {
		t.Errorf("readMethods has %d entries; git.ts READ_METHODS has 112", n)
	}
	if n := len(appLevelMethods); n != 3 {
		t.Errorf("appLevelMethods has %d entries", n)
	}
	if n := len(writeMethods); n < 150 {
		t.Errorf("writeMethods lost entries: %d < 150 seeded from git.ts", n)
	}
	if o := overlappingMethods(); len(o) > 0 {
		t.Errorf("methods classified both write and read/app-level: %q", o)
	}
	tests := []struct {
		method         string
		read, appLevel bool
		classified     bool
	}{
		{"log", true, false, true},
		{"status", true, false, true},
		{"bundleCreate", true, false, true}, // reads the object DB, writes outside the repo
		{"version", true, false, true},
		{"remoteBranches", true, true, true},
		{"clone", false, true, true},
		{"init", false, true, true},
		{"commit", false, false, true},
		{"stashBaseShas", false, false, true}, // absent from READ_METHODS in TS, so a write
		{"brandNewMethod", false, false, false},
	}
	for _, tt := range tests {
		if IsReadMethod(tt.method) != tt.read || IsAppLevelMethod(tt.method) != tt.appLevel || isClassified(tt.method) != tt.classified {
			t.Errorf("%s: read=%v app=%v classified=%v", tt.method, IsReadMethod(tt.method), IsAppLevelMethod(tt.method), isClassified(tt.method))
		}
	}
	for _, name := range []string{"", "Log", "FileDataUrl", "X"} {
		got := tsMethodName(name)
		if name != "" && strings.ToUpper(got[:1])+got[1:] != name {
			t.Errorf("tsMethodName(%q) = %q", name, got)
		}
	}
	if tsMethodName("FileDataUrl") != "fileDataUrl" || tsMethodName("") != "" {
		t.Error("tsMethodName")
	}
}

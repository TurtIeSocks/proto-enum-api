package proto

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadIndex_singleFile(t *testing.T) {
	l := &Loader{
		Sources: []Source{{Path: "testdata/sample.proto"}},
	}
	idx, err := l.LoadIndex(context.Background())
	if err != nil {
		t.Fatalf("LoadIndex: %v", err)
	}

	wantPkgs := []string{"test.sample"}
	got := idx.Packages()
	if len(got) != 1 || got[0] != wantPkgs[0] {
		t.Errorf("Packages() = %v, want %v", got, wantPkgs)
	}

	wantNames := []string{
		"test.sample.ClientOperatingSystem",
		"test.sample.Outer.Inner.Status",
		"test.sample.ProxyResponseProto.Status",
	}
	gotNames := idx.List("")
	if len(gotNames) != len(wantNames) {
		t.Fatalf("List returned %v, want %v", gotNames, wantNames)
	}
	for i, n := range wantNames {
		if gotNames[i] != n {
			t.Errorf("[%d] = %q, want %q", i, gotNames[i], n)
		}
	}
}

func TestLoadIndex_multiFile(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.proto")
	b := filepath.Join(dir, "b.proto")
	if err := os.WriteFile(a, []byte(`syntax="proto3"; package alpha;
enum Color { COLOR_RED = 0; COLOR_BLUE = 1; }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte(`syntax="proto3"; package beta;
enum Color { COLOR_GREEN = 0; }
`), 0o644); err != nil {
		t.Fatal(err)
	}

	l := &Loader{Sources: []Source{{Path: a}, {Path: b}}}
	idx, err := l.LoadIndex(context.Background())
	if err != nil {
		t.Fatalf("LoadIndex: %v", err)
	}

	if got := idx.Packages(); len(got) != 2 || got[0] != "alpha" || got[1] != "beta" {
		t.Errorf("Packages() = %v, want [alpha beta]", got)
	}

	// Same simple name "Color" lives in both packages without colliding.
	if _, ok := idx.Get("alpha.Color"); !ok {
		t.Error("alpha.Color missing")
	}
	if _, ok := idx.Get("beta.Color"); !ok {
		t.Error("beta.Color missing")
	}
}

func TestLoadIndex_glob(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"a.proto", "b.proto"} {
		body := []byte(`syntax="proto3"; package g; enum E { E_X = 0; }`)
		if err := os.WriteFile(filepath.Join(dir, n), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	l := &Loader{Sources: []Source{{Glob: filepath.Join(dir, "*.proto")}}}
	idx, err := l.LoadIndex(context.Background())
	if err != nil {
		t.Fatalf("LoadIndex: %v", err)
	}
	// Both files declare the same enum, last-write-wins, so we still get one.
	if idx.Len() != 1 {
		t.Errorf("Len = %d, want 1", idx.Len())
	}
}

func TestEnumIndex_Get(t *testing.T) {
	l := &Loader{Sources: []Source{{Path: "testdata/sample.proto"}}}
	idx, err := l.LoadIndex(context.Background())
	if err != nil {
		t.Fatalf("LoadIndex: %v", err)
	}

	e, ok := idx.Get("test.sample.ProxyResponseProto.Status")
	if !ok {
		t.Fatal("expected test.sample.ProxyResponseProto.Status")
	}
	if e.SimpleName != "Status" {
		t.Errorf("SimpleName = %q, want Status", e.SimpleName)
	}
	if e.Package != "test.sample" {
		t.Errorf("Package = %q, want test.sample", e.Package)
	}
	if len(e.Values) != 3 {
		t.Fatalf("got %d values, want 3", len(e.Values))
	}
	if e.Values[1].Name != "STATUS_OK" || e.Values[1].Number != 1 {
		t.Errorf("Values[1] = %+v, want STATUS_OK=1", e.Values[1])
	}
}

func TestEnumIndex_Resolve(t *testing.T) {
	l := &Loader{Sources: []Source{{Path: "testdata/sample.proto"}}}
	idx, _ := l.LoadIndex(context.Background())

	const enum = "test.sample.ClientOperatingSystem"
	if name, ok := idx.ResolveNumber(enum, 1); !ok || name != "CLIENT_OPERATING_SYSTEM_OS_ANDROID" {
		t.Errorf("ResolveNumber(1) = %q, %v", name, ok)
	}
	if n, ok := idx.ResolveName(enum, "CLIENT_OPERATING_SYSTEM_OS_IOS"); !ok || n != 2 {
		t.Errorf("ResolveName = %d, %v", n, ok)
	}
	if _, ok := idx.ResolveNumber("Nope", 0); ok {
		t.Error("ResolveNumber on unknown enum should fail")
	}
	if _, ok := idx.ResolveName(enum, "MISSING"); ok {
		t.Error("ResolveName on unknown value should fail")
	}
}

func TestEnumIndex_ListFilter(t *testing.T) {
	l := &Loader{Sources: []Source{{Path: "testdata/sample.proto"}}}
	idx, _ := l.LoadIndex(context.Background())

	got := idx.List("status")
	want := []string{"test.sample.Outer.Inner.Status", "test.sample.ProxyResponseProto.Status"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

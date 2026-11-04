package pkgdoc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every package must be documented and listed in its truck.
func TestRegistryIsDocumented(t *testing.T) {
	tree, err := Load("../../registry")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range tree.Check() {
		t.Error(p)
	}
}

// docs/registry must match the package files; regenerate it with
// `go run ./cmd/pkgdoc` after changing a package.
func TestGeneratedDocsAreUpToDate(t *testing.T) {
	tree, err := Load("../../registry")
	if err != nil {
		t.Fatal(err)
	}
	out := "../../docs/registry"
	want := tree.Generate()
	for p, content := range want {
		got, err := os.ReadFile(filepath.Join(out, p))
		if err != nil || string(got) != content {
			t.Errorf("docs/registry/%s is out of date; run: go run ./cmd/pkgdoc", p)
		}
	}
	filepath.WalkDir(out, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(out, p)
			if _, ok := want[filepath.ToSlash(rel)]; !ok {
				t.Errorf("docs/registry/%s has no package; run: go run ./cmd/pkgdoc", rel)
			}
		}
		return nil
	})
}

func TestLinksResolve(t *testing.T) {
	tree, err := Load("../../registry")
	if err != nil {
		t.Fatal(err)
	}
	files := tree.Generate()
	for p, content := range files {
		for _, part := range strings.Split(content, "](")[1:] {
			target := part[:strings.IndexByte(part, ')')]
			if strings.HasPrefix(target, "http") {
				continue
			}
			resolved := filepath.ToSlash(filepath.Join(filepath.Dir(p), target))
			if _, ok := files[resolved]; ok {
				continue
			}
			// links outside docs/registry, e.g. to registry sources or ../packages.md
			if _, err := os.Stat(filepath.Join("../../docs/registry", filepath.Dir(p), target)); err != nil {
				t.Errorf("%s: broken link %s", p, target)
			}
		}
	}
}

// Relative links in the hand-written docs and the README must resolve.
func TestHandWrittenDocLinks(t *testing.T) {
	files, _ := filepath.Glob("../../docs/*.md")
	files = append(files, "../../README.md")
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, part := range strings.Split(string(b), "](")[1:] {
			end := strings.IndexByte(part, ')')
			if end < 0 {
				continue
			}
			target, _, _ := strings.Cut(part[:end], "#")
			if target == "" || strings.Contains(target, "://") {
				continue
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(f), target)); err != nil {
				t.Errorf("%s: broken link %s", filepath.Base(f), part[:end])
			}
		}
	}
}

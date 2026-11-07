package registry

import (
	"fmt"
	"os"
	"strings"
)

// TreeUse is the use line that loads a package tree: "use pkg_tree" reads
// pkg_tree.pkg next to the program. See docs/publishing.md#package-trees.
const TreeUse = "pkg_tree"

// A Tree says where a program's packages come from: routes from package
// name prefixes to registries, and optionally a default ("*") registry.
type Tree struct {
	Path    string
	Default string // "" if the tree has no "*" route
	Routes  []Route
}

// TreePath is where the tree of source file src lives: pkg_tree.pkg in the
// same folder.
func TreePath(src string) string {
	return src[:strings.LastIndex(src, "/")+1] + TreeUse + ".pkg"
}

// ReadTree reads the package tree of source file src.
//
//	tree
//	  acme  http://localhost:8080
//	  *     https://raw.githubusercontent.com/DominicThemiya/Packenger/main/registry
func ReadTree(src string) (*Tree, error) {
	path := TreePath(src)
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("%s uses %s, but there is no %s", src, TreeUse, path)
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: can't read the file", path)
	}
	t := &Tree{Path: path}
	inTree := false
	seen := map[string]bool{}
	for i, line := range strings.Split(string(b), "\n") {
		at := fmt.Sprintf("%s:%d: ", path, i+1)
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") {
			continue
		}
		if line[0] != ' ' && line[0] != '\t' {
			if trimmed != "tree" || inTree {
				return nil, fmt.Errorf("%sexpected one 'tree' section", at)
			}
			inTree = true
			continue
		}
		if !inTree {
			return nil, fmt.Errorf("%sexpected one 'tree' section", at)
		}
		f := strings.Fields(trimmed)
		if len(f) != 2 {
			return nil, fmt.Errorf("%sexpected '<prefix> <registry url>'", at)
		}
		prefix, base := f[0], strings.TrimRight(f[1], "/")
		if prefix != "*" && !ValidName(prefix) {
			return nil, fmt.Errorf("%s%q is not a package name or *", at, prefix)
		}
		if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") && !strings.HasPrefix(base, "file://") {
			return nil, fmt.Errorf("%sthe registry must be an http://, https:// or file:// URL", at)
		}
		if seen[prefix] {
			return nil, fmt.Errorf("%s%s has two routes", at, prefix)
		}
		seen[prefix] = true
		if prefix == "*" {
			t.Default = base
		} else {
			t.Routes = append(t.Routes, Route{prefix, base})
		}
	}
	if !inTree {
		return nil, fmt.Errorf("%s: no 'tree' section", path)
	}
	return t, nil
}

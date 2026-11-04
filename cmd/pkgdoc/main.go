// Command pkgdoc regenerates docs/registry from the package files in
// registry/. Run it from the repository root after changing a package:
//
//	go run ./cmd/pkgdoc
package main

import (
	"fmt"
	"os"

	"github.com/DominicThemiya/Packenger/internal/pkgdoc"
)

func main() {
	t, err := pkgdoc.Load("registry")
	if err != nil {
		fmt.Fprintln(os.Stderr, "pkgdoc:", err)
		os.Exit(1)
	}
	if problems := t.Check(); len(problems) > 0 {
		for _, p := range problems {
			fmt.Fprintln(os.Stderr, "pkgdoc:", p)
		}
		os.Exit(1)
	}
	if err := t.Write("docs/registry"); err != nil {
		fmt.Fprintln(os.Stderr, "pkgdoc:", err)
		os.Exit(1)
	}
	fmt.Printf("wrote docs/registry (%d packages)\n", len(t.Pkgs))
}

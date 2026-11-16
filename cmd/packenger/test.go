package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/DominicThemiya/Packenger/internal/ui"
)

// testFiles lists the test programs in paths: a file is one test, and a
// directory holds every .pkg file under it apart from pkg_tree.pkg, in name
// order. Hidden files and directories are skipped.
func testFiles(paths []string) ([]string, error) {
	var out []string
	var walk func(dir string) error
	walk = func(dir string) error {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return fmt.Errorf("open %s: can't read the directory", dir)
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		sort.Strings(names)
		for _, n := range names {
			p := dir + "/" + n
			switch {
			case strings.HasPrefix(n, "."):
			case isDir(p):
				if err := walk(p); err != nil {
					return err
				}
			case strings.HasSuffix(n, ".pkg") && n != "pkg_tree.pkg":
				out = append(out, p)
			}
		}
		return nil
	}
	for _, p := range paths {
		if t := strings.TrimRight(p, "/"); t != "" {
			p = t
		}
		if _, err := os.Stat(p); err != nil {
			return nil, fmt.Errorf("open %s: no such file or directory", p)
		}
		if !isDir(p) {
			out = append(out, p)
			continue
		}
		before := len(out)
		if err := walk(p); err != nil {
			return nil, err
		}
		if len(out) == before {
			return nil, fmt.Errorf("no test programs (.pkg files) in %s", p)
		}
	}
	return out, nil
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// runTests runs each test program with this compiler ("packenger run") and
// reports which pass: a test passes when it exits with status 0, as
// rt/test's test_done() does when every check passed. The output of a test
// that fails is shown.
func runTests(paths []string, target, registry string, explicitReg bool) (int, error) {
	if len(paths) == 0 {
		paths = []string{"tests"}
	}
	files, err := testFiles(paths)
	if err != nil {
		return 1, err
	}
	self, err := os.Executable()
	if err != nil {
		return 1, err
	}
	color := ui.Colors(os.Stdout)
	paint := func(s, codes string) string {
		if !color {
			return s
		}
		return codes + s + "\x1b[0m"
	}
	width := 0
	for _, f := range files {
		width = max(width, utf8.RuneCountInString(f))
	}
	failed := 0
	for _, f := range files {
		args := []string{"run", "--target", target}
		if explicitReg {
			args = append(args, "--registry", registry)
		}
		args = append(args, f)
		t0 := time.Now()
		out, err := exec.Command(self, args...).CombinedOutput()
		took := time.Since(t0)
		status := 0
		if err != nil {
			var ee *exec.ExitError
			if !errors.As(err, &ee) {
				return 1, err
			}
			status = ee.ExitCode()
		}
		name := f + strings.Repeat(" ", width-utf8.RuneCountInString(f))
		if status == 0 {
			fmt.Printf("%s  %s  %s\n", paint("ok  ", "\x1b[32m"), name, paint(ui.Dur(took), "\x1b[2m"))
			continue
		}
		failed++
		fmt.Printf("%s  %s  %s\n", paint("FAIL", "\x1b[1m\x1b[31m"), name, fmt.Sprintf("exit %d", status))
		text := strings.TrimRight(string(bytes.ReplaceAll(out, []byte("\r"), nil)), "\n")
		if text != "" {
			for _, l := range strings.Split(text, "\n") {
				if l != "" {
					l = "      " + l
				}
				fmt.Println(l)
			}
		}
	}
	fmt.Printf("\n%d passed, %d failed\n", len(files)-failed, failed)
	if failed > 0 {
		return 1, nil
	}
	return 0, nil
}

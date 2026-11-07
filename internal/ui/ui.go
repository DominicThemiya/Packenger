// Package ui renders packenger's verbose build report and errors.
package ui

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/DominicThemiya/Packenger/internal/lang"
)

// ---------------------------------------------------------------- styling

type style string

const (
	reset   style = "\x1b[0m"
	bold    style = "\x1b[1m"
	dim     style = "\x1b[2m"
	red     style = "\x1b[31m"
	green   style = "\x1b[32m"
	yellow  style = "\x1b[33m"
	blue    style = "\x1b[34m"
	magenta style = "\x1b[35m"
	cyan    style = "\x1b[36m"
)

// Colors reports whether w should get ANSI colors: a terminal, and NO_COLOR
// unset (https://no-color.org).
func Colors(w *os.File) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	st, err := w.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

type painter struct{ on bool }

func (p painter) c(s string, styles ...style) string {
	if !p.on || len(styles) == 0 || s == "" {
		return s
	}
	var b strings.Builder
	for _, st := range styles {
		b.WriteString(string(st))
	}
	b.WriteString(s)
	b.WriteString(string(reset))
	return b.String()
}

func pad(s string, n int) string {
	if w := utf8.RuneCountInString(s); w < n {
		return s + strings.Repeat(" ", n-w)
	}
	return s
}

func lpad(s string, n int) string {
	if w := utf8.RuneCountInString(s); w < n {
		return strings.Repeat(" ", n-w) + s
	}
	return s
}

func Bytes(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
}

func Dur(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return fmt.Sprintf("%d µs", d.Microseconds())
	case d < 10*time.Second:
		return fmt.Sprintf("%d ms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1f s", d.Seconds())
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// bar draws d relative to slowest as a fixed-width bar with eighth-block
// precision.
func bar(d, slowest time.Duration, width int) string {
	if slowest <= 0 {
		return strings.Repeat(" ", width)
	}
	eighths := int(float64(d) / float64(slowest) * float64(width*8))
	eighths = min(max(eighths, 1), width*8)
	parts := []string{"", "▏", "▎", "▍", "▌", "▋", "▊", "▉"}
	return pad(strings.Repeat("█", eighths/8)+parts[eighths%8], width)
}

// ---------------------------------------------------------------- reporter

// Reporter prints the build as it happens. It implements lang.Reporter.
type Reporter struct {
	w     io.Writer
	p     painter
	mu    sync.Mutex
	start time.Time
	step  int
	open  string // current phase
	tools []string
}

func NewReporter(w io.Writer, color bool) *Reporter {
	return &Reporter{w: w, p: painter{color}, start: time.Now()}
}

func (r *Reporter) line(format string, a ...any) { fmt.Fprintf(r.w, format+"\n", a...) }

var phaseInfo = map[string][2]string{
	"syntax":   {"syntax", "front-end packages that define the language"},
	"grammar":  {"grammar", "assembled from those packages"},
	"parse":    {"parse", "your program, read with that grammar"},
	"emit":     {"emit", "packages rendered to NASM"},
	"assemble": {"assemble", "the target's toolchain builds the executable"},
}

func (r *Reporter) phase(name string) {
	if r.open == name {
		return
	}
	r.open = name
	r.step++
	info, ok := phaseInfo[name]
	if !ok {
		info = [2]string{name, "backend packages that translate to NASM"}
	}
	r.line("")
	r.line("  %s %s  %s", r.p.c(fmt.Sprintf("%d", r.step), bold, blue), r.p.c(pad(info[0], 10), bold), r.p.c(info[1], dim))
}

// Header starts the report.
func (r *Reporter) Header(cmd, src, out, registry, target string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	reg := strings.TrimPrefix(strings.TrimPrefix(registry, "https://"), "http://")
	title := r.p.c("packenger", bold, magenta) + " " + r.p.c(cmd, bold) + " " + src
	if out != "" {
		title += r.p.c(" → ", dim) + out
	}
	r.line("")
	r.line("  %s", title)
	r.line("  %s %s", r.p.c(pad("registry", 9), dim), reg)
	r.line("  %s %s", r.p.c(pad("target", 9), dim), target)
}

// Route notes that the packages under prefix come from their own registry.
func (r *Reporter) Route(prefix, registry string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	reg := strings.TrimPrefix(strings.TrimPrefix(registry, "https://"), "http://")
	r.line("  %s %s %s %s", r.p.c(pad("route", 9), dim), prefix, r.p.c("→", dim), reg)
}

// Lock notes that the build is checked against a lock file.
func (r *Reporter) Lock(path string, packages int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.line("  %s %s %s", r.p.c(pad("lock", 9), dim), path, r.p.c("· "+plural(packages, "package", "packages")+" pinned", dim))
}

// Locked reports a lock file that was written.
func Locked(w io.Writer, color bool, path string, packages int, registry string, routes [][2]string) {
	p := painter{color}
	fmt.Fprintf(w, "  %s %s %s\n", p.c("✓ locked", bold, green), plural(packages, "package", "packages"), p.c("in", dim)+" "+path)
	fmt.Fprintf(w, "    %s %s\n", p.c("registry", dim), registry)
	for _, r := range routes {
		fmt.Fprintf(w, "    %s %s %s %s\n", p.c("route", dim), r[0], p.c("→", dim), r[1])
	}
}

func (r *Reporter) Uses(file string, uses []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.line("  %s %s", r.p.c(pad("uses", 9), dim), r.p.c(strings.Join(uses, ", "), cyan))
}

func (r *Reporter) Round(phase string, round int, fetches []lang.Fetch) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.phase(phase)
	var slowest time.Duration
	for _, f := range fetches {
		slowest = max(slowest, f.Took)
	}
	r.line("     %s", r.p.c(roundTitle(round, len(fetches)), dim))
	for _, f := range fetches {
		r.line("     %s %s %s %s  %s  %s",
			r.p.c("↓", green),
			r.p.c(pad(f.Name, 20), cyan),
			lpad(Bytes(int64(f.Size)), 7),
			lpad(Dur(f.Took), 8),
			r.p.c(bar(f.Took, slowest, 8), blue),
			r.p.c(f.Why, dim))
	}
}

func (r *Reporter) PhaseDone(phase string, packages, bytes int, took time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.phase(phase)
	r.line("     %s %s", r.p.c("✓", green), r.p.c(fmt.Sprintf("%s · %s · %s", plural(packages, "package", "packages"), Bytes(int64(bytes)), Dur(took)), green))
}

func (r *Reporter) Grammar(g lang.GrammarInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.phase("grammar")
	r.line("     %s → %s · %s · %s",
		plural(g.Packages, "syntax package", "syntax packages"),
		plural(g.Forms, "form", "forms"),
		plural(g.Infix, "infix operator", "infix operators"),
		plural(g.Prefix, "prefix operator", "prefix operators"))
	r.line("     %s %s", r.p.c(pad("keywords", 9), dim), r.p.c(strings.Join(g.Keywords, " "), magenta))
	r.line("     %s %s", r.p.c(pad("symbols", 9), dim), r.p.c(strings.Join(g.Symbols, " "), yellow))
}

func (r *Reporter) Parsed(p lang.ParseInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.phase("parse")
	r.line("     %s %s · %s · %s · %s", r.p.c(pad(p.File, 12), cyan),
		plural(p.Tokens, "token", "tokens"), plural(p.Nodes, "node", "nodes"),
		plural(p.Statements, "top-level statement", "top-level statements"), Dur(p.Took))
	for _, f := range p.Functions {
		r.line("     %s %s · %s · %s frame", r.p.c(pad("fn "+f.Name, 12), cyan),
			plural(f.Params, "param", "params"), plural(f.Locals, "local", "locals"), Bytes(int64(f.FrameLen)))
	}
}

func (r *Reporter) Emitted(lines int, took time.Duration, variants []lang.VariantUse) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.phase("emit")
	r.line("     %s of NASM · %s", plural(lines, "line", "lines"), Dur(took))
	width := 0
	for _, v := range variants {
		width = max(width, utf8.RuneCountInString(v.Package))
	}
	for _, v := range variants {
		desc := strings.TrimSpace(v.Section + " " + map[bool]string{true: "when " + v.When}[v.When != ""])
		if desc == "" {
			desc = r.p.c("default", dim)
		} else {
			desc = r.p.c(desc, yellow)
		}
		r.line("     %s %s %s", r.p.c(pad(v.Package, width+2), cyan), lpad(fmt.Sprintf("×%d", v.Count), 4), desc)
	}
}

func (r *Reporter) Tool(name string, took time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.phase("assemble")
	r.line("     %s %s %s", r.p.c("⚙", blue), pad(name, 6), Dur(took))
}

func (r *Reporter) Built(path string, size int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.phase("assemble")
	r.line("     %s %s %s", r.p.c("✓", green), r.p.c(path, bold), r.p.c(Bytes(size)+" executable", dim))
}

// Done ends the report. note is shown after the total time (e.g. "running").
func (r *Reporter) Done(note string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.line("")
	msg := r.p.c("✓ done", bold, green) + " " + r.p.c("in "+Dur(time.Since(r.start)), green)
	if note != "" {
		msg += r.p.c(" · "+note, dim)
	}
	r.line("  %s", msg)
	if note == "running" {
		r.line("  %s", r.p.c(strings.Repeat("─", 60), dim))
	}
}

// ---------------------------------------------------------------- errors

var posRe = regexp.MustCompile(`^(.+?):(\d+):(\d+): (.*)$`)

// Error prints err. If it points at a line of srcPath, that line is shown
// with a caret under the column.
func Error(w io.Writer, color bool, err error, srcPath string) {
	p := painter{color}
	msg := err.Error()
	first, rest, _ := strings.Cut(msg, "\n")
	fmt.Fprintf(w, "\n  %s %s\n", p.c("✗ error", bold, red), p.c(first, bold))
	if rest != "" {
		for _, l := range strings.Split(rest, "\n") {
			fmt.Fprintf(w, "    %s\n", l)
		}
	}
	if m := posRe.FindStringSubmatch(first); m != nil && srcPath != "" && filepath.Base(srcPath) == m[1] {
		if src, rerr := os.ReadFile(srcPath); rerr == nil {
			ln, _ := strconv.Atoi(m[2])
			col, _ := strconv.Atoi(m[3])
			lines := strings.Split(string(src), "\n")
			if ln >= 1 && ln <= len(lines) {
				text := strings.ReplaceAll(lines[ln-1], "\t", " ")
				gutter := strconv.Itoa(ln)
				fmt.Fprintf(w, "\n    %s %s %s\n", p.c(gutter, dim), p.c("│", dim), text)
				fmt.Fprintf(w, "    %s %s %s%s\n", strings.Repeat(" ", len(gutter)), p.c("│", dim),
					strings.Repeat(" ", max(col-1, 0)), p.c("^", bold, red))
			}
		}
	}
	if hint := hintFor(msg); hint != "" {
		fmt.Fprintf(w, "\n  %s %s\n", p.c("hint", bold, yellow), hint)
	}
	fmt.Fprintln(w)
}

func hintFor(msg string) string {
	switch {
	case strings.Contains(msg, "cannot resolve") || strings.Contains(msg, "can't reach"):
		return "check your internet connection, or build from a local copy of the registry:\n" +
			"       PACKENGER_REGISTRY=file://$PWD/registry packenger ..."
	case strings.Contains(msg, "not found in registry"):
		return "check the package name in your 'use' line, and that it has been pushed to the registry"
	case strings.Contains(msg, "is not installed"):
		return "install it, e.g. 'sudo pacman -S nasm binutils' or 'sudo apt install nasm binutils'"
	}
	return ""
}

func roundTitle(round, n int) string {
	if n == 1 {
		return fmt.Sprintf("round %d · 1 download", round)
	}
	return fmt.Sprintf("round %d · %d downloads in parallel", round, n)
}

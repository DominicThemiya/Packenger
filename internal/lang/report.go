package lang

import "time"

// Reporter receives structured events while a program compiles. The
// compiler only reports facts; how they look is up to the implementation
// (see internal/ui).
type Reporter interface {
	Uses(file string, uses []string)
	Round(phase string, round int, fetches []Fetch)
	PhaseDone(phase string, packages, bytes int, took time.Duration)
	Grammar(GrammarInfo)
	Parsed(ParseInfo)
	Emitted(lines int, took time.Duration, variants []VariantUse)
	Tool(name string, took time.Duration)
	Built(path string, size int64)
}

// Fetch is one downloaded package.
type Fetch struct {
	Name, Why, Origin string
	Size              int
	Took              time.Duration
}

type GrammarInfo struct {
	Packages, Forms, Infix, Prefix int
	Keywords, Symbols              []string
}

type ParseInfo struct {
	File                      string
	Tokens, Nodes, Statements int
	Took                      time.Duration
	Functions                 []FrameInfo
}

type FrameInfo struct {
	Name                     string
	Params, Locals, FrameLen int
}

// VariantUse says how often an emit section of a backend package was used.
type VariantUse struct {
	Package, Section, When string
	Count                  int
}

type nopReporter struct{}

func (nopReporter) Uses(string, []string)                     {}
func (nopReporter) Round(string, int, []Fetch)                {}
func (nopReporter) PhaseDone(string, int, int, time.Duration) {}
func (nopReporter) Grammar(GrammarInfo)                       {}
func (nopReporter) Parsed(ParseInfo)                          {}
func (nopReporter) Emitted(int, time.Duration, []VariantUse)  {}
func (nopReporter) Tool(string, time.Duration)                {}
func (nopReporter) Built(string, int64)                       {}

func orNop(r Reporter) Reporter {
	if r == nil {
		return nopReporter{}
	}
	return r
}

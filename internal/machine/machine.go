// Package machine runs compiled Packenger programs.
//
// A compiled program is a Jot bit string. The machine knows exactly two
// things, the meanings of the bootstrap packages 0 and 1:
//
//	[ε]  = I
//	[w0] = [w] S K
//	[w1] = λx.λy.[w](x y)   ( = B [w] )
//
// It decodes the bits into a combinator graph and reduces it lazily (normal
// order, with sharing). Results are observed by applying the program to
// host atoms, e.g. a Church numeral is applied to Inc and 0.
package machine

import (
	"errors"
	"fmt"
	"strings"
)

type Tag uint8

const (
	TApp Tag = iota
	TInd     // indirection left behind by a reduction
	TS
	TK
	TI
	TB
	TNum  // host number (observation only)
	TInc  // host successor, strict in its argument
	TAtom // opaque host constant (true/false/cons/nil markers, ...)
)

type Node struct {
	Tag  Tag
	L, R *Node
	N    uint64
	Name string
}

var (
	S = &Node{Tag: TS}
	K = &Node{Tag: TK}
	I = &Node{Tag: TI}
	B = &Node{Tag: TB}
)

func App(l, r *Node) *Node   { return &Node{Tag: TApp, L: l, R: r} }
func Atom(name string) *Node { return &Node{Tag: TAtom, Name: name} }
func Num(n uint64) *Node     { return &Node{Tag: TNum, N: n} }
func follow(n *Node) *Node {
	for n.Tag == TInd {
		n = n.L
	}
	return n
}

// Decode turns Jot bits into a combinator graph, starting from start
// (normally I; validation uses an opaque atom).
func Decode(bits string, start *Node) (*Node, error) {
	cur := start
	for i := 0; i < len(bits); i++ {
		switch bits[i] {
		case '0':
			cur = App(App(cur, S), K)
		case '1':
			cur = App(B, cur)
		case ' ', '\n', '\r', '\t':
		default:
			return nil, fmt.Errorf("invalid character %q in bit string at offset %d", bits[i], i)
		}
	}
	return cur, nil
}

var ErrStepLimit = errors.New("step limit exceeded (program may not terminate; raise --max-steps)")

type Machine struct {
	MaxSteps uint64
	Steps    uint64
}

func (m *Machine) tick() error {
	m.Steps++
	if m.MaxSteps > 0 && m.Steps > m.MaxSteps {
		return ErrStepLimit
	}
	return nil
}

// WHNF reduces n to weak head normal form in place and returns the
// (indirection-free) node.
func (m *Machine) WHNF(n *Node) (*Node, error) {
	// The spine is kept across steps: after rewriting a redex we resume
	// unwinding from it instead of from n, so a step costs O(1) amortized
	// even when the spine is long (Jot programs have very long ones).
	var spine []*Node
	cur := follow(n)
	for {
		for cur.Tag == TApp {
			spine = append(spine, cur)
			cur = follow(cur.L)
		}
		k := len(spine)
		arg := func(i int) *Node { return spine[k-1-i].R }
		var used int
		switch cur.Tag {
		case TI:
			used = 1
		case TK:
			used = 2
		case TS, TB:
			used = 3
		case TInc:
			used = 1
		default: // TNum, TAtom: stuck
			return follow(n), nil
		}
		if k < used {
			return follow(n), nil
		}
		root := spine[k-used]
		switch cur.Tag {
		case TI, TK:
			*root = Node{Tag: TInd, L: arg(0)}
		case TS:
			f, g, x := arg(0), arg(1), arg(2)
			*root = Node{Tag: TApp, L: App(f, x), R: App(g, x)}
		case TB:
			f, g, x := arg(0), arg(1), arg(2)
			*root = Node{Tag: TApp, L: f, R: App(g, x)}
		case TInc:
			v, err := m.WHNF(arg(0))
			if err != nil {
				return nil, err
			}
			if v.Tag != TNum {
				return nil, fmt.Errorf("expected a number, got %s", m.Show(v, 6))
			}
			*root = Node{Tag: TNum, N: v.N + 1}
		}
		if err := m.tick(); err != nil {
			return nil, err
		}
		spine = spine[:k-used]
		cur = follow(root)
	}
}

// Unwind splits a node into its head and arguments.
func Unwind(n *Node) (*Node, []*Node) {
	var args []*Node
	n = follow(n)
	for n.Tag == TApp {
		args = append(args, n.R)
		n = follow(n.L)
	}
	for i, j := 0, len(args)-1; i < j; i, j = i+1, j-1 {
		args[i], args[j] = args[j], args[i]
	}
	return n, args
}

// Normalize reduces n to full normal form.
func (m *Machine) Normalize(n *Node) (*Node, error) {
	h, err := m.WHNF(n)
	if err != nil {
		return nil, err
	}
	head, args := Unwind(h)
	out := head
	for _, a := range args {
		na, err := m.Normalize(a)
		if err != nil {
			return nil, err
		}
		out = App(out, na)
	}
	return out, nil
}

// Show prints a (not necessarily normalized) graph, cut off at depth.
func (m *Machine) Show(n *Node, depth int) string {
	var sb strings.Builder
	var walk func(n *Node, d int, paren bool)
	walk = func(n *Node, d int, paren bool) {
		n = follow(n)
		if d > depth {
			sb.WriteString("…")
			return
		}
		switch n.Tag {
		case TApp:
			if paren {
				sb.WriteByte('(')
			}
			walk(n.L, d+1, false)
			sb.WriteByte(' ')
			walk(n.R, d+1, true)
			if paren {
				sb.WriteByte(')')
			}
		case TS:
			sb.WriteString("S")
		case TK:
			sb.WriteString("K")
		case TI:
			sb.WriteString("I")
		case TB:
			sb.WriteString("B")
		case TNum:
			fmt.Fprintf(&sb, "%d", n.N)
		case TInc:
			sb.WriteString("inc")
		case TAtom:
			sb.WriteString(n.Name)
		}
	}
	walk(n, 0, false)
	return sb.String()
}

func contains(n, target *Node) bool {
	stack := []*Node{n}
	seen := map[*Node]bool{}
	for len(stack) > 0 {
		n := follow(stack[len(stack)-1])
		stack = stack[:len(stack)-1]
		if n == target {
			return true
		}
		if n.Tag == TApp && !seen[n] {
			seen[n] = true
			stack = append(stack, n.L, n.R)
		}
	}
	return false
}

// IsApplicative reports whether appending bits to any program applies that
// program to a single term: [w·bits] = [w] T. Every package must compile to
// such a string; it is what lets the compiler encode application f x as
// "1" ++ bits(f) ++ bits(x).
func IsApplicative(bits string) bool {
	w := Atom("w")
	n, err := Decode(bits, w)
	if err != nil {
		return false
	}
	// Reduce only to weak head normal form: a body containing Y has no
	// normal form, but its head is still decided after finitely many steps.
	m := &Machine{MaxSteps: 1000000}
	v, err := m.WHNF(n)
	if err != nil {
		return false
	}
	head, args := Unwind(v)
	return head == w && len(args) == 1 && !contains(args[0], w)
}

// ---------------------------------------------------------------- observers

var (
	atomTrue  = Atom("true")
	atomFalse = Atom("false")
	atomCons  = Atom("cons")
	atomNil   = Atom("nil")
	inc       = &Node{Tag: TInc}
)

// Nat observes a Church numeral: n inc 0.
func (m *Machine) Nat(p *Node) (uint64, error) {
	v, err := m.WHNF(App(App(p, inc), Num(0)))
	if err != nil {
		return 0, err
	}
	if v.Tag != TNum {
		return 0, fmt.Errorf("result is not a number: %s", m.Show(v, 6))
	}
	return v.N, nil
}

// Bool observes a Church boolean: b true false.
func (m *Machine) Bool(p *Node) (bool, error) {
	v, err := m.WHNF(App(App(p, atomTrue), atomFalse))
	if err != nil {
		return false, err
	}
	switch v {
	case atomTrue:
		return true, nil
	case atomFalse:
		return false, nil
	}
	return false, fmt.Errorf("result is not a boolean: %s", m.Show(v, 6))
}

// List observes a Church list (l cons nil), calling elem on each element.
func (m *Machine) List(p *Node, elem func(*Node) error) error {
	cur := App(App(p, atomCons), atomNil)
	for {
		v, err := m.WHNF(cur)
		if err != nil {
			return err
		}
		head, args := Unwind(v)
		switch {
		case head == atomNil && len(args) == 0:
			return nil
		case head == atomCons && len(args) == 2:
			if err := elem(args[0]); err != nil {
				return err
			}
			cur = args[1]
		default:
			return fmt.Errorf("result is not a list: %s", m.Show(v, 6))
		}
	}
}

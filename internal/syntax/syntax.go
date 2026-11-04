// Package syntax parses Packenger source files.
//
// A source file is a sequence of top-level items. Each item starts in
// column 1; continuation lines must be indented.
//
//	name := expr     a file-local package (private helper)
//	expr             the file's body: what the package *is*
//
// Expressions:
//
//	foo              a reference to a package (or a lambda-bound name)
//	f x y            application (left associative)
//	\x y. body       abstraction (the compiler eliminates it into S/K packages)
//	(expr)           grouping
//	42               number literal  -> composed from zero/succ/dbl packages
//	"hi"             string literal  -> composed from cons/nil + numbers
//	@11100           raw bits: a sequence of the bootstrap packages 0 and 1
//
// Comments start with '#'. A comment of the form "# key: value" is
// recorded as a directive (e.g. "# output: string").
package syntax

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type Pos struct{ Line, Col int }

func (p Pos) String() string { return fmt.Sprintf("%d:%d", p.Line, p.Col) }

type Expr interface{ Position() Pos }

// Ident references a package or a lambda-bound variable. Global idents are
// produced by literal desugaring and always resolve to a hosted package.
type Ident struct {
	P      Pos
	Name   string
	Global bool
}

type Lambda struct {
	P     Pos
	Param string
	Body  Expr
}

type Apply struct {
	P       Pos
	Fn, Arg Expr
}

// Bits is a raw sequence of the two bootstrap packages.
type Bits struct {
	P    Pos
	Bits string
}

func (e *Ident) Position() Pos  { return e.P }
func (e *Lambda) Position() Pos { return e.P }
func (e *Apply) Position() Pos  { return e.P }
func (e *Bits) Position() Pos   { return e.P }

type Def struct {
	Name string
	P    Pos
	Body Expr
}

type File struct {
	Defs       []Def
	Body       Expr
	Directives map[string]string
}

// ---------------------------------------------------------------- lexer

type tokKind int

const (
	tEOF tokKind = iota
	tIdent
	tNum
	tStr
	tBits
	tLam
	tDot
	tLParen
	tRParen
	tDefine
)

type token struct {
	kind tokKind
	text string
	pos  Pos
}

var directiveRe = regexp.MustCompile(`^#\s*([A-Za-z_][\w-]*)\s*:\s*(\S+)\s*$`)

func isIdentChar(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("_-'?!/+*<>=&|~^", r)
}

func lex(src string) ([]token, map[string]string, error) {
	var toks []token
	dirs := map[string]string{}
	line, col := 1, 1
	i := 0
	advance := func(n int) {
		for k := 0; k < n; {
			r, sz := utf8.DecodeRuneInString(src[i:])
			if r == '\n' {
				line++
				col = 1
			} else {
				col++
			}
			i += sz
			k += sz
		}
	}
	for i < len(src) {
		r, sz := utf8.DecodeRuneInString(src[i:])
		pos := Pos{line, col}
		switch {
		case r == '\n' || unicode.IsSpace(r):
			advance(sz)
		case r == '#':
			end := strings.IndexByte(src[i:], '\n')
			if end < 0 {
				end = len(src) - i
			}
			if m := directiveRe.FindStringSubmatch(src[i : i+end]); m != nil {
				dirs[m[1]] = m[2]
			}
			advance(end)
		case r == '\\' || r == 'λ':
			toks = append(toks, token{tLam, string(r), pos})
			advance(sz)
		case r == '.':
			toks = append(toks, token{tDot, ".", pos})
			advance(sz)
		case r == '(':
			toks = append(toks, token{tLParen, "(", pos})
			advance(sz)
		case r == ')':
			toks = append(toks, token{tRParen, ")", pos})
			advance(sz)
		case r == ':':
			if !strings.HasPrefix(src[i:], ":=") {
				return nil, nil, fmt.Errorf("%s: unexpected ':' (did you mean ':=')", pos)
			}
			toks = append(toks, token{tDefine, ":=", pos})
			advance(2)
		case r == '@':
			j := i + 1
			for j < len(src) && (src[j] == '0' || src[j] == '1') {
				j++
			}
			if j == i+1 {
				return nil, nil, fmt.Errorf("%s: '@' must be followed by bits (0/1)", pos)
			}
			if j < len(src) && isIdentChar(rune(src[j])) {
				return nil, nil, fmt.Errorf("%s: raw bits may only contain 0 and 1", pos)
			}
			toks = append(toks, token{tBits, src[i+1 : j], pos})
			advance(j - i)
		case r == '"':
			j := i + 1
			for j < len(src) && src[j] != '"' {
				if src[j] == '\\' {
					j++
				}
				if j < len(src) && src[j] == '\n' {
					return nil, nil, fmt.Errorf("%s: unterminated string", pos)
				}
				j++
			}
			if j >= len(src) {
				return nil, nil, fmt.Errorf("%s: unterminated string", pos)
			}
			s, err := strconv.Unquote(src[i : j+1])
			if err != nil {
				return nil, nil, fmt.Errorf("%s: bad string literal: %v", pos, err)
			}
			toks = append(toks, token{tStr, s, pos})
			advance(j + 1 - i)
		case isIdentChar(r):
			j := i
			for j < len(src) {
				r2, sz2 := utf8.DecodeRuneInString(src[j:])
				if !isIdentChar(r2) {
					break
				}
				j += sz2
			}
			text := src[i:j]
			kind := tIdent
			if strings.Trim(text, "0123456789") == "" {
				kind = tNum
			}
			toks = append(toks, token{kind, text, pos})
			advance(j - i)
		default:
			return nil, nil, fmt.Errorf("%s: unexpected character %q", pos, r)
		}
	}
	toks = append(toks, token{tEOF, "", Pos{line, col}})
	return toks, dirs, nil
}

// ---------------------------------------------------------------- parser

type parser struct {
	toks      []token
	i         int
	itemStart int
}

func (p *parser) tok() token       { return p.toks[p.i] }
func (p *parser) peek(n int) token { return p.toks[min(p.i+n, len(p.toks)-1)] }

func (p *parser) atItemEnd() bool {
	t := p.tok()
	return t.kind == tEOF || (t.pos.Col == 1 && p.i != p.itemStart)
}

func Parse(src string) (*File, error) {
	toks, dirs, err := lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	f := &File{Directives: dirs}
	seen := map[string]bool{}
	for p.tok().kind != tEOF {
		p.itemStart = p.i
		if t := p.tok(); t.pos.Col != 1 {
			return nil, fmt.Errorf("%s: top-level items must start in column 1", t.pos)
		}
		if p.tok().kind == tIdent && p.peek(1).kind == tDefine {
			name := p.tok()
			p.i += 2
			body, err := p.expr()
			if err != nil {
				return nil, err
			}
			if seen[name.text] {
				return nil, fmt.Errorf("%s: %q defined twice", name.pos, name.text)
			}
			seen[name.text] = true
			f.Defs = append(f.Defs, Def{name.text, name.pos, body})
			continue
		}
		if f.Body != nil {
			return nil, fmt.Errorf("%s: a file has exactly one body expression (use 'name := ...' for helpers)", p.tok().pos)
		}
		body, err := p.expr()
		if err != nil {
			return nil, err
		}
		f.Body = body
	}
	if f.Body == nil {
		return nil, fmt.Errorf("file has no body expression")
	}
	return f, nil
}

func (p *parser) expr() (Expr, error) {
	var fn Expr
	for !p.atItemEnd() && p.tok().kind != tRParen {
		var arg Expr
		var err error
		if p.tok().kind == tLam {
			arg, err = p.lambda()
		} else {
			arg, err = p.atom()
		}
		if err != nil {
			return nil, err
		}
		if fn == nil {
			fn = arg
		} else {
			fn = &Apply{arg.Position(), fn, arg}
		}
	}
	if fn == nil {
		t := p.tok()
		return nil, fmt.Errorf("%s: expected an expression, found %s", t.pos, describe(t))
	}
	return fn, nil
}

func (p *parser) lambda() (Expr, error) {
	lam := p.tok()
	p.i++
	var params []token
	for p.tok().kind == tIdent && !p.atItemEnd() {
		params = append(params, p.tok())
		p.i++
	}
	if len(params) == 0 {
		return nil, fmt.Errorf("%s: lambda needs at least one parameter", lam.pos)
	}
	if p.tok().kind != tDot || p.atItemEnd() {
		return nil, fmt.Errorf("%s: expected '.' after lambda parameters, found %s", p.tok().pos, describe(p.tok()))
	}
	p.i++
	body, err := p.expr()
	if err != nil {
		return nil, err
	}
	for k := len(params) - 1; k >= 0; k-- {
		body = &Lambda{params[k].pos, params[k].text, body}
	}
	return body, nil
}

func (p *parser) atom() (Expr, error) {
	t := p.tok()
	switch t.kind {
	case tIdent:
		p.i++
		return &Ident{P: t.pos, Name: t.text}, nil
	case tNum:
		p.i++
		n, err := strconv.ParseUint(t.text, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%s: number too large", t.pos)
		}
		return NumberExpr(n, t.pos), nil
	case tStr:
		p.i++
		return StringExpr(t.text, t.pos), nil
	case tBits:
		p.i++
		return &Bits{t.pos, t.text}, nil
	case tLParen:
		p.i++
		e, err := p.expr()
		if err != nil {
			return nil, err
		}
		if p.tok().kind != tRParen || p.atItemEnd() {
			return nil, fmt.Errorf("%s: expected ')' to close '(' at %s", p.tok().pos, t.pos)
		}
		p.i++
		return e, nil
	}
	return nil, fmt.Errorf("%s: unexpected %s", t.pos, describe(t))
}

func describe(t token) string {
	switch t.kind {
	case tEOF:
		return "end of file"
	case tStr:
		return strconv.Quote(t.text)
	case tBits:
		return "@" + t.text
	}
	return "'" + t.text + "'"
}

// ---------------------------------------------------------------- literal sugar

func global(name string, p Pos) Expr { return &Ident{P: p, Name: name, Global: true} }

// NumberExpr builds n from the hosted packages zero, succ and dbl using its
// binary digits, so a literal costs O(log n) packages.
func NumberExpr(n uint64, p Pos) Expr {
	if n == 0 {
		return global("zero", p)
	}
	if n == 1 {
		return &Apply{p, global("succ", p), global("zero", p)}
	}
	half := &Apply{p, global("dbl", p), NumberExpr(n/2, p)}
	if n%2 == 0 {
		return half
	}
	return &Apply{p, global("succ", p), half}
}

// StringExpr builds a list of code points from the hosted packages cons and nil.
func StringExpr(s string, p Pos) Expr {
	rs := []rune(s)
	var e Expr = global("nil", p)
	for k := len(rs) - 1; k >= 0; k-- {
		e = &Apply{p, &Apply{p, global("cons", p), NumberExpr(uint64(rs[k]), p)}, e}
	}
	return e
}

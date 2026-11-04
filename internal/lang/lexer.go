package lang

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

type Pos struct {
	File      string
	Line, Col int
}

func (p Pos) String() string { return fmt.Sprintf("%s:%d:%d", p.File, p.Line, p.Col) }

type TokKind int

const (
	TEOF TokKind = iota
	TNewline
	TIdent
	TInt
	TStr
	TPunct
	THole // {name} inside an expand template
	TChar // 'a': a byte
)

type Token struct {
	Kind TokKind
	Text string
	Int  int64
	Pos  Pos
}

func (t Token) String() string {
	switch t.Kind {
	case TEOF:
		return "end of file"
	case TNewline:
		return "end of line"
	case TStr:
		return strconv.Quote(t.Text)
	case TChar:
		return strconv.QuoteRune(rune(t.Int))
	case THole:
		return "{" + t.Text + "}"
	}
	return "'" + t.Text + "'"
}

// Lex tokenizes source. Operators are not built in: puncts is the set of
// symbol literals declared by the loaded packages (plus "(", ")" and ",").
// Newlines separate statements, except inside parentheses.
func Lex(src, file string, puncts []string, holes bool) ([]Token, error) {
	// ( ) , and the type syntax : [ ] are core; everything else comes from packages
	ops := append([]string{"(", ")", ",", ":", "[", "]"}, puncts...)
	sort.Slice(ops, func(i, j int) bool { return len(ops[i]) > len(ops[j]) })
	var toks []Token
	line, col, depth := 1, 1, 0
	emit := func(t Token) {
		if t.Kind == TNewline && (depth > 0 || len(toks) == 0 || toks[len(toks)-1].Kind == TNewline) {
			return
		}
		toks = append(toks, t)
	}
	rs := []rune(src)
	for i := 0; i < len(rs); {
		r := rs[i]
		pos := Pos{file, line, col}
		adv := func(n int) {
			for k := 0; k < n; k++ {
				if rs[i] == '\n' {
					line++
					col = 1
				} else {
					col++
				}
				i++
			}
		}
		switch {
		case r == '\n':
			emit(Token{Kind: TNewline, Pos: pos})
			adv(1)
		case unicode.IsSpace(r):
			adv(1)
		case r == '/' && i+1 < len(rs) && rs[i+1] == '/':
			for i < len(rs) && rs[i] != '\n' {
				adv(1)
			}
		case holes && r == '{':
			j := i + 1
			for j < len(rs) && rs[j] != '}' {
				j++
			}
			if j >= len(rs) {
				return nil, fmt.Errorf("%s: unterminated {hole}", pos)
			}
			emit(Token{Kind: THole, Text: strings.TrimSpace(string(rs[i+1 : j])), Pos: pos})
			adv(j + 1 - i)
		case unicode.IsLetter(r) || r == '_':
			j := i
			for j < len(rs) && (unicode.IsLetter(rs[j]) || unicode.IsDigit(rs[j]) || rs[j] == '_') {
				j++
			}
			emit(Token{Kind: TIdent, Text: string(rs[i:j]), Pos: pos})
			adv(j - i)
		case unicode.IsDigit(r):
			j := i
			for j < len(rs) && (unicode.IsDigit(rs[j]) || rs[j] == '_') {
				j++
			}
			text := strings.ReplaceAll(string(rs[i:j]), "_", "")
			n, err := strconv.ParseInt(text, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("%s: number %s does not fit in 64 bits", pos, text)
			}
			emit(Token{Kind: TInt, Text: text, Int: n, Pos: pos})
			adv(j - i)
		case r == '\'':
			j := i + 1
			for j < len(rs) && rs[j] != '\'' && rs[j] != '\n' {
				if rs[j] == '\\' {
					j++
				}
				j++
			}
			if j >= len(rs) || rs[j] != '\'' {
				return nil, fmt.Errorf("%s: unterminated character literal", pos)
			}
			v, err := strconv.Unquote(string(rs[i : j+1]))
			if err != nil || len([]rune(v)) != 1 || []rune(v)[0] > 255 {
				return nil, fmt.Errorf("%s: a character literal must be one byte, e.g. 'a' or '\\n'", pos)
			}
			n := int64([]rune(v)[0])
			emit(Token{Kind: TChar, Text: strconv.FormatInt(n, 10), Int: n, Pos: pos})
			adv(j + 1 - i)
		case r == '"':
			j := i + 1
			for j < len(rs) && rs[j] != '"' && rs[j] != '\n' {
				if rs[j] == '\\' {
					j++
				}
				j++
			}
			if j >= len(rs) || rs[j] != '"' {
				return nil, fmt.Errorf("%s: unterminated string", pos)
			}
			s, err := strconv.Unquote(string(rs[i : j+1]))
			if err != nil {
				return nil, fmt.Errorf("%s: bad string literal: %v", pos, err)
			}
			emit(Token{Kind: TStr, Text: s, Pos: pos})
			adv(j + 1 - i)
		default:
			rest := string(rs[i:])
			op := ""
			for _, o := range ops {
				if strings.HasPrefix(rest, o) {
					op = o
					break
				}
			}
			if op == "" {
				return nil, fmt.Errorf("%s: unknown symbol %q (no loaded package defines it)", pos, r)
			}
			switch op {
			case "(":
				depth++
			case ")":
				if depth > 0 {
					depth--
				}
			}
			emit(Token{Kind: TPunct, Text: op, Pos: pos})
			adv(len([]rune(op)))
		}
	}
	emit(Token{Kind: TNewline, Pos: Pos{file, line, col}})
	toks = append(toks, Token{Kind: TEOF, Pos: Pos{file, line, col}})
	return toks, nil
}

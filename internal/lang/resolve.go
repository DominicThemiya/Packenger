package lang

import (
	"fmt"
	"sort"
	"strings"

	"github.com/DominicThemiya/Packenger/internal/pkgfile"
)

// Conflict resolution: a package's resolve section renames what other
// packages define, so that two packages defining the same name (or a
// package reserving a word a program needs) can be used together without
// editing either. See registry/resolve.pkg.
//
// Keyword renames change the scope's syntax patterns before the grammar is
// built (resolvePatterns); name renames, and keyword renames in library
// code, change the lexed tokens of the packages that can see the scope
// (resolveCode).

// ResolveInfo is one applied rule, for the -vb report.
type ResolveInfo struct {
	Where string // package:line of the rule
	Rule  string
	Count int    // names renamed, or patterns changed
	Unit  string // "name" or "pattern"
}

type ruleAt struct {
	f *pkgfile.File
	r *pkgfile.Rename
}

func (ra ruleAt) where() string { return fmt.Sprintf("%s:%d", ra.f.Origin, ra.r.Line) }

// rules lists every rename of the loaded packages, by package name, then
// in the order written.
func rules(front map[string]*pkgfile.File) []ruleAt {
	var out []ruleAt
	for _, name := range sortedKeys(front) {
		for _, r := range front[name].Resolve {
			out = append(out, ruleAt{front[name], r})
		}
	}
	return out
}

func inScope(name, scope string) bool {
	return name == scope || strings.HasPrefix(name, scope+"/")
}

// scopeOf lists the loaded packages a rule applies to, sorted.
func scopeOf(front map[string]*pkgfile.File, scope string) []string {
	var out []string
	for _, name := range sortedKeys(front) {
		if inScope(name, scope) {
			out = append(out, name)
		}
	}
	return out
}

// renameLits renames a literal in pattern elements; it returns how many it
// changed.
func renameLits(es []pkgfile.Elem, old, new string) int {
	n := 0
	for i := range es {
		if es[i].Lit == old {
			es[i].Lit = new
			n++
		}
		if es[i].Optional != nil {
			n += renameLits(es[i].Optional, old, new)
		}
	}
	return n
}

// resolvePatterns checks every rule and applies keyword renames to the
// syntax patterns in their scope. It returns how many patterns each
// keyword rule changed.
func resolvePatterns(front map[string]*pkgfile.File) (map[*pkgfile.Rename]int, error) {
	counts := map[*pkgfile.Rename]int{}
	for _, ra := range rules(front) {
		r := ra.r
		if front["resolve/"+r.Kind] == nil {
			return nil, fmt.Errorf("%s: rename %s needs the package resolve/%s; add 'needs resolve'", ra.where(), r.Kind, r.Kind)
		}
		scope := scopeOf(front, r.Scope)
		if len(scope) == 0 {
			return nil, fmt.Errorf("%s: no package in %s is loaded; add it to the needs line", ra.where(), r.Scope)
		}
		if r.Kind != "keyword" {
			continue
		}
		for _, name := range scope {
			for _, s := range front[name].Syntax {
				for _, p := range s.Patterns {
					if renameLits(p.Elems, r.Old, r.New) > 0 {
						counts[r]++
					}
				}
			}
		}
		if counts[r] == 0 {
			return nil, fmt.Errorf("%s: no syntax pattern in %s uses the keyword '%s'", ra.where(), r.Scope, r.Old)
		}
	}
	return counts, nil
}

// definers are the keywords that start a definition: a pattern's first
// literal when a newfunc, newtype, newglobal or newconst slot follows it
// (fn, struct, global and const in std).
func definers(front map[string]*pkgfile.File) map[string]bool {
	out := map[string]bool{}
	for _, f := range front {
		for _, s := range f.Syntax {
			for _, p := range s.Patterns {
				if len(p.Elems) < 2 || p.Elems[0].Lit == "" || p.Elems[1].Slot == nil {
					continue
				}
				switch p.Elems[1].Slot.Kind {
				case "newfunc", "newtype", "newglobal", "newconst":
					out[p.Elems[0].Lit] = true
				}
			}
		}
	}
	return out
}

// sees lists the packages whose definitions package name can use: itself,
// and what it needs, transitively, with trucks' members.
func sees(front map[string]*pkgfile.File, name string) map[string]bool {
	seen := map[string]bool{name: true}
	queue := []string{name}
	for len(queue) > 0 {
		f := front[queue[0]]
		queue = queue[1:]
		if f == nil {
			continue
		}
		var next []string
		for _, m := range f.Truck {
			next = append(next, f.Name+"/"+m)
		}
		next = append(next, f.Needs...)
		for _, n := range next {
			if !seen[n] {
				seen[n] = true
				queue = append(queue, n)
			}
		}
	}
	return seen
}

// resolveCode applies the rules to the lexed library code (package name ->
// tokens), in place.
func resolveCode(front map[string]*pkgfile.File, code map[string][]Token, patterns map[*pkgfile.Rename]int) ([]ResolveInfo, error) {
	rs := rules(front)
	if len(rs) == 0 {
		return nil, nil
	}
	defKw := definers(front)
	defs := map[string]map[string]bool{} // package -> names it defines
	for name, toks := range code {
		defs[name] = map[string]bool{}
		for i := 0; i+1 < len(toks); i++ {
			if toks[i].Kind == TIdent && defKw[toks[i].Text] && toks[i+1].Kind == TIdent {
				defs[name][toks[i+1].Text] = true
			}
		}
	}
	visible := map[string]map[string]bool{}
	for name := range code {
		visible[name] = sees(front, name)
	}
	users := func(scope string) []string { // packages with code that can see the scope
		var out []string
		for _, name := range sortedKeys(code) {
			for v := range visible[name] {
				if inScope(v, scope) {
					out = append(out, name)
					break
				}
			}
		}
		return out
	}
	var infos []ResolveInfo
	for _, ra := range rs {
		r := ra.r
		info := ResolveInfo{Where: fmt.Sprintf("%s:%d", ra.f.Name, r.Line), Rule: r.String()}
		if r.Kind == "keyword" {
			for _, name := range users(r.Scope) {
				for i, t := range code[name] {
					if t.Kind == TIdent && t.Text == r.Old {
						code[name][i].Text = r.New
					}
				}
			}
			info.Count, info.Unit = patterns[r], "pattern"
			infos = append(infos, info)
			continue
		}
		// the names the scope defines that the rule matches
		ren := map[string]string{}
		for _, name := range sortedKeys(defs) {
			if !inScope(name, r.Scope) {
				continue
			}
			for d := range defs[name] {
				switch {
				case r.Kind == "name" && d == r.Old:
					ren[d] = r.New
				case r.Kind == "prefix" && strings.HasPrefix(d, r.Old):
					ren[d] = r.New + d[len(r.Old):]
				}
			}
		}
		if len(ren) == 0 {
			return nil, fmt.Errorf("%s: nothing defined in %s matches %s '%s'", ra.where(), r.Scope, r.Kind, r.Old)
		}
		for _, name := range users(r.Scope) {
			for i, t := range code[name] {
				nw, ok := ren[t.Text]
				if t.Kind != TIdent || !ok {
					continue
				}
				// outside the scope, another package this one can see may
				// define the same name; inside it, its own definition wins
				var others []string
				for v := range visible[name] {
					if !inScope(name, r.Scope) && !inScope(v, r.Scope) && defs[v][t.Text] {
						others = append(others, v)
					}
				}
				if len(others) > 0 {
					sort.Strings(others)
					return nil, fmt.Errorf("%s: %s in %s could mean %s's or the one %s renames; give one of them another name", t.Pos, t.Text, name, others[0], ra.f.Name)
				}
				code[name][i].Text = nw
			}
		}
		for name := range defs {
			if inScope(name, r.Scope) {
				for old, nw := range ren {
					if defs[name][old] {
						delete(defs[name], old)
						defs[name][nw] = true
					}
				}
			}
		}
		info.Count, info.Unit = len(ren), "name"
		infos = append(infos, info)
	}
	return infos, nil
}

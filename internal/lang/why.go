package lang

import (
	"fmt"
	"io"
	"strings"
)

// whyEdge is one reason a package is loaded: from asks for to.
type whyEdge struct {
	from, to string
	line     string // the reason, as printed
}

// whyEdges lists every reason a package of the build is loaded, in a fixed
// order: the program's use lines, then each front-end package (by name) with
// its truck members and needs, then the backend packages translating the
// program's node kinds, then each backend package's includes and needs.
func (r *Result) whyEdges(target string) []whyEdge {
	var out []whyEdge
	for _, u := range r.Uses {
		out = append(out, whyEdge{r.File, u, fmt.Sprintf("%s uses %s", r.File, u)})
	}
	for _, name := range sortedKeys(r.Front) {
		f := r.Front[name]
		for _, m := range f.Truck {
			to := f.Name + "/" + m
			out = append(out, whyEdge{f.Name, to, fmt.Sprintf("%s is a truck with member %s", f.Name, to)})
		}
		for _, n := range f.Needs {
			out = append(out, whyEdge{f.Name, n, fmt.Sprintf("%s needs %s", f.Name, n)})
		}
	}
	// the front-end package that defines each kind of node
	definer := map[string]string{}
	for _, name := range sortedKeys(r.Front) {
		if f := r.Front[name]; len(f.Syntax) > 0 {
			definer[f.Kind()] = f.Name
		}
	}
	kinds := Kinds(r.Program)
	for _, k := range sortedKeys(kinds) {
		b := r.Back[k]
		if b == nil {
			continue
		}
		if d, ok := definer[k]; ok {
			out = append(out, whyEdge{d, b.Name, fmt.Sprintf("%s is translated by %s", d, b.Name)})
		} else {
			out = append(out, whyEdge{r.File, b.Name, fmt.Sprintf("%s has %s nodes, which %s translates", r.File, k, b.Name)})
		}
	}
	for _, k := range sortedKeys(r.Back) {
		b := r.Back[k]
		for _, inc := range Includes(b) {
			to := target + "/" + inc
			out = append(out, whyEdge{b.Name, to, fmt.Sprintf("%s includes %s", b.Name, to)})
		}
		for _, n := range b.Needs {
			out = append(out, whyEdge{b.Name, n, fmt.Sprintf("%s needs %s", b.Name, n)})
		}
	}
	return out
}

// Why explains why package name is part of the program's build: the
// shortest chain of use lines, truck members, needs, translations and
// includes leading to it, the other packages that ask for it, and the
// resolve rules that rename things in it.
func (r *Result) Why(w io.Writer, name, target string) error {
	loaded := map[string]bool{}
	for n := range r.Front {
		loaded[n] = true
	}
	for _, b := range r.Back {
		loaded[b.Name] = true
	}
	if !loaded[name] {
		return fmt.Errorf("%s is not part of the build of %s (packenger tree lists what is)", name, r.File)
	}
	edges := r.whyEdges(target)
	// breadth-first from the program, edges in order: the first path found
	// is the shortest, and always the same one
	via := map[string]int{r.File: -1}
	queue := []string{r.File}
	for len(queue) > 0 {
		if _, found := via[name]; found {
			break
		}
		at := queue[0]
		queue = queue[1:]
		for i, e := range edges {
			if _, seen := via[e.to]; e.from == at && !seen && loaded[e.to] {
				via[e.to] = i
				queue = append(queue, e.to)
			}
		}
	}
	fmt.Fprintf(w, "%s is part of the build of %s:\n", name, r.File)
	var path []string
	last := ""
	for at := name; ; {
		i, ok := via[at]
		if !ok || i < 0 {
			break
		}
		if last == "" {
			last = edges[i].from
		}
		path = append(path, edges[i].line)
		at = edges[i].from
	}
	for i := len(path) - 1; i >= 0; i-- {
		fmt.Fprintf(w, "  %s\n", path[i])
	}
	others := map[string]bool{}
	for _, e := range edges {
		if e.to == name && e.from != last {
			others[e.from] = true
		}
	}
	if len(others) > 0 {
		fmt.Fprintf(w, "also asked for by %s\n", strings.Join(sortedKeys(others), ", "))
	}
	var renames []string
	for _, ra := range rules(r.Front) {
		if inScope(name, ra.r.Scope) {
			renames = append(renames, fmt.Sprintf("renamed by %s:%d: %s", ra.f.Name, ra.r.Line, ra.r))
		}
	}
	for _, l := range renames {
		fmt.Fprintln(w, l)
	}
	return nil
}

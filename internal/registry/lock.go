package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
)

// A Lock pins every package a program is built from: where the registry is
// and the SHA-256 of each package file. See docs/publishing.md#lock-files.
type Lock struct {
	Path     string            // the lock file, for messages
	Source   string            // the program it locks, for messages
	Registry string            // registry base the lock was made against
	Routes   []Route           // the package tree's routes, pinned
	Sums     map[string]string // package name -> hex SHA-256
}

// LockPath is where the lock file of source file src lives: next to it,
// with .lock instead of .pkg.
func LockPath(src string) string { return strings.TrimSuffix(src, ".pkg") + ".lock" }

// Sum is the hex SHA-256 of a package file.
func Sum(src string) string {
	h := sha256.Sum256([]byte(src))
	return hex.EncodeToString(h[:])
}

var sumRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ReadLock reads the lock file at path, which locks source file src. It
// returns nil and no error if there is no lock file.
func ReadLock(path, src string) (*Lock, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	l := &Lock{Path: path, Source: src, Sums: map[string]string{}}
	for i, line := range strings.Split(string(b), "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "//") {
			continue
		}
		f := strings.Fields(t)
		switch {
		case len(f) == 2 && f[0] == "registry" && l.Registry == "":
			l.Registry = f[1]
		case len(f) == 2 && ValidName(f[0]) && sumRe.MatchString(f[1]) && l.Sums[f[0]] == "":
			l.Sums[f[0]] = f[1]
		case len(f) == 3 && f[0] == "route" && ValidName(f[1]) && !l.routed(f[1]):
			l.Routes = append(l.Routes, Route{f[1], f[2]})
		default:
			return nil, fmt.Errorf("%s:%d: expected 'registry <url>', 'route <prefix> <url>' or '<package> <sha256>'", path, i+1)
		}
	}
	if l.Registry == "" {
		return nil, fmt.Errorf("%s: no 'registry' line", path)
	}
	return l, nil
}

func (l *Lock) routed(prefix string) bool {
	for _, r := range l.Routes {
		if r.Prefix == prefix {
			return true
		}
	}
	return false
}

// Check verifies that package name, with source text src, is the locked
// version.
func (l *Lock) Check(name, src string) error {
	want, ok := l.Sums[name]
	if !ok {
		return fmt.Errorf("package %q is not in %s; run 'packenger lock %s' to add it", name, l.Path, l.Source)
	}
	if got := Sum(src); got != want {
		return fmt.Errorf("package %q does not match %s: it has sha256 %s, but %s was locked; run 'packenger lock %s' to accept the new version",
			name, l.Path, got[:12], want[:12], l.Source)
	}
	return nil
}

// FormatLock renders a lock file for source file src.
func FormatLock(src, registry string, routes []Route, sums map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "// Packenger lock file for %s, written by 'packenger lock %s'.\n", src, src)
	b.WriteString("// Builds fetch exactly these package versions. Don't edit it: run\n")
	b.WriteString("// 'packenger lock' again to update it.\n")
	fmt.Fprintf(&b, "registry %s\n", registry)
	for _, r := range routes {
		fmt.Fprintf(&b, "route %s %s\n", r.Prefix, r.Base)
	}
	names := make([]string, 0, len(sums))
	for n := range sums {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(&b, "%s %s\n", n, sums[n])
	}
	return b.String()
}

var (
	rawRe    = regexp.MustCompile(`^https://raw\.githubusercontent\.com/([^/]+)/([^/]+)/([^/]+)(/.*)?$`)
	commitRe = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// Pin makes a registry URL immutable where it can: a GitHub raw URL on a
// branch or tag is resolved to the commit it points at now. Other URLs are
// returned unchanged. The second result is the commit, if one was resolved.
func (r *Registry) Pin(ctx context.Context) (string, string, error) {
	return r.PinURL(ctx, r.Base)
}

// PinURL pins registry URL base like Pin.
func (r *Registry) PinURL(ctx context.Context, base string) (string, string, error) {
	m := rawRe.FindStringSubmatch(base)
	if m == nil || commitRe.MatchString(m[3]) {
		return base, "", nil
	}
	owner, repo, ref, rest := m[1], m[2], m[3], m[4]
	api := fmt.Sprintf("https://api.github.com/repos/%s/%s/commits/%s", owner, repo, ref)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", "packenger")
	req.Header.Set("Accept", "application/vnd.github.sha")
	resp, err := r.Client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("can't find the commit of %s/%s %s: can't reach api.github.com", owner, repo, ref)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
	sha := strings.TrimSpace(string(b))
	if resp.StatusCode != http.StatusOK || !commitRe.MatchString(sha) {
		return "", "", fmt.Errorf("can't find the commit of %s/%s %s on GitHub", owner, repo, ref)
	}
	return fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s%s", owner, repo, sha, rest), sha, nil
}

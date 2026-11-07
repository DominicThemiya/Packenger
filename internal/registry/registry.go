// Package registry fetches package sources from a remote host at compile
// time. Nothing is written to disk: sources live in memory for the duration
// of one compilation.
package registry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// DefaultBase is the hosted registry: the registry/ folder of the Packenger
// GitHub repository, served raw.
const DefaultBase = "https://raw.githubusercontent.com/DominicThemiya/Packenger/main/registry"

var nameRe = regexp.MustCompile(`^[A-Za-z_\-'?!+*<>=&|~^][\w\-'?!+*<>=&|~^]*(/[A-Za-z_\-'?!+*<>=&|~^][\w\-'?!+*<>=&|~^]*)*$`)

// Registry resolves package names to source text.
type Registry struct {
	Base   string
	Routes []Route // packages under these prefixes come from other registries
	Client *http.Client

	mu    sync.Mutex
	cache map[string]*entry
}

type entry struct {
	once   sync.Once
	src    string
	origin string
	err    error
}

// New returns a registry for base, which may be an http(s) URL or, for
// developing the registry itself, a file:// URL.
func New(base string) *Registry {
	return &Registry{
		Base:   strings.TrimRight(base, "/"),
		Client: &http.Client{Timeout: 30 * time.Second},
		cache:  map[string]*entry{},
	}
}

// ValidName reports whether name can be a hosted package name.
func ValidName(name string) bool { return nameRe.MatchString(name) }

// A Route sends the packages under a name prefix to their own registry:
// prefix "acme" covers "acme" and "acme/json".
type Route struct{ Prefix, Base string }

// BaseFor returns the registry that hosts package name: the route with the
// longest matching prefix, else Base.
func (r *Registry) BaseFor(name string) string {
	base, best := r.Base, -1
	for _, rt := range r.Routes {
		if (name == rt.Prefix || strings.HasPrefix(name, rt.Prefix+"/")) && len(rt.Prefix) > best {
			base, best = rt.Base, len(rt.Prefix)
		}
	}
	return base
}

// URL returns where package name is hosted.
func (r *Registry) URL(name string) string {
	segs := strings.Split(name, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.TrimRight(r.BaseFor(name), "/") + "/" + strings.Join(segs, "/") + ".pkg"
}

// Fetch downloads package name. Concurrent calls for the same name share one
// request.
func (r *Registry) Fetch(ctx context.Context, name string) (src, origin string, err error) {
	if !ValidName(name) {
		return "", "", fmt.Errorf("invalid package name %q", name)
	}
	r.mu.Lock()
	e, ok := r.cache[name]
	if !ok {
		e = &entry{}
		r.cache[name] = e
	}
	r.mu.Unlock()
	e.once.Do(func() { e.src, e.origin, e.err = r.fetch(ctx, name) })
	return e.src, e.origin, e.err
}

func (r *Registry) fetch(ctx context.Context, name string) (string, string, error) {
	u := r.URL(name)
	if path, ok := strings.CutPrefix(u, "file://"); ok {
		p, err := url.PathUnescape(path)
		if err != nil {
			return "", u, err
		}
		b, err := os.ReadFile(p)
		if os.IsNotExist(err) {
			return "", u, fmt.Errorf("package %q not found in registry (%s)", name, u)
		}
		return string(b), u, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", u, err
	}
	req.Header.Set("User-Agent", "packenger")
	resp, err := r.Client.Do(req)
	if err != nil {
		var dns *net.DNSError
		if errors.As(err, &dns) {
			return "", u, fmt.Errorf("can't download package %q: cannot resolve %s (offline, or a DNS problem)", name, dns.Name)
		}
		return "", u, fmt.Errorf("can't download package %q: can't reach %s: %w", name, req.URL.Host, errors.Unwrap(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", u, fmt.Errorf("package %q not found in registry (%s)", name, u)
	}
	if resp.StatusCode != http.StatusOK {
		return "", u, fmt.Errorf("fetching package %q: %s (%s)", name, resp.Status, u)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", u, fmt.Errorf("fetching package %q: %w", name, err)
	}
	return string(b), u, nil
}

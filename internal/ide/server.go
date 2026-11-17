package ide

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed web
var webFS embed.FS

// Server is pkide's HTTP server: the app and its API, on localhost only.
type Server struct {
	Config *Config
	Engine *Engine
	Procs  *Procs
	Port   int
	token  string
}

func NewServer(cfg *Config, port int) *Server {
	b := make([]byte, 16)
	rand.Read(b)
	reg := os.Getenv("PACKENGER_REGISTRY")
	if reg == "" {
		reg = "https://raw.githubusercontent.com/DominicThemiya/Packenger/main/registry"
	}
	return &Server{Config: cfg, Engine: NewEngine(reg), Procs: NewProcs(), Port: port, token: hex.EncodeToString(b)}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(webFS, "web")
	files := http.FileServer(http.FS(static))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/", "/index.html":
			b, _ := webFS.ReadFile("web/index.html")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.Write(bytes.ReplaceAll(b, []byte("{{TOKEN}}"), []byte(s.token)))
		case "/icon-192.png", "/icon-512.png":
			n := 192
			if strings.Contains(r.URL.Path, "512") {
				n = 512
			}
			w.Header().Set("Content-Type", "image/png")
			png.Encode(w, Icon(n))
		case "/sw.js":
			w.Header().Set("Cache-Control", "no-cache")
			files.ServeHTTP(w, r)
		default:
			files.ServeHTTP(w, r)
		}
	})
	mux.HandleFunc("/api/ping", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"app": "pkide", "version": Version(), "pid": os.Getpid()})
	})
	api := func(path string, h func(r *http.Request, body []byte) (any, error)) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Pkide-Token") != s.token {
				http.Error(w, "bad token: reload the page", http.StatusForbidden)
				return
			}
			body, _ := io.ReadAll(io.LimitReader(r.Body, 32<<20))
			out, err := h(r, body)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				writeJSON(w, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, out)
		})
	}
	s.routes(api)
	mux.HandleFunc("/api/events/", s.events)
	return s.guard(mux)
}

// guard only answers requests made to localhost by pages of this origin:
// no other site can drive the IDE, even through DNS rebinding.
func (s *Server) guard(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		port := ":" + strconv.Itoa(s.Port)
		if host != "127.0.0.1"+port && host != "localhost"+port && host != "[::1]"+port {
			http.Error(w, "pkide only answers on localhost", http.StatusForbidden)
			return
		}
		if o := r.Header.Get("Origin"); o != "" && o != "http://"+host {
			http.Error(w, "cross-origin requests are not allowed", http.StatusForbidden)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		h.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// ---------------------------------------------------------------- requests

type req struct {
	WS     string   `json:"ws"`
	Path   string   `json:"path"`
	Text   *string  `json:"text"`
	Line   int      `json:"line"`
	Col    int      `json:"col"`
	Prefix string   `json:"prefix"`
	Name   string   `json:"name"`
	To     string   `json:"to"`
	Dir    bool     `json:"dir"`
	Action string   `json:"action"`
	Arg    string   `json:"arg"`
	ID     string   `json:"id"`
	Rev    string   `json:"rev"`
	Pkc    bool     `json:"pkc"`
	Repo   *string  `json:"repo"`
	Theme  *string  `json:"theme"`
	Prof   *Profile `json:"profile"`
}

func parse(body []byte) (*req, error) {
	var q req
	if err := json.Unmarshal(body, &q); err != nil {
		return nil, fmt.Errorf("bad request: %v", err)
	}
	return &q, nil
}

// workspace checks that ws is an absolute folder.
func workspace(ws string) (string, error) {
	if ws == "" || !filepath.IsAbs(ws) {
		return "", errors.New("open a folder first")
	}
	ws = filepath.Clean(ws)
	if st, err := os.Stat(ws); err != nil || !st.IsDir() {
		return "", fmt.Errorf("%s is not a folder", ws)
	}
	return ws, nil
}

// inside resolves path within the workspace, refusing anything outside it.
func inside(ws, path string) (string, error) {
	ws, err := workspace(ws)
	if err != nil {
		return "", err
	}
	p := filepath.Clean(filepath.Join(ws, path))
	if filepath.IsAbs(path) {
		p = filepath.Clean(path)
	}
	if p != ws && !strings.HasPrefix(p, ws+string(os.PathSeparator)) {
		return "", fmt.Errorf("%s is outside the workspace", path)
	}
	return p, nil
}

// source is the file a request is about, with its unsaved text.
func (s *Server) source(q *req) (Source, error) {
	p, err := inside(q.WS, q.Path)
	if err != nil {
		return Source{}, err
	}
	text := ""
	if q.Text != nil {
		text = *q.Text
	} else if b, err := os.ReadFile(p); err == nil {
		text = string(b)
	}
	prof := s.Config.ProfileFor(q.WS)
	return Source{Path: p, Text: text, Registry: s.Config.RegistryURL(prof)}, nil
}

func reqCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 60*time.Second)
}

// Entry is a file or folder in a listing.
type Entry struct {
	Name string `json:"name"`
	Dir  bool   `json:"dir"`
}

func (s *Server) state() map[string]any {
	s.Config.mu.Lock()
	defer s.Config.mu.Unlock()
	home, _ := os.UserHomeDir()
	_, wasmtime := exec.LookPath("wasmtime")
	return map[string]any{
		"repo": s.Config.Repo, "builds": s.Config.Builds, "recent": s.Config.Recent, "theme": s.Config.Theme,
		"home": home, "dataDir": DataDir(), "configDir": ConfigDir(), "wasmtime": wasmtime == nil,
		"defaultRegistry": s.Engine.DefaultRegistry,
	}
}

func (s *Server) routes(api func(string, func(*http.Request, []byte) (any, error))) {
	api("/api/state", func(r *http.Request, b []byte) (any, error) { return s.state(), nil })
	api("/api/settings", func(r *http.Request, b []byte) (any, error) {
		q, err := parse(b)
		if err != nil {
			return nil, err
		}
		s.Config.mu.Lock()
		if q.Repo != nil {
			s.Config.Repo = *q.Repo
		}
		if q.Theme != nil {
			s.Config.Theme = *q.Theme
		}
		s.Config.mu.Unlock()
		if q.Repo != nil {
			s.Config.Detect()
		}
		return s.state(), s.Config.Save()
	})
	api("/api/workspace", func(r *http.Request, b []byte) (any, error) {
		q, err := parse(b)
		if err != nil {
			return nil, err
		}
		ws, err := workspace(q.WS)
		if err != nil {
			return nil, err
		}
		s.Config.Touch(ws)
		return map[string]any{"ws": ws, "profile": s.Config.ProfileFor(ws), "state": s.state()}, nil
	})
	api("/api/profile", func(r *http.Request, b []byte) (any, error) {
		q, err := parse(b)
		if err != nil {
			return nil, err
		}
		ws, err := workspace(q.WS)
		if err != nil || q.Prof == nil {
			return nil, errors.New("bad profile")
		}
		return q.Prof, s.Config.SetProfile(ws, q.Prof)
	})
	api("/api/dirs", func(r *http.Request, b []byte) (any, error) {
		// folders, for the open-folder dialog: anywhere the user can read
		q, err := parse(b)
		if err != nil {
			return nil, err
		}
		dir := filepath.Clean(q.Path)
		if !filepath.IsAbs(dir) {
			return nil, errors.New("give an absolute path")
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		var out []string
		for _, e := range entries {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				out = append(out, e.Name())
			}
		}
		return map[string]any{"dir": dir, "dirs": out}, nil
	})
	api("/api/fs/list", func(r *http.Request, b []byte) (any, error) {
		q, err := parse(b)
		if err != nil {
			return nil, err
		}
		p, err := inside(q.WS, q.Path)
		if err != nil {
			return nil, err
		}
		entries, err := os.ReadDir(p)
		if err != nil {
			return nil, err
		}
		out := []Entry{}
		for _, e := range entries {
			n := e.Name()
			if n == ".git" || n == "node_modules" || n == ".DS_Store" {
				continue
			}
			dir := e.IsDir()
			if e.Type()&os.ModeSymlink != 0 {
				if st, err := os.Stat(filepath.Join(p, n)); err == nil {
					dir = st.IsDir()
				}
			}
			out = append(out, Entry{n, dir})
		}
		sort.SliceStable(out, func(i, j int) bool {
			if out[i].Dir != out[j].Dir {
				return out[i].Dir
			}
			return out[i].Name < out[j].Name
		})
		return out, nil
	})
	api("/api/fs/read", func(r *http.Request, b []byte) (any, error) {
		q, err := parse(b)
		if err != nil {
			return nil, err
		}
		p, err := inside(q.WS, q.Path)
		if err != nil {
			return nil, err
		}
		st, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if st.Size() > 8<<20 {
			return nil, fmt.Errorf("%s is too big to edit (%d MB)", q.Path, st.Size()>>20)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		if bytes.IndexByte(data, 0) >= 0 {
			return nil, fmt.Errorf("%s is a binary file", q.Path)
		}
		return map[string]any{"text": string(data), "mtime": st.ModTime().UnixMilli()}, nil
	})
	api("/api/fs/write", func(r *http.Request, b []byte) (any, error) {
		q, err := parse(b)
		if err != nil || q.Text == nil {
			return nil, errors.New("bad request")
		}
		p, err := inside(q.WS, q.Path)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(p, []byte(*q.Text), 0o644); err != nil {
			return nil, err
		}
		st, _ := os.Stat(p)
		return map[string]any{"mtime": st.ModTime().UnixMilli()}, nil
	})
	api("/api/fs/create", func(r *http.Request, b []byte) (any, error) {
		q, err := parse(b)
		if err != nil {
			return nil, err
		}
		p, err := inside(q.WS, q.Path)
		if err != nil {
			return nil, err
		}
		if _, err := os.Stat(p); err == nil {
			return nil, fmt.Errorf("%s already exists", q.Path)
		}
		if q.Dir {
			return map[string]bool{"ok": true}, os.MkdirAll(p, 0o755)
		}
		os.MkdirAll(filepath.Dir(p), 0o755)
		text := ""
		if q.Text != nil {
			text = *q.Text
		}
		return map[string]bool{"ok": true}, os.WriteFile(p, []byte(text), 0o644)
	})
	api("/api/fs/rename", func(r *http.Request, b []byte) (any, error) {
		q, err := parse(b)
		if err != nil {
			return nil, err
		}
		from, err := inside(q.WS, q.Path)
		if err != nil {
			return nil, err
		}
		to, err := inside(q.WS, q.To)
		if err != nil {
			return nil, err
		}
		if _, err := os.Stat(to); err == nil {
			return nil, fmt.Errorf("%s already exists", q.To)
		}
		return map[string]bool{"ok": true}, os.Rename(from, to)
	})
	api("/api/fs/delete", func(r *http.Request, b []byte) (any, error) {
		q, err := parse(b)
		if err != nil {
			return nil, err
		}
		p, err := inside(q.WS, q.Path)
		if err != nil {
			return nil, err
		}
		ws, _ := workspace(q.WS)
		if p == ws {
			return nil, errors.New("won't delete the workspace itself")
		}
		return map[string]bool{"ok": true}, os.RemoveAll(p)
	})
	api("/api/complete", func(r *http.Request, b []byte) (any, error) {
		q, err := parse(b)
		if err != nil {
			return nil, err
		}
		src, err := s.source(q)
		if err != nil {
			return nil, err
		}
		ctx, cancel := reqCtx(r)
		defer cancel()
		return s.Engine.Complete(ctx, src, q.Line, q.Prefix)
	})
	api("/api/hover", func(r *http.Request, b []byte) (any, error) {
		q, err := parse(b)
		if err != nil {
			return nil, err
		}
		src, err := s.source(q)
		if err != nil {
			return nil, err
		}
		ctx, cancel := reqCtx(r)
		defer cancel()
		return s.Engine.Hover(ctx, src, q.Line, q.Col)
	})
	api("/api/check", func(r *http.Request, b []byte) (any, error) {
		q, err := parse(b)
		if err != nil {
			return nil, err
		}
		src, err := s.source(q)
		if err != nil {
			return nil, err
		}
		ctx, cancel := reqCtx(r)
		defer cancel()
		d := s.Engine.Check(ctx, src)
		if d == nil {
			d = []Diagnostic{}
		}
		return d, nil
	})
	api("/api/outline", func(r *http.Request, b []byte) (any, error) {
		q, err := parse(b)
		if err != nil {
			return nil, err
		}
		src, err := s.source(q)
		if err != nil {
			return nil, err
		}
		ctx, cancel := reqCtx(r)
		defer cancel()
		o := s.Engine.Outline(ctx, src)
		if o == nil {
			o = []Symbol{}
		}
		return o, nil
	})
	api("/api/grammar", func(r *http.Request, b []byte) (any, error) {
		q, err := parse(b)
		if err != nil {
			return nil, err
		}
		src, err := s.source(q)
		if err != nil {
			return nil, err
		}
		ctx, cancel := reqCtx(r)
		defer cancel()
		return s.Engine.Grammar(ctx, src)
	})
	api("/api/package", func(r *http.Request, b []byte) (any, error) {
		q, err := parse(b)
		if err != nil {
			return nil, err
		}
		src, err := s.source(q)
		if err != nil {
			return nil, err
		}
		ctx, cancel := reqCtx(r)
		defer cancel()
		text, origin, err := s.Engine.FetchPackage(ctx, src, q.Name)
		if err != nil {
			return nil, err
		}
		// a package in the workspace opens as a file
		local := ""
		if p, ok := strings.CutPrefix(origin, "file://"); ok {
			if rel, err := filepath.Rel(q.WS, p); err == nil && !strings.HasPrefix(rel, "..") {
				local = rel
			}
		}
		return map[string]string{"text": text, "origin": origin, "local": local}, nil
	})
	api("/api/loaded", func(r *http.Request, b []byte) (any, error) {
		// the packages a file loads, for the packages panel and why
		q, err := parse(b)
		if err != nil {
			return nil, err
		}
		src, err := s.source(q)
		if err != nil {
			return nil, err
		}
		ctx, cancel := reqCtx(r)
		defer cancel()
		v, err := s.Engine.Analyze(ctx, src, -1)
		if err != nil {
			return nil, err
		}
		type pkg struct {
			Name  string `json:"name"`
			Doc   string `json:"doc"`
			Truck bool   `json:"truck"`
		}
		out := []pkg{}
		if v.A != nil {
			for _, n := range sortedNames(v.A.Front) {
				f := v.A.Front[n]
				out = append(out, pkg{n, f.Doc, f.IsTruck})
			}
		}
		return out, nil
	})
	api("/api/reload", func(r *http.Request, b []byte) (any, error) {
		s.Engine.Forget()
		return map[string]bool{"ok": true}, nil
	})
	api("/api/builds/add", func(r *http.Request, b []byte) (any, error) {
		q, err := parse(b)
		if err != nil {
			return nil, err
		}
		if _, err := s.Config.AddBuild(q.Path, q.Name); err != nil {
			return nil, err
		}
		return s.state(), nil
	})
	api("/api/builds/remove", func(r *http.Request, b []byte) (any, error) {
		q, err := parse(b)
		if err != nil {
			return nil, err
		}
		return s.state(), s.Config.RemoveBuild(q.ID)
	})
	api("/api/builds/rename", func(r *http.Request, b []byte) (any, error) {
		q, err := parse(b)
		if err != nil {
			return nil, err
		}
		if bd := s.Config.FindBuild(q.ID); bd != nil && strings.TrimSpace(q.Name) != "" {
			s.Config.mu.Lock()
			bd.Name = strings.TrimSpace(q.Name)
			s.Config.mu.Unlock()
		}
		return s.state(), s.Config.Save()
	})
	api("/api/builds/detect", func(r *http.Request, b []byte) (any, error) {
		s.Config.Detect()
		return s.state(), s.Config.Save()
	})
	api("/api/builds/git", func(r *http.Request, b []byte) (any, error) {
		q, err := parse(b)
		if err != nil {
			return nil, err
		}
		rev := strings.TrimSpace(q.Rev)
		if rev == "" || strings.HasPrefix(rev, "-") {
			return nil, errors.New("give a commit, branch or tag")
		}
		p := s.Procs.Go("build packenger @ "+rev, func(w io.Writer) error {
			return s.Config.BuildFromGit(rev, q.Pkc, w)
		})
		return map[string]string{"id": p.ID}, nil
	})
	api("/api/git/refs", func(r *http.Request, b []byte) (any, error) {
		if s.Config.Repo == "" {
			return []string{}, nil
		}
		cmd := exec.Command("git", "log", "--format=%h %as %s", "-n", "40")
		cmd.Dir = s.Config.Repo
		out, err := cmd.Output()
		if err != nil {
			return []string{}, nil
		}
		tags := exec.Command("git", "tag", "--sort=-creatordate")
		tags.Dir = s.Config.Repo
		t, _ := tags.Output()
		var refs []string
		for _, l := range strings.Split(strings.TrimSpace(string(t)), "\n") {
			if l != "" {
				refs = append(refs, l+" (tag)")
			}
		}
		for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if l != "" {
				refs = append(refs, l)
			}
		}
		return refs, nil
	})
	api("/api/run", s.run)
	api("/api/fmt", s.format)
	api("/api/proc/kill", func(r *http.Request, b []byte) (any, error) {
		q, err := parse(b)
		if err != nil {
			return nil, err
		}
		if p := s.Procs.Get(q.ID); p != nil {
			p.Kill()
		}
		return map[string]bool{"ok": true}, nil
	})
	api("/api/proc/input", func(r *http.Request, b []byte) (any, error) {
		q, err := parse(b)
		if err != nil || q.Text == nil {
			return nil, errors.New("bad request")
		}
		p := s.Procs.Get(q.ID)
		if p == nil {
			return nil, errors.New("no such process")
		}
		return map[string]bool{"ok": true}, p.Input(*q.Text)
	})
}

// events streams a process's output as server-sent events. EventSource
// can't send headers, so the token comes in the query.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("token") != s.token {
		http.Error(w, "bad token", http.StatusForbidden)
		return
	}
	p := s.Procs.Get(strings.TrimPrefix(r.URL.Path, "/api/events/"))
	if p == nil {
		http.NotFound(w, r)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	offset := 0
	for {
		b, done, code, changed := p.Read(offset)
		if len(b) > 0 {
			offset += len(b)
			j, _ := json.Marshal(string(b))
			fmt.Fprintf(w, "event: out\ndata: %s\n\n", j)
		}
		if done {
			fmt.Fprintf(w, "event: exit\ndata: %d\n\n", code)
			flusher.Flush()
			return
		}
		flusher.Flush()
		select {
		case <-changed:
		case <-r.Context().Done():
			return
		case <-time.After(15 * time.Second):
			fmt.Fprint(w, ": keep-alive\n\n")
		}
	}
}

// profileBuild returns the workspace's profile and its build.
func (s *Server) profileBuild(ws string) (*Profile, *Build, error) {
	p := s.Config.ProfileFor(ws)
	b := s.Config.FindBuild(p.Build)
	if b == nil {
		return p, nil, errors.New("no Packenger build: add one in BUILDS")
	}
	return p, b, nil
}

// run starts a compiler command on a file with the workspace's profile.
func (s *Server) run(r *http.Request, body []byte) (any, error) {
	q, err := parse(body)
	if err != nil {
		return nil, err
	}
	file, err := inside(q.WS, q.Path)
	if err != nil {
		return nil, err
	}
	prof, b, err := s.profileBuild(q.WS)
	if err != nil {
		return nil, err
	}
	switch q.Action {
	case "run", "build", "test", "asm", "tree", "why", "lock":
	default:
		return nil, fmt.Errorf("unknown action %q", q.Action)
	}
	if !b.CanRun() && (q.Action == "run" || q.Action == "build" || q.Action == "test" || q.Action == "lock") {
		return nil, fmt.Errorf("%s can't %s programs (it runs sandboxed in wasmtime); choose another build", b.Name, q.Action)
	}
	args := []string{q.Action}
	if prof.Target != "" {
		args = append(args, "--target", prof.Target)
	}
	if reg := s.Config.RegistryURL(prof); reg != "" {
		args = append(args, "--registry", reg)
	}
	dir := filepath.Dir(file)
	switch q.Action {
	case "test":
		target := file
		if q.Arg != "" {
			if target, err = inside(q.WS, q.Arg); err != nil {
				return nil, err
			}
		}
		dir, _ = workspace(q.WS)
		if r, err := filepath.Rel(dir, target); err == nil {
			target = r
		}
		args = append(args, target)
	case "why":
		if q.Arg == "" {
			return nil, errors.New("why needs a package name")
		}
		args = append(args, file, q.Arg)
	case "build":
		args = append(args, file, "-o", strings.TrimSuffix(file, ".pkg"))
	default:
		args = append(args, file)
	}
	title := q.Action + " " + filepath.Base(file)
	p, err := s.Procs.Start(title+" · "+b.Name, dir, nil, b.Command(args...))
	if err != nil {
		return nil, err
	}
	return map[string]string{"id": p.ID}, nil
}

// format runs tools/pkfmt.pkg on the text of a file, with a pkfmt built
// once per build and version of pkfmt.pkg.
func (s *Server) format(r *http.Request, body []byte) (any, error) {
	q, err := parse(body)
	if err != nil || q.Text == nil {
		return nil, errors.New("bad request")
	}
	file, err := inside(q.WS, q.Path)
	if err != nil {
		return nil, err
	}
	if s.Config.Repo == "" {
		return nil, errors.New("formatting uses tools/pkfmt.pkg from the Packenger repository: set it in SETTINGS")
	}
	tool := filepath.Join(s.Config.Repo, "tools", "pkfmt.pkg")
	src, err := os.ReadFile(tool)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", tool, err)
	}
	prof, b, err := s.profileBuild(q.WS)
	if err != nil {
		return nil, err
	}
	if !b.CanRun() {
		for _, o := range s.Config.Builds {
			if o.CanRun() {
				b = o
				break
			}
		}
	}
	sum := sha256.Sum256(append(src, []byte(b.Path)...))
	exe := filepath.Join(DataDir(), "pkfmt", hex.EncodeToString(sum[:8]))
	reg := "file://" + filepath.Join(s.Config.Repo, "registry")
	if _, err := os.Stat(exe); err != nil {
		os.MkdirAll(filepath.Dir(exe), 0o755)
		cmd := exec.Command(b.Command("build", tool, "-o", exe)[0], b.Command("build", tool, "-o", exe)[1:]...)
		cmd.Env = append(os.Environ(), "PACKENGER_REGISTRY="+reg, "NO_COLOR=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("building pkfmt with %s:\n%s", b.Name, out)
		}
	}
	tmp := filepath.Join(filepath.Dir(file), ".pkide-fmt-"+filepath.Base(file))
	if err := os.WriteFile(tmp, []byte(*q.Text), 0o644); err != nil {
		return nil, err
	}
	defer os.Remove(tmp)
	cmd := exec.Command(exe, tmp)
	if r := s.Config.RegistryURL(prof); r != "" {
		cmd.Args = []string{exe, "--registry", r, tmp}
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("pkfmt: %s", strings.ReplaceAll(strings.TrimSpace(errb.String()), tmp, filepath.Base(file)))
	}
	return map[string]string{"text": out.String()}, nil
}

var version struct {
	once sync.Once
	v    string
}

// Version identifies this build of pkide: a hash of its executable, so a
// running server can be told apart from a newer binary.
func Version() string {
	version.once.Do(func() {
		version.v = "unknown"
		exe, err := os.Executable()
		if err != nil {
			return
		}
		f, err := os.Open(exe)
		if err != nil {
			return
		}
		defer f.Close()
		h := sha256.New()
		if _, err := io.Copy(h, f); err == nil {
			version.v = hex.EncodeToString(h.Sum(nil)[:8])
		}
	})
	return version.v
}

// Icon draws pkide's icon: a mauve P on Catppuccin crust, pixel by pixel.
func Icon(n int) image.Image {
	glyph := []string{
		"........",
		".#####..",
		".##..##.",
		".##..##.",
		".#####..",
		".##.....",
		".##.....",
		"........",
	}
	img := image.NewRGBA(image.Rect(0, 0, n, n))
	bg := color.RGBA{0x11, 0x11, 0x1b, 0xff}    // Catppuccin Mocha crust
	frame := color.RGBA{0x31, 0x32, 0x44, 0xff} // surface0
	amber := color.RGBA{0xcb, 0xa6, 0xf7, 0xff} // mauve
	cell := n / 12
	off := (n - 8*cell) / 2
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			c := bg
			if x < n/24 || y < n/24 || x >= n-n/24 || y >= n-n/24 {
				c = frame
			}
			gx, gy := (x-off)/cell, (y-off)/cell
			if x >= off && y >= off && gx < 8 && gy < 8 && glyph[gy][gx] == '#' {
				c = amber
			}
			img.Set(x, y, c)
		}
	}
	return img
}

package ide

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// A Build is one Packenger compiler the IDE can build programs with.
type Build struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Kind is go (the Go compiler), pkc (pkc, native), pkc-c (pkc built
	// with ctables), wasm (pkc.wasm, run with wasmtime) or other.
	Kind    string `json:"kind"`
	Path    string `json:"path"`
	Version string `json:"version,omitempty"` // the git commit it was built from
	Subject string `json:"subject,omitempty"` // that commit's subject
	// Source is the source tree it was built from (git builds), whose
	// registry/ folder matches the compiler.
	Source string `json:"source,omitempty"`
	Auto   bool   `json:"auto,omitempty"` // found, not added: refreshed on start
}

// Registry is the registry folder that came with a build, if any.
func (b *Build) Registry() string {
	if b.Source == "" {
		return ""
	}
	return "file://" + filepath.Join(b.Source, "registry")
}

// CanRun reports whether the build can assemble and run programs: pkc.wasm
// runs sandboxed, without nasm or ld, so it only checks and prints asm.
func (b *Build) CanRun() bool { return b.Kind != "wasm" }

// Command is how to run the build with args.
func (b *Build) Command(args ...string) []string {
	if b.Kind == "wasm" {
		return append([]string{"wasmtime", "run", "--dir=/", "--dir=.", b.Path}, args...)
	}
	return append([]string{b.Path}, args...)
}

// A Profile is how a workspace builds: with which compiler, for which
// target, from which registry.
type Profile struct {
	Build  string `json:"build"`
	Target string `json:"target"`
	// Registry is "" (the default: the program's lock file or package tree,
	// else PACKENGER_REGISTRY, else the universal tree), "repo" (the
	// registry folder of the Packenger checkout), "build" (the one the
	// build came with) or a URL.
	Registry string `json:"registry"`
}

// Config is pkide's saved state.
type Config struct {
	// Repo is a Packenger source checkout: for git builds, tools/pkfmt.pkg
	// and the "repo" registry.
	Repo     string              `json:"repo"`
	Builds   []*Build            `json:"builds"`
	Profiles map[string]*Profile `json:"profiles"` // workspace path -> profile
	Recent   []string            `json:"recent"`   // workspaces, latest first
	Theme    string              `json:"theme"`    // "mocha", "latte" or "custom:<name>"
	Schemes  []*Scheme           `json:"schemes"`  // the user's colour schemes

	mu   sync.Mutex
	path string
}

// Scheme is a colour scheme made in the Colors sidebar: a built-in theme
// (Base) with some of its colours replaced. Colors maps a colour's name
// (the CSS variable without "--", e.g. "mauve") to #rrggbb or #rrggbbaa.
type Scheme struct {
	Name   string            `json:"name"`
	Base   string            `json:"base"`
	Colors map[string]string `json:"colors"`
}

var hexColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}([0-9a-fA-F]{2})?$`)
var colorName = regexp.MustCompile(`^[a-z][a-z0-9]{0,23}$`)

// CheckSchemes reports the first thing wrong with schemes: a missing or
// repeated name, an unknown base, or a colour that isn't a hex colour.
func CheckSchemes(schemes []*Scheme) error {
	seen := map[string]bool{}
	for _, sc := range schemes {
		if sc == nil {
			return fmt.Errorf("a scheme is empty")
		}
		sc.Name = strings.TrimSpace(sc.Name)
		if sc.Name == "" || len(sc.Name) > 60 {
			return fmt.Errorf("a scheme needs a name of at most 60 characters")
		}
		if seen[sc.Name] {
			return fmt.Errorf("two schemes are named %q", sc.Name)
		}
		seen[sc.Name] = true
		if sc.Base != "mocha" && sc.Base != "latte" {
			return fmt.Errorf("scheme %q: the base is mocha or latte, not %q", sc.Name, sc.Base)
		}
		if len(sc.Colors) > 64 {
			return fmt.Errorf("scheme %q has too many colours", sc.Name)
		}
		for k, v := range sc.Colors {
			if !colorName.MatchString(k) || !hexColor.MatchString(v) {
				return fmt.Errorf("scheme %q: %s: %q is not a colour like #cba6f7", sc.Name, k, v)
			}
		}
	}
	return nil
}

// ConfigDir is where pkide keeps its config; DataDir its builds.
func ConfigDir() string {
	d, err := os.UserConfigDir()
	if err != nil {
		d = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(d, "pkide")
}

func DataDir() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "pkide")
	}
	return filepath.Join(os.Getenv("HOME"), ".local", "share", "pkide")
}

// LoadConfig reads the config (or starts one), and finds the compilers
// installed in the repo and on PATH.
func LoadConfig(path, repo string) (*Config, error) {
	c := &Config{Profiles: map[string]*Profile{}, path: path}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, c); err != nil {
			return nil, fmt.Errorf("%s: %v", path, err)
		}
		if c.Profiles == nil {
			c.Profiles = map[string]*Profile{}
		}
	}
	if repo != "" {
		c.Repo = repo
	}
	if c.Repo == "" {
		c.Repo = findRepo()
	}
	c.Detect()
	return c, c.Save()
}

// findRepo looks for a Packenger checkout around the working directory and
// the executable.
func findRepo() string {
	var starts []string
	if wd, err := os.Getwd(); err == nil {
		starts = append(starts, wd)
	}
	if exe, err := os.Executable(); err == nil {
		starts = append(starts, filepath.Dir(exe))
	}
	for _, d := range starts {
		for ; ; d = filepath.Dir(d) {
			if _, err := os.Stat(filepath.Join(d, "cmd", "packenger", "main.go")); err == nil {
				return d
			}
			if d == filepath.Dir(d) {
				break
			}
		}
	}
	return ""
}

func (c *Config) Save() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.saveLocked()
}

func (c *Config) saveLocked() error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}

func buildID(path string) string {
	h := sha256.Sum256([]byte(path))
	return hex.EncodeToString(h[:6])
}

// kindOf guesses a compiler's kind from its file name.
func kindOf(path string) string {
	base := filepath.Base(path)
	switch {
	case strings.HasSuffix(base, ".wasm"):
		return "wasm"
	case base == "pkc-c" || strings.HasSuffix(base, "-c") && strings.Contains(base, "pkc"):
		return "pkc-c"
	case strings.Contains(base, "pkc"):
		return "pkc"
	case strings.Contains(base, "packenger"):
		return "go"
	}
	return "other"
}

var kindNames = map[string]string{"go": "Go compiler", "pkc": "pkc", "pkc-c": "pkc (C build)", "wasm": "pkc.wasm", "other": "compiler"}

// Detect refreshes the builds found automatically: the checkout's bin/
// folder, and packenger on PATH. Builds that no longer exist are dropped.
func (c *Config) Detect() {
	c.mu.Lock()
	defer c.mu.Unlock()
	var keep []*Build
	for _, b := range c.Builds {
		if _, err := os.Stat(b.Path); err == nil && !b.Auto {
			keep = append(keep, b)
		}
	}
	have := map[string]bool{}
	for _, b := range keep {
		have[b.Path] = true
	}
	add := func(path, where string) {
		abs, err := filepath.Abs(path)
		if err != nil || have[abs] {
			return
		}
		if st, err := os.Stat(abs); err != nil || st.IsDir() {
			return
		}
		have[abs] = true
		k := kindOf(abs)
		keep = append(keep, &Build{ID: buildID(abs), Name: kindNames[k] + " · " + where, Kind: k, Path: abs, Auto: true})
	}
	if c.Repo != "" {
		for _, n := range []string{"packenger", "pkc", "pkc-c", "pkc.wasm"} {
			add(filepath.Join(c.Repo, "bin", n), "repo bin/")
		}
	}
	if p, err := exec.LookPath("packenger"); err == nil {
		add(p, "PATH")
	}
	sort.SliceStable(keep, func(i, j int) bool { return keep[i].Auto && !keep[j].Auto })
	c.Builds = keep
}

// FindBuild returns the build with id, or nil.
func (c *Config) FindBuild(id string) *Build {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, b := range c.Builds {
		if b.ID == id {
			return b
		}
	}
	return nil
}

// AddBuild registers a compiler on disk.
func (c *Config) AddBuild(path, name string) (*Build, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if st, err := os.Stat(abs); err != nil || st.IsDir() {
		return nil, fmt.Errorf("%s: no such file", abs)
	}
	k := kindOf(abs)
	if name == "" {
		name = kindNames[k] + " · " + filepath.Base(filepath.Dir(abs)) + "/" + filepath.Base(abs)
	}
	b := &Build{ID: buildID(abs), Name: name, Kind: k, Path: abs}
	c.mu.Lock()
	for _, o := range c.Builds {
		if o.ID == b.ID {
			c.mu.Unlock()
			return nil, fmt.Errorf("%s is already a build", abs)
		}
	}
	c.Builds = append(c.Builds, b)
	c.mu.Unlock()
	return b, c.Save()
}

// RemoveBuild forgets a build; for a git build it also deletes its files.
func (c *Config) RemoveBuild(id string) error {
	c.mu.Lock()
	var keep []*Build
	var gone *Build
	for _, b := range c.Builds {
		if b.ID == id {
			gone = b
		} else {
			keep = append(keep, b)
		}
	}
	c.Builds = keep
	c.mu.Unlock()
	if gone == nil {
		return fmt.Errorf("no build %s", id)
	}
	if gone.Source != "" && strings.HasPrefix(gone.Source, DataDir()) {
		// the other builds from the same commit share the folder
		shared := false
		for _, b := range keep {
			shared = shared || b.Source == gone.Source
		}
		if !shared {
			os.RemoveAll(filepath.Dir(gone.Source))
		}
	}
	return c.Save()
}

// ProfileFor returns the workspace's profile, made from the first build if
// it has none.
func (c *Config) ProfileFor(ws string) *Profile {
	c.mu.Lock()
	defer c.mu.Unlock()
	p := c.Profiles[ws]
	if p == nil {
		p = &Profile{Target: "bintables"}
		c.Profiles[ws] = p
	}
	ok := false
	for _, b := range c.Builds {
		ok = ok || b.ID == p.Build
	}
	if !ok {
		p.Build = ""
		for _, b := range c.Builds {
			if b.Kind != "wasm" {
				p.Build = b.ID
				break
			}
		}
	}
	return p
}

// SetProfile saves a workspace's profile.
func (c *Config) SetProfile(ws string, p *Profile) error {
	c.mu.Lock()
	c.Profiles[ws] = p
	c.mu.Unlock()
	return c.Save()
}

// Touch records a workspace as the most recently opened.
func (c *Config) Touch(ws string) {
	c.mu.Lock()
	recent := []string{ws}
	for _, r := range c.Recent {
		if r != ws && len(recent) < 12 {
			recent = append(recent, r)
		}
	}
	c.Recent = recent
	c.saveLocked()
	c.mu.Unlock()
}

// RegistryURL resolves a profile's registry setting to a URL ("" for the
// default).
func (c *Config) RegistryURL(p *Profile) string {
	switch p.Registry {
	case "":
		return ""
	case "repo":
		if c.Repo == "" {
			return ""
		}
		return "file://" + filepath.Join(c.Repo, "registry")
	case "build":
		if b := c.FindBuild(p.Build); b != nil {
			return b.Registry()
		}
		return ""
	}
	return p.Registry
}

// ---------------------------------------------------------------- git builds

// BuildFromGit builds the compiler (and, if the commit has it, pkc) at rev
// of the repo, writing progress to log, and registers the builds.
func (c *Config) BuildFromGit(rev string, withPkc bool, log io.Writer) error {
	repo := c.Repo
	if repo == "" {
		return errors.New("set the Packenger repository in the settings first")
	}
	run := func(dir string, env []string, name string, args ...string) (string, error) {
		fmt.Fprintf(log, "$ %s %s\n", name, strings.Join(args, " "))
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), env...)
		var out strings.Builder
		cmd.Stdout = io.MultiWriter(&out, log)
		cmd.Stderr = log
		err := cmd.Run()
		return strings.TrimSpace(out.String()), err
	}
	sha, err := run(repo, nil, "git", "rev-parse", "--verify", rev+"^{commit}")
	if err != nil {
		return fmt.Errorf("%s is not a commit, branch or tag of %s", rev, repo)
	}
	subject, _ := run(repo, nil, "git", "log", "-1", "--format=%s", sha)
	short := sha[:10]
	dest := filepath.Join(DataDir(), "builds", short)
	src := filepath.Join(dest, "src")
	os.RemoveAll(dest)
	if err := os.MkdirAll(src, 0o755); err != nil {
		return err
	}
	fmt.Fprintf(log, "$ git archive %s → %s\n", short, src)
	arch := exec.Command("git", "archive", "--format=tar", sha)
	arch.Dir = repo
	pipe, err := arch.StdoutPipe()
	if err != nil {
		return err
	}
	arch.Stderr = log
	if err := arch.Start(); err != nil {
		return err
	}
	if err := untar(pipe, src); err != nil {
		arch.Wait()
		return err
	}
	if err := arch.Wait(); err != nil {
		return err
	}
	compiler := filepath.Join(dest, "packenger")
	if _, err := run(src, nil, "go", "build", "-o", compiler, "./cmd/packenger"); err != nil {
		return fmt.Errorf("building the Go compiler at %s failed", short)
	}
	added := []*Build{{ID: buildID(compiler), Name: "Go compiler @ " + short[:7], Kind: "go", Path: compiler, Version: short, Subject: subject, Source: src}}
	if _, err := os.Stat(filepath.Join(src, "bootstrap", "pkc.pkg")); withPkc && err == nil {
		pkc := filepath.Join(dest, "pkc")
		env := []string{"PACKENGER_REGISTRY=file://" + filepath.Join(src, "registry")}
		if _, err := run(src, env, compiler, "build", "bootstrap/pkc.pkg", "-o", pkc); err != nil {
			fmt.Fprintf(log, "pkc didn't build at %s; keeping the Go compiler only\n", short)
		} else {
			added = append(added, &Build{ID: buildID(pkc), Name: "pkc @ " + short[:7], Kind: "pkc", Path: pkc, Version: short, Subject: subject, Source: src})
		}
	}
	c.mu.Lock()
	var keep []*Build
	for _, b := range c.Builds {
		dup := false
		for _, a := range added {
			dup = dup || a.ID == b.ID
		}
		if !dup {
			keep = append(keep, b)
		}
	}
	c.Builds = append(keep, added...)
	c.mu.Unlock()
	for _, a := range added {
		fmt.Fprintf(log, "added %s (%s)\n", a.Name, a.Path)
	}
	return c.Save()
}

// untar extracts a tar stream into dir.
func untar(r io.Reader, dir string) error {
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		p := filepath.Join(dir, filepath.FromSlash(h.Name))
		if !strings.HasPrefix(p, filepath.Clean(dir)+string(os.PathSeparator)) {
			continue
		}
		switch h.Typeflag {
		case tar.TypeDir:
			os.MkdirAll(p, 0o755)
		case tar.TypeReg:
			os.MkdirAll(filepath.Dir(p), 0o755)
			f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(h.Mode)&0o777)
			if err != nil {
				return err
			}
			_, err = io.Copy(f, tr)
			f.Close()
			if err != nil {
				return err
			}
		case tar.TypeSymlink:
			os.Symlink(h.Linkname, p)
		}
	}
}

// ---------------------------------------------------------------- processes

// A Proc is a command run for the user (a build, a run, a git build), whose
// output streams to the console.
type Proc struct {
	ID      string    `json:"id"`
	Title   string    `json:"title"`
	Started time.Time `json:"started"`

	mu      sync.Mutex
	out     []byte
	done    bool
	code    int
	changed chan struct{}
	stdin   io.WriteCloser
	cmd     *exec.Cmd
}

// Procs keeps the processes of this session.
type Procs struct {
	mu    sync.Mutex
	n     int
	procs map[string]*Proc
}

func NewProcs() *Procs { return &Procs{procs: map[string]*Proc{}} }

func (ps *Procs) newProc(title string) *Proc {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.n++
	p := &Proc{ID: fmt.Sprint(ps.n), Title: title, Started: time.Now(), changed: make(chan struct{})}
	ps.procs[p.ID] = p
	// keep the last 30
	if len(ps.procs) > 30 {
		oldest := ""
		for id, q := range ps.procs {
			if q.done && (oldest == "" || q.Started.Before(ps.procs[oldest].Started)) {
				oldest = id
			}
		}
		delete(ps.procs, oldest)
	}
	return p
}

func (ps *Procs) Get(id string) *Proc {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.procs[id]
}

// Write appends output, up to 4 MB.
func (p *Proc) Write(b []byte) (int, error) {
	p.mu.Lock()
	if len(p.out) < 4<<20 {
		p.out = append(p.out, b...)
	}
	close(p.changed)
	p.changed = make(chan struct{})
	p.mu.Unlock()
	return len(b), nil
}

func (p *Proc) finish(code int) {
	p.mu.Lock()
	p.done, p.code = true, code
	close(p.changed)
	p.changed = make(chan struct{})
	p.mu.Unlock()
}

// Read returns the output from offset, whether the process is done, its
// exit code, and a channel closed on the next change.
func (p *Proc) Read(offset int) ([]byte, bool, int, <-chan struct{}) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var b []byte
	if offset < len(p.out) {
		b = append([]byte(nil), p.out[offset:]...)
	}
	return b, p.done, p.code, p.changed
}

// Kill stops the process.
func (p *Proc) Kill() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cmd != nil && p.cmd.Process != nil && !p.done {
		p.cmd.Process.Kill()
	}
}

// Input sends text to the process's standard input.
func (p *Proc) Input(s string) error {
	p.mu.Lock()
	in := p.stdin
	p.mu.Unlock()
	if in == nil {
		return errors.New("the process has finished")
	}
	_, err := io.WriteString(in, s)
	return err
}

// Start runs argv in dir.
func (ps *Procs) Start(title, dir string, env []string, argv []string) (*Proc, error) {
	p := ps.newProc(title)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout, cmd.Stderr = p, p
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(p, "$ %s\n", strings.Join(argv, " "))
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(p, "%v\n", err)
		p.finish(-1)
		return p, nil
	}
	p.mu.Lock()
	p.cmd, p.stdin = cmd, in
	p.mu.Unlock()
	go func() {
		err := cmd.Wait()
		code := 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else if err != nil {
			fmt.Fprintf(p, "%v\n", err)
			code = -1
		}
		p.mu.Lock()
		p.stdin = nil
		p.mu.Unlock()
		in.Close()
		p.finish(code)
	}()
	return p, nil
}

// Go runs f as a process: its output is what it writes.
func (ps *Procs) Go(title string, f func(w io.Writer) error) *Proc {
	p := ps.newProc(title)
	go func() {
		if err := f(p); err != nil {
			fmt.Fprintf(p, "error: %v\n", err)
			p.finish(1)
			return
		}
		p.finish(0)
	}()
	return p
}

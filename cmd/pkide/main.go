// Command pkide is the Packenger IDE: a local web app with grammar-aware
// completion, diagnostics and a manager for Packenger builds. See
// docs/ide.md.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"image/png"

	"github.com/DominicThemiya/Packenger/internal/ide"
)

const usage = `pkide: the Packenger IDE, a local web app.

Usage:
  pkide [folder]             start the IDE (if it isn't running) and open it
  pkide serve                run the server only (for a login service)
  pkide install [--service]  install pkide for this user: the binary in
                             ~/.local/bin, a launcher in the app menu, and with
                             --service, a systemd user service that starts it
                             at login
  pkide uninstall            remove what install added

Flags:
  --port N        port on 127.0.0.1 (default 6470)
  --repo DIR      the Packenger source checkout (found automatically when
                  pkide runs from inside it)
  --no-browser    don't open a window

The app can also be installed from the browser: open it in Chrome, Edge or
Brave and choose "Install pkide" in the address bar.
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "pkide:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("pkide", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	port := fs.Int("port", 6470, "")
	repo := fs.String("repo", "", "")
	noBrowser := fs.Bool("no-browser", false, "")
	service := fs.Bool("service", false, "")
	cmd := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "serve", "install", "uninstall", "open", "help":
			cmd, args = args[0], args[1:]
		}
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	switch cmd {
	case "help":
		fmt.Print(usage)
		return nil
	case "serve":
		return serve(*port, *repo)
	case "install":
		return install(*port, *service)
	case "uninstall":
		return uninstall()
	}
	ws := ""
	if fs.NArg() > 0 {
		abs, err := filepath.Abs(fs.Arg(0))
		if err != nil {
			return err
		}
		if st, err := os.Stat(abs); err != nil || !st.IsDir() {
			return fmt.Errorf("%s is not a folder", fs.Arg(0))
		}
		ws = abs
	}
	u := fmt.Sprintf("http://127.0.0.1:%d/", *port)
	if ws != "" {
		u += "#ws=" + url.QueryEscape(ws)
	}
	if !replaceOld(*port) {
		if err := startDetached(*port, *repo); err != nil {
			return err
		}
	}
	fmt.Println("pkide is running at", u)
	if !*noBrowser {
		openWindow(u)
	}
	return nil
}

type ping struct {
	App     string `json:"app"`
	Version string `json:"version"`
	Pid     int    `json:"pid"`
}

// pingServer asks what answers on port; nil if it isn't pkide.
func pingServer(port int) *ping {
	c := http.Client{Timeout: 700 * time.Millisecond}
	resp, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d/api/ping", port))
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var p ping
	if json.NewDecoder(resp.Body).Decode(&p) != nil || p.App != "pkide" {
		return nil
	}
	return &p
}

// running reports whether pkide already answers on port.
func running(port int) bool { return pingServer(port) != nil }

// replaceOld stops a pkide server on port that is another build than this
// one (one started before pkide was rebuilt or reinstalled), so that the
// window opens on this version. It reports whether one is still running.
func replaceOld(port int) bool {
	p := pingServer(port)
	if p == nil {
		return false
	}
	if p.Version == ide.Version() || p.Pid <= 0 {
		return true
	}
	proc, err := os.FindProcess(p.Pid)
	if err != nil || proc.Signal(syscall.SIGTERM) != nil {
		fmt.Printf("an older pkide (pid %d) is running on port %d; stop it to use this version\n", p.Pid, port)
		return true
	}
	fmt.Printf("stopped an older pkide (pid %d)\n", p.Pid)
	for i := 0; i < 30 && pingServer(port) != nil; i++ {
		time.Sleep(100 * time.Millisecond)
	}
	return pingServer(port) != nil
}

func serve(port int, repo string) error {
	cfg, err := ide.LoadConfig(filepath.Join(ide.ConfigDir(), "config.json"), repo)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		if running(port) {
			return fmt.Errorf("pkide is already running on port %d", port)
		}
		return err
	}
	s := ide.NewServer(cfg, port)
	fmt.Printf("pkide listening on http://127.0.0.1:%d/ (repo %q)\n", port, cfg.Repo)
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	return srv.Serve(ln)
}

// startDetached starts "pkide serve" in the background, logging to the
// data folder, and waits until it answers.
func startDetached(port int, repo string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	os.MkdirAll(ide.DataDir(), 0o755)
	logf, err := os.OpenFile(filepath.Join(ide.DataDir(), "pkide.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	args := []string{"serve", "--port", strconv.Itoa(port)}
	if repo != "" {
		args = append(args, "--repo", repo)
	}
	c := exec.Command(exe, args...)
	c.Stdout, c.Stderr = logf, logf
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := c.Start(); err != nil {
		return err
	}
	c.Process.Release()
	for i := 0; i < 50; i++ {
		if running(port) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("the server didn't start; see %s", logf.Name())
}

// openWindow opens u as an app window in a Chromium-based browser, or in
// the default browser.
func openWindow(u string) {
	for _, b := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "brave-browser", "brave", "microsoft-edge"} {
		if p, err := exec.LookPath(b); err == nil {
			c := exec.Command(p, "--app="+u)
			c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
			if c.Start() == nil {
				c.Process.Release()
				return
			}
		}
	}
	if p, err := exec.LookPath("xdg-open"); err == nil {
		exec.Command(p, u).Start()
		return
	}
	fmt.Println("open", u, "in your browser")
}

func home() string {
	h, _ := os.UserHomeDir()
	return h
}

func paths() (bin, desktop, icon, unit string) {
	h := home()
	return filepath.Join(h, ".local", "bin", "pkide"),
		filepath.Join(h, ".local", "share", "applications", "pkide.desktop"),
		filepath.Join(h, ".local", "share", "icons", "hicolor", "512x512", "apps", "pkide.png"),
		filepath.Join(h, ".config", "systemd", "user", "pkide.service")
}

func install(port int, service bool) error {
	bin, desktop, icon, unit := paths()
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return err
	}
	if exe != bin {
		data, err := os.ReadFile(exe)
		if err != nil {
			return err
		}
		os.MkdirAll(filepath.Dir(bin), 0o755)
		tmp := bin + ".new"
		if err := os.WriteFile(tmp, data, 0o755); err != nil {
			return err
		}
		if err := os.Rename(tmp, bin); err != nil {
			return err
		}
		fmt.Println("installed", bin)
	}
	// remember the repository, so the installed binary finds it
	repoFlag := ""
	if cfg, err := ide.LoadConfig(filepath.Join(ide.ConfigDir(), "config.json"), ""); err == nil && cfg.Repo != "" {
		repoFlag = " --repo " + cfg.Repo
	}
	os.MkdirAll(filepath.Dir(icon), 0o755)
	f, err := os.Create(icon)
	if err != nil {
		return err
	}
	if err := pngEncode(f, 512); err != nil {
		return err
	}
	os.MkdirAll(filepath.Dir(desktop), 0o755)
	entry := fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=pkide
GenericName=Packenger IDE
Comment=Write, build and run Packenger programs and packages
Exec=%s --port %d%s
Icon=pkide
Terminal=false
Categories=Development;IDE;
StartupWMClass=127.0.0.1__
`, bin, port, repoFlag)
	if err := os.WriteFile(desktop, []byte(entry), 0o644); err != nil {
		return err
	}
	fmt.Println("installed", desktop)
	if service {
		os.MkdirAll(filepath.Dir(unit), 0o755)
		u := fmt.Sprintf(`[Unit]
Description=pkide, the Packenger IDE

[Service]
ExecStart=%s serve --port %d%s
Restart=on-failure

[Install]
WantedBy=default.target
`, bin, port, repoFlag)
		if err := os.WriteFile(unit, []byte(u), 0o644); err != nil {
			return err
		}
		if out, err := exec.Command("systemctl", "--user", "daemon-reload").CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl: %s", out)
		}
		if out, err := exec.Command("systemctl", "--user", "enable", "--now", "pkide.service").CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl: %s", out)
		}
		fmt.Println("installed and started", unit)
	}
	fmt.Println("pkide is in your app menu. Make sure ~/.local/bin is on your PATH to run it from a terminal.")
	return nil
}

func pngEncode(f *os.File, n int) error {
	defer f.Close()
	return encodeIcon(f, n)
}

func uninstall() error {
	bin, desktop, icon, unit := paths()
	if _, err := os.Stat(unit); err == nil {
		exec.Command("systemctl", "--user", "disable", "--now", "pkide.service").Run()
	}
	var errs []error
	for _, p := range []string{unit, desktop, icon, bin} {
		if err := os.Remove(p); err == nil {
			fmt.Println("removed", p)
		} else if !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	fmt.Println("your settings and builds stay in", ide.ConfigDir(), "and", ide.DataDir())
	return errors.Join(errs...)
}

func encodeIcon(f *os.File, n int) error { return png.Encode(f, ide.Icon(n)) }

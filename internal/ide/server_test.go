package ide

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func server(t *testing.T) (*Server, http.Handler, string) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.pkg"), []byte("use std\nprint 1\n"), 0o644)
	cfg := &Config{Profiles: map[string]*Profile{}, path: filepath.Join(t.TempDir(), "config.json")}
	s := NewServer(cfg, 6470)
	return s, s.Handler(), dir
}

func call(h http.Handler, host, origin, token, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	r.Host = host
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if token != "" {
		r.Header.Set("X-Pkide-Token", token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestServerGuards(t *testing.T) {
	s, h, dir := server(t)
	read := `{"ws":"` + dir + `","path":"a.pkg"}`
	if w := call(h, "127.0.0.1:6470", "", s.token, "/api/fs/read", read); w.Code != 200 || !strings.Contains(w.Body.String(), "print 1") {
		t.Fatalf("read: %d %s", w.Code, w.Body)
	}
	// another host name (DNS rebinding), another site, no token
	if w := call(h, "evil.example:6470", "", s.token, "/api/fs/read", read); w.Code != 403 {
		t.Errorf("foreign host: %d", w.Code)
	}
	if w := call(h, "127.0.0.1:6470", "http://evil.example", s.token, "/api/fs/read", read); w.Code != 403 {
		t.Errorf("foreign origin: %d", w.Code)
	}
	if w := call(h, "127.0.0.1:6470", "", "", "/api/fs/read", read); w.Code != 403 {
		t.Errorf("no token: %d", w.Code)
	}
	// nothing outside the workspace
	for _, p := range []string{"../a.pkg", "/etc/passwd", "x/../../etc/passwd"} {
		w := call(h, "127.0.0.1:6470", "", s.token, "/api/fs/read", `{"ws":"`+dir+`","path":"`+p+`"}`)
		if w.Code != 400 || !strings.Contains(w.Body.String(), "outside the workspace") {
			t.Errorf("%s: %d %s", p, w.Code, w.Body)
		}
	}
	if w := call(h, "127.0.0.1:6470", "", s.token, "/api/fs/delete", `{"ws":"`+dir+`","path":"."}`); w.Code != 400 {
		t.Errorf("deleting the workspace: %d", w.Code)
	}
	// the page carries the token
	r := httptest.NewRequest("GET", "/", nil)
	r.Host = "localhost:6470"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if !strings.Contains(w.Body.String(), s.token) {
		t.Errorf("the page has no token")
	}
}

func TestSchemes(t *testing.T) {
	s, h, _ := server(t)
	post := func(body string) *httptest.ResponseRecorder {
		return call(h, "127.0.0.1:6470", "", s.token, "/api/settings", body)
	}
	good := `{"schemes":[{"name":"Dusk","base":"mocha","colors":{"base":"#101018","sel":"#9399b247"}}],"theme":"custom:Dusk"}`
	if w := post(good); w.Code != 200 || !strings.Contains(w.Body.String(), `"#101018"`) {
		t.Fatalf("saving a scheme: %d %s", w.Code, w.Body)
	}
	if b, _ := os.ReadFile(s.Config.path); !strings.Contains(string(b), "Dusk") {
		t.Errorf("the scheme isn't in the config file: %s", b)
	}
	for _, bad := range []string{
		`{"schemes":[{"name":"x","base":"mocha","colors":{"base":"red"}}]}`,
		`{"schemes":[{"name":"x","base":"mocha","colors":{"base":"#12121b;}body{"}}]}`,
		`{"schemes":[{"name":"x","base":"mocha","colors":{"a b":"#121212"}}]}`,
		`{"schemes":[{"name":"x","base":"frappe","colors":{}}]}`,
		`{"schemes":[{"name":"x","base":"mocha"},{"name":"x","base":"latte"}]}`,
		`{"schemes":[{"name":" ","base":"mocha"}]}`,
	} {
		if w := post(bad); w.Code != 400 {
			t.Errorf("%s: %d %s", bad, w.Code, w.Body)
		}
	}
	if len(s.Config.Schemes) != 1 {
		t.Errorf("a bad request changed the schemes: %d", len(s.Config.Schemes))
	}
}

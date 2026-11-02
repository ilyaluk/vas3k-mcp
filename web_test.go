package main

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSameOrigin(t *testing.T) {
	for _, tt := range []struct {
		name, origin, fetch, forwarded string
		tls, want                      bool
	}{
		{name: "missing"},
		{name: "matching", origin: "http://club.test", want: true},
		{name: "browser form", origin: "http://club.test", fetch: "same-origin", want: true},
		{name: "same origin fetch only", fetch: "same-origin", want: true},
		{name: "none fetch only", fetch: "none"},
		{name: "opaque", origin: "null"},
		{name: "opaque same origin fetch", origin: "null", fetch: "same-origin"},
		{name: "cross site", origin: "http://evil.test"},
		{name: "sibling", origin: "http://sibling.club.test"},
		{name: "same site fetch", origin: "http://club.test", fetch: "same-site"},
		{name: "cross site fetch", origin: "http://club.test", fetch: "cross-site"},
		{name: "unknown fetch", origin: "http://club.test", fetch: "garbage"},
		{name: "wrong scheme", origin: "https://club.test"},
		{name: "wrong port", origin: "http://club.test:80"},
		{name: "path", origin: "http://club.test/"},
		{name: "userinfo", origin: "http://evil@club.test"},
		{name: "query", origin: "http://club.test?x=1"},
		{name: "empty query", origin: "http://club.test?"},
		{name: "fragment", origin: "http://club.test#x"},
		{name: "empty fragment", origin: "http://club.test#"},
		{name: "bad port", origin: "http://club.test:bad"},
		{name: "bad url", origin: "://"},
		{name: "multiple origins", origin: "http://club.test http://evil.test"},
		{name: "tls", origin: "https://club.test", tls: true, want: true},
		{name: "forwarded", origin: "https://club.test", forwarded: "https", want: true},
		{name: "forwarded list not trusted", origin: "https://club.test", forwarded: "http, https"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "http://club.test/", nil)
			if tt.origin != "" {
				r.Header.Set("Origin", tt.origin)
			}
			r.Header.Set("Sec-Fetch-Site", tt.fetch)
			r.Header.Set("X-Forwarded-Proto", tt.forwarded)
			if tt.tls {
				r.TLS = &tls.ConnectionState{}
			}
			if got := sameOrigin(r); got != tt.want {
				t.Errorf("sameOrigin = %v, want %v", got, tt.want)
			}
		})
	}
	for _, header := range []string{"Origin", "Sec-Fetch-Site"} {
		r := httptest.NewRequest(http.MethodPost, "http://club.test/", nil)
		r.Header.Set("Origin", "http://club.test")
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		r.Header.Add(header, "same-origin")
		if sameOrigin(r) {
			t.Fatalf("accepted duplicate %s", header)
		}
	}
}

func testWebStore(t *testing.T) *store {
	t.Helper()
	s, err := openStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func webFormRequest(values url.Values) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "http://club.test/", strings.NewReader(values.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "http://club.test")
	return r
}

func TestWebSetupAndSecurityHeaders(t *testing.T) {
	s := testWebStore(t)
	h := newWebHandler(s, func(context.Context, string) (User, error) {
		t.Fatal("unconfigured GET called upstream")
		return User{}, nil
	}, func() (string, time.Time) { return "not checked since startup", time.Time{} })
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://club.test/", nil))
	if w.Code != http.StatusOK {
		t.Fatal(w.Code)
	}
	for _, text := range []string{
		"https://vas3k.club/apps/create/", "redirect", `type="password"`,
		"Only the name and description are required",
		"Leave the website and redirect URI blank",
		"Not configured", "http://club.test/mcp", "Not checked", version,
	} {
		if !strings.Contains(w.Body.String(), text) {
			t.Errorf("setup page missing %q", text)
		}
	}
	for key, value := range map[string]string{
		"Cache-Control": "no-store", "X-Content-Type-Options": "nosniff", "Referrer-Policy": "same-origin",
	} {
		if got := w.Header().Get(key); got != value {
			t.Errorf("%s = %q", key, got)
		}
	}
	for _, directive := range []string{"default-src 'none'", "frame-ancestors 'none'", "form-action 'self'", "base-uri 'none'"} {
		if !strings.Contains(w.Header().Get("Content-Security-Policy"), directive) {
			t.Error("missing CSP directive:", directive)
		}
	}
	files, err := os.ReadDir(s.dir)
	if err != nil || len(files) != 0 {
		t.Fatalf("GET mutated disk: %v %v", files, err)
	}
}

func TestWebSaveVerifyBeforeReplaceAndForget(t *testing.T) {
	s := testWebStore(t)
	calls := 0
	h := newWebHandler(s, func(_ context.Context, token string) (User, error) {
		calls++
		if s.Token() == token {
			t.Error("token saved before verification")
		}
		if token == "st_rejected" {
			return User{}, errors.New("upstream secret st_rejected")
		}
		return User{Slug: "tester", FullName: "Test User"}, nil
	}, nil)
	for _, token := range []string{"st_first", "st_second", "st_rejected"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, webFormRequest(url.Values{"action": {"save"}, "token": {token}}))
		if strings.Contains(w.Body.String(), token) {
			t.Fatal("response leaked token")
		}
		if token == "st_rejected" {
			if w.Code != http.StatusBadRequest || s.Token() != "st_second" {
				t.Fatal("failed verification changed saved credential")
			}
			reopened, err := openStore(s.dir)
			if err != nil || reopened.Token() != "st_second" {
				t.Fatal("failed verification changed disk")
			}
		} else if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" || s.Token() != token {
			t.Fatalf("save failed: code %d", w.Code)
		}
	}
	if calls != 3 {
		t.Fatalf("verification calls = %d", calls)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, webFormRequest(url.Values{"action": {"forget"}}))
	if w.Code != http.StatusSeeOther || s.Token() != "" || calls != 3 {
		t.Fatal("forget failed or called upstream")
	}
}

func TestWebConfiguredEscapesAndDoesNotWrite(t *testing.T) {
	s := testWebStore(t)
	if err := s.Replace("st_saved_secret"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.dir, tokenFilename)
	before, _ := os.Stat(path)
	calls := 0
	h := newWebHandler(s, func(_ context.Context, token string) (User, error) {
		calls++
		if token != "st_saved_secret" {
			t.Fatal("GET passed wrong stored token")
		}
		return User{Slug: `<script>alert("slug")</script>`, FullName: "<b>Name</b>"}, nil
	}, func() (string, time.Time) { return "OK", time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) })
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "https://club.test/", nil)
	h.ServeHTTP(w, r)
	after, _ := os.Stat(path)
	if calls != 1 || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("GET did not check once without disk mutation")
	}
	body := w.Body.String()
	if strings.Contains(body, "st_saved_secret") || strings.Contains(body, "<script>") || strings.Contains(body, "<b>Name</b>") {
		t.Fatal("page leaked credential or rendered upstream markup")
	}
	for _, text := range []string{"&lt;script&gt;", "&lt;b&gt;Name&lt;/b&gt;", "2026-01-02T03:04:05Z", "OK", "https://club.test/mcp"} {
		if !strings.Contains(body, text) {
			t.Errorf("missing %q", text)
		}
	}
}

func TestWebUpstreamFailureIsGeneric(t *testing.T) {
	s := testWebStore(t)
	if err := s.Replace("st_secret"); err != nil {
		t.Fatal(err)
	}
	h := newWebHandler(s, func(context.Context, string) (User, error) {
		return User{}, errors.New("response contains st_secret")
	}, func() (string, time.Time) { return "error: response contains st_secret", time.Now() })
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://club.test/", nil))
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "st_secret") ||
		!strings.Contains(w.Body.String(), "Identity could not be verified") || !strings.Contains(w.Body.String(), "Error") {
		t.Fatal("upstream error not safely displayed")
	}
}

func TestWebRejectsUnsafePostsBeforeCallback(t *testing.T) {
	s := testWebStore(t)
	if err := s.Replace("st_original"); err != nil {
		t.Fatal(err)
	}
	h := newWebHandler(s, func(context.Context, string) (User, error) {
		t.Error("invalid request called upstream")
		return User{}, nil
	}, nil)
	for _, tt := range []struct {
		name   string
		mutate func(*http.Request)
		values url.Values
		code   int
	}{
		{name: "missing origin", mutate: func(r *http.Request) { r.Header.Del("Origin") }, code: 403},
		{name: "cross origin", mutate: func(r *http.Request) { r.Header.Set("Origin", "https://evil.test") }, code: 403},
		{name: "same site", mutate: func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-site") }, code: 403},
		{name: "bad content type", mutate: func(r *http.Request) { r.Header.Set("Content-Type", "application/json") }, code: 415},
		{name: "query token", mutate: func(r *http.Request) { r.URL.RawQuery = "token=st_query" }, code: 400},
		{name: "invalid token", values: url.Values{"action": {"save"}, "token": {"invalid"}}, code: 400},
		{name: "duplicate token", values: url.Values{"action": {"save"}, "token": {"st_a", "st_b"}}, code: 400},
		{name: "unknown action", values: url.Values{"action": {"surprise"}}, code: 400},
		{name: "duplicate action", values: url.Values{"action": {"save", "forget"}, "token": {"st_a"}}, code: 400},
		{name: "too large", values: url.Values{"action": {"save"}, "token": {"st_" + strings.Repeat("a", 5000)}}, code: 400},
	} {
		t.Run(tt.name, func(t *testing.T) {
			values := tt.values
			if values == nil {
				values = url.Values{"action": {"save"}, "token": {"st_new"}}
			}
			r := webFormRequest(values)
			if tt.mutate != nil {
				tt.mutate(r)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tt.code {
				t.Fatalf("code = %d, want %d", w.Code, tt.code)
			}
			if s.Token() != "st_original" {
				t.Fatal("invalid POST changed state")
			}
		})
	}
	// Forged forget is just as sensitive as saving a new token.
	r := webFormRequest(url.Values{"action": {"forget"}})
	r.Header.Del("Origin")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || s.Token() != "st_original" {
		t.Fatal("unguarded forget")
	}
}

func TestWebRoutingAndMethods(t *testing.T) {
	h := newWebHandler(testWebStore(t), nil, nil)
	for _, tt := range []struct {
		method, path string
		want         int
	}{
		{"GET", "/other", 404}, {"POST", "/other", 404},
		{"DELETE", "/", 405}, {"HEAD", "/", 405}, {"PUT", "/", 405},
		{"GET", "/?token=st_must_not_appear", 400},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(tt.method, "http://club.test"+tt.path, nil))
		if w.Code != tt.want || strings.Contains(w.Body.String(), "st_must_not_appear") {
			t.Errorf("%s %s: %d", tt.method, tt.path, w.Code)
		}
		if tt.want == 405 && w.Header().Get("Allow") != "GET, POST" {
			t.Error("missing Allow")
		}
	}
}

func TestProxyAuthority(t *testing.T) {
	for _, tt := range []struct {
		host string
		want bool
	}{
		{"public.exe.xyz", true}, {"public.exe.xyz:8443", true}, {"[::1]:8443", true},
		{"", false}, {"public.exe.xyz, evil.invalid", false}, {"public.exe.xyz/path", false},
		{"user@public.exe.xyz", false}, {"public.exe.xyz?x", false}, {"public.exe.xyz#x", false},
		{" public.exe.xyz", false}, {"public.exe.xyz:0", false}, {"public.exe.xyz:65536", false},
		{"public.exe.xyz:abc", false}, {"public.exe.xyz:", false},
	} {
		r := httptest.NewRequest("POST", "http://internal:8000/", nil)
		r.Header.Set("X-Forwarded-Proto", "https")
		r.Header.Set("X-Forwarded-Host", tt.host)
		r.Header.Set("Origin", "https://"+tt.host)
		if got := sameOrigin(r); got != tt.want {
			t.Errorf("host %q: sameOrigin=%v want %v", tt.host, got, tt.want)
		}
	}
	r := httptest.NewRequest("POST", "http://internal:8000/", nil)
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-Host", "public.exe.xyz")
	r.Header.Set("Origin", "https://evil.invalid")
	if sameOrigin(r) {
		t.Fatal("cross-origin accepted behind proxy")
	}
	r.Header.Set("Origin", "https://public.exe.xyz")
	r.Header.Add("X-Forwarded-Host", "evil.invalid")
	if sameOrigin(r) {
		t.Fatal("ambiguous forwarded authority accepted")
	}
	h := newWebHandler(testWebStore(t), nil, nil)
	r = httptest.NewRequest("GET", "http://internal:8000/", nil)
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-Host", "public.exe.xyz")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if !strings.Contains(w.Body.String(), "https://public.exe.xyz/mcp") {
		t.Fatal("public MCP URL absent")
	}
}

func TestConfiguredClientGuide(t *testing.T) {
	s := testWebStore(t)
	if err := s.Replace("st_OWNER_SECRET"); err != nil {
		t.Fatal(err)
	}
	h := newWebHandler(s, func(context.Context, string) (User, error) { return User{Slug: "owner", FullName: "Owner"}, nil }, nil)
	r := httptest.NewRequest("GET", "http://backend:8000/", nil)
	r.Header.Set("X-Forwarded-Host", "hammer-endgame.exe.xyz:8443")
	r.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	body := w.Body.String()
	for _, text := range []string{
		"Connect your MCP client", "Manually on your own computer, not on this VM",
		"ssh exe.dev ssh-key generate-api-key --vm=hammer-endgame --label=vas3k-claude --exp=90d",
		"https://hammer-endgame.exe.xyz:8443/mcp", "No sign in", "Request headers", "limited beta",
		"X-Exedev-Authorization", "Bearer &lt;token&gt;", "Authorization: Bearer &lt;token&gt;",
		"HTTP Basic auth", "any username",
		"claude mcp add --transport http --scope user vas3k", "mcpServers", "90 days", "revoke",
		"server never generates", "proxy strips it",
	} {
		if !strings.Contains(body, text) {
			t.Errorf("missing %q", text)
		}
	}
	if strings.Contains(body, "st_OWNER_SECRET") || strings.Contains(body, "http://backend:8000/mcp") {
		t.Fatal("secret/internal URL leaked")
	}
	// Without a saved credential, only the setup guide is displayed.
	if err := s.Forget(); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if strings.Contains(w.Body.String(), "Connect your MCP client") {
		t.Fatal("configured guide on setup page")
	}
	// No hostname is executed, used for upstream routing or accepted as a VM ID
	// without an exact exe.xyz suffix and a shell-safe label.
	for _, host := range []string{"evil.example", "x.y.exe.xyz", "x;touch.exe.xyz", "x_foo.exe.xyz"} {
		command, _, _ := clientExamples(host, "https://"+host+"/mcp")
		if !strings.Contains(command, "--vm=<vm-name>") {
			t.Fatalf("unsafe VM inference: %s", command)
		}
	}
	_, command, config := clientExamples("outside.test", "https://outside.test/'$(evil)/mcp")
	if !strings.Contains(command, `'"'"'`) {
		t.Fatal("URL not shell quoted")
	}
	if !strings.Contains(config, "Bearer <token>") {
		t.Fatal("unreadable JSON placeholder")
	}
	r = httptest.NewRequest("GET", "http://backend:8000/", nil)
	r.Header.Set("X-Forwarded-Host", `evil.example/<script>alert(1)</script>`)
	if err := s.Replace("st_test"); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if strings.Contains(w.Body.String(), "<script>") {
		t.Fatal("HTML injection")
	}
}

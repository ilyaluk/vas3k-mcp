package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAppSetupAndCredentialIsolation(t *testing.T) {
	var hits atomic.Int32
	c := mockClub(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("Authorization") != "" || r.Header.Get("X-Service-Token") != "st_owner" {
			t.Error("caller credentials escaped upstream")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"user":{"slug":"owner","full_name":"Owner","email":"PRIVATE","telegram":{"private":"PRIVATE"}}}`)
	})
	s, err := openStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(appHandler(s, c))
	defer server.Close()
	client := server.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	callerAuthorization := "Bearer CALLER_VM_TOKEN"
	rpc := func(method string, params any) map[string]any {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
		req, _ := http.NewRequest("POST", server.URL+"/mcp", strings.NewReader(string(b)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Authorization", callerAuthorization)
		resp, e := client.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer resp.Body.Close()
		var result map[string]any
		if e = json.NewDecoder(resp.Body).Decode(&result); e != nil {
			t.Fatal(e)
		}
		if resp.StatusCode != 200 {
			t.Fatalf("RPC status %d: %v", resp.StatusCode, result)
		}
		return result
	}
	result := rpc("tools/list", map[string]any{})
	listed := result["result"].(map[string]any)["tools"].([]any)
	if len(listed) != 5 {
		t.Fatalf("%d tools", len(listed))
	}
	wantTools := map[string]bool{"get_user": true, "get_post": true, "list_post_comments": true, "get_feed": true, "search": true}
	for _, tool := range listed {
		name := tool.(map[string]any)["name"].(string)
		if !wantTools[name] {
			t.Fatalf("unexpected/duplicate tool %q", name)
		}
		delete(wantTools, name)
		if tool.(map[string]any)["annotations"].(map[string]any)["readOnlyHint"] != true {
			t.Fatal("write exposed")
		}
	}
	if len(wantTools) != 0 {
		t.Fatalf("missing tools: %v", wantTools)
	}
	result = rpc("tools/call", map[string]any{"name": "get_user", "arguments": map[string]any{}})
	if !strings.Contains(fmt.Sprint(result), "not configured, open / to set up") || hits.Load() != 0 {
		t.Fatal("unconfigured call")
	}
	form := url.Values{"action": {"save"}, "token": {"st_owner"}}
	req, _ := http.NewRequest("POST", server.URL+"/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", server.URL)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 303 || s.Token() != "st_owner" || hits.Load() != 1 {
		t.Fatalf("setup: status=%d token saved=%v", resp.StatusCode, s.Token() != "")
	}
	result = rpc("tools/call", map[string]any{"name": "get_user", "arguments": map[string]any{}})
	if strings.Contains(fmt.Sprint(result), "PRIVATE") || !strings.Contains(fmt.Sprint(result), "owner") {
		t.Fatalf("allowlist failure: %v", result)
	}
	if hits.Load() != 2 {
		t.Fatal("wrong request count")
	}
	callerAuthorization = "Basic dXNlcjpDQUxMRVJfVk1fVE9LRU4="
	result = rpc("tools/call", map[string]any{"name": "get_user", "arguments": map[string]any{}})
	if strings.Contains(fmt.Sprint(result), "CALLER_VM_TOKEN") || hits.Load() != 3 {
		t.Fatal("Basic proxy credential was used instead of stored club token")
	}
	for _, method := range []string{"GET", "POST", "DELETE"} {
		req, _ = http.NewRequest(method, server.URL+"/mcp-full", nil)
		resp, err = client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 404 {
			t.Fatal("write route exists")
		}
	}
	// Neither an unconfigured/setup operation nor an MCP request needs app auth.
	// Browser CSRF headers are nevertheless checked before any upstream request.
	req, _ = http.NewRequest("POST", server.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Origin", "https://evil.invalid")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatal("cross-origin MCP accepted")
	}
	if _, err = c.call(context.Background(), s.Token(), "vote", json.RawMessage(`{}`)); err == nil {
		t.Fatal("non-read tool")
	}
}

func TestStateLock(t *testing.T) {
	dir := t.TempDir()
	first, err := lockStateDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := lockStateDir(dir)
	if err == nil {
		second.Close()
		t.Fatal("second instance acquired lock")
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := lockStateDir(dir)
	if err != nil {
		t.Fatal("lock survived process close:", err)
	}
	next.Close()
}

// Browser defenses must reject requests before executing a tool. The last two
// cases also document their limit: these headers are not caller authentication.
func TestMCPBrowserSecurityBoundary(t *testing.T) {
	for _, tt := range []struct {
		name, origin, fetch string
		want                int
	}{
		{"foreign origin", "https://evil.invalid", "", 403},
		{"null origin", "null", "", 403},
		{"cross-site iframe navigation", "", "cross-site", 403},
		{"sibling origin", "https://sibling.exe.xyz", "same-site", 403},
		{"forged matching origin with cross-site metadata", "https://owner.exe.xyz", "cross-site", 403},
		{"non-browser without credentials", "", "", 200},
		{"same-origin browser without app credentials", "https://owner.exe.xyz", "same-origin", 200},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var hits atomic.Int32
			c := mockClub(t, func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"user":{"slug":"owner","full_name":"Owner"}}`)
			})
			s, err := openStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Replace("st_test_owner"); err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("POST", "https://owner.exe.xyz/mcp", strings.NewReader(
				`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_user","arguments":{}}}`))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Accept", "application/json, text/event-stream")
			if tt.origin != "" {
				r.Header.Set("Origin", tt.origin)
			}
			if tt.fetch != "" {
				r.Header.Set("Sec-Fetch-Site", tt.fetch)
			}
			w := httptest.NewRecorder()
			appHandler(s, c).ServeHTTP(w, r)
			if w.Code != tt.want {
				t.Fatalf("status = %d, want %d", w.Code, tt.want)
			}
			wantHits := int32(0)
			if tt.want == 200 {
				wantHits = 1
				if !strings.Contains(w.Body.String(), "Owner") {
					t.Fatal("expected owner data in successful response")
				}
			}
			if hits.Load() != wantHits {
				t.Fatalf("upstream calls = %d, want %d", hits.Load(), wantHits)
			}
			if w.Header().Get("Access-Control-Allow-Origin") != "" {
				t.Fatal("unexpected cross-origin read permission")
			}
		})
	}
}

func TestStateLockRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "outside-lock")
	if err := os.WriteFile(target, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, ".lock")); err != nil {
		t.Fatal(err)
	}
	if lock, err := lockStateDir(dir); err == nil {
		lock.Close()
		t.Fatal("followed symlink lock")
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "unchanged" {
		t.Fatal("symlink target changed")
	}
}

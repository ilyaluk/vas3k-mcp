package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func mockClub(t *testing.T, h http.HandlerFunc) *clubClient {
	t.Helper()
	server := httptest.NewTLSServer(h)
	t.Cleanup(server.Close)
	c, err := newClubClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	c.http.Transport = server.Client().Transport
	return c
}

// resultObject asserts the public JSON contract without depending on DTO layout.
func resultObject(t *testing.T, result any) map[string]any {
	t.Helper()
	b, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]any
	if err := json.Unmarshal(b, &obj); err != nil {
		t.Fatal(err)
	}
	return obj
}

func TestDocumentedReadRoutes(t *testing.T) {
	cases := []struct{ name, args, path, query, body string }{
		{"get_user", `{}`, "/user/me.json", "", `{"user":{"slug":"owner","full_name":"Owner"}}`},
		{"get_user", `{"slug":"test-user"}`, "/user/test-user.json", "", `{"user":{"slug":"test-user"}}`},
		{"get_user", `{"slug":"owner","include":["tags"]}`, "/user/owner/tags.json", "", `{"tags":{"tech":[{"code":"go","name":"Go"}]}}`},
		{"get_user", `{"slug":"owner","include":["badges"]}`, "/user/owner/badges.json", "", `{"user_badges":[{"from_user":null}]}`},
		{"get_user", `{"slug":"owner","include":["achievements"]}`, "/user/owner/achievements.json", "", `{"user_achievements":[]}`},
		{"get_user", `{"telegram_id":"123"}`, "/user/by_telegram_id/123.json", "", `{"user":{"slug":"owner"}}`},
		{"get_post", `{"post_type":"request","slug":"12"}`, "/request/12.json", "", `{"post":{"id":"uuid","_club":{"type":"request","slug":"12"}}}`},
		{"get_post", `{"post_type":"post","slug":"12","format":"json"}`, "/post/12.json", "", `{"post":{"title":"Title"}}`},
		{"get_post", `{"post_type":"post","slug":"12","format":"markdown"}`, "/post/12.md", "", "# title\n\nbody"},
		{"list_post_comments", `{"post_type":"post","slug":"12"}`, "/post/12/comments.json", "", `{"comments":[]}`},
		{"get_feed", `{}`, "/feed.json", "page=1", `{"items":[]}`},
		{"get_feed", `{"post_type":"request","ordering":"top_week","page":2}`, "/request/top_week/feed.json", "page=2", `{"items":[]}`},
		{"search", `{"query":" Go "}`, "/search.json", "ordering=-rank&page=1&q=Go", `{"query":"Go","type":null,"ordering":"-rank","page":1,"pages":1,"results":[]}`},
		{"search", `{"scope":"all","query":"Go"}`, "/search.json", "ordering=-rank&page=1&q=Go", `{"results":[]}`},
		{"search", `{"scope":"posts","query":"C++ & Go? #тест","ordering":"-created_at","page":2}`, "/search/posts.json", "ordering=-created_at&page=2&q=C%2B%2B+%26+Go%3F+%23%D1%82%D0%B5%D1%81%D1%82", `{"results":[]}`},
		{"search", `{"scope":"comments","query":"abc"}`, "/search.json", "ordering=-rank&page=1&q=abc&type=comment", `{"results":[]}`},
		{"search", `{"scope":"users","query":"abc","ordering":"created_at","page":10000}`, "/search.json", "ordering=created_at&page=10000&q=abc&type=user", `{"results":[]}`},
		{"search", `{"scope":"users","prefix":"abc"}`, "/search/users.json", "prefix=abc", `{"users":[]}`},
		{"search", `{"scope":"tags","query":"C++","group":"tech"}`, "/search/tags.json", "group=tech&prefix=C%2B%2B", `{"tags":[]}`},
		{"search", `{"scope":"tags"}`, "/search/tags.json", "", `{"tags":[]}`},
	}
	for _, tt := range cases {
		t.Run(tt.name+tt.path+tt.query, func(t *testing.T) {
			hits := 0
			c := mockClub(t, func(w http.ResponseWriter, r *http.Request) {
				hits++
				if r.Method != "GET" || r.URL.Path != tt.path || r.URL.RawQuery != tt.query {
					t.Errorf("request: %s %s", r.Method, r.URL.String())
				}
				if r.Header.Get("X-Service-Token") != "st_fake" || r.Header.Get("Authorization") != "" || strings.Contains(r.URL.String(), "st_fake") {
					t.Error("credential boundary")
				}
				w.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(tt.path, ".md") {
					w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
				}
				_, _ = io.WriteString(w, tt.body)
			})
			got, err := c.call(context.Background(), "st_fake", tt.name, json.RawMessage(tt.args))
			if err != nil || got == nil || hits != 1 {
				t.Fatalf("result=%v err=%v hits=%d", got, err, hits)
			}
			if strings.HasSuffix(tt.path, ".md") && resultObject(t, got)["markdown"] != tt.body {
				t.Fatalf("markdown changed: %v", got)
			}
		})
	}
}

func TestValidationBeforeUpstream(t *testing.T) {
	var hits atomic.Int32
	c := mockClub(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{}`)
	})
	cases := []struct{ name, args string }{
		{"get_user", `{"url":"https://evil.invalid"}`},
		{"get_user", `{"slug":"../me"}`}, {"get_user", `{"slug":"%2fme"}`},
		{"get_user", `{"slug":""}`}, {"get_user", `{"telegram_id":""}`},
		{"get_user", `{"slug":"owner","telegram_id":"123"}`},
		{"get_user", `{"telegram_id":"1?service_token=bad"}`},
		{"get_user", `{"include":["private"]}`}, {"get_user", `{"include":"profile"}`},
		{"get_user", `{"include":[]}`}, {"get_user", `{"include":["profile","profile"]}`},
		{"get_user", `{"include":["profile","tags","badges","achievements","profile"]}`},
		{"get_user", `{"slug":"","telegram_id":"123"}`},
		{"get_user", `{"include":[null]}`}, {"get_user", `{"include":[1]}`},
		{"get_post", `{"post_type":"//evil.invalid","slug":"x"}`},
		{"get_post", `{"post_type":"idea","slug":"x"}`},
		{"get_post", `{"post_type":"post","slug":"x","format":"html"}`},
		{"get_post", `{"post_type":"post","slug":"x","format":""}`},
		{"list_post_comments", `{"post_type":"post","slug":"x","format":"json"}`},
		{"get_feed", `{"ordering":"../x"}`}, {"get_feed", `{"page":-1}`},
		{"get_feed", `{"page":10001}`}, {"get_feed", `{"page":1.5}`},
		{"search", `{}`}, {"search", `{"query":" \n\t "}`},
		{"search", `{"query":123}`}, {"search", `{"query":"ok","page":0}`},
		{"search", `{"query":"ok","page":-1}`}, {"search", `{"query":"ok","page":10001}`},
		{"search", `{"query":"ok","page":1.5}`}, {"search", `{"query":"ok","ordering":"new"}`},
		{"search", `{"query":"ok","ordering":""}`}, {"search", `{"query":"ok","scope":""}`},
		{"search", `{"query":"ok","scope":"invented"}`},
		{"search", `{"query":"ok","scope":"all","scope":"users"}`},
		{"search", `{"query":"ok","scope":"all","ſcope":"users"}`},
		{"search", `{"query":"ok","scope":"all","SCOPE":"users"}`},
		{"search", `{"QUERY":"ok"}`},
		{"search", `{"query":"ok","url":"https://evil.invalid"}`},
		{"search", `{"query":"` + strings.Repeat("я", 1001) + `"}`},
		{"search", `{"scope":"users","prefix":"ab"}`},
		{"search", `{"scope":"users","prefix":"123456789012345"}`},
		{"search", `{"scope":"users","prefix":""}`},
		{"search", `{"scope":"users","prefix":"abc","query":"abc"}`},
		{"search", `{"scope":"users","prefix":"abc","query":""}`},
		{"search", `{"scope":"users","prefix":"abc","ordering":"-rank"}`},
		{"search", `{"scope":"users","prefix":"abc","page":1}`},
		{"search", `{"scope":"users","query":"abc","group":"tech"}`},
		{"search", `{"scope":"users","prefix":"abc","group":"tech"}`},
		{"search", `{"scope":"all","query":"abc","prefix":"abc"}`},
		{"search", `{"scope":"posts","query":"abc","prefix":"abc"}`},
		{"search", `{"scope":"comments","query":"abc","group":"tech"}`},
		{"search", `{"scope":"tags","query":"ab"}`},
		{"search", `{"scope":"tags","query":"1234567890123456"}`},
		{"search", `{"scope":"tags","group":"invented"}`},
		{"search", `{"scope":"tags","prefix":"abc"}`},
		{"search", `{"scope":"tags","ordering":""}`},
		{"search", `{"scope":"users","prefix":"abc","ordering":""}`},
		{"search", `{"scope":"tags","ordering":"-rank"}`},
		{"search", `{"scope":"tags","page":1}`},
		{"bookmark_post", `{}`}, {"get_user", `null`}, {"get_user", `[]`},
		{"get_user", `{} {}`}, {"get_user", `{"slug":"owner","slug":"other"}`},
		{"get_user", `{"slug":"owner","Slug":"other"}`},
	}
	// Deprecated names must not survive as hidden aliases.
	for _, name := range []string{"get_me", "get_user_tags", "get_user_badges", "get_user_achievements", "find_user_by_telegram", "get_post_markdown", "search_posts", "search_users", "search_tags"} {
		cases = append(cases, struct{ name, args string }{name, `{}`})
	}
	// Optional does not mean nullable; every supplied argument has a strict type.
	for _, tt := range []struct {
		name, base string
		fields     []string
	}{
		{"get_user", `{}`, []string{"slug", "telegram_id", "include"}},
		{"get_post", `{"post_type":"post","slug":"x"}`, []string{"post_type", "slug", "format"}},
		{"list_post_comments", `{"post_type":"post","slug":"x"}`, []string{"post_type", "slug"}},
		{"get_feed", `{}`, []string{"post_type", "ordering", "page"}},
		{"search", `{"query":"ok"}`, []string{"scope", "query", "prefix", "group", "ordering", "page"}},
	} {
		for _, field := range tt.fields {
			for _, value := range []string{`null`, `true`, `[]`, `{}`} {
				var args map[string]json.RawMessage
				json.Unmarshal([]byte(tt.base), &args)
				args[field] = json.RawMessage(value)
				b, _ := json.Marshal(args)
				cases = append(cases, struct{ name, args string }{tt.name, string(b)})
			}
		}
	}
	for _, tt := range cases {
		if _, err := c.call(context.Background(), "st_fake", tt.name, json.RawMessage(tt.args)); err == nil {
			t.Errorf("accepted %s %s", tt.name, tt.args)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("%d invalid requests reached upstream", hits.Load())
	}
	for _, raw := range []string{"http://vas3k.club", "https://u:p@vas3k.club", "https://vas3k.club/path", "https://vas3k.club?token=x", "https://vas3k.club/#x", "//vas3k.club"} {
		if _, err := newClubClient(raw); err == nil {
			t.Errorf("accepted base %q", raw)
		}
	}
}

func TestOutputAllowlist(t *testing.T) {
	cases := []struct{ name, args, body string }{
		{"get_user", `{"slug":"owner"}`, `{"secret":"SECRET","user":{"slug":"owner","email":"SECRET","telegram":{"token":"SECRET"},"metadata":"SECRET","full_name":"Safe"}}`},
		{"get_user", `{"slug":"owner","include":["tags"]}`, `{"tags":{"unknown":[{"name":"SECRET"}],"tech":[{"code":"go","name":"Go","private":"SECRET"}]}}`},
		{"get_user", `{"slug":"owner","include":["badges"]}`, `{"user_badges":[{"badge":{"title":"Safe","private":"SECRET"},"from_user":{"slug":"x","email":"SECRET","telegram":{"id":"SECRET"}},"post":{"id":"123","private":"SECRET"}}]}`},
		{"get_user", `{"slug":"owner","include":["achievements"]}`, `{"user_achievements":[{"achievement":{"code":"ok","private":"SECRET"},"private":"SECRET"}]}`},
		{"get_post", `{"post_type":"post","slug":"1"}`, `{"post":{"title":"Safe","private":"SECRET","authors":[{"name":"Safe","email":"SECRET"}],"_club":{"private":"SECRET"}}}`},
		{"list_post_comments", `{"post_type":"post","slug":"1"}`, `{"comments":[{"text":"Safe","private":"SECRET","author":{"email":"SECRET","telegram":{"id":"SECRET"}}}]}`},
		{"get_feed", `{}`, `{"items":[{"title":"Safe","email":"SECRET","authors":[{"email":"SECRET"}],"_club":{"private":"SECRET"}}]}`},
		{"search", `{"scope":"users","prefix":"abc"}`, `{"users":[{"slug":"Safe","email":"SECRET","telegram":{"id":"SECRET"}}]}`},
		{"search", `{"scope":"tags"}`, `{"tags":[{"code":"Safe","private":"SECRET"}]}`},
		{"search", `{"query":"abc"}`, `{"private":"SECRET","type":null,"results":[{"type":"post","private":"SECRET","post":{"title":"Safe","email":"SECRET","authors":[{"name":"Safe","email":"SECRET"}],"_club":{"private":"SECRET"}}},{"type":"comment","private":"SECRET","comment":{"text":"Safe","email":"SECRET","author":{"email":"SECRET","telegram":{"id":"SECRET"}},"_club":{"private":"SECRET"}}},{"type":"user","user":{"slug":"Safe","email":"SECRET","telegram":{"id":"SECRET"},"metadata":{"private":"SECRET"}}}]}`},
	}
	for _, tt := range cases {
		t.Run(tt.name+tt.args, func(t *testing.T) {
			c := mockClub(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, tt.body)
			})
			got, err := c.call(context.Background(), "st_fake", tt.name, json.RawMessage(tt.args))
			if err != nil {
				t.Fatal(err)
			}
			b, _ := json.Marshal(got)
			if strings.Contains(string(b), "SECRET") {
				t.Fatalf("leaked unknown field: %s", b)
			}
			if !strings.Contains(string(b), "Safe") && !strings.Contains(string(b), "Go") && !strings.Contains(string(b), "ok") {
				t.Fatalf("safe data lost: %s", b)
			}
		})
	}
}

func TestUpstreamFailures(t *testing.T) {
	for _, tt := range []struct {
		name     string
		status   int
		ct, body string
	}{
		{"auth", 400, "application/json", `{"error":{"message":"st_SECRET"}}`},
		{"server", 500, "text/plain", "st_SECRET"},
		{"malformed", 200, "application/json", `{"secret":"st_SECRET"`},
		{"null", 200, "application/json", `null`},
		{"html", 200, "text/html", "st_SECRET"},
		{"invalidOwner", 200, "application/json", `{"user":{}}`},
		{"oversize", 200, "application/json", strings.Repeat(" ", maxJSONBytes+1)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := mockClub(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tt.ct)
				w.WriteHeader(tt.status)
				io.WriteString(w, tt.body)
			})
			_, err := c.me(context.Background(), "st_fake")
			if err == nil || strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("unsafe error: %v", err)
			}
			status, when := c.status()
			if !strings.HasPrefix(status, "error:") || when.IsZero() {
				t.Fatal("check not recorded")
			}
		})
	}
}
func TestRedirectsNeverFollowed(t *testing.T) {
	var otherHits atomic.Int32
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { otherHits.Add(1) }))
	defer other.Close()
	for _, location := range []string{other.URL + "/leak", "/user/me.json"} {
		c := mockClub(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, location, http.StatusFound) })
		if _, err := c.me(context.Background(), "st_fake"); err == nil {
			t.Fatal("followed redirect")
		}
	}
	if otherHits.Load() != 0 {
		t.Fatal("cross-origin credential forwarding")
	}
}
func TestTimeoutAndLimits(t *testing.T) {
	c := mockClub(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	c.http.Timeout = 20 * time.Millisecond
	if _, err := c.me(context.Background(), "st_fake"); err == nil {
		t.Fatal("missing timeout")
	}
	c = mockClub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, strings.Repeat("x", maxMarkdownBytes+1))
	})
	if _, err := c.call(context.Background(), "st_fake", "get_post", json.RawMessage(`{"post_type":"post","slug":"1","format":"markdown"}`)); err == nil {
		t.Fatal("markdown cap")
	}
	c = mockClub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"comments":[`+strings.Repeat(`{},`, maxItems)+`{}]}`)
	})
	if _, err := c.call(context.Background(), "st_fake", "list_post_comments", json.RawMessage(`{"post_type":"post","slug":"1"}`)); err == nil {
		t.Fatal("collection cap")
	}
}
func TestFeedPreview(t *testing.T) {
	c := mockClub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/feed+json")
		json.NewEncoder(w).Encode(map[string]any{"items": []any{map[string]any{"content_text": strings.Repeat("界", 1001), "_club": map[string]any{"type": "post", "slug": "1", "content_truncated": true, "content_full_chars": 99999, "unknown": "SECRET"}}}})
	})
	got, err := c.call(context.Background(), "st_fake", "get_feed", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	p := got.(feedResponse).Items[0]
	if len([]rune(p.ContentText)) != 1000 || !p.Club.ContentTruncated || p.Club.ContentFullChars != 1001 {
		t.Fatalf("bad preview: %+v", p.Club)
	}
}

func TestGetUserSectionsAndExactFanout(t *testing.T) {
	sections := []string{"profile", "tags", "badges", "achievements"}
	cases := []struct {
		name, args  string
		paths, keys []string
	}{
		{"default", `{}`, []string{"/user/me.json"}, []string{"user"}},
		{"self_sections_only", `{"include":["tags"]}`, []string{"/user/me/tags.json"}, []string{"tags"}},
		{"telegram_profile", `{"telegram_id":"123"}`, []string{"/user/by_telegram_id/123.json"}, []string{"user"}},
		{"telegram_sections_only", `{"telegram_id":"123","include":["tags","badges","achievements"]}`, []string{"/user/by_telegram_id/123.json", "/user/resolved/tags.json", "/user/resolved/badges.json", "/user/resolved/achievements.json"}, []string{"tags", "user_badges", "user_achievements"}},
		{"telegram_all", `{"telegram_id":"123","include":["profile","tags","badges","achievements"]}`, []string{"/user/by_telegram_id/123.json", "/user/resolved/tags.json", "/user/resolved/badges.json", "/user/resolved/achievements.json"}, []string{"user", "tags", "user_badges", "user_achievements"}},
	}
	// Every nonempty subset must fetch and return precisely those sections.
	for mask := 1; mask < 1<<len(sections); mask++ {
		var include, paths, keys []string
		for i, section := range sections {
			if mask&(1<<i) == 0 {
				continue
			}
			include = append(include, section)
			key, path := section, "/user/owner/"+section+".json"
			switch section {
			case "profile":
				key, path = "user", "/user/owner.json"
			case "badges":
				key = "user_badges"
			case "achievements":
				key = "user_achievements"
			}
			paths, keys = append(paths, path), append(keys, key)
		}
		b, _ := json.Marshal(map[string]any{"slug": "owner", "include": include})
		cases = append(cases, struct {
			name, args  string
			paths, keys []string
		}{fmt.Sprint(mask), string(b), paths, keys})
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			var paths []string
			c := mockClub(t, func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				paths = append(paths, r.URL.Path)
				mu.Unlock()
				if r.Method != http.MethodGet || r.URL.RawQuery != "" || r.Header.Get("X-Service-Token") != "st_fake" || r.Header.Get("Authorization") != "" {
					t.Errorf("unsafe fanout request: %s %s", r.Method, r.URL)
				}
				w.Header().Set("Content-Type", "application/json")
				// Empty/missing upstream collections still become requested []s.
				io.WriteString(w, `{"user":{"slug":"resolved"},"tags":{},"user_badges":null,"user_achievements":[]}`)
			})
			got, err := c.call(context.Background(), "st_fake", "get_user", json.RawMessage(tt.args))
			if err != nil {
				t.Fatal(err)
			}
			obj := resultObject(t, got)
			keys := make([]string, 0, len(obj))
			for key := range obj {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			sort.Strings(tt.keys)
			if !reflect.DeepEqual(keys, tt.keys) {
				t.Fatalf("returned keys %v, want %v: %+v", keys, tt.keys, obj)
			}
			mu.Lock()
			actualPaths := append([]string(nil), paths...)
			mu.Unlock()
			sort.Strings(actualPaths)
			sort.Strings(tt.paths)
			if !reflect.DeepEqual(actualPaths, tt.paths) {
				t.Fatalf("requests %v, want %v", actualPaths, tt.paths)
			}
			for _, key := range []string{"user_badges", "user_achievements"} {
				if value, ok := obj[key]; ok {
					items, ok := value.([]any)
					if !ok || len(items) != 0 {
						t.Fatalf("%s must be []: %#v", key, value)
					}
				}
			}
			if value, ok := obj["tags"]; ok {
				groups := value.(map[string]any)
				for _, group := range []string{"club", "tech", "hobbies", "personal", "collectible", "other"} {
					items, ok := groups[group].([]any)
					if !ok || len(items) != 0 {
						t.Fatalf("tags.%s must be []: %#v", group, groups[group])
					}
				}
			}
		})
	}
}

func TestGetUserFanoutErrorsReturnNoPartialResult(t *testing.T) {
	for _, path := range []string{"/user/owner.json", "/user/owner/tags.json", "/user/owner/badges.json", "/user/owner/achievements.json"} {
		for _, failure := range []string{"http", "json", "type"} {
			t.Run(path+failure, func(t *testing.T) {
				c := mockClub(t, func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if r.URL.Path == path {
						switch failure {
						case "http":
							w.WriteHeader(http.StatusBadGateway)
							io.WriteString(w, `{"private":"SECRET"}`)
						case "json":
							io.WriteString(w, `{"private":"SECRET"`)
						case "type":
							io.WriteString(w, `{"user":true,"tags":true,"user_badges":true,"user_achievements":true}`)
						}
						return
					}
					io.WriteString(w, `{"user":{"slug":"owner"},"tags":{"tech":[{"name":"SAFE_PARTIAL"}]},"user_badges":[],"user_achievements":[]}`)
				})
				got, err := c.call(context.Background(), "st_fake", "get_user", json.RawMessage(`{"slug":"owner","include":["profile","tags","badges","achievements"]}`))
				if err == nil || got != nil || strings.Contains(err.Error(), "SECRET") {
					t.Fatalf("partial/unsafe result=%v error=%v", got, err)
				}
			})
		}
	}
}

func TestTelegramLookupMustReturnSafeSlugBeforeFanout(t *testing.T) {
	for _, slug := range []string{"", "../owner", "%2fowner", "https://evil.invalid", "owner?token=x", strings.Repeat("a", 65)} {
		t.Run(slug, func(t *testing.T) {
			var hits atomic.Int32
			c := mockClub(t, func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				if r.URL.Path != "/user/by_telegram_id/123.json" {
					t.Errorf("unsafe slug used: %s", r.URL)
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{"user": map[string]any{"slug": slug}})
			})
			got, err := c.call(context.Background(), "st_fake", "get_user", json.RawMessage(`{"telegram_id":"123","include":["tags","badges","achievements"]}`))
			if err == nil || got != nil || hits.Load() != 1 {
				t.Fatalf("result=%v error=%v hits=%d", got, err, hits.Load())
			}
		})
	}
}

func TestGenericSearchMixedPreviewsAndMetadata(t *testing.T) {
	for _, size := range []int{0, 1000, 1001} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			text := strings.Repeat("界", size)
			c := mockClub(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{
					"query": "界", "type": nil, "ordering": "-upvotes", "page": 2, "pages": 3,
					"results": []any{
						map[string]any{"type": "post", "post": map[string]any{"id": "post-id", "title": "Title", "content_text": text, "_club": map[string]any{"type": "question", "slug": "42", "comment_count": 7, "view_count": 8, "upvotes": 9, "is_public": true, "is_commentable": true, "coauthors": []string{"coauthor"}, "content_truncated": true, "content_full_chars": 99999}}},
						map[string]any{"type": "comment", "comment": map[string]any{"id": "comment-id", "url": "https://vas3k.club/post/42/#comment", "text": text, "author": map[string]any{"slug": "author", "full_name": "Comment Author"}, "reply_to_id": "parent-id", "upvotes": 4, "created_at": "2026-01-02", "_club": map[string]any{"content_truncated": true, "content_full_chars": 99999}}},
						map[string]any{"type": "user", "user": map[string]any{"slug": "member", "full_name": "Member Name", "bio": "Full user profile"}},
					},
				})
			})
			got, err := c.call(context.Background(), "st_fake", "search", json.RawMessage(`{"query":"界","ordering":"-upvotes","page":2}`))
			if err != nil {
				t.Fatal(err)
			}
			r := resultObject(t, got)
			if r["query"] != "界" || r["ordering"] != "-upvotes" || r["page"] != float64(2) || r["pages"] != float64(3) {
				t.Fatalf("lost pagination metadata: %+v", r)
			}
			if typ, ok := r["type"]; !ok || typ != nil {
				t.Fatalf("mixed search must retain type:null: %+v", r)
			}
			results := r["results"].([]any)
			if len(results) != 3 {
				t.Fatalf("lost mixed results: %+v", results)
			}
			for i, kind := range []string{"post", "comment", "user"} {
				entry := results[i].(map[string]any)
				if entry["type"] != kind || len(entry) != 2 {
					t.Fatalf("bad union member: %+v", entry)
				}
				item, ok := entry[kind].(map[string]any)
				if !ok {
					t.Fatalf("missing %s result: %+v", kind, entry)
				}
				if kind == "user" {
					if item["slug"] != "member" || item["bio"] != "Full user profile" {
						t.Fatalf("lost user: %+v", item)
					}
					continue
				}
				field := "content_text"
				if kind == "comment" {
					field = "text"
				}
				want := text
				if size > 1000 {
					want = strings.Repeat("界", 1000)
				}
				if item[field] != want {
					t.Fatalf("%s preview has %d runes, want %d", kind, len([]rune(item[field].(string))), len([]rune(want)))
				}
				club, _ := item["_club"].(map[string]any)
				if size > 1000 {
					if club["content_truncated"] != true || club["content_full_chars"] != float64(size) {
						t.Fatalf("bad %s truncation metadata: %+v", kind, club)
					}
				} else if club["content_truncated"] == true || (club["content_full_chars"] != nil && club["content_full_chars"] != float64(0)) {
					t.Fatalf("upstream %s preview claims survived: %+v", kind, club)
				}
				if kind == "post" {
					if item["id"] != "post-id" || item["title"] != "Title" || club["slug"] != "42" || club["type"] != "question" || club["comment_count"] != float64(7) || club["view_count"] != float64(8) || club["upvotes"] != float64(9) || club["is_public"] != true || club["is_commentable"] != true || !reflect.DeepEqual(club["coauthors"], []any{"coauthor"}) {
						t.Fatalf("lost post metadata: %+v", item)
					}
				} else if item["id"] != "comment-id" || item["reply_to_id"] != "parent-id" || item["upvotes"] != float64(4) || item["created_at"] != "2026-01-02" || item["author"].(map[string]any)["slug"] != "author" {
					t.Fatalf("lost comment metadata: %+v", item)
				}
			}
		})
	}
}

func TestFullPostAndCommentBodiesAreNotSearchPreviews(t *testing.T) {
	text := strings.Repeat("界", 1001)
	for _, name := range []string{"get_post", "list_post_comments"} {
		t.Run(name, func(t *testing.T) {
			c := mockClub(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{"post": map[string]any{"content_text": text}, "comments": []any{map[string]any{"text": text, "_club": map[string]any{"content_truncated": true, "content_full_chars": 12345}}}})
			})
			got, err := c.call(context.Background(), "st_fake", name, json.RawMessage(`{"post_type":"post","slug":"1"}`))
			if err != nil {
				t.Fatal(err)
			}
			obj := resultObject(t, got)
			if name == "get_post" {
				if obj["post"].(map[string]any)["content_text"] != text {
					t.Fatal("full post was truncated")
				}
			} else {
				comment := obj["comments"].([]any)[0].(map[string]any)
				if comment["text"] != text {
					t.Fatal("full comment was truncated")
				}
				if _, ok := comment["_club"]; ok {
					t.Fatalf("search-only comment metadata escaped: %+v", comment)
				}
			}
		})
	}
}

func TestSearchUnicodeInputBoundaries(t *testing.T) {
	for _, tt := range []struct {
		scope, field string
		sizes        []int
		max          int
	}{
		{"all", "query", []int{1, 1000, 1001}, 1000},
		{"posts", "query", []int{1, 1000, 1001}, 1000},
		{"comments", "query", []int{1, 1000, 1001}, 1000},
		{"users", "query", []int{1, 1000, 1001}, 1000},
		{"users", "prefix", []int{2, 3, 14, 15}, 14},
		{"tags", "query", []int{2, 3, 15, 16}, 15},
	} {
		for _, size := range tt.sizes {
			t.Run(fmt.Sprintf("%s_%s_%d", tt.scope, tt.field, size), func(t *testing.T) {
				text := strings.Repeat("界", size)
				var hits atomic.Int32
				c := mockClub(t, func(w http.ResponseWriter, r *http.Request) {
					hits.Add(1)
					key := "q"
					if tt.field == "prefix" || tt.scope == "tags" {
						key = "prefix"
					}
					if r.URL.Query().Get(key) != text {
						t.Errorf("Unicode query changed: %s", r.URL)
					}
					w.Header().Set("Content-Type", "application/json")
					io.WriteString(w, `{"results":[],"users":[],"tags":[]}`)
				})
				input := text
				if tt.field == "query" {
					input = " \t" + text + "\n "
				}
				args, _ := json.Marshal(map[string]any{"scope": tt.scope, tt.field: input})
				_, err := c.call(context.Background(), "st_fake", "search", args)
				valid := size <= tt.max && (size >= 3 || (tt.field == "query" && tt.scope != "tags"))
				if valid && (err != nil || hits.Load() != 1) {
					t.Fatalf("valid Unicode boundary rejected: err=%v hits=%d", err, hits.Load())
				}
				if !valid && (err == nil || hits.Load() != 0) {
					t.Fatalf("invalid Unicode boundary accepted: err=%v hits=%d", err, hits.Load())
				}
			})
		}
	}
}

func TestCollectionLimitsAcrossCombinedTools(t *testing.T) {
	for _, tt := range []struct{ name, args, field string }{
		{"get_user", `{"slug":"owner","include":["badges"]}`, "user_badges"},
		{"get_user", `{"slug":"owner","include":["achievements"]}`, "user_achievements"},
		{"get_feed", `{}`, "items"},
		{"list_post_comments", `{"post_type":"post","slug":"1"}`, "comments"},
		{"search", `{"query":"go"}`, "results"},
		{"search", `{"scope":"posts","query":"go"}`, "results"},
		{"search", `{"scope":"comments","query":"go"}`, "results"},
		{"search", `{"scope":"users","query":"go"}`, "results"},
		{"search", `{"scope":"users","prefix":"abc"}`, "users"},
		{"search", `{"scope":"tags"}`, "tags"},
	} {
		for _, count := range []int{maxItems, maxItems + 1} {
			t.Run(fmt.Sprintf("%s_%s_%d", tt.name, tt.args, count), func(t *testing.T) {
				c := mockClub(t, func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					item := `{}`
					if tt.field == "results" {
						item = `{"type":"user","user":{"slug":"owner"}}`
					}
					io.WriteString(w, `{"`+tt.field+`":[`+strings.Repeat(item+`,`, count-1)+item+`]}`)
				})
				_, err := c.call(context.Background(), "st_fake", tt.name, json.RawMessage(tt.args))
				if count == maxItems && err != nil {
					t.Fatalf("limit boundary rejected: %v", err)
				}
				if count > maxItems && (err == nil || !strings.Contains(err.Error(), "1000 items")) {
					t.Fatalf("expected item limit, got %v", err)
				}
			})
		}
	}
	// The tag cap applies across groups, not separately to each group.
	for _, count := range []int{500, 501} {
		c := mockClub(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			items := strings.Repeat(`{},`, count-1) + `{}`
			io.WriteString(w, `{"tags":{"club":[`+items+`],"tech":[`+items+`]}}`)
		})
		got, err := c.call(context.Background(), "st_fake", "get_user", json.RawMessage(`{"slug":"owner","include":["tags"]}`))
		if count == 500 && err != nil {
			t.Fatal(err)
		}
		if count == 501 && (err == nil || got != nil) {
			t.Fatalf("aggregate tag cap result=%v err=%v", got, err)
		}
	}
}

func TestSearchModesThroughMCP(t *testing.T) {
	for _, tt := range []struct{ name, args, path, query, body, key string }{
		{"default", `{"query":"go"}`, "/search.json", "ordering=-rank&page=1&q=go", `{"query":"go","type":null,"ordering":"-rank","page":1,"pages":2,"results":[{"type":"post","post":{"title":"Found"}},{"type":"comment","comment":{"text":"Found comment"}},{"type":"user","user":{"slug":"found"}}]}`, "results"},
		{"all", `{"scope":"all","query":"go"}`, "/search.json", "ordering=-rank&page=1&q=go", `{"query":"go","type":null,"ordering":"-rank","page":1,"pages":2,"results":[]}`, "results"},
		{"posts", `{"scope":"posts","query":"go"}`, "/search/posts.json", "ordering=-rank&page=1&q=go", `{"query":"go","type":"post","ordering":"-rank","page":1,"pages":2,"results":[]}`, "results"},
		{"comments", `{"scope":"comments","query":"go"}`, "/search.json", "ordering=-rank&page=1&q=go&type=comment", `{"query":"go","type":"comment","ordering":"-rank","page":1,"pages":2,"results":[]}`, "results"},
		{"users_fulltext", `{"scope":"users","query":"go"}`, "/search.json", "ordering=-rank&page=1&q=go&type=user", `{"query":"go","type":"user","ordering":"-rank","page":1,"pages":2,"results":[]}`, "results"},
		{"users_prefix", `{"scope":"users","prefix":"abc"}`, "/search/users.json", "prefix=abc", `{"users":[{"slug":"abc"}]}`, "users"},
		{"tags_query", `{"scope":"tags","query":"gol","group":"tech"}`, "/search/tags.json", "group=tech&prefix=gol", `{"tags":[{"code":"go","name":"Golang"}]}`, "tags"},
		{"tags_all", `{"scope":"tags"}`, "/search/tags.json", "", `{"tags":[]}`, "tags"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var hits atomic.Int32
			c := mockClub(t, func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				if r.Method != http.MethodGet || r.URL.Path != tt.path || r.URL.RawQuery != tt.query || r.Header.Get("X-Service-Token") != "st_fake" || r.Header.Get("Authorization") != "" {
					t.Errorf("unexpected upstream request: %s %s", r.Method, r.URL)
				}
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, tt.body)
			})
			h := newMCPHandler(readTools(), func(ctx context.Context, name string, args json.RawMessage) (any, error) {
				return c.call(ctx, "st_fake", name, args)
			})
			w := mcpTestRequest(h, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search","arguments":`+tt.args+`}}`)
			if w.Code != http.StatusOK || hits.Load() != 1 {
				t.Fatalf("MCP search failed: hits=%d %s", hits.Load(), w.Body.String())
			}
			envelope := mcpTestEnvelope(t, w)
			var result struct {
				IsError bool `json:"isError"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			}
			if err := json.Unmarshal(envelope["result"], &result); err != nil || result.IsError || len(result.Content) != 1 {
				t.Fatalf("invalid MCP search: %s", w.Body.String())
			}
			var output, upstream map[string]any
			if err := json.Unmarshal([]byte(result.Content[0].Text), &output); err != nil {
				t.Fatal(err)
			}
			json.Unmarshal([]byte(tt.body), &upstream)
			if _, ok := output[tt.key].([]any); !ok {
				t.Fatalf("missing collection in MCP output: %+v", output)
			}
			if tt.key == "results" {
				for _, key := range []string{"query", "type", "ordering", "page", "pages"} {
					if value, ok := output[key]; !ok || value != upstream[key] {
						t.Fatalf("MCP lost %s: %+v", key, output)
					}
				}
			}
		})
	}
}

func TestLegacyToolNamesRejectedThroughMCP(t *testing.T) {
	h := newMCPHandler(readTools(), func(context.Context, string, json.RawMessage) (any, error) {
		t.Error("legacy name reached callback")
		return nil, nil
	})
	for _, name := range []string{"get_me", "get_user_tags", "get_user_badges", "get_user_achievements", "find_user_by_telegram", "get_post_markdown", "search_posts", "search_users", "search_tags"} {
		t.Run(name, func(t *testing.T) {
			w := mcpTestRequest(h, fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":%q,"arguments":{}}}`, name))
			mcpTestError(t, w, http.StatusOK, -32602)
		})
	}
}

func TestSearchTaggedUnionDropsOtherVariants(t *testing.T) {
	for _, kind := range []string{"post", "comment", "user"} {
		t.Run(kind, func(t *testing.T) {
			c := mockClub(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{"results": []any{map[string]any{
					"type":    kind,
					"post":    map[string]any{"title": "Post"},
					"comment": map[string]any{"text": "Comment"},
					"user":    map[string]any{"slug": "owner"},
				}}})
			})
			got, err := c.call(context.Background(), "st_fake", "search", json.RawMessage(`{"query":"go"}`))
			if err != nil {
				t.Fatal(err)
			}
			entry := resultObject(t, got)["results"].([]any)[0].(map[string]any)
			if entry["type"] != kind || len(entry) != 2 || entry[kind] == nil {
				t.Fatalf("union variant mismatch: %+v", entry)
			}
		})
	}
}

func TestSearchMalformedUnionReturnsNoPartialResult(t *testing.T) {
	for _, item := range []string{
		`{}`, `null`, `{"type":"unknown","user":{"slug":"owner"}}`,
		`{"type":"post"}`, `{"type":"comment"}`, `{"type":"user"}`,
		`{"type":"post","post":null}`, `{"type":"comment","comment":null}`, `{"type":"user","user":null}`,
		`{"type":"post","comment":{"text":"wrong variant"}}`,
		`{"type":"comment","user":{"slug":"wrong-variant"}}`,
		`{"type":"user","post":{"title":"wrong variant"}}`,
	} {
		t.Run(item, func(t *testing.T) {
			c := mockClub(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"results":[{"type":"user","user":{"slug":"SAFE_PARTIAL"}},`+item+`]}`)
			})
			got, err := c.call(context.Background(), "st_fake", "search", json.RawMessage(`{"query":"go"}`))
			if err == nil || got != nil {
				t.Fatalf("malformed union returned partial data: result=%v err=%v", got, err)
			}
		})
	}
}

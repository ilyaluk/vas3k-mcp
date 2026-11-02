package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
func TestDocumentedReadRoutes(t *testing.T) {
	cases := []struct{ name, args, path, query, body string }{
		{"get_me", `{}`, "/user/me.json", "", `{"user":{"slug":"owner","full_name":"Owner"}}`},
		{"get_user", `{"slug":"test-user"}`, "/user/test-user.json", "", `{"user":{"slug":"test-user"}}`},
		{"get_user_tags", `{"slug":"owner"}`, "/user/owner/tags.json", "", `{"tags":{"tech":[{"code":"go","name":"Go"}]}}`},
		{"get_user_badges", `{"slug":"owner"}`, "/user/owner/badges.json", "", `{"user_badges":[{"from_user":null}]}`},
		{"get_user_achievements", `{"slug":"owner"}`, "/user/owner/achievements.json", "", `{"user_achievements":[]}`},
		{"find_user_by_telegram", `{"telegram_id":"123"}`, "/user/by_telegram_id/123.json", "", `{"user":{"slug":"owner"}}`},
		{"get_post", `{"post_type":"request","slug":"12"}`, "/request/12.json", "", `{"post":{"id":"uuid","_club":{"type":"request","slug":"12"}}}`},
		{"get_post_markdown", `{"post_type":"post","slug":"12"}`, "/post/12.md", "", "# title\n\nbody"},
		{"list_post_comments", `{"post_type":"post","slug":"12"}`, "/post/12/comments.json", "", `{"comments":[]}`},
		{"get_feed", `{}`, "/feed.json", "page=1", `{"items":[]}`},
		{"get_feed", `{"post_type":"request","ordering":"top_week","page":2}`, "/request/top_week/feed.json", "page=2", `{"items":[]}`},
		{"search_users", `{"prefix":"abc"}`, "/search/users.json", "prefix=abc", `{"users":[]}`},
		{"search_posts", `{"query":" Go "}`, "/search/posts.json", "ordering=-rank&page=1&q=Go", `{"query":"Go","type":"post","ordering":"-rank","page":1,"pages":1,"results":[]}`},
		{"search_posts", `{"query":"C++ & Go? #тест","ordering":"-created_at","page":2}`, "/search/posts.json", "ordering=-created_at&page=2&q=C%2B%2B+%26+Go%3F+%23%D1%82%D0%B5%D1%81%D1%82", `{"results":[]}`},
		{"search_tags", `{"prefix":"C++","group":"tech"}`, "/search/tags.json", "group=tech&prefix=C%2B%2B", `{"tags":[]}`},
	}
	for _, tt := range cases {
		t.Run(tt.name+tt.path, func(t *testing.T) {
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
				if tt.name == "get_post_markdown" {
					w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
				}
				_, _ = io.WriteString(w, tt.body)
			})
			got, err := c.call(context.Background(), "st_fake", tt.name, json.RawMessage(tt.args))
			if err != nil || got == nil || hits != 1 {
				t.Fatalf("result=%v err=%v hits=%d", got, err, hits)
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
	for _, tt := range []struct{ name, args string }{
		{"get_me", `{"url":"https://evil.invalid"}`},
		{"get_user", `{"slug":"../me"}`}, {"get_user", `{"slug":"%2fme"}`},
		{"get_post", `{"post_type":"//evil.invalid","slug":"x"}`},
		{"get_post", `{"post_type":"idea","slug":"x"}`},
		{"get_feed", `{"ordering":"../x"}`}, {"get_feed", `{"page":-1}`},
		{"get_feed", `{"page":10001}`}, {"get_feed", `{"page":1.5}`},
		{"find_user_by_telegram", `{"telegram_id":"1?service_token=bad"}`},
		{"search_users", `{"prefix":"ab"}`}, {"search_users", `{"prefix":"123456789012345"}`},
		{"search_tags", `{"group":"invented"}`}, {"search_tags", `{"prefix":"a"}`},
		{"search_posts", `{}`}, {"search_posts", `{"query":" \n\t "}`},
		{"search_posts", `{"query":123}`}, {"search_posts", `{"query":"ok","page":0}`},
		{"search_posts", `{"query":"ok","page":-1}`}, {"search_posts", `{"query":"ok","page":10001}`},
		{"search_posts", `{"query":"ok","page":1.5}`}, {"search_posts", `{"query":"ok","ordering":"new"}`},
		{"search_posts", `{"query":"ok","url":"https://evil.invalid"}`},
		{"search_posts", `{"query":"` + strings.Repeat("я", 1001) + `"}`},
		{"bookmark_post", `{}`}, {"get_user", `null`}, {"get_user", `[]`},
	} {
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
		{"get_user_tags", `{"slug":"owner"}`, `{"tags":{"unknown":[{"name":"SECRET"}],"tech":[{"code":"go","name":"Go","private":"SECRET"}]}}`},
		{"get_user_badges", `{"slug":"owner"}`, `{"user_badges":[{"badge":{"title":"Safe","private":"SECRET"},"from_user":{"slug":"x","email":"SECRET"},"post":{"id":"123","private":"SECRET"}}]}`},
		{"get_user_achievements", `{"slug":"owner"}`, `{"user_achievements":[{"achievement":{"code":"ok","private":"SECRET"},"private":"SECRET"}]}`},
		{"get_post", `{"post_type":"post","slug":"1"}`, `{"post":{"title":"Safe","private":"SECRET","authors":[{"name":"Safe","private":"SECRET"}],"_club":{"private":"SECRET"}}}`},
		{"list_post_comments", `{"post_type":"post","slug":"1"}`, `{"comments":[{"text":"Safe","private":"SECRET","author":{"email":"SECRET"}}]}`},
		{"search_users", `{"prefix":"abc"}`, `{"users":[{"slug":"Safe","private":"SECRET"}]}`},
		{"search_tags", `{}`, `{"tags":[{"code":"Safe","private":"SECRET"}]}`},
		{"search_posts", `{"query":"abc"}`, `{"private":"SECRET","results":[{"type":"post","private":"SECRET","user":{"email":"SECRET"},"post":{"title":"Safe","private":"SECRET","authors":[{"name":"Safe","email":"SECRET"}],"_club":{"private":"SECRET"}}}]}`},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
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
	if _, err := c.call(context.Background(), "st_fake", "get_post_markdown", json.RawMessage(`{"post_type":"post","slug":"1"}`)); err == nil {
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

func TestSearchPostsPreviewsAndPagination(t *testing.T) {
	for _, size := range []int{0, 1000, 1001} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			text := strings.Repeat("界", size)
			c := mockClub(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(searchPostsResponse{
					Query: "界", Type: "post", Ordering: "-upvotes", Page: 2, Pages: 3,
					Results: []postMatch{{Type: "post", Post: Post{
						Title: "Title", ContentText: text,
						Club: postInfo{Type: "question", Slug: "42", ContentTruncated: true, ContentFullChars: 9999},
					}}},
				})
			})
			got, err := c.call(context.Background(), "st_fake", "search_posts", json.RawMessage(`{"query":"界","ordering":"-upvotes","page":2}`))
			if err != nil {
				t.Fatal(err)
			}
			r := got.(searchPostsResponse)
			if r.Page != 2 || r.Pages != 3 || r.Query != "界" || r.Ordering != "-upvotes" || len(r.Results) != 1 {
				t.Fatalf("metadata: %+v", r)
			}
			p := r.Results[0].Post
			if p.Title != "Title" || p.Club.Slug != "42" || p.Club.Type != "question" {
				t.Fatalf("lost post metadata: %+v", p)
			}
			if size > 1000 {
				if p.ContentText != strings.Repeat("界", 1000) || !p.Club.ContentTruncated || p.Club.ContentFullChars != size {
					t.Fatal("invalid truncated preview")
				}
			} else if p.ContentText != text || p.Club.ContentTruncated || p.Club.ContentFullChars != 0 {
				t.Fatal("invalid untruncated preview")
			}
		})
	}
}

func TestSearchPostsCollectionLimit(t *testing.T) {
	c := mockClub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"results":[`+strings.Repeat(`{},`, maxItems)+`{}]}`)
	})
	if _, err := c.call(context.Background(), "st_fake", "search_posts", json.RawMessage(`{"query":"go"}`)); err == nil || !strings.Contains(err.Error(), "1000 items") {
		t.Fatalf("expected item limit, got %v", err)
	}
}

func TestSearchPostsThroughMCP(t *testing.T) {
	c := mockClub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"query":"go","type":"post","page":1,"pages":1,"results":[{"type":"post","post":{"title":"Found"}}]}`)
	})
	h := newMCPHandler(readTools(), func(ctx context.Context, name string, args json.RawMessage) (any, error) {
		return c.call(ctx, "st_fake", name, args)
	})
	w := mcpTestRequest(h, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search_posts","arguments":{"query":"go"}}}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Found") || strings.Contains(w.Body.String(), `"isError":true`) {
		t.Fatalf("search failed: %s", w.Body.String())
	}
}

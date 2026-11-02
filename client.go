package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const maxJSONBytes = 4 << 20
const maxMarkdownBytes = 1 << 20
const maxItems = 1000

var slugPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
var telegramPattern = regexp.MustCompile(`^[0-9]{1,20}$`)
var postTypes = []string{"post", "intro", "link", "question", "request", "project", "event", "battle", "weekly_digest", "guide", "thread", "docs"}
var orderings = []string{"activity", "new", "top", "top_week", "top_month", "top_year", "hot"}
var groups = []string{"club", "tech", "hobbies", "personal", "collectible", "other"}

func contains(values []string, s string) bool {
	for _, v := range values {
		if v == s {
			return true
		}
	}
	return false
}

type clubClient struct {
	base       *url.URL
	http       *http.Client
	mu         sync.Mutex
	lastStatus string
	lastTime   time.Time
}

func newClubClient(baseURL string) (*clubClient, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("base URL must be an HTTPS origin without credentials, path, query or fragment")
	}
	u.Path = ""
	return &clubClient{base: u, http: &http.Client{
		Timeout: 15 * time.Second,
		// Refuse ALL redirects, even on-origin: a mismatched post type must be fixed
		// by the caller, not silently redirected into a different representation.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, lastStatus: "not checked since startup"}, nil
}

func (c *clubClient) status() (string, time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastStatus, c.lastTime
}
func (c *clubClient) record(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastTime = time.Now().UTC()
	c.lastStatus = "OK"
	if err != nil {
		c.lastStatus = "error: " + err.Error()
	}
}

// Only this function performs upstream I/O. It has no method parameter: every
// request is GET and uses an origin-relative path assembled from validated input.
func (c *clubClient) get(ctx context.Context, token, path string, query url.Values, markdown bool) (body []byte, err error) {
	defer func() { c.record(err) }()
	if !validToken(token) {
		return nil, errors.New("invalid stored service token; open / to replace it")
	}
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.ContainsAny(path, "?#\\") {
		return nil, errors.New("invalid upstream path")
	}
	u := *c.base
	u.Path = path
	u.RawPath = ""
	u.RawQuery = query.Encode()
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if e != nil {
		return nil, errors.New("cannot build upstream request")
	}
	req.Header.Set("X-Service-Token", token)
	req.Header.Set("User-Agent", "vas3k-mcp-go/"+version)
	req.Header.Set("Accept", "application/json, application/feed+json")
	if markdown {
		req.Header.Set("Accept", "text/markdown, text/plain")
	}
	resp, e := c.http.Do(req)
	if e != nil {
		return nil, errors.New("upstream request failed (network or timeout)")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("upstream HTTP %d", resp.StatusCode)
	}
	media, _, e := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if e != nil || (!markdown && media != "application/json" && media != "application/feed+json") || (markdown && media != "text/plain" && media != "text/markdown") {
		return nil, errors.New("unexpected upstream content type")
	}
	limit := int64(maxJSONBytes)
	if markdown {
		limit = maxMarkdownBytes
	}
	body, e = io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if e != nil {
		return nil, errors.New("cannot read upstream response")
	}
	if int64(len(body)) > limit {
		return nil, errors.New("upstream response exceeds size limit")
	}
	if !utf8.Valid(body) {
		return nil, errors.New("invalid upstream text encoding")
	}
	return body, nil
}

func getJSON[T any](ctx context.Context, c *clubClient, token, path string, q url.Values) (T, error) {
	var result T
	b, err := c.get(ctx, token, path, q, false)
	if err != nil {
		return result, err
	}
	// Reject scalars/null/arrays at the envelope while dropping unknown fields at
	// every level when decoding into explicit DTOs.
	if len(bytes.TrimSpace(b)) == 0 || bytes.TrimSpace(b)[0] != '{' {
		err = errors.New("invalid upstream JSON envelope")
	} else if json.Unmarshal(b, &result) != nil {
		err = errors.New("invalid upstream JSON")
	}
	if err != nil {
		c.record(err)
	}
	return result, err
}

func (c *clubClient) me(ctx context.Context, token string) (User, error) {
	r, err := getJSON[userResponse](ctx, c, token, "/user/me.json", nil)
	if err == nil && !slugPattern.MatchString(r.User.Slug) {
		err = errors.New("upstream returned no valid owner profile")
		c.record(err)
	}
	return r.User, err
}

// Separate argument structs prevent accidentally accepting unrelated URL fields.
type slugArgs struct {
	Slug string `json:"slug"`
}
type telegramArgs struct {
	ID string `json:"telegram_id"`
}
type postArgs struct {
	Type string `json:"post_type"`
	Slug string `json:"slug"`
}
type feedArgs struct {
	Type     string `json:"post_type"`
	Ordering string `json:"ordering"`
	Page     int    `json:"page"`
}
type userSearchArgs struct {
	Prefix string `json:"prefix"`
}
type tagSearchArgs struct {
	Prefix string `json:"prefix"`
	Group  string `json:"group"`
}

func decodeArgs(raw json.RawMessage, dst any) error {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	b := bytes.TrimSpace(raw)
	if len(b) == 0 || b[0] != '{' {
		return errors.New("arguments must be an object")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return errors.New("invalid or unknown tool arguments")
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("invalid tool arguments")
	}
	return nil
}
func itemLimit(n int) error {
	if n > maxItems {
		return errors.New("upstream collection exceeds 1000 items; narrow the query")
	}
	return nil
}

func (c *clubClient) call(ctx context.Context, token, name string, raw json.RawMessage) (any, error) {
	switch name {
	case "get_me":
		if err := decodeArgs(raw, &struct{}{}); err != nil {
			return nil, err
		}
		u, err := c.me(ctx, token)
		return userResponse{u}, err
	case "get_user", "get_user_tags", "get_user_badges", "get_user_achievements":
		var a slugArgs
		if err := decodeArgs(raw, &a); err != nil {
			return nil, err
		}
		if !slugPattern.MatchString(a.Slug) {
			return nil, errors.New("invalid slug (1-64 letters, digits, underscore or hyphen)")
		}
		path := "/user/" + a.Slug
		switch name {
		case "get_user":
			return getJSON[userResponse](ctx, c, token, path+".json", nil)
		case "get_user_tags":
			r, e := getJSON[tagsResponse](ctx, c, token, path+"/tags.json", nil)
			if e == nil {
				e = itemLimit(len(r.Tags.Club) + len(r.Tags.Tech) + len(r.Tags.Hobbies) + len(r.Tags.Personal) + len(r.Tags.Collectible) + len(r.Tags.Other))
			}
			return r, e
		case "get_user_badges":
			r, e := getJSON[badgesResponse](ctx, c, token, path+"/badges.json", nil)
			if e == nil {
				e = itemLimit(len(r.Badges))
			}
			return r, e
		default:
			r, e := getJSON[achievementsResponse](ctx, c, token, path+"/achievements.json", nil)
			if e == nil {
				e = itemLimit(len(r.Achievements))
			}
			return r, e
		}
	case "find_user_by_telegram":
		var a telegramArgs
		if err := decodeArgs(raw, &a); err != nil {
			return nil, err
		}
		if !telegramPattern.MatchString(a.ID) {
			return nil, errors.New("invalid Telegram ID (1-20 digits)")
		}
		return getJSON[userResponse](ctx, c, token, "/user/by_telegram_id/"+a.ID+".json", nil)
	case "get_post", "get_post_markdown", "list_post_comments":
		var a postArgs
		if err := decodeArgs(raw, &a); err != nil {
			return nil, err
		}
		if !contains(postTypes, a.Type) || !slugPattern.MatchString(a.Slug) {
			return nil, errors.New("invalid post_type or slug")
		}
		path := "/" + a.Type + "/" + a.Slug
		switch name {
		case "get_post":
			return getJSON[postResponse](ctx, c, token, path+".json", nil)
		case "get_post_markdown":
			b, e := c.get(ctx, token, path+".md", nil, true)
			return markdownResponse{string(b)}, e
		default:
			r, e := getJSON[commentsResponse](ctx, c, token, path+"/comments.json", nil)
			if e == nil {
				e = itemLimit(len(r.Comments))
			}
			return r, e
		}
	case "get_feed":
		var a feedArgs
		if err := decodeArgs(raw, &a); err != nil {
			return nil, err
		}
		if a.Type == "" {
			a.Type = "all"
		}
		if a.Ordering == "" {
			a.Ordering = "activity"
		}
		if a.Page == 0 {
			a.Page = 1
		}
		if (a.Type != "all" && !contains(postTypes, a.Type)) || !contains(orderings, a.Ordering) || a.Page < 1 || a.Page > 10000 {
			return nil, errors.New("invalid feed type, ordering or page (1-10000)")
		}
		path := "/" + a.Type + "/" + a.Ordering + "/feed.json"
		if a.Type == "all" && a.Ordering == "activity" {
			path = "/feed.json"
		}
		r, e := getJSON[feedResponse](ctx, c, token, path, url.Values{"page": {fmt.Sprint(a.Page)}})
		if e == nil {
			e = itemLimit(len(r.Items))
		}
		for i := range r.Items {
			p := &r.Items[i]
			chars := []rune(p.ContentText)
			if len(chars) > 1000 {
				p.ContentText = string(chars[:1000])
				p.Club.ContentTruncated = true
				p.Club.ContentFullChars = len(chars)
			}
		}
		return r, e
	case "search_users":
		var a userSearchArgs
		if err := decodeArgs(raw, &a); err != nil {
			return nil, err
		}
		if n := utf8.RuneCountInString(a.Prefix); n < 3 || n >= 15 {
			return nil, errors.New("prefix must be 3-14 characters")
		}
		r, e := getJSON[searchUsersResponse](ctx, c, token, "/search/users.json", url.Values{"prefix": {a.Prefix}})
		if e == nil {
			e = itemLimit(len(r.Users))
		}
		return r, e
	case "search_tags":
		var a tagSearchArgs
		if err := decodeArgs(raw, &a); err != nil {
			return nil, err
		}
		n := utf8.RuneCountInString(a.Prefix)
		if (a.Prefix != "" && (n < 3 || n > 15)) || (a.Group != "" && !contains(groups, a.Group)) {
			return nil, errors.New("invalid tag prefix (3-15 characters) or group")
		}
		q := url.Values{}
		if a.Prefix != "" {
			q.Set("prefix", a.Prefix)
		}
		if a.Group != "" {
			q.Set("group", a.Group)
		}
		r, e := getJSON[searchTagsResponse](ctx, c, token, "/search/tags.json", q)
		if e == nil {
			e = itemLimit(len(r.Tags))
		}
		return r, e
	default:
		return nil, errors.New("unknown read tool")
	}
}

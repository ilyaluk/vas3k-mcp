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
var searchOrderings = []string{"-rank", "-upvotes", "-created_at", "created_at"}
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
type getUserArgs struct {
	Slug       *string  `json:"slug"`
	TelegramID *string  `json:"telegram_id"`
	Include    []string `json:"include"`
}
type postArgs struct {
	Type string `json:"post_type"`
	Slug string `json:"slug"`
}
type getPostArgs struct {
	postArgs
	Format *string `json:"format"`
}
type feedArgs struct {
	Type     string `json:"post_type"`
	Ordering string `json:"ordering"`
	Page     int    `json:"page"`
}
type searchArgs struct {
	Scope    *string `json:"scope"`
	Query    *string `json:"query"`
	Prefix   *string `json:"prefix"`
	Group    *string `json:"group"`
	Ordering *string `json:"ordering"`
	Page     *int    `json:"page"`
}

func decodeArgs(raw json.RawMessage, dst any) error {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	b := bytes.TrimSpace(raw)
	if len(b) == 0 || b[0] != '{' {
		return errors.New("arguments must be an object")
	}
	// JSON null would otherwise silently become a scalar's zero value or a
	// missing pointer, bypassing presence checks for incompatible arguments.
	fields := json.NewDecoder(bytes.NewReader(b))
	if _, err := fields.Token(); err != nil {
		return errors.New("invalid tool arguments")
	}
	seen := map[string]bool{}
	for fields.More() {
		key, err := fields.Token()
		if err != nil {
			return errors.New("invalid tool arguments")
		}
		name, ok := key.(string)
		if !ok {
			return errors.New("invalid tool arguments")
		}
		// Schema property names are lowercase ASCII snake_case. Reject aliases
		// that encoding/json would otherwise match using Unicode case folding.
		for _, ch := range name {
			if (ch < 'a' || ch > 'z') && ch != '_' {
				return errors.New("invalid tool argument name")
			}
		}
		if seen[name] {
			return errors.New("duplicate tool argument")
		}
		seen[name] = true
		var value json.RawMessage
		if err := fields.Decode(&value); err != nil {
			return errors.New("invalid tool arguments")
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return errors.New("tool arguments must not be null")
		}
	}
	if _, err := fields.Token(); err != nil {
		return errors.New("invalid tool arguments")
	}
	if fields.Decode(new(any)) != io.EOF {
		return errors.New("invalid tool arguments")
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
	case "get_user":
		var a getUserArgs
		if err := decodeArgs(raw, &a); err != nil {
			return nil, err
		}
		return c.getUser(ctx, token, a)
	case "get_post":
		var a getPostArgs
		if err := decodeArgs(raw, &a); err != nil {
			return nil, err
		}
		if !contains(postTypes, a.Type) || !slugPattern.MatchString(a.Slug) {
			return nil, errors.New("invalid post_type or slug")
		}
		format := "json"
		if a.Format != nil {
			format = *a.Format
		}
		if format != "json" && format != "markdown" {
			return nil, errors.New("format must be json or markdown")
		}
		path := "/" + a.Type + "/" + a.Slug
		if format == "markdown" {
			b, err := c.get(ctx, token, path+".md", nil, true)
			if err != nil {
				return nil, err
			}
			return markdownResponse{string(b)}, nil
		}
		return getJSON[postResponse](ctx, c, token, path+".json", nil)
	case "list_post_comments":
		var a postArgs
		if err := decodeArgs(raw, &a); err != nil {
			return nil, err
		}
		if !contains(postTypes, a.Type) || !slugPattern.MatchString(a.Slug) {
			return nil, errors.New("invalid post_type or slug")
		}
		r, err := getJSON[commentsResponse](ctx, c, token, "/"+a.Type+"/"+a.Slug+"/comments.json", nil)
		if err == nil {
			err = itemLimit(len(r.Comments))
		}
		if err != nil {
			return nil, err
		}
		// Full comment lists never contain search-only preview metadata, even if
		// an upstream response tries to supply it.
		for i := range r.Comments {
			r.Comments[i].Club = nil
		}
		return r, nil
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
			previewPost(p)
		}
		return r, e
	case "search":
		var a searchArgs
		if err := decodeArgs(raw, &a); err != nil {
			return nil, err
		}
		return c.search(ctx, token, a)
	default:
		return nil, errors.New("unknown read tool")
	}
}

func (c *clubClient) getUser(ctx context.Context, token string, a getUserArgs) (any, error) {
	if a.TelegramID != nil {
		if a.Slug != nil {
			return nil, errors.New("telegram_id and slug are mutually exclusive")
		}
		if !telegramPattern.MatchString(*a.TelegramID) {
			return nil, errors.New("invalid Telegram ID (1-20 digits)")
		}
	}
	slug := "me"
	if a.Slug != nil {
		slug = *a.Slug
	}
	if !slugPattern.MatchString(slug) {
		return nil, errors.New("invalid slug (1-64 letters, digits, underscore or hyphen)")
	}
	if a.Include == nil {
		a.Include = []string{"profile"}
	}
	if len(a.Include) < 1 || len(a.Include) > 4 {
		return nil, errors.New("include must contain 1-4 unique sections")
	}
	sections := map[string]bool{}
	for _, section := range a.Include {
		if !contains([]string{"profile", "tags", "badges", "achievements"}, section) || sections[section] {
			return nil, errors.New("include must contain unique profile, tags, badges or achievements sections")
		}
		sections[section] = true
	}

	var result getUserResponse
	if a.TelegramID != nil {
		r, err := getJSON[userResponse](ctx, c, token, "/user/by_telegram_id/"+*a.TelegramID+".json", nil)
		if err != nil {
			return nil, err
		}
		if !slugPattern.MatchString(r.User.Slug) {
			err := errors.New("upstream returned no valid user slug")
			c.record(err)
			return nil, err
		}
		slug = r.User.Slug
		if sections["profile"] {
			result.User = &r.User
		}
	} else if sections["profile"] {
		var u User
		var err error
		if slug == "me" {
			u, err = c.me(ctx, token)
		} else {
			var r userResponse
			r, err = getJSON[userResponse](ctx, c, token, "/user/"+slug+".json", nil)
			u = r.User
		}
		if err != nil {
			return nil, err
		}
		result.User = &u
	}
	path := "/user/" + slug
	if sections["tags"] {
		r, err := getJSON[tagsResponse](ctx, c, token, path+"/tags.json", nil)
		if err == nil {
			err = itemLimit(len(r.Tags.Club) + len(r.Tags.Tech) + len(r.Tags.Hobbies) + len(r.Tags.Personal) + len(r.Tags.Collectible) + len(r.Tags.Other))
		}
		if err != nil {
			return nil, err
		}
		for _, group := range []*[]Tag{&r.Tags.Club, &r.Tags.Tech, &r.Tags.Hobbies, &r.Tags.Personal, &r.Tags.Collectible, &r.Tags.Other} {
			if *group == nil {
				*group = []Tag{}
			}
		}
		result.Tags = &r.Tags
	}
	if sections["badges"] {
		r, err := getJSON[badgesResponse](ctx, c, token, path+"/badges.json", nil)
		if err == nil {
			err = itemLimit(len(r.Badges))
		}
		if err != nil {
			return nil, err
		}
		if r.Badges == nil {
			r.Badges = []badge{}
		}
		result.Badges = &r.Badges
	}
	if sections["achievements"] {
		r, err := getJSON[achievementsResponse](ctx, c, token, path+"/achievements.json", nil)
		if err == nil {
			err = itemLimit(len(r.Achievements))
		}
		if err != nil {
			return nil, err
		}
		if r.Achievements == nil {
			r.Achievements = []achievement{}
		}
		result.Achievements = &r.Achievements
	}
	return result, nil
}

func (c *clubClient) search(ctx context.Context, token string, a searchArgs) (any, error) {
	scope := "all"
	if a.Scope != nil {
		scope = *a.Scope
	}
	if !contains([]string{"all", "posts", "comments", "users", "tags"}, scope) {
		return nil, errors.New("invalid search scope")
	}
	if a.Query != nil && a.Prefix != nil {
		return nil, errors.New("query and prefix are mutually exclusive")
	}
	if a.Group != nil && scope != "tags" {
		return nil, errors.New("group is only allowed for tags")
	}
	if a.Prefix != nil && scope != "users" {
		return nil, errors.New("prefix is only allowed for users")
	}
	if scope == "tags" {
		if a.Ordering != nil || a.Page != nil {
			return nil, errors.New("tag search does not support ordering or page")
		}
		q := url.Values{}
		if a.Query != nil {
			query := strings.TrimSpace(*a.Query)
			if n := utf8.RuneCountInString(query); n < 3 || n > 15 {
				return nil, errors.New("tag query must be 3-15 characters after trimming whitespace")
			}
			q.Set("prefix", query)
		}
		if a.Group != nil {
			if !contains(groups, *a.Group) {
				return nil, errors.New("invalid tag group")
			}
			q.Set("group", *a.Group)
		}
		r, err := getJSON[searchTagsResponse](ctx, c, token, "/search/tags.json", q)
		if err == nil {
			err = itemLimit(len(r.Tags))
		}
		if err != nil {
			return nil, err
		}
		if r.Tags == nil {
			r.Tags = []tagMatch{}
		}
		return r, nil
	}
	if a.Prefix != nil {
		if a.Ordering != nil || a.Page != nil {
			return nil, errors.New("user autocomplete does not support ordering or page")
		}
		if n := utf8.RuneCountInString(*a.Prefix); n < 3 || n > 14 {
			return nil, errors.New("prefix must be 3-14 characters")
		}
		r, err := getJSON[searchUsersResponse](ctx, c, token, "/search/users.json", url.Values{"prefix": {*a.Prefix}})
		if err == nil {
			err = itemLimit(len(r.Users))
		}
		if err != nil {
			return nil, err
		}
		if r.Users == nil {
			r.Users = []userMatch{}
		}
		return r, nil
	}
	if a.Query == nil {
		return nil, errors.New("query is required for full-text search")
	}
	query := strings.TrimSpace(*a.Query)
	if n := utf8.RuneCountInString(query); n < 1 || n > 1000 {
		return nil, errors.New("query must be 1-1000 characters after trimming whitespace")
	}
	ordering, page := "-rank", 1
	if a.Ordering != nil {
		ordering = *a.Ordering
	}
	if a.Page != nil {
		page = *a.Page
	}
	if !contains(searchOrderings, ordering) || page < 1 || page > 10000 {
		return nil, errors.New("invalid search ordering or page (1-10000)")
	}
	path := "/search.json"
	q := url.Values{"q": {query}, "ordering": {ordering}, "page": {fmt.Sprint(page)}}
	switch scope {
	case "posts":
		path = "/search/posts.json"
	case "comments":
		q.Set("type", "comment")
	case "users":
		q.Set("type", "user")
	}
	r, err := getJSON[searchResponse](ctx, c, token, path, q)
	if err == nil {
		err = itemLimit(len(r.Results))
	}
	if err != nil {
		return nil, err
	}
	if r.Results == nil {
		r.Results = []searchResult{}
	}
	for i := range r.Results {
		item := &r.Results[i]
		if err := item.normalize(); err != nil {
			c.record(err)
			return nil, err
		}
	}
	return r, nil
}

// Enforce the tagged union rather than forwarding unrelated payload variants.
// Missing/unknown primary variants are malformed upstream data, not empty hits.
func (r *searchResult) normalize() error {
	switch r.Type {
	case "post":
		if r.Post == nil {
			return errors.New("upstream search result has no post")
		}
		r.Comment, r.User = nil, nil
		previewPost(r.Post)
	case "comment":
		if r.Comment == nil {
			return errors.New("upstream search result has no comment")
		}
		r.Post, r.User = nil, nil
		previewComment(r.Comment)
	case "user":
		if r.User == nil {
			return errors.New("upstream search result has no user")
		}
		r.Post, r.Comment = nil, nil
	default:
		return errors.New("upstream search result has an invalid type")
	}
	return nil
}

// Apply the same Unicode-safe preview policy to all post collections.
func previewPost(p *Post) {
	chars := []rune(p.ContentText)
	// These fields are local metadata, not upstream claims.
	p.Club.ContentTruncated = len(chars) > 1000
	p.Club.ContentFullChars = 0
	if p.Club.ContentTruncated {
		p.ContentText = string(chars[:1000])
		p.Club.ContentFullChars = len(chars)
	}
}

func previewComment(c *comment) {
	chars := []rune(c.Text)
	// Ignore upstream metadata; this is generated only for search previews.
	c.Club = nil
	if len(chars) > 1000 {
		c.Text = string(chars[:1000])
		c.Club = &contentPreview{ContentTruncated: true, ContentFullChars: len(chars)}
	}
}

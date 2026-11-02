package main

// Validation is repeated in the club client: schema hints are not a URL boundary.
func readTools() []Tool {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	enum := func(values []string) map[string]any { return map[string]any{"type": "string", "enum": values} }
	page := func() map[string]any { return map[string]any{"type": "integer", "minimum": 1, "maximum": 10000} }
	tools := []Tool{}
	add := func(name, desc string, props map[string]any, required ...string) {
		schema := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
		if len(required) > 0 {
			schema["required"] = required
		}
		tools = append(tools, Tool{Name: name, Description: desc, InputSchema: schema, Annotations: ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: true}})
	}
	add("get_user", "Read a member; defaults to self (slug me). include selects returned sections (default [profile]); others aren't fetched, except Telegram lookup resolves a profile first. Use slug OR telegram_id. Contact email/Telegram account data are omitted.", map[string]any{
		"slug":        str("Member slug: 1-64 letters, digits, underscore or hyphen; default me."),
		"telegram_id": str("Numeric Telegram ID (1-20 digits), instead of slug."),
		"include":     map[string]any{"type": "array", "items": enum([]string{"profile", "tags", "badges", "achievements"}), "minItems": 1, "maxItems": 4, "uniqueItems": true},
	})
	postProps := func() map[string]any {
		return map[string]any{"post_type": enum(postTypes), "slug": str("Post slug: 1-64 letters, digits, underscore or hyphen.")}
	}
	post := postProps()
	post["format"] = enum([]string{"json", "markdown"})
	add("get_post", "Read a full post. format defaults to json (post and metadata); markdown returns raw title/body as {markdown}.", post, "post_type", "slug")
	add("list_post_comments", "Read full visible comments, oldest first. Maximum 1000; larger responses fail explicitly.", postProps(), "post_type", "slug")
	feedTypes := append([]string{"all"}, postTypes...)
	add("get_feed", "Browse the feed; defaults all/activity/page 1. Post bodies are 1000-character previews with _club truncation markers; get_post reads full text. Retain filters when paging; upstream next_url may drop them.", map[string]any{"post_type": enum(feedTypes), "ordering": enum(orderings), "page": page()})
	add("search", "Search: scope defaults all (posts/comments/users), or posts (excludes intros), comments, users, tags. Full-text query uses ordering/page (defaults -rank/1); returns results plus page/pages. Retain query/scope/ordering when paging. Post/comment previews cap at 1000 characters with _club truncation markers; read full text via get_post/list_post_comments. users+prefix autocompletes slugs (3-14 chars, max25), no query/ordering/page. tags uses optional query substring (3-15 chars) and/or group, no prefix/ordering/page (max1000).", map[string]any{
		"scope":    enum([]string{"all", "posts", "comments", "users", "tags"}),
		"query":    map[string]any{"type": "string", "minLength": 1, "maxLength": 1000, "description": "Required nonblank full-text query; optional tag-name substring for tags."},
		"prefix":   map[string]any{"type": "string", "minLength": 3, "maxLength": 14, "description": "Username autocomplete, scope users only; mutually exclusive with query."},
		"group":    enum(groups),
		"ordering": enum(searchOrderings),
		"page":     page(),
	})
	return tools
}

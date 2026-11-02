package main

import (
	"bytes"
	"context"
	"encoding/json"
	"html/template"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// publicScheme assumes a private reverse proxy strips client-supplied
// X-Forwarded-Proto and supplies exactly "https" for HTTPS requests. Do not
// expose this unauthenticated application directly to untrusted callers.
func publicScheme(r *http.Request) string {
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		return "https"
	}
	return "http"
}

// exe.dev supplies the original authority in X-Forwarded-Host. Trust that
// header only behind a proxy that replaces client-supplied values. Never pick
// one entry from an ambiguous forwarded chain or silently fall back on errors.
func publicHost(r *http.Request) string {
	host := r.Host
	if values := r.Header.Values("X-Forwarded-Host"); len(values) != 0 {
		if len(values) != 1 {
			return ""
		}
		host = values[0]
	}
	if host == "" || strings.ContainsAny(host, ",/@?#\\%") ||
		strings.IndexFunc(host, func(c rune) bool { return unicode.IsSpace(c) || unicode.IsControl(c) }) >= 0 {
		return ""
	}
	u, err := url.Parse("https://" + host)
	if err != nil || u.User != nil || u.Host != host || u.Hostname() == "" || strings.HasSuffix(host, ":") {
		return ""
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return ""
		}
	}
	return host
}

// sameOrigin deliberately does not treat sibling subdomains as trusted.
// Non-browser clients must send an explicit matching Origin header.
func sameOrigin(r *http.Request) bool {
	fetchHeaders := r.Header.Values("Sec-Fetch-Site")
	if len(fetchHeaders) > 1 {
		return false
	}
	fetch := r.Header.Get("Sec-Fetch-Site")
	if fetch != "" && fetch != "same-origin" && fetch != "none" {
		return false
	}
	origins := r.Header.Values("Origin")
	if len(origins) == 0 {
		return fetch == "same-origin"
	}
	if len(origins) != 1 || origins[0] == "" {
		return false
	}
	origin, err := url.Parse(origins[0])
	if err != nil || origin.User != nil || origin.Opaque != "" ||
		origin.Path != "" || origin.RawQuery != "" || origin.ForceQuery ||
		origin.Fragment != "" || origin.Host == "" ||
		origins[0] != origin.Scheme+"://"+origin.Host {
		return false
	}
	host := publicHost(r)
	return host != "" && origin.Scheme == publicScheme(r) && strings.EqualFold(origin.Host, host)
}

type webPage struct {
	Configured     bool
	Slug           string
	Name           string
	MCPURL         string
	Check          string
	CheckedAt      string
	Version        string
	Error          string
	VMTokenCommand string
	ClientCommand  string
	ClientJSON     string
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

// Display-only examples. Never execute these strings or use the inferred VM
// name for authorization, upstream selection, or credential handling.
func clientExamples(host, mcpURL string) (vmCommand, clientCommand, clientJSON string) {
	u, err := url.Parse("https://" + host)
	if err == nil {
		labels := strings.Split(strings.ToLower(u.Hostname()), ".")
		if len(labels) == 3 && labels[1] == "exe" && labels[2] == "xyz" &&
			slugPattern.MatchString(labels[0]) && !strings.Contains(labels[0], "_") {
			vmCommand = "ssh exe.dev ssh-key generate-api-key --vm=" + labels[0] + " --label=vas3k-claude --exp=90d"
		}
	}
	if vmCommand == "" {
		vmCommand = "ssh exe.dev ssh-key generate-api-key --vm=<vm-name> --label=vas3k-claude --exp=90d"
	}
	clientCommand = "claude mcp add --transport http --scope user vas3k " + shellQuote(mcpURL) +
		" --header 'X-Exedev-Authorization: Bearer <token>'"
	config := map[string]any{"mcpServers": map[string]any{"vas3k": map[string]any{
		"type": "http", "url": mcpURL,
		"headers": map[string]string{"X-Exedev-Authorization": "Bearer <token>"},
	}}}
	var b bytes.Buffer
	encoder := json.NewEncoder(&b)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false) // html/template escapes the whole example.
	_ = encoder.Encode(config)
	return vmCommand, clientCommand, strings.TrimSpace(b.String())
}

var setupTemplate = template.Must(template.New("setup").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Club MCP</title><style>
body{font:16px/1.5 system-ui,sans-serif;max-width:44rem;margin:3rem auto;padding:0 1rem;color:#202124;background:#fafafa}
h1{font-size:1.7rem}section{margin:1.5rem 0;padding:1rem;border:1px solid #ccc;border-radius:6px}
label{display:block}input{box-sizing:border-box;width:100%;padding:.6rem;margin:.4rem 0}button{padding:.5rem 1rem;cursor:pointer}
code{overflow-wrap:anywhere}pre{white-space:pre-wrap;overflow-wrap:anywhere;font-size:.85rem}.error{color:#a01818}small{color:#555}
</style></head><body>
<h1>Club MCP</h1>
{{if .Error}}<p class="error" role="alert">{{.Error}}</p>{{end}}
<section><h2>Status</h2>
{{if .Configured}}<p>Configured. {{if .Slug}}Logged in as <strong>{{.Name}}</strong> (<code>{{.Slug}}</code>).{{else}}Identity could not be verified.{{end}}</p>
{{else}}<p>Not configured.</p>{{end}}
<p>MCP URL: <code>{{.MCPURL}}</code></p>
<p>Last upstream check: {{.Check}}{{if .CheckedAt}} — {{.CheckedAt}}{{end}}</p>
<small>Version {{.Version}}</small></section>
{{if .Configured}}<section><h2>Connect your MCP client</h2>
<p><strong>Manually on your own computer, not on this VM</strong>, run:</p>
<pre><code>{{.VMTokenCommand}}</code></pre>
<p>This creates an <strong>exe.dev VM-scoped token</strong>, not a club service token.
The server never generates it. With the preferred header below, the proxy strips it before it reaches this server.
Do not paste it into the setup form.</p>
<p><strong>Clients with HTTP Basic auth:</strong> use the MCP URL above, any username,
and the VM token as the password. Use credential settings, not a token embedded in the URL.</p>
<p><strong>claude.ai:</strong> add a custom connector with URL <code>{{.MCPURL}}</code>.
Choose <strong>No sign in</strong> and the <strong>Request headers</strong> option (limited beta).
Set <code>X-Exedev-Authorization</code> to <code>Bearer &lt;token&gt;</code>.
If that custom header name isn't available, use <code>Authorization: Bearer &lt;token&gt;</code>
(exe.dev's deprecated fallback). The app never uses or stores either client header.</p>
<p><strong>Claude Code</strong> (replace <code>&lt;token&gt;</code> locally):</p>
<pre><code>{{.ClientCommand}}</code></pre>
<details><summary>JSON example for clients supporting remote HTTP configuration</summary>
<pre><code>{{.ClientJSON}}</code></pre></details>
<p>Keep the proxy and connector private. Anyone with this token acts as you.
It expires after <strong>90 days</strong>: renew before expiry, update your client's header, and revoke the old token/key.
Never share either token.</p>
<small>Docs: <a href="https://exe.dev/docs/https-tokens-for-vms" target="_blank" rel="noopener noreferrer">exe.dev tokens</a> ·
<a href="https://claude.com/docs/connectors/custom/add-unlisted" target="_blank" rel="noopener noreferrer">Claude connectors</a></small>
</section>{{end}}
<section><h2>{{if .Configured}}Replace token{{else}}Connect your account{{end}}</h2>
<ol><li>Open <a href="https://vas3k.club/apps/create/" target="_blank" rel="noopener noreferrer">Create an app</a> on the club website.</li>
<li>Give it a name such as “Personal MCP” and a short description. For the website URL, use any valid personal or project URL; it is not used by this server. If a redirect URL is required, any valid URL works (for example <code>https://example.com/</code>); this app does not use redirects.</li>
<li>Create the app and copy its service token (starting with <code>st_</code>) below. It is checked with the club before being saved.</li></ol>
<form method="post" action="/"><input type="hidden" name="action" value="save">
<label for="token">Service token</label><input id="token" type="password" name="token" maxlength="512" autocomplete="off" spellcheck="false" required>
<button type="submit">Verify and save</button></form></section>
{{if .Configured}}<form method="post" action="/"><input type="hidden" name="action" value="forget"><button type="submit">Forget token</button></form>{{end}}
<p><small>This page has no authentication. Keep it behind your private, authenticated proxy. Tokens are stored only on this server.</small></p>
</body></html>`))

func newWebHandler(s *store, me func(context.Context, string) (User, error), status func() (string, time.Time)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// Credentials are accepted only in the bounded POST form, never URLs.
		if r.URL.RawQuery != "" || r.URL.ForceQuery {
			http.Error(w, "Query parameters are not accepted", http.StatusBadRequest)
			return
		}
		render := func(code int, message string, identity *User) {
			page := webPage{
				Configured: s.Token() != "", MCPURL: publicScheme(r) + "://" + publicHost(r) + "/mcp",
				Version: version, Error: message, Check: "Not checked",
			}
			page.VMTokenCommand, page.ClientCommand, page.ClientJSON = clientExamples(publicHost(r), page.MCPURL)
			if identity != nil {
				page.Slug, page.Name = identity.Slug, identity.FullName
			}
			if status != nil {
				check, checkedAt := status()
				if !checkedAt.IsZero() {
					page.CheckedAt = checkedAt.UTC().Format(time.RFC3339)
					if strings.EqualFold(check, "ok") {
						page.Check = "OK"
					} else if check != "" {
						page.Check = "Error"
					}
				}
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(code)
			_ = setupTemplate.Execute(w, page)
		}
		if r.Method == http.MethodGet {
			var identity *User
			if token := s.Token(); token != "" {
				user, err := me(r.Context(), token)
				if err == nil {
					identity = &user
				}
			}
			render(http.StatusOK, "", identity)
			return
		}
		if !sameOrigin(r) {
			http.Error(w, "Same-origin request required", http.StatusForbidden)
			return
		}
		contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || contentType != "application/x-www-form-urlencoded" {
			http.Error(w, "Expected a URL-encoded form", http.StatusUnsupportedMediaType)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid or oversized form", http.StatusBadRequest)
			return
		}
		if len(r.PostForm["action"]) != 1 {
			http.Error(w, "One action is required", http.StatusBadRequest)
			return
		}
		switch r.PostForm.Get("action") {
		case "save":
			tokens := r.PostForm["token"]
			if len(tokens) != 1 || !validToken(tokens[0]) {
				render(http.StatusBadRequest, "Enter a valid service token starting with st_.", nil)
				return
			}
			if _, err := me(r.Context(), tokens[0]); err != nil {
				render(http.StatusBadRequest, "Could not verify the token with the club. The saved token has not changed.", nil)
				return
			}
			if err := s.Replace(tokens[0]); err != nil {
				render(http.StatusInternalServerError, "Could not save the token.", nil)
				return
			}
		case "forget":
			if err := s.Forget(); err != nil {
				render(http.StatusInternalServerError, "Could not forget the token.", nil)
				return
			}
		default:
			http.Error(w, "Unknown action", http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
	})
}

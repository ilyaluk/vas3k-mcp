# vas3k-mcp (Go)

A small, single-owner, read-only Go port of [uburuntu/vas3k-mcp][original].
MIT licensed; upstream attribution and [LICENSE](LICENSE) are retained.
Only the Go standard library is used: no third-party modules or transitive
dependencies. The empty `go.sum` is intentional.

**AI disclosure:** This Go port is entirely vibe-coded with Shelley (AI),
including the implementation, tests, and documentation. Passing automated
checks is not a substitute for an independent security review.

## Scope

`GET /` is the browser setup/status page; `/mcp` is a stateless, hand-written,
limited MCP Streamable HTTP implementation with JSON responses. It is not a
general MCP framework: no resources, prompts, writes, OAuth, legacy SSE,
session persistence, or resumable event streams.
Supported initialize-based protocol revisions are **2025-11-25**,
**2025-06-18**, and **2025-03-26**; this is not a claim of latest-spec support.

Five read tools cover the original reads plus full-text search:

| Tool | Reads |
| --- | --- |
| `get_user` | Self/member profile and selected tags, badges, achievements; slug or Telegram ID lookup |
| `get_post` | Full post as JSON or Markdown |
| `list_post_comments` | Full comments, independently of the post body |
| `get_feed` | Filtered/ordered feed |
| `search` | Full-text posts/comments/users, username autocomplete, or tags |

Their upstream endpoints and service-token authentication are documented in
the club's [`templates/openid/list_apps.html`][club-api] and
[`search/api.py`][club-search], pinned at
`e35d73be53f6a12c0bf40c3522999e655740dc3b`. Other club API features are
deliberately not exposed.
Private contact email/Telegram fields are deliberately omitted from profiles.
Feed and search previews are capped at 1,000 Unicode characters with explicit truncation
flags; Markdown is the raw title/text, not rendered HTML. Upstream requests
time out after 15 seconds, with response limits of 4 MiB for JSON and 1 MiB
for Markdown, and a maximum of 1,000 collection items.

### Search

One `search` tool covers these modes; `scope` defaults to `all`:

| Arguments | Behavior |
| --- | --- |
| `{"query":"golang"}` | Full-text search across posts, comments, and users |
| `{"scope":"posts","query":"golang"}` | Posts only (upstream excludes intros) |
| `{"scope":"comments","query":"golang"}` | Comments only |
| `{"scope":"users","query":"engineer"}` | Full-text member search, not just usernames |
| `{"scope":"users","prefix":"vas"}` | Username autocomplete: 3–14 characters, up to 25 matches, most recently active first |
| `{"scope":"tags","query":"gol","group":"tech"}` | Tag-name substring (3–15 characters) and/or group; both optional |

Full-text queries must be nonblank and at most 1,000 Unicode characters after
trimming whitespace. They accept `ordering`: `-rank` (default, relevance),
`-upvotes`, `-created_at` (newest first), or `created_at` (oldest first), and
`page` (default 1, range 1–10,000). Query syntax is passed through, including
`author:vas3k`, `title:…`, `since:2024`, `until:2024-03-09`, phrases, `AND`/`OR`.

Full-text results include `query`, `type` (null for all), `ordering`, `page`,
`pages`, and `results` containing typed `post`, `comment`, or `user` objects.
Post/comment text is previewed at 1,000 Unicode characters, with locally computed
`_club.content_truncated` and `_club.content_full_chars` markers when truncated.
Use `get_post` for full post text or `list_post_comments` for full comments;
search results retain post `_club.type`/`slug` and comment URLs/IDs.
Retain query, scope, and ordering when paging; stop at returned `page >= pages`
(upstream may clamp out-of-range pages).

Username autocomplete returns `{users:[…]}` and tag search returns `{tags:[…]}`.
Autocomplete cannot combine `query` with `prefix`. `prefix` is only for users;
`group` is only for tags. Autocomplete/tag modes reject `page` and `ordering`,
rather than silently ignore them. Tag search is capped at 1,000 items.

Full-text search uses `/search.json` (with a type filter for users/comments),
and `/search/posts.json` for posts. Member full-text search deliberately uses
the generic, paginated user search rather than the compact, activity-sorted
25-result `/search/users.json?q=…` variant. Autocomplete uses
`/search/users.json?prefix=…`; tags use `/search/tags.json`.

### Fetching members and posts

`get_user` defaults to the owner (`slug: "me"`) and `include: ["profile"]`.
Choose any nonempty, duplicate-free subset of `profile`, `tags`, `badges`,
`achievements`. Only requested sections are returned; slug-based extras-only
requests avoid downloading the profile. For example:

```json
{"slug":"vas3k","include":["profile","tags"]}
```

Use `telegram_id` **instead of** `slug` for numeric Telegram lookup. When
extras are requested, the client resolves the member's slug first. Profile
results use `user`, with optional `tags`, `user_badges`, `user_achievements`
keys. A failed section fails the whole call; no incomplete success is returned.

`get_post` requires `post_type` and `slug`. `format` defaults to `json`
(`{post:…}`); `format: "markdown"` returns `{markdown:…}`. Comments stay a
separate tool to avoid fetching a potentially large thread with every post.
`get_feed` remains separate because feed browsing and relevance search have
different filters and ordering.

### Migration from the 13-tool interface

This is a breaking tool-schema change. Refresh/reconnect MCP clients to load
five tools; old names are removed, not advertised or retained as hidden aliases:

| Old call | New call |
| --- | --- |
| `get_me {}` | `get_user {}` |
| `find_user_by_telegram {telegram_id}` | `get_user {telegram_id}` |
| `get_user_tags/get_user_badges/get_user_achievements {slug}` | `get_user {slug,include:["tags"/"badges"/"achievements"]}` |
| `get_post_markdown {post_type,slug}` | `get_post {post_type,slug,format:"markdown"}` |
| `search_posts {query,…}` | `search {scope:"posts",query,…}` |
| `search_users {prefix}` | `search {scope:"users",prefix}` |
| `search_tags {prefix,group}` | `search {scope:"tags",query:prefix,group}` |

`get_user {slug}`, `get_post {post_type,slug}`, `get_feed`, and
`list_post_comments` retain their default behavior. Consolidation reduces
repeated schemas/descriptions; section selection and bounded previews also
avoid returning unnecessary content. No write tools were added.

## Build and run

Requires Go **1.26+** on Linux (development VM: Go 1.27.3).
An advisory file lock enforces one process per state directory.

```sh
go test ./...
go vet ./...
CGO_ENABLED=0 go build -trimpath -o vas3k-mcp .
./vas3k-mcp
```

| Flag | Environment fallback | Default |
| --- | --- | --- |
| `-listen` | `VAS3K_LISTEN` | `0.0.0.0:8000` |
| `-state-dir` | `VAS3K_STATE_DIR` | `./state` |
| `-base-url` | None; flag only | `https://vas3k.club` |

Explicit flags override environment values. The upstream base URL must use
HTTPS; change it only to an origin you trust with the club credential.
There is no token flag or token environment variable: use the setup page.

## Deploy on exe.dev

Keep the exe.dev HTTPS proxy **private**. The server intentionally has **no
application authentication** and listens on `0.0.0.0:8000`, as required for
the [exe.dev proxy deployment][exe-mcp]. Never expose that socket through a
public ingress, an untrusted network, or another bypass of the private proxy.
Use a dedicated VM; don't grant untrusted users proxy access.

From the checkout, after the build above:

```sh
sudo install -o root -g root -m 0755 vas3k-mcp /usr/local/bin/vas3k-mcp
sudo install -o root -g root -m 0644 deploy/vas3k-mcp.service /etc/systemd/system/vas3k-mcp.service
sudo systemctl daemon-reload
sudo systemctl enable --now vas3k-mcp.service
sudo systemctl status vas3k-mcp.service --no-pager
```

No user creation is needed: `DynamicUser=yes` allocates a temporary service
identity. `StateDirectory=vas3k-mcp` keeps credentials in
`/var/lib/vas3k-mcp` across restarts with mode `0700`; systemd manages
ownership. Its filesystem is otherwise read-only, home directories
inaccessible, and privileges restricted. See [systemd's documentation][systemd-exec].
For updates, test/build again, stop the service, reinstall the binary, then
start it. Logs: `sudo journalctl -u vas3k-mcp.service -n 50 --no-pager`.

### Connect your club account in the browser

Open `https://<vm>.exe.xyz/` and sign into exe.dev. The setup page guides you:

1. Open [club apps](https://vas3k.club/apps/) and
   [create your own app](https://vas3k.club/apps/create/).
2. Supply a name (e.g. `My vas3k MCP`) and description—the only required fields.
   Leave the website and redirect URI blank. This port does not perform OAuth
   or need a callback, client ID, or client secret.
   See the [club app form][club-form].
3. Copy the app's **service token** (`st_…`) into this VM's setup form, never
   into chat. The server verifies it using `GET /user/me.json` with
   `X-Service-Token`, then atomically saves it in a `0600` file inside the
   `0700` state directory.

Once configured, `/` shows the owner identity, MCP URL, last credential check,
and server version, with browser controls to replace or forget the token.
Forgetting removes the local credential, **not** the upstream app/token.
If leaked, delete/recreate the app as the [club documentation][club-api]
instructs.

## Connect your MCP client

On **your own computer**, generate an exe.dev VM token (not the club service
token). Replace `<vm>` with your VM name:

```sh
ssh exe.dev ssh-key generate-api-key --vm=<vm> --label=vas3k-claude --exp=90d
```

- **Server URL:** `https://<vm>.exe.xyz/mcp` (Streamable HTTP).
- **HTTP Basic auth:** any username, VM token as password.
  [exe.dev supports this][exe-tokens]; use your client's credential settings,
  not credentials embedded in the URL.
- **claude.ai:** choose **No sign-in**, then set
  `X-Exedev-Authorization: Bearer <VM_TOKEN>` under
  [**Request headers** (beta)][claude-headers].
  If that custom header name isn't available, use `Authorization: Bearer <VM_TOKEN>`.

Keep the proxy/connector private. Renew before the **90-day expiry** and update
your client; revoke leaked tokens. The configured `/` page has more examples.
The app never generates, uses, or stores proxy credentials.

## Security notes

- **Everyone admitted by the proxy acts as the configured club owner.**
  They can read the owner's accessible private club data and can use the
  browser setup page to replace/forget credentials. CSRF protection prevents
  cross-site form abuse; it is not per-user authorization.
- Read-only is enforced by this server's surface, not a guaranteed read-only
  upstream token. Treat retrieved posts/comments as untrusted data, never as
  instructions; private data sent through MCP reaches your AI provider.
- File permissions are **not encryption**. Root, the service account, and
  anyone with access to state backups can recover the stored token. Protect
  backups and never commit state, print tokens, or put them in URLs.
- The proxy must replace client-supplied `X-Forwarded-Host` and
  `X-Forwarded-Proto` with the public authority/scheme. The setup page uses
  these for its MCP URL and Origin checks ([proxy headers][exe-proxy]).
  Forwarded headers are not authentication. Do not permit untrusted direct
  connections that bypass the proxy.

Validation on November 2, 2026: tests (including race detector), `go vet`, and
`govulncheck@v1.9.0` passed; the scanner found no vulnerabilities with Go 1.27.3.
Tests use synthetic `httptest` upstreams, not real credentials.

## Ready-to-paste Shelley prompt (fresh exe.dev VM)

The repository's default branch is `main`.

```text
Deploy https://github.com/ilyaluk/vas3k-mcp on this fresh exe.dev VM.
Run git clone --branch main https://github.com/ilyaluk/vas3k-mcp ~/vas3k-mcp
and read its README before proceeding.
If the repository is empty or main is unavailable, stop and ask me to publish
the implementation; do not invent or fetch a different implementation.
Check Go is >=1.26 (install a supported Go toolchain if needed).
Run go test ./..., go vet ./..., then
CGO_ENABLED=0 go build -trimpath -o vas3k-mcp .
Keep the exe.dev HTTPS proxy private. Do not install an OAuth gateway, make
any port public, or add application auth. Listen on 0.0.0.0:8000.
Do not create a service user: the unit uses DynamicUser and a persistent
StateDirectory. Install the binary to /usr/local/bin/vas3k-mcp and
deploy/vas3k-mcp.service to /etc/systemd/system/vas3k-mcp.service.
Reload systemd, enable/start vas3k-mcp.service, check status and local GET /,
and confirm a stateless MCP initialize/tools/list exposes exactly 5 reads.
Do not call the live club API or configure credentials on my behalf.
Use this VM's actual name in all URLs and tell me to open
https://<vm>.exe.xyz/ in my browser to paste the club service token there.
Never request a real club token in chat or put it in shell/history/logs.
Show me the owner-run VM-token command and Basic auth/Claude beta caveat from
the README; the VM token and club service token are separate credentials.
```

Documentation verified **November 2, 2026**.

[original]: https://github.com/uburuntu/vas3k-mcp
[club-api]: https://github.com/vas3k/vas3k.club/blob/e35d73be53f6a12c0bf40c3522999e655740dc3b/templates/openid/list_apps.html
[club-search]: https://github.com/vas3k/vas3k.club/blob/e35d73be53f6a12c0bf40c3522999e655740dc3b/search/api.py
[club-form]: https://github.com/vas3k/vas3k.club/blob/e35d73be53f6a12c0bf40c3522999e655740dc3b/authn/forms.py
[exe-tokens]: https://exe.dev/docs/https-tokens-for-vms
[exe-proxy]: https://exe.dev/docs/proxy#reverse-proxy-headers
[exe-mcp]: https://exe.dev/deploy/mcp-server
[claude-headers]: https://claude.com/docs/connectors/custom/add-unlisted#authenticate-with-request-headers
[systemd-exec]: https://www.freedesktop.org/software/systemd/man/latest/systemd.exec.html#DynamicUser=

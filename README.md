# vas3k-mcp (Go)

A small, single-owner, read-only Go port of [uburuntu/vas3k-mcp][original].
MIT licensed; upstream attribution and [LICENSE](LICENSE) are retained.
Only the Go standard library is used: no third-party modules or transitive
dependencies. The empty `go.sum` is intentional.

## Scope

`GET /` is the browser setup/status page; `/mcp` is a stateless, hand-written,
limited MCP Streamable HTTP implementation with JSON responses. It is not a
general MCP framework: no resources, prompts, writes, OAuth, legacy SSE,
session persistence, or resumable event streams.
Supported initialize-based protocol revisions are **2025-11-25**,
**2025-06-18**, and **2025-03-26**; this is not a claim of latest-spec support.

All 12 original read tools are available:

| Tool | Reads |
| --- | --- |
| `get_me` | Owner's profile |
| `get_user` | Profile by club username |
| `get_user_tags` | Profile tags |
| `get_user_badges` | Received badges |
| `get_user_achievements` | Achievements |
| `find_user_by_telegram` | Profile by numeric Telegram ID |
| `get_post` | Post JSON |
| `get_post_markdown` | Post Markdown |
| `list_post_comments` | Post comments |
| `get_feed` | Filtered/ordered feed |
| `search_users` | Username-prefix search |
| `search_tags` | Tag search |

Their upstream endpoints and service-token authentication are documented in
the club's [`templates/openid/list_apps.html`][club-api], pinned at
`e35d73be53f6a12c0bf40c3522999e655740dc3b`. Other club API features are
deliberately not exposed.
Private contact email/Telegram fields are deliberately omitted from profiles.
Feed previews are capped at 1,000 Unicode characters with explicit truncation
flags; Markdown is the raw title/text, not rendered HTML. Upstream requests
time out after 15 seconds, with response limits of 4 MiB for JSON and 1 MiB
for Markdown, and a maximum of 1,000 collection items.

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
getent passwd vas3k-mcp >/dev/null || sudo useradd --system --user-group \
  --home-dir /var/lib/vas3k-mcp --no-create-home --shell /usr/sbin/nologin vas3k-mcp
sudo install -o root -g root -m 0755 vas3k-mcp /usr/local/bin/vas3k-mcp
sudo install -o root -g root -m 0644 deploy/vas3k-mcp.service /etc/systemd/system/vas3k-mcp.service
sudo systemctl daemon-reload
sudo systemctl enable --now vas3k-mcp.service
sudo systemctl status vas3k-mcp.service --no-pager
```

The unit uses a dedicated system user, `StateDirectory=vas3k-mcp` with mode
`0700`, and `-state-dir /var/lib/vas3k-mcp`. Its filesystem is otherwise
read-only, home directories inaccessible, and privileges restricted.
For updates, test/build again, stop the service, reinstall the binary, then
start it. Logs: `sudo journalctl -u vas3k-mcp.service -n 50 --no-pager`.

### Connect your club account in the browser

Open `https://<vm>.exe.xyz/` and sign into exe.dev. The setup page guides you:

1. Open [club apps](https://vas3k.club/apps/) and
   [create your own app](https://vas3k.club/apps/create/).
2. Supply a name (e.g. `My vas3k MCP`) and description. Website can be
   `https://<vm>.exe.xyz/`; if filling the redirect URL, any valid URL such as
   the same address is fine. These optional fields are unused by this port:
   it does not perform OAuth or need a callback, client ID, or client secret.
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

## Claude: private proxy, two different credentials

The **exe.dev VM-scoped credential is not the club `st_…` service token**.
Only the service token goes into the setup form; only the VM token goes into
Claude's connector headers. Generate the latter on your own computer,
replacing `<vm>` with the VM name:

```sh
ssh exe.dev ssh-key generate-api-key --vm=<vm> --label=vas3k-claude --exp=90d
```

In Claude, add a custom connector at `https://<vm>.exe.xyz/mcp`, choose
**No sign-in**, and add a required Request header:

- Preferred: `X-Exedev-Authorization`, value `Bearer <VM_TOKEN>`.
  exe.dev consumes and strips it.
- If that custom name is not approved: `Authorization`, value
  `Bearer <VM_TOKEN>`, is an exe.dev-supported **deprecated fallback**.

[Claude's current documentation][claude-headers] says Request headers are a
**limited beta**, and custom header names require Anthropic approval.
Check that your account has the UI and the chosen header works first.
Without Request headers, this no-OAuth/private-proxy design does **not**
work with claude.ai; do not make this unauthenticated app public to fix it.
The [exe.dev MCP recipe][exe-mcp]'s blanket OAuth-only description of
claude.ai does not account for this newer beta.

The [exe.dev token documentation][exe-tokens] describes the preferred header
and deprecated fallback. VM tokens grant VM web access, not merely `/mcp`.
Keep the connector private to you; renew before 90 days and revoke compromised
credentials. A successful browser login alone does not authenticate Claude's
cloud requests. This composition must be checked with your actual account.

For Claude Code, run locally (user scope, not a shared project configuration):

```sh
claude mcp add --transport http --scope user vas3k 'https://<vm>.exe.xyz/mcp' \
  --header 'X-Exedev-Authorization: Bearer <VM_TOKEN>'
```

Clients supporting remote HTTP JSON configuration can use:

```json
{"mcpServers":{"vas3k":{"type":"http","url":"https://<vm>.exe.xyz/mcp","headers":{"X-Exedev-Authorization":"Bearer <VM_TOKEN>"}}}}
```

Replace placeholders locally. Protect the client's saved header like a secret;
do not commit it. The configured `/` page shows examples with your real hostname.
The server never generates a VM token. The preferred header is stripped by the
proxy; the deprecated fallback has no documented stripping guarantee, so the
app discards both header names and never uses or stores them.
See [Claude Code's HTTP connector syntax][claude-code].

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
Create the dedicated system user/group vas3k-mcp if absent, with nologin and
home /var/lib/vas3k-mcp. Install the binary to /usr/local/bin/vas3k-mcp and
deploy/vas3k-mcp.service to /etc/systemd/system/vas3k-mcp.service.
Reload systemd, enable/start vas3k-mcp.service, check status and local GET /,
and confirm a stateless MCP initialize/tools/list exposes exactly 12 reads.
Do not call the live club API or configure credentials on my behalf.
Use this VM's actual name in all URLs and tell me to open
https://<vm>.exe.xyz/ in my browser to paste the club service token there.
Never request a real club token in chat or put it in shell/history/logs.
Show me the owner-run VM-token command and Claude header/beta caveats from
the README; the VM token and club service token are separate credentials.
```

Documentation verified **November 2, 2026**.

[original]: https://github.com/uburuntu/vas3k-mcp
[club-api]: https://github.com/vas3k/vas3k.club/blob/e35d73be53f6a12c0bf40c3522999e655740dc3b/templates/openid/list_apps.html
[club-form]: https://github.com/vas3k/vas3k.club/blob/e35d73be53f6a12c0bf40c3522999e655740dc3b/authn/forms.py
[exe-tokens]: https://exe.dev/docs/https-tokens-for-vms
[exe-proxy]: https://exe.dev/docs/proxy#reverse-proxy-headers
[exe-mcp]: https://exe.dev/deploy/mcp-server
[claude-headers]: https://claude.com/docs/connectors/custom/add-unlisted#authenticate-with-request-headers
[claude-code]: https://code.claude.com/docs/en/mcp#option-1-add-a-remote-http-server

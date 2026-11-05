<img src="assets/icon.svg" width="64" height="64" alt="">

# urlshortener

A self-hosted link shortener with multiple domains, analytics, roles, SSO and a Shlink importer. It ships as one Go binary with the web UI built in, backed by PostgreSQL.

## Features

- **Multiple short domains.** Each domain has its own links, members and fallback URL for the root and unknown slugs.
- **Login:** email/password and OpenID Connect (Keycloak, Authentik, Entra ID, Google, …). Either can be turned off.
- **Roles**
  - *Instance admin*: manages everything (users, domains, all links).
  - *Domain admin*: manages a domain's settings, members and all of its links.
  - *Domain user*: creates links on the domain and manages their own.
- **Analytics:** visits per day, referrers, countries, browsers, OS, devices and time of day. Bot and link-preview traffic is detected and counted separately. Per-link visits can be exported as CSV. No IP addresses or full user agents are stored.
- **Shlink import** from the UI, with optional visit history. Re-running it is safe because existing slugs are skipped.
- **Link options:** custom or random slugs, tags, title, expiry window, max visits, password protection, enable/disable, iOS/Android-specific destinations, query-parameter forwarding. A link with a visit limit never reveals its target without using up a visit: no `+` preview, no target on `HEAD`, and link-preview bots count. Only wrong passwords count against the per-IP attempt limit.
- **Extras:** QR codes (PNG download), link preview by adding `+` to any short link (`go.example.com/abc+`), API keys for the REST API, dark mode, one-field quick shortening that copies the result to the clipboard.

## Quick start

```sh
docker compose up -d
open http://localhost:8080
```

On first start you create the instance admin in the browser. You can also set `ADMIN_EMAIL` and `ADMIN_PASSWORD`, which closes the setup page right away and is recommended for public deployments. Next, add a domain on the **Domains** page and point its DNS at the server.

The UI and API are served only on the `APP_URL` host. Every other host is treated as a short domain. For a local test, set `APP_URL=http://localhost:8080` and add `127.0.0.1:8080` as a domain with scheme `http`.

## Running behind a reverse proxy (Caddy)

The app routes on the `Host` header and needs the real client IP for rate limiting and visitor countries. [`deploy/Caddyfile`](deploy/Caddyfile) is a complete example:

- the admin host (`APP_URL`) proxies to the app;
- a catch-all `https://` site serves every short domain, with certificates issued on demand;
- Caddy's `ask` hook calls `/.well-known/urlshortener/tls-check?domain=…`. That endpoint returns 200 only for the admin host and domains added in the UI, so nobody can make Caddy request certificates for other hostnames. It only answers requests from loopback or private addresses.

Set `TRUST_PROXY=true` on the app. Caddy replaces any client-supplied `X-Forwarded-For` with the real client IP, so the default `PROXY_HOPS=1` is right. If Caddy itself sits behind another proxy, such as a CDN listed in Caddy's `trusted_proxies`, set `PROXY_HOPS=2`. With `APP_URL` on `https://`, session cookies are marked `Secure`; TLS ends at Caddy and the app speaks plain HTTP behind it.

Other proxies (nginx, Traefik) work the same way. They must pass the original `Host` header (nginx: `proxy_set_header Host $host;`) and append the client IP to `X-Forwarded-For`.

## Configuration

All settings are environment variables.

| Variable | Default | Description |
|---|---|---|
| `DATABASE_URL` | *(required)* | PostgreSQL connection string. Migrations run automatically. |
| `APP_URL` | `http://localhost:8080` | Public URL of the admin UI. Its host serves the UI; all other hosts serve short links. |
| `LISTEN_ADDR` | `:8080` | Listen address. |
| `TRUST_PROXY` | `false` | Take the client IP from `X-Forwarded-For` and the country from `CF-IPCountry`. These headers are only believed when the direct peer is on loopback or a private network, so clients reaching the app port directly can't spoof them. Enable only behind a reverse proxy. If that proxy isn't Cloudflare, it should strip `CF-IPCountry`, or visitors can fake their country in analytics. |
| `PROXY_HOPS` | `1` | Number of proxies in front of the app that append to `X-Forwarded-For`. The client IP is read this many entries from the right, so clients can't spoof it. |
| `GEOIP_DB` | | Path to a MaxMind GeoLite2-Country `.mmdb` file for visitor countries. |
| `LOCAL_AUTH` | `true` | Allow email/password login. With `LOCAL_AUTH=false`, set `OIDC_ADMIN_GROUP` (or `ADMIN_EMAIL` before switching) so someone is an instance admin. |
| `ADMIN_EMAIL`, `ADMIN_PASSWORD` | | Create this instance admin on startup if it doesn't exist yet. |
| `SESSION_TTL` | `720h` | Session lifetime. SSO users removed at the IdP keep an existing session until it expires; disable them here too, or shorten this. |
| `OIDC_ISSUER` | | OIDC issuer URL. Setting it together with `OIDC_CLIENT_ID` enables SSO. |
| `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET` | | Client credentials. Redirect URI: `<APP_URL>/api/auth/oidc/callback`. |
| `OIDC_NAME` | `SSO` | Button label ("Continue with …"). |
| `OIDC_SCOPES` | `openid email profile` | Requested scopes. Add `groups` if your IdP needs it for the groups claim. |
| `OIDC_ADMIN_GROUP` | | Members of this group become instance admins. The role is synced on every login. |
| `OIDC_GROUPS_CLAIM` | `groups` | Claim holding the user's groups. |
| `OIDC_AUTO_CREATE` | `true` | Create accounts for unknown SSO users. When `false`, only users added beforehand can sign in. |
| `OIDC_SKIP_EMAIL_VERIFIED` | `false` | Link SSO logins to existing accounts even without `email_verified` (needed for Entra ID). |

SSO logins are matched to existing accounts by verified email. This means a domain admin can add `alice@example.com` to a domain before Alice has ever signed in. Only instance admins can give such a pre-created account a password, and SSO accounts can't add a local password themselves, so the IdP stays in control of SSO users.

## Performance and scaling

- **Redirects** are answered from an in-memory LRU cache of domains and links, including "not found" results, so the hot path usually doesn't touch the database.
- **Visits** go into an in-memory buffer and are written in batches with `COPY` every second. A slow database doesn't slow down redirects. If the buffer fills up, visits are dropped rather than redirects delayed.
- **Several replicas** can run behind a load balancer. Cache invalidations go to all replicas through Postgres `LISTEN/NOTIFY`, with a 60-second TTL as a safety net. Migrations take an advisory lock so only one replica runs them.
- **Max visits is exact,** even under concurrency and across replicas. Links with a limit reserve each visit with one atomic database update; all other links stay on the batched path. Bot hits use up the limit but stay out of the human visit counts. One edge: adding a limit to a link that is already getting traffic has a short transition window. Requests already in flight during the edit, and visits still in the write buffer (up to about a second), can be counted on top, so that one change can overshoot slightly.
- **Known limits:** login rate limiting is per replica. Link search uses `ILIKE`; at millions of links, add a `pg_trgm` index.

## Importing from Shlink

Open **Import** (instance admins only, because the server fetches the URL you enter from its own network). Enter your Shlink URL and an API key (`shlink api-key:generate`) and pick a target domain. Links on a Shlink domain whose hostname also exists here keep that domain; all others go to the target domain. With visit history enabled, each visit is imported with its date, referrer, country and parsed user agent. Shlink REST API v3 (Shlink 3.x/4.x) and v2 are supported. iOS/Android targets carry over from Shlink 3 `deviceLongUrls` and from Shlink 4 device redirect rules. Visit counters (and so max-visits quotas) follow Shlink's own totals; the imported history feeds the charts. Shlink 3 desktop-specific URLs and other Shlink 4 rule types (language, query parameter, …) can't be represented here. The job report counts the links that had them, and those links redirect to their default URL. Each link is imported together with its visit history in one transaction, and one import runs at a time per server.

## REST API

Every UI action goes through the JSON API under `/api`. Create a key in **Settings → API keys** and send it as `Authorization: Bearer <key>` or `X-Api-Key: <key>`.

```sh
curl -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"domainId":1,"targetUrl":"https://example.com","slug":"hello","tags":["demo"]}' \
  https://admin.example.com/api/links
```

Main endpoints:

- `GET/POST /api/links` and `GET/PUT/DELETE /api/links/{id}`
- `GET /api/links/{id}/stats?days=30`, `GET /api/links/{id}/qr`, `GET /api/links/{id}/visits.csv`
- `GET /api/stats?days=30&domainId=`
- `GET/POST /api/domains`, `PATCH/DELETE /api/domains/{id}`
- `GET/PUT /api/domains/{id}/members`
- `GET/POST /api/users`, `PATCH/DELETE /api/users/{id}`
- `POST /api/import/shlink`

Health check: `GET /healthz` on any host.

## Development

Requirements: Go 1.27+, Node 24+, Docker (for Postgres).

```sh
docker run -d --name pg -e POSTGRES_PASSWORD=dev -p 5432:5432 postgres:17-alpine
npm --prefix web ci && npm --prefix web run build
DATABASE_URL=postgres://postgres:dev@localhost:5432/postgres?sslmode=disable go run .
# Frontend with hot reload on :5173 (proxies /api to :8080):
npm --prefix web run dev

# Tests (integration tests need a throwaway database; they truncate it):
TEST_DATABASE_URL=postgres://postgres:dev@localhost:5432/postgres?sslmode=disable go test -p 1 ./...
```

## Releases

Push a tag such as `v1.2.3`. The release workflow:

1. runs the full CI (Go tests against Postgres, frontend type check and build, Docker build);
2. builds binaries for Linux, macOS and Windows (amd64/arm64) with GoReleaser and publishes a GitHub release with checksums and a changelog;
3. pushes a multi-arch image to `ghcr.io/robinferch/urlshortener` tagged `1.2.3`, `1.2`, `1` and `latest`, with SBOM and signed build provenance.

Pre-release tags (`v1.3.0-rc.1`) don't move `latest`. Dependabot keeps Go, npm, Docker and Actions dependencies current.

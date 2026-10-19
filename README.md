# stickynotes backend

> *One Go binary behind the extension and the library.*

The REST API for [stickynotes](https://www.stickynotes.in): a browser extension and web library that keeps the passages you select on any page, filed by topic with every source listed. A single Go service backs the Manifest V3 extension, the Next.js web app and the public share pages.

**Website:** [stickynotes.in](https://www.stickynotes.in) · **API:** [stickynotes-backend-production.up.railway.app](https://stickynotes-backend-production.up.railway.app/health) · **Extension:** [Chrome Web Store](https://chromewebstore.google.com/detail/stickynotes/fciennljceeoagambkjppepghigkcnep) · **Contact:** contactus@paperboxd.in

---

## Table of Contents

- [Overview](#overview)
- [Why This Stack](#why-this-stack)
- [Architecture](#architecture)
- [Project Layout](#project-layout)
- [Request Lifecycle](#request-lifecycle)
- [Authentication & Sessions](#authentication--sessions)
- [Authorization](#authorization)
- [Data Model](#data-model)
- [Sync Protocol](#sync-protocol)
- [Library & Sharing](#library--sharing)
- [Tags & Reading Suggestions](#tags--reading-suggestions)
- [Data Layer (sqlc + migrations)](#data-layer-sqlc--migrations)
- [External Services](#external-services)
- [API Reference](#api-reference)
- [Error Envelope](#error-envelope)
- [Security](#security)
- [Testing](#testing)
- [Makefile](#makefile)
- [Environment Variables](#environment-variables)
- [Local Development](#local-development)
- [Deployment](#deployment)
- [Conventions](#conventions)
- [Known Limits](#known-limits)
- [Troubleshooting](#troubleshooting)
- [Related Repositories](#related-repositories)

---

## Overview

This is the only backend stickynotes has. It owns the database, the sessions and the one optional third-party feature (reading suggestions). Clients hold no secrets of ours and never touch the database:

- The **extension** keeps its own copy of the user's notes and syncs it through `/sync/push` and `/sync/pull`.
- The **web app** calls the API from its Next.js server only. Browser JavaScript never holds a token, so the API sends no CORS headers at all.
- **Share pages** read one public topic by its slug, without signing in.

**At a glance:**

| | |
|---|---|
| Language | Go 1.26 (`go.mod` pins 1.26.9) |
| HTTP router | [chi v5](https://github.com/go-chi/chi) |
| Database | PostgreSQL via [pgx/v5](https://github.com/jackc/pgx) (a `pgxpool`) — no ORM |
| SQL → Go | [sqlc](https://sqlc.dev), generated into `internal/db` |
| Migrations | [golang-migrate](https://github.com/golang-migrate/migrate), embedded with `go:embed` and applied on every start (4 so far) |
| Auth | Google sign-in only. HS256 JWT access tokens (15 min) plus opaque refresh tokens (60 days, stored hashed, rotated on every use, one per device) |
| Rate limiting | [httprate](https://github.com/go-chi/httprate), in memory: 300/min per user or IP, 20/min on sign-in and extension token exchange |
| Cache | None |
| Third parties | Google tokeninfo (sign-in). Gemini and Brave Search, only for users who turn reading suggestions on |
| Logging | `log/slog` JSON for the app, chi's logger for requests |
| Deployment | Railway: one service plus a Postgres on the private network |

Eight direct dependencies: chi, httprate, golang-jwt v5, golang-migrate v4, google/uuid, pgx v5, godotenv and `x/sync` (for `singleflight`).

**Design stance:** one process, one binary, one database. The access token is the session on the hot path; refresh tokens live in Postgres so Settings can list a user's devices and sign one out. There is no cache, no queue and no scheduler: the only background work is a reading-suggestions build that a page visit starts. The extension is offline-first and makes its own ids, so every write the API accepts is safe to repeat.

---

## Why This Stack

| Choice | Why |
|---|---|
| **Go, chi, pgx + sqlc, embedded golang-migrate** | The same stack as `paperboxd-backend`: one static binary, hand-written SQL that sqlc turns into typed Go (a wrong column is a compile error, not a runtime panic), and migrations compiled into the binary so a deploy converges the schema with the code that needs it. |
| **Authorization in app code** | Every query on user data filters by `user_id`, and ownership is checked inside the SQL (joins on `topics.user_id`). One place to read, one place to test. |
| **Short access tokens + rotating refresh tokens** | Validating a request is a signature check with no database round-trip. Refresh tokens in a table give each browser and extension a row: Settings lists them, and Disconnect takes effect within 15 minutes. |
| **Client-made UUIDs** | The extension creates topics and notes offline and pushes them later. Ids made on the device let a push be repeated after a lost response without duplicating anything. |
| **PKCE to connect the extension** | The website owns sign-in. It mints a 60-second code bound to the extension's PKCE challenge; the extension redeems it with a verifier that never leaves the extension. No token crosses the web page. |
| **In-memory rate limits** | Correct for one instance. Move to `httprate-redis` only when running more than one replica. |

**Deliberately not used:** Supabase (authorization is plain SQL), NextAuth on the web (one session instead of two), CORS (browser → Next.js server → Go; the extension uses `host_permissions`), Redis (nothing to cache), an ORM, and any cron or job runner.

---

## Architecture

```
  Web app (Next.js server) ──┐
  Browser extension (MV3) ───┼── HTTPS · JSON · Authorization: Bearer <jwt> ──┐
  Share pages (SSR, public) ─┘                                                │
                                                                              ▼
                 ┌──────────────────────────────────────────────────────────────┐
                 │  cmd/api/main.go                                             │
                 │  config.Load · pgxpool · embedded migrations · routes()      │
                 └──────────────────────────────┬───────────────────────────────┘
                                                │  chi.Router
                 ┌──────────────────────────────▼───────────────────────────────┐
                 │  Global middleware                                           │
                 │  ClientIPFromXFFTrustedProxies(1) · Logger · Recoverer       │
                 │  Timeout(30s) · RequestSize(1 MB) · httprate 300/min         │
                 └──────────────────────────────┬───────────────────────────────┘
                                                │  per route
                 ┌──────────────────────────────▼───────────────────────────────┐
                 │  Authenticate: Bearer JWT → user id in the request context   │
                 │  Public: /health · /auth/* · /extension/token · /shared/*    │
                 └───────────────┬──────────────────────────────┬───────────────┘
                                 │                              │
                 ┌───────────────▼──────────────┐ ┌─────────────▼────────────────┐
                 │  internal/auth               │ │  internal/notes              │
                 │  Google sign-in, refresh,    │ │  sync push/pull, library,    │
                 │  logout, /me, devices,       │ │  sharing, tags, reading      │
                 │  extension PKCE codes        │ │  suggestions                 │
                 └──────┬────────────────┬──────┘ └──────┬────────────────┬──────┘
                        │                │               │                │
          ┌─────────────▼──────┐  ┌──────▼───────────────▼──────┐  ┌──────▼──────────┐
          │  Google tokeninfo  │  │  internal/db (sqlc)         │  │  Gemini, Brave  │
          │  aud allowlist     │  │  typed queries              │  │  opt-in only    │
          └────────────────────┘  └──────────────┬──────────────┘  └─────────────────┘
                                                 │  pgx
                                          ┌──────▼──────┐
                                          │ PostgreSQL  │
                                          └─────────────┘
```

**Layer rules:**

- **`internal/auth` and `internal/notes`** each hold their handlers and the logic behind them. There is no service layer to pass through; the logic worth testing on its own is pure functions (`apply`, `pickLinks`, `normTags`, `urlKey`, `titleKey`).
- **`internal/db`** is 100% sqlc-generated. Never edit it by hand: edit `queries/*.sql` or `migrations/`, then `make sqlc`.
- **`internal/token` and `internal/reqctx` are separate packages** so the middleware can validate tokens and set the user id without importing `auth` (no import cycle).
- **`routes()` is separate from `main()`** so the integration tests run the real router.

---

## Project Layout

```
cmd/api/
├── main.go              # config → pool → migrations → routes() → graceful shutdown
└── main_test.go         # integration tests: the real router against docker Postgres

internal/
├── auth/
│   ├── auth.go          # /auth/google, /auth/refresh, /auth/logout, /me, /devices, sessions
│   ├── extension.go     # /extension/codes and /extension/token (PKCE S256)
│   ├── google.go        # tokeninfo + the aud allowlist
│   └── google_test.go
├── config/              # env → Config, validated at boot
├── db/                  # sqlc-GENERATED queries and models — do not hand-edit
├── middleware/
│   ├── auth.go          # Authenticate: Bearer JWT → user id
│   └── ratelimit_key.go # KeyByUserOrIP: the rate-limit bucket (+ test)
├── notes/
│   ├── sync.go          # /sync/push and /sync/pull
│   ├── library.go       # /library, /topics/{id}/share, /shared/{slug}
│   ├── suggest.go       # /topics/{id}/tags and /topics/{id}/suggestions (Gemini + Brave)
│   └── suggest_test.go
├── reqctx/              # the user id in and out of the request context
├── token/               # JWT generate/parse (HS256)
└── types/               # request/response shapes and the error envelope

migrations/              # numbered up/down SQL pairs, plus embed.go (go:embed *.sql)
queries/                 # sqlc input: auth, extension, library, suggestions, sync, users
docker-compose.yml       # local Postgres 17 on :5433
railway.json             # Railway health check: /health
sqlc.yaml
Makefile
```

---

## Request Lifecycle

Every request passes the same global middleware (`routes()` in `cmd/api/main.go`), in order:

| Middleware | Role |
|---|---|
| `ClientIPFromXFFTrustedProxies(1)` | Railway's edge is the one proxy in front of the API and appends the address it saw to `X-Forwarded-For`, so the rightmost entry is the client. Anything to its left came from the client and is ignored. |
| `Logger` | One log line per request |
| `Recoverer` | A panic becomes a 500, not a crashed process |
| `Timeout(30s)` | Ceiling on any single request |
| `RequestSize(1 MB)` | Body cap. The extension splits its pushes to stay under half of it. |
| `httprate` 300/min | Global rate limit, keyed per user or IP (below) |

Then `Authenticate` runs on the signed-in routes, the handler executes, and the response goes out through `types.WriteJSON` / `types.WriteError`.

The server itself uses read and write timeouts of 15 s and an idle timeout of 60 s. On `SIGINT`/`SIGTERM` it stops accepting connections and drains in-flight requests for up to 30 s, so a deploy doesn't cut a push in half.

**Rate-limit keys** (`middleware.KeyByUserOrIP`): the user when the request carries an access token that *verifies*, else the client IP. A made-up, forged or expired token counts as none, so sending a fresh fake token with every request can't buy a fresh bucket, and a spoofed `X-Forwarded-For` can't either. Many web users share the Next.js server's egress IP, which is why signed-in requests key on the user.

| Route | Limit | Why |
|---|---|---|
| Everything | 300/min | Per user or IP |
| `POST /api/v1/auth/google` | 20/min | Each call hits Google |
| `POST /api/v1/extension/token` | 20/min | Code redemption |

A 429 uses the same error envelope as every other error (`RATE_LIMITED`).

---

## Authentication & Sessions

One identity system, two ways in.

| | Web app | Extension |
|---|---|---|
| How a session starts | The Next.js server runs Google's OAuth code flow (state + PKCE), then sends Google's `id_token` to `POST /auth/google` | The signed-in web page asks `POST /extension/codes` for a single-use code bound to the extension's PKCE challenge; the extension redeems it at `POST /extension/token` with its verifier |
| Access token | 15-minute HS256 JWT carrying `user_id` | Same |
| Refresh token | 32 random bytes (hex), 60 days, stored as a SHA-256 hash, rotated on every refresh | Same |
| Where the client keeps them | HttpOnly cookies on the web origin | `chrome.storage.local` |
| Device label (`device_info`) | `{ "user_agent": … }`: the browser's own, which the Next.js server forwards at sign-in | `{ "user_agent": …, "client": "extension" }` |

**Google sign-in** (`POST /api/v1/auth/google`, `internal/auth/google.go`). The API asks Google's tokeninfo endpoint about the ID token (5 s timeout). A 200 proves signature, expiry and issuer, but not that the token was minted for *this* app, so its `aud` must also be in `GOOGLE_CLIENT_IDS`; without that check, a valid Google token from any other app would sign in here. The allowlist can't be empty (the server refuses to boot), and the check fails closed anyway. The account also needs a verified email. Users are upserted by Google `sub`, which never changes; email, name and picture refresh on every sign-in.

**Rotation.** `POST /auth/refresh` revokes the presented token and issues a new pair in one statement (`UPDATE … SET revoked_at = now() … RETURNING`), so a refresh token is exchanged exactly once even when two requests race. A token that was already used gets 401. The new row keeps the old `device_info`, so a device has one identity across rotations.

**Connecting the extension** (`internal/auth/extension.go`):

```
      web app                     extension                      API
          │◄──── PKCE challenge ──────┤                           │
          │                           │                           │
          ├── POST /extension/codes ──┼──────────────────────────►│   code: 32 random bytes, stored hashed,
          │                           │                           │
          │◄──────────────────────────┼──────── { code } ─────────┤   bound to the challenge, 60 s, one use
          │                           │                           │
          ├────────── code ──────────►│                           │
          │                           │                           │
          │                           ├── POST /extension/token ─►│   consumes the code, then checks
          │                           │                           │
          │                           │◄─── access + refresh ─────┤   S256(code_verifier) == challenge
```

The web app asks for the code with the user's own session (a server action calls `POST /extension/codes { code_challenge }`) and hands it to the extension through the page. The extension redeems it with `POST /extension/token { code, code_verifier }`, and the API compares the challenge in constant time. The code is consumed *before* the verifier is checked, so a wrong guess burns it. The challenge must be a 43-character base64url S256 value and the verifier 43–128 unreserved characters (RFC 7636). The code alone is useless to anyone who sees it on the way.

**Ending sessions.**

- `POST /auth/logout` revokes a refresh token.
- `DELETE /devices/{id}` revokes another device's refresh token; that device is signed out once its access token runs out (at most 15 minutes).
- `DELETE /me` deletes the account. Every token, topic, source, note, suggestion and share link goes with it by cascade. An access token can outlive the account by up to 15 minutes; `/me` then answers 401 `Account not found`.

**Devices.** A device is its one live refresh token. Refreshing replaces the row, so its `created_at` is when the device was last active. `GET /me` adds `extension_seen_at`: the last activity of the newest connected extension, or `null` when none is connected.

---

## Authorization

There is no row-level security. Authorization is in the SQL every handler runs:

- **Every query on user data filters by `user_id`**, taken from the verified token, never from the request body.
- **Ownership is checked in the statement that writes.** `UpsertSource` inserts only `FROM topics t WHERE t.id = @topic_id AND t.user_id = @user_id`, so a note can't be filed into someone else's topic: no row comes back and the action is rejected. `MoveSource`, `MoveTopicSources` and `MergeTopicTags` join on topics the user owns in the same way.
- **Ids are global but rows aren't shared.** A client may send any UUID. A topic id already used by another account is rejected (`topic id is already taken`); a delete of someone else's highlight is a quiet no-op.
- **Shared topics** are the one read without a user: `GET /shared/{slug}` returns only what a share page shows (title, author name and avatar, sources and notes), never an email.

`TestSync` and `TestLibrary` assert that a second account can't write into, move, rename, delete, merge or read the first account's notes.

---

## Data Model

```
users ─┬──< refresh_tokens        one live row per signed-in browser or extension (hashed)
       ├──< extension_codes       single-use PKCE codes, 60 s
       └──< topics ─┬──< sources ──< highlights
                    └──── topic_suggestions   (one row per topic, opt-in)
```

| Table | Key columns | Notes |
|---|---|---|
| `users` | `google_sub` (unique), `email`, `name`, `avatar_url`, `avatar`, `suggestions` | `avatar` is a key from the website's painted set, `''` until picked. `suggestions` is `NULL` until the user decides. |
| `refresh_tokens` | `token_hash` (unique), `device_info` jsonb, `expires_at`, `revoked_at` | Partial index on live rows per user |
| `extension_codes` | `code_hash` (PK), `code_challenge`, `expires_at`, `used_at` | |
| `topics` | `id` (client UUID), `title` (1–300), `is_public`, `share_slug` (unique), `tags` text[] (≤ 12), `deleted_at` | **Unique `(user_id, lower(title))` among live topics**: the same title created on two offline devices folds into one topic |
| `sources` | `url` (http(s), ≤ 2048), `title` (≤ 300), `site` (≤ 255), `topic_id`, `deleted_at` | **Unique `(user_id, url)`**: one source per page, in exactly one topic |
| `highlights` | `id` (client UUID), `text` (≤ 10,000), `section` (≤ 300), `anchor` jsonb, `color`, `created_at` (the device's), `deleted_at` | `color` is one of yellow, pink, blue, green |
| `topic_suggestions` | `topic_id` (PK), `notes_key`, `links` jsonb, `tags` text[], `built_at` | Deleted when the user turns suggestions off |

**Deletes are markers.** Deleting a note blanks its text and anchor and sets `deleted_at`, keeping the row so the user's other devices learn about it on their next pull. A source whose last note goes is deleted too, and deleting a topic marks everything in it. A `set_updated_at` trigger on `users`, `topics`, `sources` and `highlights` keeps `updated_at` current, and pull reads by it.

**Deleting an account is not a marker.** Every user-owned table references `users(id) ON DELETE CASCADE`, so `DELETE FROM users WHERE id = $1` removes everything at once.

### Migrations

| Migration | What |
|---|---|
| 000001 | Initial schema: users, refresh tokens, extension codes, topics, sources, highlights, `updated_at` triggers |
| 000002 | `highlights.color`: yellow, pink, blue or green; existing notes are yellow |
| 000003 | `users.avatar`: the avatar picked on the website |
| 000004 | `topics.tags` (at most 12), `users.suggestions`, and the `topic_suggestions` table |

---

## Sync Protocol

The extension keeps the user's notes on the device and syncs them in two calls: push everything queued, then pull what changed. The web library edits through the same push, so every device picks its changes up on the next pull.

### Push

`POST /api/v1/sync/push` takes up to 200 actions. They apply **in order, in one transaction, with a savepoint per action**: a rejected action is rolled back alone and the rest still apply. Every action is safe to repeat (say the response was lost and the client sends the batch again).

```json
POST /api/v1/sync/push
{
  "actions": [
    { "type": "create_topic", "id": "6f1c5f0e-…", "title": "Consistent hashing" },
    {
      "type": "create_highlight", "id": "0b9e2d71-…", "topic_id": "6f1c5f0e-…",
      "url": "https://example.com/consistent-hashing", "page_title": "Consistent hashing, explained",
      "site": "example.com", "text": "A key belongs to the first machine found by walking clockwise.",
      "section": "The ring", "anchor": { "prefix": "…", "suffix": "…" },
      "color": "yellow", "created_at": "2026-10-20T09:14:03.120Z"
    }
  ]
}

200 { "topic_ids": { "6f1c5f0e-…": "a27d93b4-…" }, "rejected": [] }
```

`topic_ids` maps a client's topic id to the id that won when a same-title topic already existed; the extension rewrites its store and its queued actions with it. `rejected` lists `{ index, error }` for actions that will never succeed, which clients drop.

| Action | Fields | Effect | Rejected when |
|---|---|---|---|
| `create_topic` | `id`, `title` | Creates the topic. If the user already has a live topic with that title (ignoring case), that one wins and `topic_ids` says so. | No id, a title outside 1–300 characters, or the id belongs to another account |
| `create_highlight` | `id`, `topic_id`, `url`, `page_title`, `site`, `text`, `section`, `anchor`, `color`, `created_at` | Upserts the page's source (one per URL; a live source keeps its topic, because moving one is an explicit action) and inserts the note. A repeat is a no-op. A note saved offline into a topic deleted or merged since brings that topic back. | Missing ids, text outside 1–10,000 characters, a URL that isn't http(s) or is over 2048 characters, a topic that isn't the user's |
| `delete_highlight` | `id` | Blanks the note into a deletion marker; its source goes too once empty. An unknown or foreign id is a quiet no-op. | No id |
| `move_source` | `url`, `topic_id` | Moves a page, with all its notes, to another of the user's topics | No such source or topic |
| `rename_topic` | `id`, `title` | Renames | Another topic already has the title (ignoring case), or no such topic |
| `delete_topic` | `id` | Marks every note and source in the topic, deletes it and kills its share link | No id |
| `merge_topic` | `id`, `topic_id` (the target) | Moves every live source into the target, merges the tags (the target's first, at most 12) and deletes the merged topic | Same id twice, or the target isn't a live topic of the user's |

**Forgiving where it costs nothing.** Page titles, sites and sections are trimmed and cut to length rather than rejected. An anchor that isn't valid JSON, or is over 4 KB, is dropped (it's only a hint for finding the passage again). An unknown colour becomes yellow. `created_at` comes from the device unless it is unreadable or more than a day in the future. A value the database refuses (SQLSTATE classes 22 and 23) rejects that one action as `invalid data`; any other error fails the whole push with a 500.

### Pull

`GET /api/v1/sync/pull?cursor=<RFC 3339>` returns every topic, source and highlight whose `updated_at` is after the cursor, deletion markers included, plus the cursor for next time. Without a cursor it returns the whole live copy (no markers).

```json
GET /api/v1/sync/pull?cursor=2026-10-20T09:10:00.512Z

200 {
  "topics":     [{ "id": "a27d93b4-…", "title": "Consistent hashing", "deleted": false }],
  "sources":    [{ "topic_id": "a27d93b4-…", "url": "https://example.com/consistent-hashing",
                   "title": "Consistent hashing, explained", "site": "example.com", "deleted": false }],
  "highlights": [{ "id": "0b9e2d71-…", "url": "https://example.com/consistent-hashing", "text": "…",
                   "section": "The ring", "anchor": { "prefix": "…", "suffix": "…" },
                   "color": "yellow", "created_at": "2026-10-20T09:14:03.12Z", "deleted": false }],
  "cursor": "2026-10-20T09:15:02.481236Z"
}
```

- **Five minutes of overlap.** The server reads from `cursor − 5 min`, so a row written by a transaction that committed after an earlier pull read past it is never skipped. Pull results are idempotent, so the overlap costs only bytes.
- **One snapshot.** The three reads run in one read-only `REPEATABLE READ` transaction, so a highlight never arrives without its source.
- **The cursor is the server's clock**, taken before the reads, never the device's.

The extension pulls only when its outbox is empty, so an older server copy can't undo a change still waiting to go. The client side is described in the web repo's README.

---

## Library & Sharing

**`GET /api/v1/library`** returns every live topic, source and note in one read-only snapshot. It is what the web library and topic pages render; edits go back through `/sync/push`.

```json
{
  "topics":     [{ "id": "…", "title": "…", "is_public": false, "share_slug": null, "tags": ["hashing"], "created_at": "…" }],
  "sources":    [{ "id": "…", "topic_id": "…", "url": "…", "title": "…", "site": "…" }],
  "highlights": [{ "id": "…", "source_id": "…", "text": "…", "section": "…", "color": "pink", "created_at": "…" }]
}
```

**`POST /api/v1/topics/{id}/share`** with `{ "public": true | false }` returns `{ public, slug }`:

- Sharing gives the topic a 12-character slug: 9 random bytes, base64url (72 bits). It is unguessable and short enough to paste.
- Sharing again keeps the existing link.
- **Going private clears the slug**, so a link once handed out stays dead even if the topic is shared again; sharing again mints a new one.
- Deleting the topic kills its link too.

**`GET /api/v1/shared/{slug}`** (no sign-in) returns `{ title, author, author_avatar, sources, highlights }` for a public, live topic, or 404.

---

## Tags & Reading Suggestions

### Tags

`PUT /api/v1/topics/{id}/tags` replaces a topic's tags and returns them as stored: lower case, single spaces, no leading `#`, at most 32 characters each, no repeats, at most 12 (the table checks the limit too). Merging two topics keeps both topics' tags.

### Reading suggestions

`internal/notes/suggest.go`, migration 000004. Under each topic, pages to read next plus tag ideas. **Off until the user turns them on**, because they send note text to Google.

**Consent.** `users.suggestions` is `NULL` until the user decides, and the web offers suggestions only then. `PATCH /me { "suggestions": true | false }` sets it. Turning them off deletes every saved `topic_suggestions` row, and a build still running at that moment saves nothing (the insert only happens while the user's switch is on).

**`GET /api/v1/topics/{id}/suggestions`** returns `{ links: [{ url, title, site }], tags: [] }`, or 403 while suggestions are off:

1. Check the switch and that the topic is the user's. No notes yet means nothing to go on: empty lists.
2. Hash the topic's title and the ids of its 30 newest notes into a `notes_key`. Notes are never edited, so the ids say exactly when the input changes.
3. **No saved result:** start a build and wait up to 12 s for it (under the server's 15 s write timeout). A slower build finishes in the background and shows on the next visit.
4. **Saved result, notes changed, and the last build is a day old** (judged by the database's clock): serve the saved result and rebuild in the background.
5. Before answering, drop links the topic has since gained as sources and tag ideas it already wears.

**A build:**

1. **Gemini** gets the topic's title and its 30 newest notes, each cut to 500 characters, with a system prompt that treats the passages as data, never instructions. It answers in a JSON schema: 3–4 web searches of 3–8 plain words, and up to 4 short tags. Models in `GEMINI_MODELS` are tried in order, so an overloaded (503), out-of-quota (429) or retired (404) model falls through to the next.
2. **Brave Search** runs the searches in parallel (8 web results each, moderate safe search). Only http(s) results are kept, since they become links. The build fails only if every search does.
3. **Pick six links** by taking each search's first result, then each one's second, and so on, skipping repeats and pages already among the topic's sources. Pages are compared by address (no scheme, `www.`, trailing slash or `#fragment`) and by title (before a `" | "` or `" - "` site suffix), so one paper on two sites counts once.
4. Save the links, the tag ideas and the `notes_key`.

**Guards:**

| Guard | Value |
|---|---|
| Builds per topic at a time | 1 (`singleflight`) |
| Builds per user | 25 in any 24 hours |
| After any failed build | No builds anywhere for 10 minutes: quietly no suggestions |
| Build timeout | 30 s, detached from the request so a visit that stopped waiting doesn't cancel it |
| HTTP timeout to Gemini and Brave | 20 s |
| Without both API keys | No builds; the server logs a warning at boot |

**What leaves the server:** the topic title and up to 30 notes (500 characters each) go to Gemini; only the searches Gemini wrote go to Brave. No URLs, emails or ids. The Settings switch says in plain words that Google may use what it receives and people at Google may read it; the website's privacy policy (`/privacy#suggestions`) has the full statement.

---

## Data Layer (sqlc + migrations)

**The schema lives in `migrations/`** as numbered `up`/`down` SQL pairs. That is the single source of truth, not a Go struct. **Queries live in `queries/*.sql`** as named, hand-written SQL. `sqlc generate` reads both and writes typed Go into `internal/db`, so a typo or a type mismatch fails `go build` rather than a request.

```bash
# 1. Add a migration pair (migrations/00000N_name.{up,down}.sql) and/or edit a query in queries/
# 2. Regenerate the typed Go
make sqlc
# 3. Check it compiles, and commit the regenerated internal/db with the SQL
go build ./... && go vet ./...
```

`sqlc.yaml` overrides worth knowing:

| Override | Why |
|---|---|
| `uuid` → `github.com/google/uuid.UUID` | One UUID type everywhere |
| `timestamptz` → `time.Time` | NOT NULL timestamps as plain `time.Time`; nullable ones stay `pgtype.Timestamptz` |
| `highlights.anchor`, `topic_suggestions.links` → `json.RawMessage` | Pass through to clients as JSON, not base64 |
| `emit_empty_slices: true` | Empty results encode as `[]`, never `null` |

**Migrations run on every start.** `migrations/embed.go` embeds `*.sql` into the binary; `runMigrations` hands them to golang-migrate through its `pgx/v5` driver (rewriting `postgres://` to the `pgx5://` scheme it expects) and treats "no change" as success. A failed migration stops the server before it listens, so a bad deploy fails its health check instead of serving a half-migrated schema. There is no separate migrate step to forget.

---

## External Services

| Service | Used for | When it's missing |
|---|---|---|
| **Google OAuth tokeninfo** | Verifying the ID token at every sign-in | Sign-in fails (401) |
| **Gemini API** (`generateContent`) | Searches and tag ideas for reading suggestions | No suggestions |
| **Brave Search API** | Running those searches | No suggestions |

That's the whole list: no email provider, file storage, analytics or error tracker. The base URLs are config fields (`GoogleTokenInfoURL`, `GeminiURL`, `BraveURL`) so the tests point them at stub servers.

---

## API Reference

Everything except `/health` is under `/api/v1`. Bodies are JSON; signed-in routes take `Authorization: Bearer <access token>`.

| Method & path | Auth | Request → response |
|---|---|---|
| `GET /health` | — | Pings Postgres → `{ "status": "ok" }`, or 503 when the database is unreachable |
| `POST /auth/google` | — (20/min) | `{ id_token }` → session |
| `POST /auth/refresh` | — | `{ refresh_token }` → a new session; the old token is revoked |
| `POST /auth/logout` | — | `{ refresh_token }` → 204 |
| `POST /extension/token` | — (20/min) | `{ code, code_verifier }` → session for the extension |
| `GET /me` | Bearer | The user, `extension_seen_at` and `suggestions` |
| `PATCH /me` | Bearer | `{ avatar?, suggestions? }` → 204. A field left out stays as it is. |
| `DELETE /me` | Bearer | Deletes the account and everything in it → 204 |
| `POST /extension/codes` | Bearer | `{ code_challenge }` → `{ code }` (single use, 60 s) |
| `POST /sync/push` | Bearer | `{ actions }` → `{ topic_ids, rejected }` |
| `GET /sync/pull?cursor=` | Bearer | Changes since the cursor → `{ topics, sources, highlights, cursor }` |
| `GET /library` | Bearer | Every live topic, source and note |
| `POST /topics/{id}/share` | Bearer | `{ public }` → `{ public, slug }` |
| `PUT /topics/{id}/tags` | Bearer | `{ tags }` → `{ tags }` as stored |
| `GET /topics/{id}/suggestions` | Bearer | `{ links, tags }`; 403 while suggestions are off |
| `GET /devices` | Bearer | `[{ id, client, user_agent, last_active }]`, most recent first |
| `DELETE /devices/{id}` | Bearer | Signs that device out → 204, or 404 |
| `GET /shared/{slug}` | — | A public topic → `{ title, author, author_avatar, sources, highlights }`, or 404 |

A session (sign-in, refresh, extension token):

```json
{
  "access_token": "eyJhbGciOiJIUzI1NiIs…",
  "refresh_token": "9f8c…",
  "expires_in": 900,
  "user": { "id": "…", "email": "ana@example.com", "name": "Ana Ruiz", "avatar_url": "https://…", "avatar": "fox", "suggestions": null }
}
```

`GET /me` returns the same `user` fields plus `"extension_seen_at": "2026-10-20T09:15:02Z" | null`. The avatar key must match `^[a-z0-9-]{0,32}$`; the website owns the set of avatars and shows a default for a key it doesn't know.

---

## Error Envelope

Every error is the same JSON shape, so clients parse failures one way:

```json
{ "error": "Human readable message", "code": "SNAKE_CASE_CODE" }
```

| Code | HTTP | Meaning |
|---|---|---|
| `VALIDATION_ERROR` | 400 | Body or parameters failed validation |
| `UNAUTHORIZED` | 401 | No `Bearer` header, an unverified Google email, or a deleted account |
| `INVALID_TOKEN` | 401 | A bad JWT, a used or expired refresh token or extension code, a PKCE mismatch, or a Google token for another app |
| `EXPIRED_TOKEN` | 401 | The access token is past its `exp`: refresh and retry |
| `FORBIDDEN` | 403 | Reading suggestions are off |
| `NOT_FOUND` | 404 | No such topic, device or shared slug |
| `RATE_LIMITED` | 429 | Rate limit hit |
| `INTERNAL_ERROR` | 500 / 503 | Server error (details go to the log, never the response), or the database is unreachable on `/health` |

Codes and helpers live in `internal/types/errors.go` (`types.WriteError`, `types.WriteInternalError`). Middleware and handlers all route through them, so a 401 from auth and a 429 from the rate limiter look the same to a client.

---

## Security

- **Secrets at rest are hashes.** Refresh tokens and extension codes are stored as SHA-256 hashes, so a leaked table can't be replayed.
- **Single-use means single-use.** Refresh tokens and extension codes are consumed with one `UPDATE … WHERE … IS NULL … RETURNING`, so two racing requests can't both win.
- **PKCE S256 with a constant-time compare**, and a wrong verifier burns the code.
- **Google tokens must be ours:** `aud` allowlist plus a verified email.
- **Rate limits can't be gamed** with forged tokens or a spoofed `X-Forwarded-For` (`ratelimit_key_test.go`).
- **Bounded requests:** 1 MB bodies, 200 actions per push, 30 s per request.
- **Links are http(s) only**, checked in code and by a table `CHECK`, because source URLs become links on public share pages.
- **Share links are unguessable** (72 random bits) and die for good when a topic goes private.
- **No IP addresses stored.** Sessions keep a user-agent label for the devices list and nothing else.
- **500s leak nothing**: the detail goes to the log only.
- The 2026-10-12 review (govulncheck, AgentShield and a read of auth, sync, sharing and the extension bridge) moved to Go 1.26.9, chi v5.4.0 and `x/text` v0.43.0; govulncheck found nothing after.

---

## Testing

```bash
go test ./...      # unit tests only; the integration tests skip without TEST_DATABASE_URL
make docker-up
make test          # unit + integration, against the docker Postgres on :5433
```

**Unit tests** need no database:

| File | Covers |
|---|---|
| `internal/auth/google_test.go` | A Google token for another app is rejected, ours accepted, and an empty allowlist fails closed |
| `internal/middleware/ratelimit_key_test.go` | A valid token keys on the user; no, forged, expired or made-up tokens key on the rightmost `X-Forwarded-For` address; direct connections key on the socket address |
| `internal/notes/suggest_test.go` | `pickLinks` takes turns and skips what was read, `normTags`, and Gemini then Brave against stub servers |

**Integration tests** (`cmd/api/main_test.go`) run the real router against the docker Postgres. Google, Gemini and Brave are `httptest` stubs. The `id_token` a test sends becomes the account's Google `sub`, so each test makes and touches only its own users and the tests don't collide.

| Test | Covers |
|---|---|
| `TestAuthFlow` | Sign-in → `/me` → refresh rotation (a token works once) → logout → account deletion ends every session |
| `TestSync` | Extension PKCE exchange (a wrong verifier burns the code), repeat-safe pushes, same-title topic merges, another account unable to touch these notes, per-action rejections, an oversized push refused whole, moves and deletes reaching other devices as markers, highlighter colours |
| `TestLibrary` | Reading the library, renames and their case-insensitive clash, the share-link lifecycle, merges, topic deletes, a note saved offline into a deleted topic, devices and Disconnect, cross-account isolation |
| `TestTagsAndSuggestions` | Tag cleaning and ownership, partial `PATCH /me`, off until turned on, built once then served from what was saved, read pages and worn tags filtered out, merges keeping tags, turning off deleting results |

---

## Makefile

| Command | Description |
|---|---|
| `make dev` | `go run ./cmd/api` |
| `make build` | Build `bin/api` |
| `make test` | `go test ./...` with `TEST_DATABASE_URL` set to the local docker Postgres |
| `make sqlc` | Regenerate `internal/db` from `queries/` and `migrations/` |
| `make docker-up` / `make docker-down` | Start / stop the local Postgres. `up` waits until it accepts connections. |
| `make fmt` | `go fmt ./...` |
| `make tidy` | `go mod tidy` |

There is no migrate target: starting the server applies pending migrations to whatever `DATABASE_URL` points at.

---

## Environment Variables

Read from the environment, falling back to a `.env` file in development (real environment variables win). Copy `.env.example` to `.env`; never commit `.env`.

| Variable | Required | Description |
|---|---|---|
| `DATABASE_URL` | yes | Postgres URL. Local: `postgres://stickynotes:stickynotes@localhost:5433/stickynotes?sslmode=disable` |
| `JWT_SECRET` | yes | At least 32 characters; signs access tokens. `openssl rand -hex 32` |
| `GOOGLE_CLIENT_IDS` | yes | Comma-separated OAuth client IDs. A Google ID token is accepted only if its `aud` is listed. |
| `PORT` | — | Default `8080`. Railway sets it. |
| `GEMINI_API_KEY` | for suggestions | From aistudio.google.com |
| `BRAVE_SEARCH_API_KEY` | for suggestions | From api-dashboard.search.brave.com |
| `GEMINI_MODELS` | — | Comma-separated, tried in order. Default `gemini-3.8-flash,gemini-3.5-flash-lite`. Google retires models (`gemini-2.5-flash` stopped serving new keys in 2026): list newer ones here when it does. |
| `TEST_DATABASE_URL` | tests | Set by `make test`. The integration tests skip without it. |

The server refuses to start without the three required variables, and logs a warning when either suggestions key is missing.

---

## Local Development

Needs Go (an older toolchain downloads the version `go.mod` asks for), Docker, and [sqlc](https://docs.sqlc.dev/en/latest/overview/install.html) only if you change SQL.

```bash
make docker-up          # Postgres 17 on localhost:5433
cp .env.example .env    # fill JWT_SECRET and GOOGLE_CLIENT_IDS
make dev                # migrates, then serves http://localhost:8080
curl localhost:8080/health
```

`GOOGLE_CLIENT_IDS` takes the web OAuth client's ID from the stickynotes Google Cloud project; the web app's `.env` uses the same client. A real sign-in needs the web app running too, since it runs the OAuth flow: see the `stickynotes` README. Port 5433 keeps the local database clear of other projects' Postgres on 5432. `docker compose down -v` wipes it.

---

## Deployment

- **Host:** Railway. One service built from GitHub `main` (Railpack, Go version from `go.mod`) and a Postgres service on the private network only.
- **Public domain:** `https://stickynotes-backend-production.up.railway.app` (port 8080).
- **Variables:** `DATABASE_URL=${{Postgres.DATABASE_URL}}`, a fresh `JWT_SECRET` (never the local one), `GOOGLE_CLIENT_IDS=<web client id>`, and `GEMINI_API_KEY` + `BRAVE_SEARCH_API_KEY` for reading suggestions. Railway sets `PORT`.
- **Health check:** `railway.json` sets `/health`, which pings Postgres, so a crash-looping deploy no longer shows as healthy.
- **Schema:** applied on boot from the embedded migrations, before the server listens.
- **Lifecycle:** `SIGINT`/`SIGTERM` drain in-flight requests for up to 30 s.
- **Deploy order:** when a migration ships, deploy the backend before the web. Clients tolerate an API that is ahead of them: an older extension sends no colour and gets yellow, and the web hides reading suggestions when `/me` has no `suggestions` field.
- **One replica.** Rate limits and the suggestions pause and in-flight builds live in the process. Before running a second replica, move rate limiting to `httprate-redis` and that suggestions state to Postgres.

---

## Conventions

- **sqlc is generated — never hand-edit `internal/db`.** Change `queries/` or `migrations/`, run `make sqlc`, commit both.
- **Schema changes are a new numbered migration pair**, up and down.
- **Every query on user data filters by `user_id`**, and ownership is checked in the statement that writes. There is no row-level security to fall back on.
- **Writes are repeat-safe:** client ids, `ON CONFLICT DO NOTHING`, one-statement consumes.
- **A bad hint never costs a note.** Anchors, colours, titles and timestamps are coerced, not rejected.
- **Errors go through `types.WriteError`**, and 500s through `types.WriteInternalError` with the detail in the log.
- **Optional features degrade quietly.** Without the suggestions keys the API serves everything else and says so once at boot.
- **Comments explain *why*.** The rightmost `X-Forwarded-For` entry, consuming a code before checking its verifier, the five-minute pull overlap, the savepoint per action: keep those comments, they are why the next change doesn't re-break it.
- **Deliberate shortcuts say so.** A `ponytail:` comment names the ceiling and the upgrade path.

---

## Known Limits

Each is marked with a `ponytail:` comment where it lives.

| Limit | Where | When it matters, and the fix |
|---|---|---|
| A first pull and `GET /library` send every live note in one response | `notes/sync.go`, `notes/library.go` | Page them past tens of thousands of highlights |
| Each sign-in makes one tokeninfo round-trip to Google | `auth/google.go` | Verify locally against Google's JWKS if sign-in volume grows |
| `/auth/refresh` has no Bearer, so web refreshes share the Next.js server's IP bucket (300/min) | `cmd/api/main.go` | Key refresh by token hash if that limit bites |
| The suggestions pause and in-flight builds live in the process | `notes/suggest.go` | With more than one replica, each pauses on its own and two could build one topic at once: move both to Postgres |
| Two different pages with the same plain title count as one link | `notes/suggest.go` (`titleKey`) | Compare the rest of the title if it starts hiding pages worth reading |
| A note saved offline into a deleted topic whose title a live topic has since taken lands in the deleted topic and stays hidden | `notes/sync.go` (`ReviveTopic`) | Route it to the live topic if this happens |

Also: revoked refresh tokens and used extension codes are never deleted (only an account deletion removes them). A periodic `DELETE` is the fix if those tables grow.

---

## Troubleshooting

| Symptom | Cause / fix |
|---|---|
| Won't start: `DATABASE_URL is required` | Set `DATABASE_URL` (`.env` locally, Railway variables in production). |
| Won't start: `JWT_SECRET must be at least 32 characters` | `openssl rand -hex 32`. |
| Won't start: `GOOGLE_CLIENT_IDS is required` | Set it to the web OAuth client's ID. |
| Won't start: `ping postgres` or `run migrations` in the log | Postgres isn't reachable (`make docker-up`), or a migration failed: the log line says which. |
| Boot warning: `reading suggestions find nothing until GEMINI_API_KEY and BRAVE_SEARCH_API_KEY are set` | Expected without the keys. Everything else works. |
| Sign-in fails; the log has `google_oauth_audience_mismatch` | The web app's `GOOGLE_CLIENT_ID` isn't in `GOOGLE_CLIENT_IDS`. |
| The integration tests are skipped | `TEST_DATABASE_URL` is unset. Use `make test` after `make docker-up`. |
| `make test` can't connect to :5433 | Docker isn't running, or the container isn't up: `make docker-up`. |
| `TestLibrary` fails on the pull after a merge, locally | The Docker VM's clock lags the host, so rows look older than the cursor. Restart Docker Desktop. |
| `GET /topics/{id}/suggestions` is 403 | The user hasn't turned suggestions on (`PATCH /me { "suggestions": true }`). |
| Suggestions stay empty | Missing API keys; a build failed in the last 10 minutes (builds are paused); the user hit 25 builds in 24 hours; or the topic has no notes. The log has `suggestions: build failed, pausing builds` with the reason. |
| Gemini answers 404 for a model | Google retired it. List current models in `GEMINI_MODELS`. |
| Gemini answers 503 "high demand" | The free tier is overloaded. The next model in `GEMINI_MODELS` is tried; if all fail, builds pause for 10 minutes. |
| `At most 200 actions per push` | Send smaller batches. The extension sends at most 100 actions and 512 KB per push. |
| A note saved in the extension never reaches the library | The extension was never connected: there is no `POST /api/v1/extension/codes` in the log. Unconnected saves wait in the extension's outbox and sync once it connects. |
| A disconnected device keeps syncing for a few minutes | Its access token lives up to 15 minutes. Expected. |
| 429 `RATE_LIMITED` | 300/min per user or IP; 20/min on `/auth/google` and `/extension/token`. |

---

## Related Repositories

| Repository | Description | Stack |
|---|---|---|
| `stickynotes` | Web app and browser extension (`extension/`); `PLAN.md` there holds the decisions and phase status | Next.js 15.5, React 19.1, TypeScript 5.9 · Manifest V3 |

---

## Contact

**Email:** contactus@paperboxd.in  
**Website:** [stickynotes.in](https://www.stickynotes.in), a [PaperBoxd](https://paperboxd.in) production

---

*Powered by Go, PostgreSQL, and a single binary.*

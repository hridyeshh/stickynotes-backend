# stickynotes-backend

Go API for stickynotes: chi, pgx + sqlc, Postgres. Migrations are embedded and run on start.

```sh
make docker-up        # Postgres on localhost:5433
cp .env.example .env  # then fill JWT_SECRET and GOOGLE_CLIENT_IDS (GEMINI_API_KEY and BRAVE_SEARCH_API_KEY are optional: reading suggestions)
make dev              # http://localhost:8080/health
make test             # unit tests + DB integration test (needs docker-up)
make sqlc             # after editing migrations/ or queries/
```

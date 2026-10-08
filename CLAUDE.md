# stickynotes-backend

Plan, status and next steps: `../stickynotes/PLAN.md`. Read it first.

- Go API mirroring `../paperboxd-backend`: chi, pgx + sqlc, embedded golang-migrate, slog, `types.WriteError`.
  Never copy paperboxd env values.
- `make docker-up && make dev`, `make test`, and `make sqlc` after editing `migrations/` or `queries/`.
- Every query on user data filters by user_id; there is no row-level security.

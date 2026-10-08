// Command api serves the stickynotes HTTP API.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/httprate"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hridyeshh/stickynotes-backend/internal/auth"
	"github.com/hridyeshh/stickynotes-backend/internal/config"
	"github.com/hridyeshh/stickynotes-backend/internal/db"
	appMiddleware "github.com/hridyeshh/stickynotes-backend/internal/middleware"
	"github.com/hridyeshh/stickynotes-backend/internal/types"
	"github.com/hridyeshh/stickynotes-backend/migrations"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg, err := config.Load()
	if err != nil {
		slog.Error("load config", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("create db pool", "error", err)
		os.Exit(1)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		slog.Error("ping postgres", "error", err)
		os.Exit(1)
	}

	// Embedded migrations run on every start, so a deploy converges the schema.
	if err := runMigrations(cfg.DatabaseURL); err != nil {
		slog.Error("run migrations", "error", err)
		os.Exit(1)
	}

	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      routes(cfg, pool),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	serverErr := make(chan error, 1)
	go func() {
		slog.Info("starting server", "port", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	select {
	case err := <-serverErr:
		slog.Error("server error", "error", err)
		os.Exit(1)
	case <-ctx.Done():
		slog.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("graceful shutdown failed", "error", err)
	}
	slog.Info("server stopped")
}

// routes builds the HTTP handler. It is separate from main so tests run the real router.
func routes(cfg *config.Config, pool *pgxpool.Pool) http.Handler {
	authH := auth.NewHandler(db.New(pool), cfg)

	r := chi.NewRouter()
	r.Use(chimiddleware.RealIP) // trusts X-Forwarded-For: right behind Railway's proxy, wrong if exposed directly
	r.Use(chimiddleware.Logger)
	r.Use(chimiddleware.Recoverer)
	r.Use(chimiddleware.Timeout(30 * time.Second))
	r.Use(chimiddleware.RequestSize(1 << 20))

	// Rate limits key on the Bearer token when present, else on IP.
	// ponytail: /auth/refresh has no Bearer, so every web user's refresh shares the
	// Next server's egress-IP bucket; key refresh by token hash if 300/min ever bites.
	limit := func(perMinute int) func(http.Handler) http.Handler {
		return httprate.Limit(perMinute, time.Minute,
			httprate.WithKeyFuncs(appMiddleware.KeyByAuthorizationOrIP),
			httprate.WithLimitHandler(func(w http.ResponseWriter, _ *http.Request) {
				types.WriteError(w, http.StatusTooManyRequests, types.ErrCodeRateLimited, "Too many requests")
			}),
		)
	}
	r.Use(limit(300))

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		if err := pool.Ping(r.Context()); err != nil {
			types.WriteError(w, http.StatusServiceUnavailable, types.ErrCodeInternalServer, "Database unreachable")
			return
		}
		types.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	r.Route("/api/v1", func(r chi.Router) {
		r.Route("/auth", func(r chi.Router) {
			r.With(limit(20)).Post("/google", authH.Google) // each call hits Google
			r.Post("/refresh", authH.Refresh)
			r.Post("/logout", authH.Logout)
		})

		r.Group(func(r chi.Router) {
			r.Use(appMiddleware.Authenticate(cfg.JWTSecret))
			r.Get("/me", authH.Me)
			r.Delete("/me", authH.DeleteMe)
		})
	})

	return r
}

// runMigrations applies pending up migrations from the embedded SQL files.
// It is a no-op when the schema is already current.
func runMigrations(databaseURL string) error {
	src, err := iofs.New(migrations.Files, ".")
	if err != nil {
		return fmt.Errorf("open migration source: %w", err)
	}
	defer src.Close()

	// golang-migrate picks its driver by URL scheme; pgx/v5 registers as "pgx5".
	migrateURL := databaseURL
	for _, prefix := range []string{"postgresql://", "postgres://"} {
		if strings.HasPrefix(migrateURL, prefix) {
			migrateURL = "pgx5://" + strings.TrimPrefix(migrateURL, prefix)
			break
		}
	}

	m, err := migrate.NewWithSourceInstance("iofs", src, migrateURL)
	if err != nil {
		return fmt.Errorf("init migrate: %w", err)
	}
	defer m.Close()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}

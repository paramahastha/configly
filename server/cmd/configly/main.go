package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/paramahastha/configly/server/internal/api"
	"github.com/paramahastha/configly/server/internal/auth"
	"github.com/paramahastha/configly/server/internal/model"
	"github.com/paramahastha/configly/server/internal/sse"
	"github.com/paramahastha/configly/server/internal/store"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	addr := flag.String("addr", envOr("CONFIGLY_ADDR", ":8080"), "listen address")
	dbPath := flag.String("db", envOr("CONFIGLY_DB", "configly.db"), "SQLite database path")
	flag.Parse()

	st, err := store.NewSQLite(*dbPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	ctx := context.Background()
	if err := bootstrap(ctx, st); err != nil {
		return fmt.Errorf("bootstrap: %w", err)
	}

	broker := sse.NewBroker()
	h := &api.Handler{Store: st, Broker: broker}

	srv := &http.Server{
		Handler:           h.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Bind before logging so "listening on" is only printed on success.
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	log.Printf("configly listening on %s", ln.Addr())

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-serveErr:
		if err != nil && err != http.ErrServerClosed {
			return fmt.Errorf("server: %w", err)
		}
	case <-quit:
	}

	log.Println("shutting down...")
	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		log.Printf("shutdown error: %v", err)
	}
	log.Println("stopped")
	return nil
}

// bootstrap runs only on the first start (when the users table is empty).
// It creates the admin user, prints the one-time API key, and seeds the
// default project's environments.
func bootstrap(ctx context.Context, st store.Store) error {
	users, err := st.ListUsers(ctx)
	if err != nil {
		return fmt.Errorf("list users: %w", err)
	}

	if len(users) == 0 {
		adminEmail := envOr("CONFIGLY_ADMIN_EMAIL", "admin@configly.local")
		adminPW := envOr("CONFIGLY_ADMIN_PASSWORD", "changeme")

		hash, err := auth.HashPassword(adminPW)
		if err != nil {
			return fmt.Errorf("hash admin password: %w", err)
		}

		u := &model.User{Email: adminEmail, PasswordHash: hash, Role: model.RoleAdmin}
		if err := st.CreateUser(ctx, u); err != nil {
			return fmt.Errorf("create admin user: %w", err)
		}

		rawKey, err := auth.NewAPIKey()
		if err != nil {
			return fmt.Errorf("generate api key: %w", err)
		}

		k := &model.APIKey{
			UserID:  u.ID,
			Name:    "bootstrap",
			Prefix:  rawKey[:8],
			KeyHash: store.HashAPIKey(rawKey),
		}
		if err := st.CreateAPIKey(ctx, k, "system"); err != nil {
			return fmt.Errorf("create api key: %w", err)
		}

		fmt.Println()
		fmt.Println("============================================================")
		fmt.Println(" Configly bootstrap")
		fmt.Println("------------------------------------------------------------")
		fmt.Printf(" Admin email:    %s\n", adminEmail)
		fmt.Printf(" Admin password: %s\n", adminPW)
		fmt.Printf(" Admin API key:  %s\n", rawKey)
		fmt.Println()
		fmt.Println(" SAVE THIS API KEY. It is shown only once.")
		fmt.Println("============================================================")
		fmt.Println()
	}

	return seedDefaultProject(ctx, st)
}

// seedDefaultProject creates the default project's standard environments (dev,
// staging, prod) on first run. Skipped on subsequent starts when envs exist.
func seedDefaultProject(ctx context.Context, st store.Store) error {
	envs, err := st.ListEnvironments(ctx, "default")
	if err != nil {
		return fmt.Errorf("list environments: %w", err)
	}
	if len(envs) > 0 {
		return nil
	}

	for _, name := range []string{"dev", "staging", "prod"} {
		env := &model.Environment{Project: "default", Name: name}
		if err := st.CreateEnvironment(ctx, env); err != nil {
			return fmt.Errorf("create environment %q: %w", name, err)
		}
	}
	return nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

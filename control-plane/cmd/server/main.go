// control-plane/cmd/server/main.go
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/api"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/observability"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/storage"
)

func main() {
	logger := observability.NewLogger(os.Getenv("LOG_LEVEL"))
	slog.SetDefault(logger)

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		slog.Error("DATABASE_URL environment variable is required")
		os.Exit(1)
	}

	pool, err := storage.Connect(context.Background(), dbURL)
	if err != nil {
		slog.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err := storage.RunMigrations(dbURL); err != nil {
		slog.Error("failed to run migrations", "error", err)
		os.Exit(1)
	}
	slog.Info("migrations applied")

	port := os.Getenv("SERVER_PORT")
	if port == "" {
		port = "8080"
	}

	r := api.NewRouter(pool, logger)
	slog.Info("server starting", "port", port)
	if err := http.ListenAndServe(fmt.Sprintf(":%s", port), r); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

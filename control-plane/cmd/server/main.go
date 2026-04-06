// control-plane/cmd/server/main.go
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/api"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/events"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/jobs"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/k8s"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/mlflow"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/models"
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

	// Kubernetes dynamic client (optional — warn and skip if unavailable)
	k8sClient, err := k8s.NewDynamicClient()
	if err != nil {
		slog.Warn("kubernetes client unavailable, dispatcher will not submit RayJobs", "error", err)
	}

	// Kafka publisher (optional — fall back to no-op if not configured)
	var publisher events.Publisher
	if brokerAddr := os.Getenv("KAFKA_BROKER"); brokerAddr != "" {
		kp := events.NewKafkaPublisher(brokerAddr)
		defer kp.Close()
		publisher = kp
		slog.Info("kafka publisher configured", "broker", brokerAddr)
	} else {
		publisher = &events.NoOpPublisher{}
		slog.Warn("KAFKA_BROKER not set, events will not be published")
	}

	store := jobs.NewPostgresJobStore(pool)

	// Start dispatcher goroutine
	if k8sClient != nil {
		dispatchInterval := 5 * time.Second
		dispatcher := jobs.NewDispatcher(store, k8sClient, publisher, dispatchInterval)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go dispatcher.Run(ctx)
		slog.Info("dispatcher started", "interval", dispatchInterval)
	}

	// Internal HTTP server (operator callbacks) on a separate port
	internalPort := os.Getenv("SERVER_INTERNAL_PORT")
	if internalPort == "" {
		internalPort = "8081"
	}
	internalHandler := api.NewInternalRouter(store, publisher)
	go func() {
		slog.Info("internal server starting", "port", internalPort)
		if err := http.ListenAndServe(fmt.Sprintf(":%s", internalPort), internalHandler); err != nil {
			slog.Error("internal server stopped", "error", err)
		}
	}()

	// MLflow client and models service
	mlflowTrackingURI := os.Getenv("MLFLOW_TRACKING_URI")
	if mlflowTrackingURI == "" {
		mlflowTrackingURI = "http://localhost:5000"
		slog.Warn("MLFLOW_TRACKING_URI not set, using default", "uri", mlflowTrackingURI)
	}
	mlflowClient := mlflow.New(mlflowTrackingURI)
	modelStore := models.NewPostgresModelStore(pool)
	modelsSvc := models.NewService(modelStore, store, mlflowClient)

	// Public HTTP server
	port := os.Getenv("SERVER_PORT")
	if port == "" {
		port = "8080"
	}
	r := api.NewRouter(pool, store, publisher, modelsSvc)
	slog.Info("server starting", "port", port)
	if err := http.ListenAndServe(fmt.Sprintf(":%s", port), r); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

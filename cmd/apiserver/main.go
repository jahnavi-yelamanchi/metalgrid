package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	metalgridv1alpha1 "github.com/jahnavi-yelamanchi/metalgrid/api/v1alpha1"
	"github.com/jahnavi-yelamanchi/metalgrid/internal/api"
	"github.com/jahnavi-yelamanchi/metalgrid/internal/queue"
	"github.com/jahnavi-yelamanchi/metalgrid/internal/store"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	addr := envOr("LISTEN_ADDR", ":8080")
	dsn := envOr("DATABASE_URL", "postgres://metalgrid:metalgrid@localhost:5432/metalgrid")
	natsURL := envOr("NATS_URL", "nats://localhost:4222")
	namespace := envOr("JOB_NAMESPACE", "default")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	scheme := clientgoscheme.Scheme
	utilruntime.Must(metalgridv1alpha1.AddToScheme(scheme))
	k8sClient, err := client.New(ctrl.GetConfigOrDie(), client.Options{Scheme: scheme})
	if err != nil {
		logger.Error("building k8s client", "error", err)
		os.Exit(1)
	}

	st, err := store.New(ctx, dsn)
	if err != nil {
		logger.Error("connecting to postgres", "error", err)
		os.Exit(1)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		logger.Error("migrating schema", "error", err)
		os.Exit(1)
	}

	q, err := queue.Connect(ctx, natsURL)
	if err != nil {
		logger.Error("connecting to nats", "error", err)
		os.Exit(1)
	}
	defer q.Close()

	srv := &api.Server{
		Store:     st,
		Queue:     q,
		K8s:       k8sClient,
		Namespace: namespace,
		Logger:    logger,
	}

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()

	logger.Info("listening", "addr", addr)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

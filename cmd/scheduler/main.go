// Command scheduler is an HTTP extender for the stock kube-scheduler: it
// adds bin-pack/spread node scoring and a gang-scheduling capacity gate
// without running a second full scheduler binary.
package main

import (
	"log/slog"
	"net/http"
	"os"
	"time"

	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/jahnavi-yelamanchi/metalgrid/internal/scheduler"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	addr := envOr("LISTEN_ADDR", ":8888")

	clientset, err := kubernetes.NewForConfig(ctrl.GetConfigOrDie())
	if err != nil {
		logger.Error("building k8s clientset", "error", err)
		os.Exit(1)
	}

	ext := &scheduler.Extender{Clientset: clientset}
	srv := &http.Server{
		Addr:              addr,
		Handler:           ext.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	logger.Info("listening", "addr", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
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

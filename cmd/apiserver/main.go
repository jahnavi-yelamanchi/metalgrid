package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	metalgridv1alpha1 "github.com/jahnavi-yelamanchi/metalgrid/api/v1alpha1"
	"github.com/jahnavi-yelamanchi/metalgrid/internal/api"
	"github.com/jahnavi-yelamanchi/metalgrid/internal/grpcapi"
	"github.com/jahnavi-yelamanchi/metalgrid/internal/grpcapi/pb"
	"github.com/jahnavi-yelamanchi/metalgrid/internal/queue"
	"github.com/jahnavi-yelamanchi/metalgrid/internal/service"
	"github.com/jahnavi-yelamanchi/metalgrid/internal/store"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	addr := envOr("LISTEN_ADDR", ":8080")
	grpcAddr := envOr("GRPC_LISTEN_ADDR", ":9090")
	dsn := envOr("DATABASE_URL", "postgres://metalgrid:metalgrid@localhost:5432/metalgrid")
	natsURL := envOr("NATS_URL", "nats://localhost:4222")
	namespace := envOr("JOB_NAMESPACE", "default")
	jwtSecret := os.Getenv("JWT_SECRET") // empty disables auth (local dev)
	rateLimit, _ := strconv.ParseFloat(envOr("RATE_LIMIT_RPS", "5"), 64)
	rateBurst, _ := strconv.Atoi(envOr("RATE_LIMIT_BURST", "10"))

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

	jobs := &service.JobService{
		Store:     st,
		Queue:     q,
		K8s:       k8sClient,
		Namespace: namespace,
		Logger:    logger,
	}
	srv := &api.Server{
		Jobs:      jobs,
		Logger:    logger,
		JWTSecret: []byte(jwtSecret),
		RateLimit: rateLimit,
		RateBurst: rateBurst,
	}

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	grpcServer := grpc.NewServer()
	pb.RegisterJobsServiceServer(grpcServer, &grpcapi.Server{Jobs: jobs})
	reflection.Register(grpcServer)
	grpcLis, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		logger.Error("binding grpc listener", "error", err)
		os.Exit(1)
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
		grpcServer.GracefulStop()
	}()

	go func() {
		logger.Info("grpc listening", "addr", grpcAddr)
		if err := grpcServer.Serve(grpcLis); err != nil {
			logger.Error("grpc server stopped", "error", err)
		}
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

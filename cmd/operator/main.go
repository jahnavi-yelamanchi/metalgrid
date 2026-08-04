package main

import (
	"context"
	"encoding/json"
	"flag"
	"os"

	"github.com/nats-io/nats.go"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	metalgridv1alpha1 "github.com/jahnavi-yelamanchi/metalgrid/api/v1alpha1"
	"github.com/jahnavi-yelamanchi/metalgrid/internal/controller"
	"github.com/jahnavi-yelamanchi/metalgrid/internal/queue"
	"github.com/jahnavi-yelamanchi/metalgrid/internal/webhook"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(metalgridv1alpha1.AddToScheme(scheme))
}

func main() {
	var metricsAddr, probeAddr, natsURL, namespace string
	var enableLeaderElection, enableWebhook bool
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "address for metrics endpoint")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "address for health probe endpoint")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false, "enable leader election for controller manager")
	var leaderElectionNamespace string
	flag.StringVar(&leaderElectionNamespace, "leader-election-namespace", envOr("JOB_NAMESPACE", "default"), "namespace to hold the leader election Lease in (no in-cluster pod namespace to infer outside a cluster)")
	flag.StringVar(&natsURL, "nats-url", envOr("NATS_URL", nats.DefaultURL), "NATS server URL for the job submission queue")
	flag.StringVar(&namespace, "namespace", envOr("JOB_NAMESPACE", "default"), "namespace to create AcceleratorJob resources in")
	flag.BoolVar(&enableWebhook, "enable-webhook", false, "serve the AcceleratorJob admission webhook (requires TLS certs in ./k8s-webhook-server/serving-certs; on for in-cluster deployment, off for local dev)")
	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	setupLog := ctrl.Log.WithName("setup")

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                  scheme,
		Metrics:                 metricsserver.Options{BindAddress: metricsAddr},
		LeaderElection:          enableLeaderElection,
		LeaderElectionID:        "metalgrid-operator-leader",
		LeaderElectionNamespace: leaderElectionNamespace,
		HealthProbeBindAddress:  probeAddr,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if err := (&controller.AcceleratorJobReconciler{Client: mgr.GetClient()}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "AcceleratorJob")
		os.Exit(1)
	}

	if err := (&controller.InferenceServiceReconciler{Client: mgr.GetClient()}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "InferenceService")
		os.Exit(1)
	}

	if enableWebhook {
		if err := (&webhook.AcceleratorJobValidator{Client: mgr.GetClient()}).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create webhook", "webhook", "AcceleratorJob")
			os.Exit(1)
		}
	}

	q, err := queue.Connect(context.Background(), natsURL)
	if err != nil {
		setupLog.Error(err, "unable to connect to nats")
		os.Exit(1)
	}
	defer q.Close()

	if err := mgr.Add(&submissionConsumer{
		queue:   q,
		handler: controller.SubmissionHandler(mgr.GetClient(), namespace),
	}); err != nil {
		setupLog.Error(err, "unable to register submission consumer")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// submissionConsumer drains the NATS JetStream job queue and creates
// AcceleratorJob CRDs. It only runs on the leader replica so two operator
// pods never race to create the same job twice.
type submissionConsumer struct {
	queue   *queue.Queue
	handler func(context.Context, []byte) error
}

func (s *submissionConsumer) Start(ctx context.Context) error {
	err := s.queue.ConsumeFairShare(ctx, 5, submissionTeam, s.handler)
	if ctx.Err() != nil {
		return nil
	}
	return err
}

// submissionTeam extracts the team from a queued job submission so the
// consumer can interleave fairly across teams instead of strict FIFO.
func submissionTeam(payload []byte) string {
	var sub queue.JobSubmission
	if err := json.Unmarshal(payload, &sub); err != nil {
		return ""
	}
	return sub.Team
}

func (s *submissionConsumer) NeedLeaderElection() bool {
	return true
}

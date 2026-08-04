// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
package main

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"time"

	"github.com/nats-io/nats.go"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	metalgridv1alpha1 "github.com/jahnavi-yelamanchi/metalgrid/api/v1alpha1"
	"github.com/jahnavi-yelamanchi/metalgrid/internal/controller"
	"github.com/jahnavi-yelamanchi/metalgrid/internal/metrics"
	"github.com/jahnavi-yelamanchi/metalgrid/internal/queue"
	"github.com/jahnavi-yelamanchi/metalgrid/internal/tracing"
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
	otlpEndpoint := envOr("OTLP_ENDPOINT", "") // empty disables tracing (noop tracer)
	// Development:false selects zap's JSON encoder — matches every other
	// component (all on slog JSON) so a log shipper (Loki/promtail) sees one
	// consistent structured format cluster-wide.
	opts := zap.Options{Development: false}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	setupLog := ctrl.Log.WithName("setup")
	metrics.RegisterOperator(crmetrics.Registry)

	shutdownTracing, err := tracing.Init(context.Background(), "metalgrid-operator", otlpEndpoint)
	if err != nil {
		setupLog.Error(err, "unable to initialize tracing")
		os.Exit(1)
	}
	defer func() { _ = shutdownTracing(context.Background()) }()

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

	if err := mgr.Add(&metricsPoller{client: mgr.GetClient(), queue: q, namespace: namespace}); err != nil {
		setupLog.Error(err, "unable to register metrics poller")
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

// metricsPoller periodically refreshes the gauges that don't have a natural
// single event to update on: how many jobs are in each phase right now,
// how deep the submit queue is, and how much accelerator capacity is
// allocated cluster-wide. Runs only on the leader so replicas don't fight
// over setting the same gauges.
type metricsPoller struct {
	client    client.Client
	queue     *queue.Queue
	namespace string
}

func (p *metricsPoller) Start(ctx context.Context) error {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		p.poll(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (p *metricsPoller) poll(ctx context.Context) {
	var jobs metalgridv1alpha1.AcceleratorJobList
	if err := p.client.List(ctx, &jobs, client.InNamespace(p.namespace)); err == nil {
		counts := map[metalgridv1alpha1.AcceleratorJobPhase]int{}
		for _, j := range jobs.Items {
			counts[j.Status.Phase]++
		}
		for _, phase := range []metalgridv1alpha1.AcceleratorJobPhase{
			metalgridv1alpha1.AcceleratorJobPending, metalgridv1alpha1.AcceleratorJobRunning,
			metalgridv1alpha1.AcceleratorJobSucceeded, metalgridv1alpha1.AcceleratorJobFailed,
		} {
			metrics.JobPhaseCount.WithLabelValues(string(phase)).Set(float64(counts[phase]))
		}
	}

	if depth, err := p.queue.PendingCount(ctx); err == nil {
		metrics.QueueDepth.Set(float64(depth))
	}

	// Node.Status.Allocatable is static (capacity minus system-reserved) and
	// does not shrink as pods land — the only way to learn what's actually
	// in use right now is to sum requests across live pods.
	var nodes corev1.NodeList
	var total int64
	if err := p.client.List(ctx, &nodes); err == nil {
		for _, n := range nodes.Items {
			cap := n.Status.Capacity[controller.AcceleratorResourceName]
			total += cap.Value()
		}
	}

	var pods corev1.PodList
	var allocated int64
	if err := p.client.List(ctx, &pods, client.InNamespace(p.namespace)); err == nil {
		for _, pod := range pods.Items {
			if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
				continue
			}
			for _, c := range pod.Spec.Containers {
				if q, ok := c.Resources.Requests[controller.AcceleratorResourceName]; ok {
					allocated += q.Value()
				}
			}
		}
	}

	if total > 0 {
		metrics.AcceleratorUtilization.Set(float64(allocated) / float64(total))
	}
}

func (p *metricsPoller) NeedLeaderElection() bool {
	return true
}

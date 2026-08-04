package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	metalgridv1alpha1 "github.com/jahnavi-yelamanchi/metalgrid/api/v1alpha1"
)

// AcceleratorResourceName is the extended resource the mock device plugin advertises.
const AcceleratorResourceName corev1.ResourceName = "metalgrid.dev/accelerator"

const jobLabel = "metalgrid.dev/job"

// AcceleratorJobReconciler reconciles an AcceleratorJob object.
type AcceleratorJobReconciler struct {
	client.Client
}

// +kubebuilder:rbac:groups=metalgrid.dev,resources=acceleratorjobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=metalgrid.dev,resources=acceleratorjobs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=metalgrid.dev,resources=acceleratorjobs/finalizers,verbs=update
// +kubebuilder:rbac:groups=metalgrid.dev,resources=acceleratorpools,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;update;patch;delete

func (r *AcceleratorJobReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var job metalgridv1alpha1.AcceleratorJob
	if err := r.Get(ctx, req.NamespacedName, &job); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !job.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &job)
	}

	if !controllerutil.ContainsFinalizer(&job, metalgridv1alpha1.AcceleratorFinalizer) {
		controllerutil.AddFinalizer(&job, metalgridv1alpha1.AcceleratorFinalizer)
		if err := r.Update(ctx, &job); err != nil {
			return ctrl.Result{}, fmt.Errorf("adding finalizer: %w", err)
		}
		return ctrl.Result{Requeue: true}, nil
	}

	if job.Status.Phase == metalgridv1alpha1.AcceleratorJobSucceeded || job.Status.Phase == metalgridv1alpha1.AcceleratorJobFailed {
		return ctrl.Result{}, nil
	}

	var pool *metalgridv1alpha1.AcceleratorPool
	if job.Spec.Pool != "" {
		var p metalgridv1alpha1.AcceleratorPool
		if err := r.Get(ctx, types.NamespacedName{Name: job.Spec.Pool}, &p); err != nil {
			if apierrors.IsNotFound(err) {
				// Soft dependency wait, not a terminal failure: the pool may
				// simply not have synced into our watch cache yet, or an
				// operator could still create it. Retry with backoff instead
				// of failing the job outright.
				logger.Info("AcceleratorPool not found, waiting", "pool", job.Spec.Pool)
				return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
			}
			return ctrl.Result{}, fmt.Errorf("getting AcceleratorPool: %w", err)
		}
		pool = &p
	}

	gangSize := job.Spec.GangSize
	if gangSize < 1 {
		gangSize = 1
	}

	var podList corev1.PodList
	if err := r.List(ctx, &podList, client.InNamespace(job.Namespace), client.MatchingLabels{jobLabel: job.Name}); err != nil {
		return ctrl.Result{}, fmt.Errorf("listing pods: %w", err)
	}
	existing := make(map[string]corev1.Pod, len(podList.Items))
	for _, p := range podList.Items {
		existing[p.Name] = p
	}

	var created bool
	for i := int32(0); i < gangSize; i++ {
		name := podName(&job, i)
		if _, ok := existing[name]; ok {
			continue
		}
		newPod := buildPod(&job, pool, name, i, gangSize)
		if err := controllerutil.SetControllerReference(&job, newPod, r.Scheme()); err != nil {
			return ctrl.Result{}, fmt.Errorf("setting owner reference: %w", err)
		}
		if err := r.Create(ctx, newPod); err != nil {
			return ctrl.Result{}, fmt.Errorf("creating pod: %w", err)
		}
		logger.Info("created job pod", "pod", newPod.Name)
		created = true
	}

	if created && job.Status.Phase == "" {
		job.Status.Phase = metalgridv1alpha1.AcceleratorJobPending
		if err := r.Status().Update(ctx, &job); err != nil {
			return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
		}
	}
	if created {
		// Freshly created pods aren't in podList yet; re-reconcile once the
		// Owns(&corev1.Pod{}) watch fires on their creation/status events.
		return ctrl.Result{}, nil
	}

	return ctrl.Result{}, r.syncStatusFromPods(ctx, &job, podList.Items)
}

func (r *AcceleratorJobReconciler) syncStatusFromPods(ctx context.Context, job *metalgridv1alpha1.AcceleratorJob, pods []corev1.Pod) error {
	if len(pods) == 0 {
		return nil
	}

	phase := aggregatePhase(pods)
	message := ""
	if phase == metalgridv1alpha1.AcceleratorJobFailed {
		for _, p := range pods {
			if p.Status.Phase == corev1.PodFailed {
				message = p.Status.Message
				break
			}
		}
	}

	if phase == job.Status.Phase {
		return nil
	}

	job.Status.Phase = phase
	job.Status.Message = message
	if job.Status.PodName == "" {
		job.Status.PodName = pods[0].Name
	}
	now := metav1.Now()
	if phase == metalgridv1alpha1.AcceleratorJobRunning && job.Status.StartTime == nil {
		job.Status.StartTime = &now
	}
	if (phase == metalgridv1alpha1.AcceleratorJobSucceeded || phase == metalgridv1alpha1.AcceleratorJobFailed) && job.Status.CompletionTime == nil {
		job.Status.CompletionTime = &now
	}
	return r.Status().Update(ctx, job)
}

// aggregatePhase combines gang member pod phases into a single job phase:
// any failure fails the job, all-succeeded succeeds it, any running/succeeded
// mix counts as running, otherwise it's still pending.
func aggregatePhase(pods []corev1.Pod) metalgridv1alpha1.AcceleratorJobPhase {
	succeeded := 0
	for _, p := range pods {
		switch p.Status.Phase {
		case corev1.PodFailed:
			return metalgridv1alpha1.AcceleratorJobFailed
		case corev1.PodSucceeded:
			succeeded++
		}
	}
	if succeeded == len(pods) {
		return metalgridv1alpha1.AcceleratorJobSucceeded
	}
	for _, p := range pods {
		if p.Status.Phase == corev1.PodRunning || p.Status.Phase == corev1.PodSucceeded {
			return metalgridv1alpha1.AcceleratorJobRunning
		}
	}
	return metalgridv1alpha1.AcceleratorJobPending
}

func (r *AcceleratorJobReconciler) reconcileDelete(ctx context.Context, job *metalgridv1alpha1.AcceleratorJob) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(job, metalgridv1alpha1.AcceleratorFinalizer) {
		return ctrl.Result{}, nil
	}
	// Pod cleanup is handled by k8s GC via the owner reference. Finalizer exists
	// so future out-of-band cleanup (store rows, quota release) has a hook.
	controllerutil.RemoveFinalizer(job, metalgridv1alpha1.AcceleratorFinalizer)
	if err := r.Update(ctx, job); err != nil {
		return ctrl.Result{}, fmt.Errorf("removing finalizer: %w", err)
	}
	return ctrl.Result{}, nil
}

func podName(job *metalgridv1alpha1.AcceleratorJob, index int32) string {
	if job.Spec.GangSize <= 1 {
		return job.Name
	}
	return fmt.Sprintf("%s-%d", job.Name, index)
}

// priorityClassName maps a job's Priority hint onto one of three native
// PriorityClasses, giving preemption and scheduler queue ordering for free
// from the stock kube-scheduler (no custom queueing code required).
func priorityClassName(priority int32) string {
	switch {
	case priority >= 100:
		return "metalgrid-high"
	case priority <= -100:
		return "metalgrid-low"
	default:
		return "metalgrid-normal"
	}
}

func buildPod(job *metalgridv1alpha1.AcceleratorJob, pool *metalgridv1alpha1.AcceleratorPool, name string, index, gangSize int32) *corev1.Pod {
	resources := *job.Spec.Resources.DeepCopy()
	if resources.Requests == nil {
		resources.Requests = corev1.ResourceList{}
	}
	if resources.Limits == nil {
		resources.Limits = corev1.ResourceList{}
	}
	qty := resource.MustParse(fmt.Sprintf("%d", job.Spec.AcceleratorCount))
	resources.Requests[AcceleratorResourceName] = qty
	resources.Limits[AcceleratorResourceName] = qty

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: job.Namespace,
			Labels: map[string]string{
				jobLabel:              job.Name,
				"metalgrid.dev/team":  job.Spec.Team,
				"metalgrid.dev/index": fmt.Sprint(index),
			},
			Annotations: map[string]string{
				"metalgrid.dev/gang-size": fmt.Sprint(gangSize),
			},
		},
		Spec: corev1.PodSpec{
			RestartPolicy:     corev1.RestartPolicyNever,
			PriorityClassName: priorityClassName(job.Spec.Priority),
			Containers: []corev1.Container{
				{
					Name:      "job",
					Image:     job.Spec.Image,
					Command:   job.Spec.Command,
					Args:      job.Spec.Args,
					Resources: resources,
				},
			},
		},
	}

	if pool != nil {
		pod.Spec.NodeSelector = pool.Spec.NodeSelector
		pod.Spec.Tolerations = pool.Spec.Tolerations
		pod.Labels["metalgrid.dev/pool"] = pool.Name
		pod.Annotations["metalgrid.dev/placement-strategy"] = string(pool.Spec.PlacementStrategy)
	}

	return pod
}

func (r *AcceleratorJobReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&metalgridv1alpha1.AcceleratorJob{}).
		Owns(&corev1.Pod{}).
		Complete(r)
}

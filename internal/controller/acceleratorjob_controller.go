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
	"github.com/jahnavi-yelamanchi/metalgrid/internal/metrics"
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
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create

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

	if job.Status.NextRetryTime != nil {
		if wait := time.Until(job.Status.NextRetryTime.Time); wait > 0 {
			return ctrl.Result{RequeueAfter: wait}, nil
		}
		return ctrl.Result{}, r.startRetry(ctx, &job)
	}

	if job.Spec.Checkpoint {
		if err := r.ensureCheckpointPVC(ctx, &job); err != nil {
			return ctrl.Result{}, fmt.Errorf("ensuring checkpoint PVC: %w", err)
		}
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
		checkpointPVC := ""
		if job.Spec.Checkpoint {
			checkpointPVC = checkpointPVCName(&job)
		}
		newPod := buildPod(&job, pool, name, i, gangSize, checkpointPVC)
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

	if phase == metalgridv1alpha1.AcceleratorJobFailed && job.Status.Phase != metalgridv1alpha1.AcceleratorJobFailed {
		return r.scheduleRetryOrFail(ctx, job, failureMessage(pods))
	}

	if phase == job.Status.Phase {
		return nil
	}

	job.Status.Phase = phase
	if job.Status.PodName == "" {
		job.Status.PodName = pods[0].Name
	}
	now := metav1.Now()
	if phase == metalgridv1alpha1.AcceleratorJobRunning && job.Status.StartTime == nil {
		job.Status.StartTime = &now
		metrics.SchedulingLatencySeconds.Observe(now.Sub(job.CreationTimestamp.Time).Seconds())
	}
	if phase == metalgridv1alpha1.AcceleratorJobSucceeded && job.Status.CompletionTime == nil {
		job.Status.CompletionTime = &now
	}
	return r.Status().Update(ctx, job)
}

func failureMessage(pods []corev1.Pod) string {
	for _, p := range pods {
		if p.Status.Phase == corev1.PodFailed {
			return p.Status.Message
		}
	}
	return ""
}

// backoffDuration is the exponential delay before a job's Nth retry,
// capped so a job that fails many times doesn't wait forever.
func backoffDuration(retryCount int32) time.Duration {
	const base = 5 * time.Second
	const maxBackoff = 60 * time.Second
	d := base << retryCount // 5s, 10s, 20s, 40s, ...
	if d > maxBackoff || d <= 0 { // shift overflow also falls through to the cap
		return maxBackoff
	}
	return d
}

// scheduleRetryOrFail either schedules a backed-off retry (deferred to
// startRetry once the backoff elapses, so failed pods stay around for
// inspection until then) or, once MaxRetries is exhausted, marks the job
// permanently Failed.
func (r *AcceleratorJobReconciler) scheduleRetryOrFail(ctx context.Context, job *metalgridv1alpha1.AcceleratorJob, failMsg string) error {
	now := metav1.Now()

	if job.Status.RetryCount >= job.Spec.MaxRetries {
		job.Status.Phase = metalgridv1alpha1.AcceleratorJobFailed
		job.Status.Message = fmt.Sprintf("failed after %d retries: %s", job.Status.RetryCount, failMsg)
		job.Status.CompletionTime = &now
		return r.Status().Update(ctx, job)
	}

	job.Status.RetryCount++
	job.Status.Phase = metalgridv1alpha1.AcceleratorJobPending
	wait := backoffDuration(job.Status.RetryCount)
	retryAt := metav1.NewTime(now.Add(wait))
	job.Status.NextRetryTime = &retryAt
	job.Status.Message = fmt.Sprintf("retrying after failure (attempt %d/%d) in %s: %s", job.Status.RetryCount, job.Spec.MaxRetries, wait, failMsg)
	return r.Status().Update(ctx, job)
}

// startRetry runs once a scheduled retry's backoff has elapsed: it deletes
// the failed generation's pods and clears NextRetryTime so the normal
// pod-creation path (further up Reconcile) recreates them fresh. A
// checkpoint PVC, if any, is untouched by this delete and still has the
// prior attempt's data for the job image to resume from.
func (r *AcceleratorJobReconciler) startRetry(ctx context.Context, job *metalgridv1alpha1.AcceleratorJob) error {
	job.Status.NextRetryTime = nil
	if err := r.Status().Update(ctx, job); err != nil {
		return fmt.Errorf("clearing retry timer: %w", err)
	}

	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(job.Namespace), client.MatchingLabels{jobLabel: job.Name}); err != nil {
		return fmt.Errorf("listing pods to retry: %w", err)
	}
	for i := range pods.Items {
		if err := r.Delete(ctx, &pods.Items[i]); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("deleting failed pod %s: %w", pods.Items[i].Name, err)
		}
	}
	return nil
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

const checkpointVolumeName = "checkpoint"
const checkpointMountPath = "/checkpoint"

func checkpointPVCName(job *metalgridv1alpha1.AcceleratorJob) string {
	return job.Name + "-checkpoint"
}

// ensureCheckpointPVC creates the job's checkpoint volume once; it's owned
// by the job (not by any single pod attempt), so it survives pod deletion
// and recreation across retries and keeps whatever the job image wrote.
func (r *AcceleratorJobReconciler) ensureCheckpointPVC(ctx context.Context, job *metalgridv1alpha1.AcceleratorJob) error {
	name := checkpointPVCName(job)
	var existing corev1.PersistentVolumeClaim
	err := r.Get(ctx, types.NamespacedName{Namespace: job.Namespace, Name: name}, &existing)
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: job.Namespace},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
			},
		},
	}
	if err := controllerutil.SetControllerReference(job, pvc, r.Scheme()); err != nil {
		return fmt.Errorf("setting owner reference: %w", err)
	}
	return r.Create(ctx, pvc)
}

func buildPod(job *metalgridv1alpha1.AcceleratorJob, pool *metalgridv1alpha1.AcceleratorPool, name string, index, gangSize int32, checkpointPVC string) *corev1.Pod {
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

	container := corev1.Container{
		Name:      "job",
		Image:     job.Spec.Image,
		Command:   job.Spec.Command,
		Args:      job.Spec.Args,
		Resources: resources,
	}

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
			RestartPolicy:         corev1.RestartPolicyNever,
			PriorityClassName:     priorityClassName(job.Spec.Priority),
			ActiveDeadlineSeconds: job.Spec.TimeoutSeconds,
		},
	}

	if checkpointPVC != "" {
		container.Env = append(container.Env, corev1.EnvVar{Name: "METALGRID_CHECKPOINT_DIR", Value: checkpointMountPath})
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: checkpointVolumeName, MountPath: checkpointMountPath})
		pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
			Name: checkpointVolumeName,
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: checkpointPVC},
			},
		})
	}
	pod.Spec.Containers = []corev1.Container{container}

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

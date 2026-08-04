package controller

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	metalgridv1alpha1 "github.com/jahnavi-yelamanchi/metalgrid/api/v1alpha1"
)

func TestAggregatePhase(t *testing.T) {
	podWithPhase := func(phase corev1.PodPhase) corev1.Pod {
		return corev1.Pod{Status: corev1.PodStatus{Phase: phase}}
	}

	cases := []struct {
		name string
		pods []corev1.Pod
		want metalgridv1alpha1.AcceleratorJobPhase
	}{
		{"single pending", []corev1.Pod{podWithPhase(corev1.PodPending)}, metalgridv1alpha1.AcceleratorJobPending},
		{"single running", []corev1.Pod{podWithPhase(corev1.PodRunning)}, metalgridv1alpha1.AcceleratorJobRunning},
		{"single succeeded", []corev1.Pod{podWithPhase(corev1.PodSucceeded)}, metalgridv1alpha1.AcceleratorJobSucceeded},
		{"single failed", []corev1.Pod{podWithPhase(corev1.PodFailed)}, metalgridv1alpha1.AcceleratorJobFailed},
		{
			"gang all succeeded",
			[]corev1.Pod{podWithPhase(corev1.PodSucceeded), podWithPhase(corev1.PodSucceeded)},
			metalgridv1alpha1.AcceleratorJobSucceeded,
		},
		{
			"gang one still pending counts as pending",
			[]corev1.Pod{podWithPhase(corev1.PodRunning), podWithPhase(corev1.PodPending)},
			metalgridv1alpha1.AcceleratorJobRunning,
		},
		{
			"gang one failed fails the whole job",
			[]corev1.Pod{podWithPhase(corev1.PodRunning), podWithPhase(corev1.PodFailed)},
			metalgridv1alpha1.AcceleratorJobFailed,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := aggregatePhase(tc.pods); got != tc.want {
				t.Errorf("aggregatePhase() = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestPriorityClassName(t *testing.T) {
	cases := map[int32]string{
		0:    "metalgrid-normal",
		50:   "metalgrid-normal",
		-50:  "metalgrid-normal",
		100:  "metalgrid-high",
		500:  "metalgrid-high",
		-100: "metalgrid-low",
		-500: "metalgrid-low",
	}
	for priority, want := range cases {
		if got := priorityClassName(priority); got != want {
			t.Errorf("priorityClassName(%d) = %s, want %s", priority, got, want)
		}
	}
}

func TestPodNameGang(t *testing.T) {
	single := &metalgridv1alpha1.AcceleratorJob{
		ObjectMeta: metav1.ObjectMeta{Name: "job-1"},
		Spec:       metalgridv1alpha1.AcceleratorJobSpec{GangSize: 1},
	}
	if got := podName(single, 0); got != "job-1" {
		t.Errorf("single pod name = %s, want job-1", got)
	}

	gang := &metalgridv1alpha1.AcceleratorJob{
		ObjectMeta: metav1.ObjectMeta{Name: "job-2"},
		Spec:       metalgridv1alpha1.AcceleratorJobSpec{GangSize: 3},
	}
	if got := podName(gang, 2); got != "job-2-2" {
		t.Errorf("gang pod name = %s, want job-2-2", got)
	}
}

func TestBuildPodRequestsAcceleratorResource(t *testing.T) {
	job := &metalgridv1alpha1.AcceleratorJob{
		ObjectMeta: metav1.ObjectMeta{Name: "job-1", Namespace: "default"},
		Spec: metalgridv1alpha1.AcceleratorJobSpec{
			Image:            "busybox:1.36",
			AcceleratorType:  "mock-gpu",
			AcceleratorCount: 3,
			Team:             "platform",
		},
	}

	pod := buildPod(job, nil, "job-1", 0, 1, "")

	if len(pod.Spec.Containers) != 1 {
		t.Fatalf("expected 1 container, got %d", len(pod.Spec.Containers))
	}
	qty := pod.Spec.Containers[0].Resources.Requests[AcceleratorResourceName]
	if got := qty.Value(); got != 3 {
		t.Errorf("expected accelerator request 3, got %d", got)
	}
	limQty := pod.Spec.Containers[0].Resources.Limits[AcceleratorResourceName]
	if got := limQty.Value(); got != 3 {
		t.Errorf("expected accelerator limit 3, got %d", got)
	}
	if pod.Spec.RestartPolicy != corev1.RestartPolicyNever {
		t.Errorf("expected RestartPolicyNever, got %s", pod.Spec.RestartPolicy)
	}
}

func TestBuildPodAppliesPoolPlacement(t *testing.T) {
	job := &metalgridv1alpha1.AcceleratorJob{
		ObjectMeta: metav1.ObjectMeta{Name: "job-1", Namespace: "default"},
		Spec: metalgridv1alpha1.AcceleratorJobSpec{
			Image:            "busybox:1.36",
			AcceleratorType:  "mock-gpu",
			AcceleratorCount: 1,
			Team:             "platform",
			Pool:             "pool-a",
		},
	}
	pool := &metalgridv1alpha1.AcceleratorPool{
		ObjectMeta: metav1.ObjectMeta{Name: "pool-a"},
		Spec: metalgridv1alpha1.AcceleratorPoolSpec{
			AcceleratorType:   "mock-gpu",
			NodeSelector:      map[string]string{"metalgrid.dev/zone": "a"},
			PlacementStrategy: metalgridv1alpha1.PlacementSpread,
			Tolerations:       []corev1.Toleration{{Key: "metalgrid.dev/dedicated", Operator: corev1.TolerationOpExists}},
		},
	}

	pod := buildPod(job, pool, "job-1", 0, 1, "")

	if pod.Spec.NodeSelector["metalgrid.dev/zone"] != "a" {
		t.Errorf("expected node selector from pool, got %v", pod.Spec.NodeSelector)
	}
	if len(pod.Spec.Tolerations) != 1 {
		t.Errorf("expected 1 toleration from pool, got %d", len(pod.Spec.Tolerations))
	}
	if pod.Annotations["metalgrid.dev/placement-strategy"] != "Spread" {
		t.Errorf("expected placement-strategy annotation Spread, got %s", pod.Annotations["metalgrid.dev/placement-strategy"])
	}
}

func TestBuildPodWithCheckpoint(t *testing.T) {
	job := &metalgridv1alpha1.AcceleratorJob{
		ObjectMeta: metav1.ObjectMeta{Name: "job-1", Namespace: "default"},
		Spec: metalgridv1alpha1.AcceleratorJobSpec{
			Image: "busybox:1.36", AcceleratorType: "mock-gpu", AcceleratorCount: 1, Team: "platform",
		},
	}

	pod := buildPod(job, nil, "job-1", 0, 1, "job-1-checkpoint")

	if len(pod.Spec.Volumes) != 1 || pod.Spec.Volumes[0].PersistentVolumeClaim == nil || pod.Spec.Volumes[0].PersistentVolumeClaim.ClaimName != "job-1-checkpoint" {
		t.Fatalf("expected a checkpoint PVC volume, got %+v", pod.Spec.Volumes)
	}
	mounts := pod.Spec.Containers[0].VolumeMounts
	if len(mounts) != 1 || mounts[0].MountPath != checkpointMountPath {
		t.Errorf("expected volume mount at %s, got %+v", checkpointMountPath, mounts)
	}
}

func TestBuildPodWithoutCheckpointHasNoVolume(t *testing.T) {
	job := &metalgridv1alpha1.AcceleratorJob{
		ObjectMeta: metav1.ObjectMeta{Name: "job-1", Namespace: "default"},
		Spec: metalgridv1alpha1.AcceleratorJobSpec{
			Image: "busybox:1.36", AcceleratorType: "mock-gpu", AcceleratorCount: 1, Team: "platform",
		},
	}
	pod := buildPod(job, nil, "job-1", 0, 1, "")
	if len(pod.Spec.Volumes) != 0 {
		t.Errorf("expected no volumes when checkpoint disabled, got %+v", pod.Spec.Volumes)
	}
}

func TestBuildPodTimeout(t *testing.T) {
	timeout := int64(300)
	job := &metalgridv1alpha1.AcceleratorJob{
		ObjectMeta: metav1.ObjectMeta{Name: "job-1", Namespace: "default"},
		Spec: metalgridv1alpha1.AcceleratorJobSpec{
			Image: "busybox:1.36", AcceleratorType: "mock-gpu", AcceleratorCount: 1, Team: "platform",
			TimeoutSeconds: &timeout,
		},
	}
	pod := buildPod(job, nil, "job-1", 0, 1, "")
	if pod.Spec.ActiveDeadlineSeconds == nil || *pod.Spec.ActiveDeadlineSeconds != 300 {
		t.Errorf("expected ActiveDeadlineSeconds=300, got %v", pod.Spec.ActiveDeadlineSeconds)
	}
}

func TestBackoffDurationGrowsAndCaps(t *testing.T) {
	prev := time.Duration(0)
	for i := int32(0); i < 6; i++ {
		d := backoffDuration(i)
		if d < prev {
			t.Errorf("backoff should not shrink: attempt %d = %s < previous %s", i, d, prev)
		}
		if d > 60*time.Second {
			t.Errorf("backoff should cap at 60s, attempt %d = %s", i, d)
		}
		prev = d
	}
	// Absurdly large retry counts must not overflow into something tiny/negative.
	if got := backoffDuration(1000); got != 60*time.Second {
		t.Errorf("expected overflow to fall back to the cap, got %s", got)
	}
}

func newTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := metalgridv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

func TestScheduleRetryOrFailRetriesUnderBudget(t *testing.T) {
	job := &metalgridv1alpha1.AcceleratorJob{
		ObjectMeta: metav1.ObjectMeta{Name: "job-1", Namespace: "default"},
		Spec:       metalgridv1alpha1.AcceleratorJobSpec{MaxRetries: 3},
		Status:     metalgridv1alpha1.AcceleratorJobStatus{RetryCount: 0},
	}
	c := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithObjects(job).WithStatusSubresource(job).Build()
	r := &AcceleratorJobReconciler{Client: c}

	if err := r.scheduleRetryOrFail(context.Background(), job, "boom"); err != nil {
		t.Fatalf("scheduleRetryOrFail: %v", err)
	}

	if job.Status.Phase != metalgridv1alpha1.AcceleratorJobPending {
		t.Errorf("expected Pending (still retrying), got %s", job.Status.Phase)
	}
	if job.Status.RetryCount != 1 {
		t.Errorf("expected RetryCount 1, got %d", job.Status.RetryCount)
	}
	if job.Status.NextRetryTime == nil {
		t.Fatal("expected NextRetryTime to be set")
	}
}

func TestScheduleRetryOrFailStopsAtMaxRetries(t *testing.T) {
	job := &metalgridv1alpha1.AcceleratorJob{
		ObjectMeta: metav1.ObjectMeta{Name: "job-1", Namespace: "default"},
		Spec:       metalgridv1alpha1.AcceleratorJobSpec{MaxRetries: 2},
		Status:     metalgridv1alpha1.AcceleratorJobStatus{RetryCount: 2},
	}
	c := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithObjects(job).WithStatusSubresource(job).Build()
	r := &AcceleratorJobReconciler{Client: c}

	if err := r.scheduleRetryOrFail(context.Background(), job, "boom again"); err != nil {
		t.Fatalf("scheduleRetryOrFail: %v", err)
	}

	if job.Status.Phase != metalgridv1alpha1.AcceleratorJobFailed {
		t.Errorf("expected Failed once retries exhausted, got %s", job.Status.Phase)
	}
	if job.Status.NextRetryTime != nil {
		t.Error("expected no NextRetryTime once permanently failed")
	}
	if job.Status.CompletionTime == nil {
		t.Error("expected CompletionTime to be set on final failure")
	}
}

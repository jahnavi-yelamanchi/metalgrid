package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AcceleratorJobPhase is the lifecycle phase of an AcceleratorJob.
type AcceleratorJobPhase string

const (
	AcceleratorJobPending   AcceleratorJobPhase = "Pending"
	AcceleratorJobRunning   AcceleratorJobPhase = "Running"
	AcceleratorJobSucceeded AcceleratorJobPhase = "Succeeded"
	AcceleratorJobFailed    AcceleratorJobPhase = "Failed"

	// AcceleratorFinalizer is set on every AcceleratorJob so the controller can
	// clean up out-of-band state (e.g. store/audit rows) before k8s GC removes it.
	AcceleratorFinalizer = "metalgrid.dev/finalizer"
)

// AcceleratorJobSpec describes a workload requesting accelerator capacity.
type AcceleratorJobSpec struct {
	// Image is the container image to run.
	// +kubebuilder:validation:Required
	Image string `json:"image"`

	// Command overrides the container entrypoint, if set.
	// +optional
	Command []string `json:"command,omitempty"`

	// Args are passed to the container command.
	// +optional
	Args []string `json:"args,omitempty"`

	// AcceleratorType selects which accelerator kind this job needs, e.g. "mock-gpu".
	// +kubebuilder:validation:Required
	AcceleratorType string `json:"acceleratorType"`

	// AcceleratorCount is how many accelerator units this job requests.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	AcceleratorCount int32 `json:"acceleratorCount"`

	// Priority is a scheduling priority hint; higher runs first. Reserved for Phase 2.
	// +kubebuilder:default=0
	Priority int32 `json:"priority,omitempty"`

	// Team attributes this job to a tenant for quota/audit purposes.
	// +kubebuilder:validation:Required
	Team string `json:"team"`

	// Pool names the AcceleratorPool this job should be scheduled into. If
	// unset, the job schedules on any node advertising the accelerator type.
	// +optional
	Pool string `json:"pool,omitempty"`

	// GangSize is how many identical pods make up this job. Pods in a gang
	// are scheduled all-or-nothing via the coscheduling plugin. Defaults to 1.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	GangSize int32 `json:"gangSize,omitempty"`

	// Resources sets non-accelerator container resource requests/limits.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// MaxRetries is how many times a failed job is retried with backoff
	// before it's marked permanently Failed. 0 means no retries.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=3
	MaxRetries int32 `json:"maxRetries,omitempty"`

	// Checkpoint, if true, mounts a PVC at /checkpoint that survives across
	// retries so the job image can resume instead of restarting from scratch.
	// +optional
	Checkpoint bool `json:"checkpoint,omitempty"`

	// TimeoutSeconds kills the job's pods if they run longer than this.
	// +optional
	TimeoutSeconds *int64 `json:"timeoutSeconds,omitempty"`
}

// AcceleratorJobStatus reflects the observed state of an AcceleratorJob.
type AcceleratorJobStatus struct {
	// Phase is the current lifecycle phase.
	// +optional
	Phase AcceleratorJobPhase `json:"phase,omitempty"`

	// PodName is the name of the pod running this job, once created.
	// +optional
	PodName string `json:"podName,omitempty"`

	// Message carries a human-readable explanation for the current phase.
	// +optional
	Message string `json:"message,omitempty"`

	// StartTime is when the job's pod started running.
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// CompletionTime is when the job reached Succeeded or Failed.
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	// RetryCount is how many times this job has been retried after failure.
	// +optional
	RetryCount int32 `json:"retryCount,omitempty"`

	// NextRetryTime, if set, is when the controller will delete the failed
	// generation's pods and create a fresh one. Backoff is enforced by
	// requeuing until this time passes, not by blocking the reconciler.
	// +optional
	NextRetryTime *metav1.Time `json:"nextRetryTime,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Team",type=string,JSONPath=`.spec.team`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// AcceleratorJob is a single accelerator workload submission.
type AcceleratorJob struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AcceleratorJobSpec   `json:"spec,omitempty"`
	Status AcceleratorJobStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AcceleratorJobList is a list of AcceleratorJob resources.
type AcceleratorJobList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AcceleratorJob `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AcceleratorJob{}, &AcceleratorJobList{})
}

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// InferenceServicePhase is the lifecycle phase of an InferenceService.
type InferenceServicePhase string

const (
	InferenceServicePending InferenceServicePhase = "Pending"
	InferenceServiceReady   InferenceServicePhase = "Ready"
)

// InferenceServiceSpec describes a model-serving deployment. The backend is
// a mock HTTP server shaped like an OpenAI-style completions endpoint
// (deploy/no real GPU weights needed) — swap Image for a real vLLM/Triton
// image to serve an actual model.
type InferenceServiceSpec struct {
	// Model is a label for which model this service serves; the mock
	// backend echoes it back in responses.
	// +kubebuilder:validation:Required
	Model string `json:"model"`

	// Image overrides the serving container image. Defaults to the mock
	// completions server if unset.
	// +optional
	Image string `json:"image,omitempty"`

	// Replicas is how many serving pods to run.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	Replicas int32 `json:"replicas,omitempty"`

	// Team attributes this service to a tenant.
	// +kubebuilder:validation:Required
	Team string `json:"team"`
}

// InferenceServiceStatus reflects the observed state of an InferenceService.
type InferenceServiceStatus struct {
	// +optional
	Phase InferenceServicePhase `json:"phase,omitempty"`

	// Endpoint is the in-cluster DNS name serving completions requests.
	// +optional
	Endpoint string `json:"endpoint,omitempty"`

	// ReadyReplicas mirrors the backing Deployment's ready replica count.
	// +optional
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Model",type=string,JSONPath=`.spec.model`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Endpoint",type=string,JSONPath=`.status.endpoint`

// InferenceService runs a model-serving Deployment+Service.
type InferenceService struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   InferenceServiceSpec   `json:"spec,omitempty"`
	Status InferenceServiceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// InferenceServiceList is a list of InferenceService resources.
type InferenceServiceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []InferenceService `json:"items"`
}

func init() {
	SchemeBuilder.Register(&InferenceService{}, &InferenceServiceList{})
}

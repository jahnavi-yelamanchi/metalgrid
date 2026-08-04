package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// QuotaPolicySpec caps how many accelerators a team may hold across its
// non-terminal AcceleratorJobs at once.
type QuotaPolicySpec struct {
	// Team this policy applies to.
	// +kubebuilder:validation:Required
	Team string `json:"team"`

	// MaxAccelerators is the total accelerator count the team may request
	// across all Pending/Running jobs simultaneously.
	// +kubebuilder:validation:Minimum=1
	MaxAccelerators int32 `json:"maxAccelerators"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Team",type=string,JSONPath=`.spec.team`
// +kubebuilder:printcolumn:name="Max",type=integer,JSONPath=`.spec.maxAccelerators`

// QuotaPolicy sets a per-team accelerator quota (teams can span namespaces),
// enforced at admission time.
type QuotaPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec QuotaPolicySpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

// QuotaPolicyList is a list of QuotaPolicy resources.
type QuotaPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []QuotaPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&QuotaPolicy{}, &QuotaPolicyList{})
}

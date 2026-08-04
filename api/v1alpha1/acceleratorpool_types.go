package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PlacementStrategy controls how the scheduler scores nodes within a pool.
type PlacementStrategy string

const (
	// PlacementBinPack packs jobs onto the fewest nodes (MostAllocated scoring).
	PlacementBinPack PlacementStrategy = "BinPack"
	// PlacementSpread spreads jobs across nodes (LeastAllocated scoring).
	PlacementSpread PlacementStrategy = "Spread"
)

// AcceleratorPoolSpec describes a logical slice of accelerator capacity: which
// nodes belong to it and how jobs should be placed within it.
type AcceleratorPoolSpec struct {
	// AcceleratorType this pool provides, matching AcceleratorJob.spec.acceleratorType.
	// +kubebuilder:validation:Required
	AcceleratorType string `json:"acceleratorType"`

	// NodeSelector restricts jobs in this pool to matching nodes.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// Tolerations allows jobs in this pool to run on tainted nodes.
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// PlacementStrategy is BinPack (default) or Spread.
	// +kubebuilder:validation:Enum=BinPack;Spread
	// +kubebuilder:default=BinPack
	PlacementStrategy PlacementStrategy `json:"placementStrategy,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.acceleratorType`
// +kubebuilder:printcolumn:name="Strategy",type=string,JSONPath=`.spec.placementStrategy`

// AcceleratorPool is a named slice of accelerator capacity, shared across
// namespaces like a StorageClass — not scoped to one team's namespace.
type AcceleratorPool struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec AcceleratorPoolSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

// AcceleratorPoolList is a list of AcceleratorPool resources.
type AcceleratorPoolList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AcceleratorPool `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AcceleratorPool{}, &AcceleratorPoolList{})
}

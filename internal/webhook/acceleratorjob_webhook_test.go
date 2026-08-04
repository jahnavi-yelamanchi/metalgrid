package webhook

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	metalgridv1alpha1 "github.com/jahnavi-yelamanchi/metalgrid/api/v1alpha1"
)

func TestCheckQuotaNoPolicyAllowsAnything(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = metalgridv1alpha1.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()

	job := &metalgridv1alpha1.AcceleratorJob{
		ObjectMeta: metav1.ObjectMeta{Name: "job-1"},
		Spec:       metalgridv1alpha1.AcceleratorJobSpec{Team: "unmanaged-team", AcceleratorCount: 1000},
	}
	if err := checkQuota(context.Background(), c, job); err != nil {
		t.Errorf("expected no error for team without a QuotaPolicy, got %v", err)
	}
}

func TestCheckQuotaRejectsOverQuota(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = metalgridv1alpha1.AddToScheme(scheme)

	policy := &metalgridv1alpha1.QuotaPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "platform-quota"},
		Spec:       metalgridv1alpha1.QuotaPolicySpec{Team: "platform", MaxAccelerators: 4},
	}
	existing := &metalgridv1alpha1.AcceleratorJob{
		ObjectMeta: metav1.ObjectMeta{Name: "existing-job", Namespace: "default"},
		Spec:       metalgridv1alpha1.AcceleratorJobSpec{Team: "platform", AcceleratorCount: 3},
		Status:     metalgridv1alpha1.AcceleratorJobStatus{Phase: metalgridv1alpha1.AcceleratorJobRunning},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(policy, existing).Build()

	newJob := &metalgridv1alpha1.AcceleratorJob{
		ObjectMeta: metav1.ObjectMeta{Name: "new-job", Namespace: "default"},
		Spec:       metalgridv1alpha1.AcceleratorJobSpec{Team: "platform", AcceleratorCount: 2},
	}

	err := checkQuota(context.Background(), c, newJob)
	if err == nil {
		t.Fatal("expected quota rejection (3 running + 2 requested > 4 max), got nil")
	}
}

func TestCheckQuotaIgnoresTerminalJobs(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = metalgridv1alpha1.AddToScheme(scheme)

	policy := &metalgridv1alpha1.QuotaPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "platform-quota"},
		Spec:       metalgridv1alpha1.QuotaPolicySpec{Team: "platform", MaxAccelerators: 4},
	}
	finished := &metalgridv1alpha1.AcceleratorJob{
		ObjectMeta: metav1.ObjectMeta{Name: "finished-job", Namespace: "default"},
		Spec:       metalgridv1alpha1.AcceleratorJobSpec{Team: "platform", AcceleratorCount: 3},
		Status:     metalgridv1alpha1.AcceleratorJobStatus{Phase: metalgridv1alpha1.AcceleratorJobSucceeded},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(policy, finished).Build()

	newJob := &metalgridv1alpha1.AcceleratorJob{
		ObjectMeta: metav1.ObjectMeta{Name: "new-job", Namespace: "default"},
		Spec:       metalgridv1alpha1.AcceleratorJobSpec{Team: "platform", AcceleratorCount: 4},
	}

	if err := checkQuota(context.Background(), c, newJob); err != nil {
		t.Errorf("expected succeeded jobs to be excluded from quota usage, got error: %v", err)
	}
}

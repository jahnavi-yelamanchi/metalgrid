// Package webhook implements admission validation for MetalGrid CRDs.
package webhook

import (
	"context"
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	metalgridv1alpha1 "github.com/jahnavi-yelamanchi/metalgrid/api/v1alpha1"
)

// AcceleratorJobValidator enforces per-team QuotaPolicy at admission time,
// so an over-quota submission is rejected before it ever becomes a pod.
type AcceleratorJobValidator struct {
	Client client.Client
}

// +kubebuilder:webhook:path=/validate-metalgrid-dev-v1alpha1-acceleratorjob,mutating=false,failurePolicy=fail,sideEffects=None,groups=metalgrid.dev,resources=acceleratorjobs,verbs=create,versions=v1alpha1,name=vacceleratorjob.metalgrid.dev,admissionReviewVersions=v1
// +kubebuilder:rbac:groups=metalgrid.dev,resources=quotapolicies,verbs=get;list;watch

func (v *AcceleratorJobValidator) ValidateCreate(ctx context.Context, job *metalgridv1alpha1.AcceleratorJob) (admission.Warnings, error) {
	return nil, checkQuota(ctx, v.Client, job)
}

func (v *AcceleratorJobValidator) ValidateUpdate(context.Context, *metalgridv1alpha1.AcceleratorJob, *metalgridv1alpha1.AcceleratorJob) (admission.Warnings, error) {
	return nil, nil // only CREATE is registered in the webhook config; status updates never reach here.
}

func (v *AcceleratorJobValidator) ValidateDelete(context.Context, *metalgridv1alpha1.AcceleratorJob) (admission.Warnings, error) {
	return nil, nil
}

// checkQuota rejects job if admitting it would push its team over any
// matching QuotaPolicy's MaxAccelerators, counted across that team's
// non-terminal AcceleratorJobs. A team with no QuotaPolicy is unlimited.
func checkQuota(ctx context.Context, c client.Client, job *metalgridv1alpha1.AcceleratorJob) error {
	var policies metalgridv1alpha1.QuotaPolicyList
	if err := c.List(ctx, &policies); err != nil {
		return fmt.Errorf("listing quota policies: %w", err)
	}

	var policy *metalgridv1alpha1.QuotaPolicy
	for i := range policies.Items {
		if policies.Items[i].Spec.Team == job.Spec.Team {
			policy = &policies.Items[i]
			break
		}
	}
	if policy == nil {
		return nil
	}

	var jobs metalgridv1alpha1.AcceleratorJobList
	if err := c.List(ctx, &jobs); err != nil {
		return fmt.Errorf("listing accelerator jobs: %w", err)
	}

	var used int32
	for _, j := range jobs.Items {
		if j.Spec.Team != job.Spec.Team {
			continue
		}
		if j.Status.Phase == metalgridv1alpha1.AcceleratorJobSucceeded || j.Status.Phase == metalgridv1alpha1.AcceleratorJobFailed {
			continue
		}
		used += j.Spec.AcceleratorCount
	}

	if used+job.Spec.AcceleratorCount > policy.Spec.MaxAccelerators {
		return fmt.Errorf("team %q quota exceeded: %d in use, %d requested, %d max",
			job.Spec.Team, used, job.Spec.AcceleratorCount, policy.Spec.MaxAccelerators)
	}
	return nil
}

func (v *AcceleratorJobValidator) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &metalgridv1alpha1.AcceleratorJob{}).
		WithValidator(v).
		Complete()
}

var _ admission.Validator[*metalgridv1alpha1.AcceleratorJob] = &AcceleratorJobValidator{}

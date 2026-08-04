package controller

import (
	"context"
	"encoding/json"
	"fmt"

	"go.opentelemetry.io/otel"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	metalgridv1alpha1 "github.com/jahnavi-yelamanchi/metalgrid/api/v1alpha1"
	"github.com/jahnavi-yelamanchi/metalgrid/internal/queue"
	"github.com/jahnavi-yelamanchi/metalgrid/internal/tracing"
)

var submissionTracer = otel.Tracer("metalgrid/controller")

// SubmissionHandler creates an AcceleratorJob CRD for each queued submission.
// It's idempotent: if the CRD already exists (e.g. a redelivered message),
// it's a no-op rather than an error.
func SubmissionHandler(c client.Client, namespace string) func(context.Context, []byte) error {
	return func(ctx context.Context, payload []byte) error {
		var sub queue.JobSubmission
		if err := json.Unmarshal(payload, &sub); err != nil {
			return fmt.Errorf("decoding job submission: %w", err)
		}

		ctx = tracing.ExtractTraceParent(ctx, sub.TraceParent)
		ctx, span := submissionTracer.Start(ctx, "job.submission.process")
		defer span.End()

		job := &metalgridv1alpha1.AcceleratorJob{
			ObjectMeta: metav1.ObjectMeta{
				Name:      sub.ID,
				Namespace: namespace,
			},
			Spec: metalgridv1alpha1.AcceleratorJobSpec{
				Image:            sub.Image,
				Command:          sub.Command,
				Args:             sub.Args,
				AcceleratorType:  sub.AcceleratorType,
				AcceleratorCount: sub.AcceleratorCount,
				Priority:         sub.Priority,
				Team:             sub.Team,
			},
		}

		if err := c.Create(ctx, job); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating AcceleratorJob %s: %w", sub.ID, err)
		}
		return nil
	}
}

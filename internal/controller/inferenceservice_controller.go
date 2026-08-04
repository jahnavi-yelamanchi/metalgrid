package controller

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	metalgridv1alpha1 "github.com/jahnavi-yelamanchi/metalgrid/api/v1alpha1"
)

const defaultInferenceImage = "metalgrid/mockinference:dev"
const inferencePort = 8000

// InferenceServiceReconciler reconciles an InferenceService into a
// Deployment+Service pair. No finalizer needed: unlike AcceleratorJob there's
// no out-of-band state to clean up, so owner-reference GC is sufficient.
type InferenceServiceReconciler struct {
	client.Client
}

// +kubebuilder:rbac:groups=metalgrid.dev,resources=inferenceservices,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=metalgrid.dev,resources=inferenceservices/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete

func (r *InferenceServiceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var svc metalgridv1alpha1.InferenceService
	if err := r.Get(ctx, req.NamespacedName, &svc); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !svc.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	if err := r.ensureDeployment(ctx, &svc); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensuring deployment: %w", err)
	}
	if err := r.ensureService(ctx, &svc); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensuring service: %w", err)
	}

	return ctrl.Result{}, r.syncStatus(ctx, &svc)
}

func inferenceLabels(svc *metalgridv1alpha1.InferenceService) map[string]string {
	return map[string]string{
		"metalgrid.dev/inference-service": svc.Name,
	}
}

func (r *InferenceServiceReconciler) ensureDeployment(ctx context.Context, svc *metalgridv1alpha1.InferenceService) error {
	replicas := svc.Spec.Replicas
	if replicas < 1 {
		replicas = 1
	}
	image := svc.Spec.Image
	if image == "" {
		image = defaultInferenceImage
	}
	labels := inferenceLabels(svc)

	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: svc.Name, Namespace: svc.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, dep, func() error {
		dep.Spec.Replicas = &replicas
		dep.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
		dep.Spec.Template = corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: labels},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "inference",
						Image: image,
						Env: []corev1.EnvVar{
							{Name: "MODEL", Value: svc.Spec.Model},
						},
						Ports: []corev1.ContainerPort{{ContainerPort: inferencePort}},
					},
				},
			},
		}
		return controllerutil.SetControllerReference(svc, dep, r.Scheme())
	})
	return err
}

func (r *InferenceServiceReconciler) ensureService(ctx context.Context, svc *metalgridv1alpha1.InferenceService) error {
	labels := inferenceLabels(svc)

	k8sSvc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: svc.Name, Namespace: svc.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, k8sSvc, func() error {
		k8sSvc.Spec.Selector = labels
		k8sSvc.Spec.Ports = []corev1.ServicePort{
			{Port: inferencePort, TargetPort: intstr.FromInt32(inferencePort)},
		}
		return controllerutil.SetControllerReference(svc, k8sSvc, r.Scheme())
	})
	return err
}

func (r *InferenceServiceReconciler) syncStatus(ctx context.Context, svc *metalgridv1alpha1.InferenceService) error {
	var dep appsv1.Deployment
	if err := r.Get(ctx, types.NamespacedName{Namespace: svc.Namespace, Name: svc.Name}, &dep); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	phase := metalgridv1alpha1.InferenceServicePending
	if dep.Status.ReadyReplicas > 0 {
		phase = metalgridv1alpha1.InferenceServiceReady
	}
	endpoint := fmt.Sprintf("%s.%s.svc.cluster.local:%d", svc.Name, svc.Namespace, inferencePort)

	if svc.Status.Phase == phase && svc.Status.Endpoint == endpoint && svc.Status.ReadyReplicas == dep.Status.ReadyReplicas {
		return nil
	}
	svc.Status.Phase = phase
	svc.Status.Endpoint = endpoint
	svc.Status.ReadyReplicas = dep.Status.ReadyReplicas
	return r.Status().Update(ctx, svc)
}

func (r *InferenceServiceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&metalgridv1alpha1.InferenceService{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Complete(r)
}

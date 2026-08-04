package controller

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	metalgridv1alpha1 "github.com/jahnavi-yelamanchi/metalgrid/api/v1alpha1"
)

func inferenceTestScheme(t *testing.T) *runtime.Scheme {
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

func TestInferenceServiceReconcileCreatesDeploymentAndService(t *testing.T) {
	svc := &metalgridv1alpha1.InferenceService{
		ObjectMeta: metav1.ObjectMeta{Name: "svc-1", Namespace: "default"},
		Spec:       metalgridv1alpha1.InferenceServiceSpec{Model: "mock-model", Team: "platform", Replicas: 2},
	}
	c := fake.NewClientBuilder().WithScheme(inferenceTestScheme(t)).WithObjects(svc).WithStatusSubresource(svc).Build()
	r := &InferenceServiceReconciler{Client: c}
	ctx := context.Background()

	if err := r.ensureDeployment(ctx, svc); err != nil {
		t.Fatalf("ensureDeployment: %v", err)
	}
	if err := r.ensureService(ctx, svc); err != nil {
		t.Fatalf("ensureService: %v", err)
	}

	var dep appsv1.Deployment
	if err := c.Get(ctx, client.ObjectKeyFromObject(svc), &dep); err != nil {
		t.Fatalf("expected Deployment to exist: %v", err)
	}
	if dep.Spec.Replicas == nil || *dep.Spec.Replicas != 2 {
		t.Errorf("expected 2 replicas, got %v", dep.Spec.Replicas)
	}
	if len(dep.Spec.Template.Spec.Containers) != 1 || dep.Spec.Template.Spec.Containers[0].Image != defaultInferenceImage {
		t.Errorf("expected default mock image, got %+v", dep.Spec.Template.Spec.Containers)
	}

	var k8sSvc corev1.Service
	if err := c.Get(ctx, client.ObjectKeyFromObject(svc), &k8sSvc); err != nil {
		t.Fatalf("expected Service to exist: %v", err)
	}
	if len(k8sSvc.Spec.Ports) != 1 || k8sSvc.Spec.Ports[0].Port != inferencePort {
		t.Errorf("expected port %d, got %+v", inferencePort, k8sSvc.Spec.Ports)
	}
}

func TestInferenceServiceUsesCustomImage(t *testing.T) {
	svc := &metalgridv1alpha1.InferenceService{
		ObjectMeta: metav1.ObjectMeta{Name: "svc-2", Namespace: "default"},
		Spec:       metalgridv1alpha1.InferenceServiceSpec{Model: "real-model", Team: "platform", Image: "vllm/vllm-openai:latest"},
	}
	c := fake.NewClientBuilder().WithScheme(inferenceTestScheme(t)).WithObjects(svc).Build()
	r := &InferenceServiceReconciler{Client: c}
	ctx := context.Background()

	if err := r.ensureDeployment(ctx, svc); err != nil {
		t.Fatalf("ensureDeployment: %v", err)
	}

	var dep appsv1.Deployment
	if err := c.Get(ctx, client.ObjectKeyFromObject(svc), &dep); err != nil {
		t.Fatalf("expected Deployment to exist: %v", err)
	}
	if dep.Spec.Template.Spec.Containers[0].Image != "vllm/vllm-openai:latest" {
		t.Errorf("expected custom image to override default, got %s", dep.Spec.Template.Spec.Containers[0].Image)
	}
}

func TestInferenceServiceSyncStatusReady(t *testing.T) {
	svc := &metalgridv1alpha1.InferenceService{
		ObjectMeta: metav1.ObjectMeta{Name: "svc-3", Namespace: "default"},
		Spec:       metalgridv1alpha1.InferenceServiceSpec{Model: "mock-model", Team: "platform"},
	}
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "svc-3", Namespace: "default"},
		Status:     appsv1.DeploymentStatus{ReadyReplicas: 1},
	}
	c := fake.NewClientBuilder().WithScheme(inferenceTestScheme(t)).WithObjects(svc, dep).WithStatusSubresource(svc).Build()
	r := &InferenceServiceReconciler{Client: c}

	if err := r.syncStatus(context.Background(), svc); err != nil {
		t.Fatalf("syncStatus: %v", err)
	}
	if svc.Status.Phase != metalgridv1alpha1.InferenceServiceReady {
		t.Errorf("expected Ready phase, got %s", svc.Status.Phase)
	}
	if svc.Status.Endpoint != "svc-3.default.svc.cluster.local:8000" {
		t.Errorf("unexpected endpoint: %s", svc.Status.Endpoint)
	}
}

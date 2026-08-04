package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	extenderv1 "k8s.io/kube-scheduler/extender/v1"
)

// AcceleratorResourceName is the extended resource the mock device plugin
// advertises; duplicated from internal/controller to avoid this standalone
// binary depending on the operator's controller package.
const AcceleratorResourceName corev1.ResourceName = "metalgrid.dev/accelerator"

const (
	jobLabel           = "metalgrid.dev/job"
	gangSizeAnnotation = "metalgrid.dev/gang-size"
	strategyAnnotation = "metalgrid.dev/placement-strategy"
)

// Extender implements the kube-scheduler HTTP extender webhook contract:
// Prioritize scores candidate nodes bin-pack vs spread, Filter gates gang
// jobs so they don't strand some members while siblings can never fit.
type Extender struct {
	Clientset kubernetes.Interface
}

func (e *Extender) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /filter", e.handleFilter)
	mux.HandleFunc("POST /prioritize", e.handlePrioritize)
	return mux
}

func (e *Extender) handlePrioritize(w http.ResponseWriter, r *http.Request) {
	var args extenderv1.ExtenderArgs
	if err := json.NewDecoder(r.Body).Decode(&args); err != nil || args.Nodes == nil {
		http.Error(w, "invalid extender args", http.StatusBadRequest)
		return
	}

	strategy := args.Pod.Annotations[strategyAnnotation]
	if strategy == "" {
		strategy = StrategyBinPack
	}

	result := make(extenderv1.HostPriorityList, 0, len(args.Nodes.Items))
	for _, node := range args.Nodes.Items {
		capacity := node.Status.Allocatable[AcceleratorResourceName]
		allocated, err := e.allocatedAccelerators(r.Context(), node.Name)
		if err != nil {
			http.Error(w, fmt.Sprintf("computing allocation for %s: %v", node.Name, err), http.StatusInternalServerError)
			return
		}
		result = append(result, extenderv1.HostPriority{
			Host:  node.Name,
			Score: scoreNode(strategy, capacity.Value(), allocated),
		})
	}

	writeJSON(w, result)
}

func (e *Extender) handleFilter(w http.ResponseWriter, r *http.Request) {
	var args extenderv1.ExtenderArgs
	if err := json.NewDecoder(r.Body).Decode(&args); err != nil || args.Nodes == nil {
		http.Error(w, "invalid extender args", http.StatusBadRequest)
		return
	}

	gangSize, _ := strconv.Atoi(args.Pod.Annotations[gangSizeAnnotation])
	if gangSize <= 1 {
		writeJSON(w, extenderv1.ExtenderFilterResult{Nodes: args.Nodes})
		return
	}

	ctx := r.Context()
	jobName := args.Pod.Labels[jobLabel]
	alreadyPlaced, err := e.siblingsPlaced(ctx, jobName, args.Pod.Name)
	if err != nil {
		http.Error(w, fmt.Sprintf("counting gang siblings: %v", err), http.StatusInternalServerError)
		return
	}

	requestPerPod := podAcceleratorRequest(args.Pod)

	var totalFree int64
	for _, node := range args.Nodes.Items {
		capacity := node.Status.Allocatable[AcceleratorResourceName]
		allocated, err := e.allocatedAccelerators(ctx, node.Name)
		if err != nil {
			http.Error(w, fmt.Sprintf("computing allocation for %s: %v", node.Name, err), http.StatusInternalServerError)
			return
		}
		if free := capacity.Value() - allocated; free > 0 {
			totalFree += free
		}
	}

	if gangFeasible(gangSize, alreadyPlaced, requestPerPod, totalFree) {
		writeJSON(w, extenderv1.ExtenderFilterResult{Nodes: args.Nodes})
		return
	}

	failed := extenderv1.FailedNodesMap{}
	for _, node := range args.Nodes.Items {
		failed[node.Name] = "waiting for enough combined capacity to seat the whole gang"
	}
	writeJSON(w, extenderv1.ExtenderFilterResult{FailedNodes: failed})
}

// allocatedAccelerators sums accelerator requests for non-terminal pods
// currently bound to node. Node.Status.Allocatable is static; this is the
// only way to learn how much of it is actually in use right now.
func (e *Extender) allocatedAccelerators(ctx context.Context, node string) (int64, error) {
	pods, err := e.Clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{
		FieldSelector: "spec.nodeName=" + node,
	})
	if err != nil {
		return 0, err
	}
	var total int64
	for _, p := range pods.Items {
		if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		total += podAcceleratorRequest(&p)
	}
	return total, nil
}

// siblingsPlaced counts other pods in the same gang (same job label) that
// have already been assigned a node, excluding the pod being scheduled now.
func (e *Extender) siblingsPlaced(ctx context.Context, jobName, excludePod string) (int, error) {
	if jobName == "" {
		return 0, nil
	}
	pods, err := e.Clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{
		LabelSelector: jobLabel + "=" + jobName,
	})
	if err != nil {
		return 0, err
	}
	count := 0
	for _, p := range pods.Items {
		if p.Name == excludePod {
			continue
		}
		if p.Spec.NodeName != "" {
			count++
		}
	}
	return count, nil
}

func podAcceleratorRequest(pod *corev1.Pod) int64 {
	var total int64
	for _, c := range pod.Spec.Containers {
		if q, ok := c.Resources.Requests[AcceleratorResourceName]; ok {
			total += q.Value()
		}
	}
	return total
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

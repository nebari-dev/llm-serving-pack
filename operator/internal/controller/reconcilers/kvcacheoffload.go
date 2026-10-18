package reconcilers

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
)

// ValidateKVCacheOffload is shared by admission and reconciliation. Reconciliation
// also validates so objects admitted before the webhook was enabled cannot
// produce pods with an unaccounted-for cache allocation.
func ValidateKVCacheOffload(model *llmv1alpha1.LLMModel) error {
	offload := model.Spec.Serving.KVCacheOffload
	if offload == nil {
		return nil
	}
	if offload.CPUMemoryGiB < 1 {
		return fmt.Errorf("spec.serving.kvCacheOffload.cpuMemoryGiB must be positive")
	}
	if model.Spec.Resources.GPU.Count < 1 || model.Spec.Resources.GPU.Type != "nvidia" {
		return fmt.Errorf("spec.serving.kvCacheOffload requires an NVIDIA GPU")
	}
	if model.Spec.Serving.DataParallelism != nil && *model.Spec.Serving.DataParallelism < 1 {
		return fmt.Errorf("spec.serving.dataParallelism must be positive with kvCacheOffload")
	}
	for _, args := range [][]string{model.Spec.Serving.VLLMArgs, model.Spec.Advanced.VLLM.ExtraArgs} {
		for _, arg := range args {
			flag, _, _ := strings.Cut(arg, "=")
			// vLLM also accepts underscores in flag names and dotted JSON flags.
			flag = strings.ReplaceAll(flag, "_", "-")
			flag, _, _ = strings.Cut(flag, ".")
			if flag == "--kv-offloading-size" || flag == "--kv-offloading-backend" ||
				flag == "--kv-transfer-config" || flag == "--config" ||
				strings.HasPrefix(flag, "-dp") ||
				strings.HasPrefix(flag, "--data-parallel") {
				return fmt.Errorf("%s conflicts with spec.serving.kvCacheOffload; "+
					"use typed serving fields or omit kvCacheOffload to manage raw arguments", arg)
			}
		}
	}

	request := model.Spec.Resources.Requests[corev1.ResourceMemory]
	limit := model.Spec.Resources.Limits[corev1.ResourceMemory]
	if request.Sign() <= 0 || limit.Sign() <= 0 {
		return fmt.Errorf("spec.serving.kvCacheOffload requires positive baseline " +
			"spec.resources.requests.memory and spec.resources.limits.memory")
	}
	if limit.Cmp(request) < 0 {
		return fmt.Errorf("spec.resources.limits.memory must be >= requests.memory with kvCacheOffload")
	}
	additional := kvCacheOffloadMemory(model)
	// Resource quantities can represent numbers larger than the int64 byte
	// budgets supported by Kubernetes containers. Check before rendering.
	maxMemory := resource.NewQuantity(1<<63-1, resource.BinarySI)
	for _, base := range []resource.Quantity{request, limit} {
		total := base.DeepCopy()
		total.Add(additional)
		if total.Cmp(*maxMemory) > 0 {
			return fmt.Errorf("baseline memory plus kvCacheOffload allocation exceeds int64 bytes")
		}
	}
	return nil
}

func kvCacheOffloadMemory(model *llmv1alpha1.LLMModel) resource.Quantity {
	memory := resource.NewQuantity(int64(model.Spec.Serving.KVCacheOffload.CPUMemoryGiB)*(1<<30), resource.BinarySI)
	if model.Spec.Serving.DataParallelism != nil {
		// Quantity retains arbitrary precision if the multiplication exceeds
		// int64; ValidateKVCacheOffload rejects the resulting container budget.
		memory.Mul(int64(*model.Spec.Serving.DataParallelism))
	}
	return *memory
}

// withKVCacheOffloadMemory never modifies the input ResourceList or quantities.
// Repeated reconciliations must add the allocation to the CR baseline exactly once.
func withKVCacheOffloadMemory(base corev1.ResourceList, model *llmv1alpha1.LLMModel) corev1.ResourceList {
	if model.Spec.Serving.KVCacheOffload == nil {
		return base
	}
	result := base.DeepCopy()
	memory := result[corev1.ResourceMemory]
	memory.Add(kvCacheOffloadMemory(model))
	result[corev1.ResourceMemory] = memory
	return result
}

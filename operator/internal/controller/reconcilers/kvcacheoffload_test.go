package reconcilers

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
)

func offloadModel() *llmv1alpha1.LLMModel {
	model := defaultModel()
	model.Spec.Serving.KVCacheOffload = &llmv1alpha1.KVCacheOffloadSpec{CPUMemoryGiB: 4}
	model.Spec.Resources.Requests = corev1.ResourceList{
		corev1.ResourceMemory: resource.MustParse("8Gi"),
		corev1.ResourceCPU:    resource.MustParse("2"),
	}
	model.Spec.Resources.Limits = corev1.ResourceList{
		corev1.ResourceMemory: resource.MustParse("12Gi"),
		corev1.ResourceCPU:    resource.MustParse("4"),
	}
	return model
}

func TestKVCacheOffloadResources(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		tp, dp, pods int32
		request      string
		limit        string
	}{
		{"one engine", 1, 1, 1, "12Gi", "16Gi"},
		{"TP shares the buffer", 4, 1, 1, "12Gi", "16Gi"},
		{"DP needs one buffer per engine", 1, 2, 1, "16Gi", "20Gi"},
		{"replicas do not multiply per-pod memory", 1, 1, 3, "12Gi", "16Gi"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := offloadModel()
			model.Spec.Serving.TensorParallelism = &tc.tp
			model.Spec.Serving.DataParallelism = &tc.dp
			model.Spec.Serving.Replicas = &tc.pods
			model.Spec.Resources.GPU.Count = tc.tp * tc.dp
			before := model.DeepCopy()
			for range 2 {
				result, err := BuildModelServiceResources(model, defaultStorage(), defaultConfig())
				if err != nil {
					t.Fatal(err)
				}
				container := result.Deployment.Spec.Template.Spec.Containers[0]
				for _, arg := range []struct{ flag, value string }{
					{"--kv-offloading-size", "4"}, {"--kv-offloading-backend", "native"},
				} {
					i := slices.Index(container.Args, arg.flag)
					if i < 0 || i+1 >= len(container.Args) || container.Args[i+1] != arg.value {
						t.Fatalf("missing %s %s in %v", arg.flag, arg.value, container.Args)
					}
				}
				request := container.Resources.Requests[corev1.ResourceMemory]
				limit := container.Resources.Limits[corev1.ResourceMemory]
				if request.Cmp(resource.MustParse(tc.request)) != 0 || limit.Cmp(resource.MustParse(tc.limit)) != 0 {
					t.Fatalf("memory = %s/%s, want %s/%s", request.String(), limit.String(), tc.request, tc.limit)
				}
				cpu := container.Resources.Limits[corev1.ResourceCPU]
				gpu := container.Resources.Limits[nvidiaGPUKey]
				if cpu.Cmp(resource.MustParse("4")) != 0 || gpu.Value() != int64(tc.tp*tc.dp) {
					t.Fatalf("unrelated resource limits changed: %v", container.Resources.Limits)
				}
				if !reflect.DeepEqual(model, before) {
					t.Fatal("rendering changed the input model")
				}
			}
		})
	}
}

func TestKVCacheOffloadDisableRestoresBaseline(t *testing.T) {
	model := offloadModel()
	if _, err := BuildModelServiceResources(model, defaultStorage(), defaultConfig()); err != nil {
		t.Fatal(err)
	}
	model.Spec.Serving.KVCacheOffload = nil
	result, err := BuildModelServiceResources(model, defaultStorage(), defaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	container := result.Deployment.Spec.Template.Spec.Containers[0]
	if slices.Contains(container.Args, "--kv-offloading-size") || slices.Contains(container.Args, "--kv-offloading-backend") {
		t.Fatalf("offload flags retained: %v", container.Args)
	}
	if !reflect.DeepEqual(container.Resources.Requests, model.Spec.Resources.Requests) {
		t.Fatal("baseline requests were not restored")
	}
	if !reflect.DeepEqual(container.Resources.Limits, buildResourceLimits(model)) {
		t.Fatal("baseline limits were not restored")
	}
}

func TestKVCacheOffloadValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*llmv1alpha1.LLMModel)
		want string
	}{
		{"zero budget", func(m *llmv1alpha1.LLMModel) { m.Spec.Serving.KVCacheOffload.CPUMemoryGiB = 0 }, "must be positive"},
		{"negative budget", func(m *llmv1alpha1.LLMModel) { m.Spec.Serving.KVCacheOffload.CPUMemoryGiB = -1 }, "must be positive"},
		{"no GPU", func(m *llmv1alpha1.LLMModel) { m.Spec.Resources.GPU.Count = 0 }, "NVIDIA GPU"},
		{"unsupported GPU", func(m *llmv1alpha1.LLMModel) { m.Spec.Resources.GPU.Type = "amd" }, "NVIDIA GPU"},
		{"no request", func(m *llmv1alpha1.LLMModel) { m.Spec.Resources.Requests = nil }, "positive baseline"},
		{"no limit", func(m *llmv1alpha1.LLMModel) { m.Spec.Resources.Limits = nil }, "positive baseline"},
		{"zero request", func(m *llmv1alpha1.LLMModel) {
			m.Spec.Resources.Requests[corev1.ResourceMemory] = resource.MustParse("0")
		}, "positive baseline"},
		{"limit below request", func(m *llmv1alpha1.LLMModel) {
			m.Spec.Resources.Limits[corev1.ResourceMemory] = resource.MustParse("4Gi")
		}, "must be >= requests"},
		{"zero DP", func(m *llmv1alpha1.LLMModel) { m.Spec.Serving.DataParallelism = int32Ptr(0) }, "dataParallelism must be positive"},
		{"allocation overflow", func(m *llmv1alpha1.LLMModel) {
			m.Spec.Serving.KVCacheOffload.CPUMemoryGiB = 1 << 30
			m.Spec.Serving.DataParallelism = int32Ptr(1 << 30)
		}, "exceeds int64"},
		{"total overflow", func(m *llmv1alpha1.LLMModel) {
			m.Spec.Resources.Limits[corev1.ResourceMemory] = resource.MustParse("9223372036854775807")
		}, "exceeds int64"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := offloadModel()
			tc.edit(model)
			// Exercise the renderer as well as shared validation: existing
			// objects may reach reconciliation without passing a webhook.
			_, err := BuildModelServiceResources(model, defaultStorage(), defaultConfig())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestKVCacheOffloadArgumentConflicts(t *testing.T) {
	for _, flag := range []string{
		"--kv-offloading-size", "--kv-offloading-size=8",
		"--kv_offloading_backend=lmcache", "--kv-transfer-config",
		"--kv-transfer-config.kv_connector=LMCacheConnectorV1",
		"--config", "--config=custom.yaml",
		"--data-parallel-size", "--data_parallel_size=2",
		"--data-parallel-size-local=2", "--data-parallel-external-lb",
		"-dp", "-dp=2", "-dpl=2", "-dpe",
	} {
		for _, advanced := range []bool{false, true} {
			t.Run(flag+map[bool]string{false: "/serving", true: "/advanced"}[advanced], func(t *testing.T) {
				model := offloadModel()
				if advanced {
					model.Spec.Advanced.VLLM.ExtraArgs = []string{flag}
				} else {
					model.Spec.Serving.VLLMArgs = []string{flag}
				}
				if err := ValidateKVCacheOffload(model); err == nil || !strings.Contains(err.Error(), "conflicts") {
					t.Fatalf("expected conflict for %s, got %v", flag, err)
				}
				// Existing escape-hatch users must remain unaffected.
				model.Spec.Serving.KVCacheOffload = nil
				if err := ValidateKVCacheOffload(model); err != nil {
					t.Fatalf("raw arguments rejected without typed offload: %v", err)
				}
			})
		}
	}
}

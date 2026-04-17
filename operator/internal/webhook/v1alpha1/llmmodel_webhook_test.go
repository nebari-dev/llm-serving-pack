/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	"context"
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
)

// newManagedNamespace creates a namespace with the nebari.dev/managed=true label.
func newManagedNamespace(name string) *corev1.Namespace {
	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				"nebari.dev/managed": "true",
			},
		},
	}
}

// newUnmanagedNamespace creates a namespace without the nebari.dev/managed label.
func newUnmanagedNamespace(name string) *corev1.Namespace {
	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}
}

// newBaseLLMModel returns a minimal valid LLMModel in the given namespace.
func newBaseLLMModel(name, namespace string) *llmv1alpha1.LLMModel {
	return &llmv1alpha1.LLMModel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: llmv1alpha1.LLMModelSpec{
			Model: llmv1alpha1.ModelSpec{
				Name:   "mistralai/Mistral-7B-v0.1",
				Source: llmv1alpha1.ModelSourceHuggingFace,
			},
			Resources: llmv1alpha1.ResourceSpec{
				GPU: llmv1alpha1.GPUSpec{
					Count: 1,
					Type:  "nvidia",
				},
			},
			Access: llmv1alpha1.AccessSpec{
				Groups: []string{"data-scientists"},
			},
		},
	}
}

var _ = Describe("LLMModel Webhook", func() {
	var (
		bgCtx = context.Background()
	)

	Context("ValidateCreate", func() {
		It("should accept a valid LLMModel in a managed namespace", func() {
			ns := newManagedNamespace("test-managed-create-valid")
			Expect(k8sClient.Create(bgCtx, ns)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(bgCtx, ns) })

			model := newBaseLLMModel("my-model", ns.Name)
			Expect(k8sClient.Create(bgCtx, model)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(bgCtx, model) })
		})

		It("should reject a LLMModel with no public flag and no groups", func() {
			ns := newManagedNamespace("test-managed-empty-access")
			Expect(k8sClient.Create(bgCtx, ns)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(bgCtx, ns) })

			model := newBaseLLMModel("my-model-empty-access", ns.Name)
			model.Spec.Access = llmv1alpha1.AccessSpec{} // neither Public nor Groups
			err := k8sClient.Create(bgCtx, model)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.access"))
		})

		It("should accept a LLMModel with Access.Public=true and no groups", func() {
			ns := newManagedNamespace("test-managed-public")
			Expect(k8sClient.Create(bgCtx, ns)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(bgCtx, ns) })

			model := newBaseLLMModel("my-model-public", ns.Name)
			public := true
			model.Spec.Access = llmv1alpha1.AccessSpec{Public: &public}
			Expect(k8sClient.Create(bgCtx, model)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(bgCtx, model) })
		})

		It("should reject a LLMModel in a namespace without the managed label", func() {
			ns := newUnmanagedNamespace("test-unmanaged-create")
			Expect(k8sClient.Create(bgCtx, ns)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(bgCtx, ns) })

			model := newBaseLLMModel("my-model-unmanaged", ns.Name)
			err := k8sClient.Create(bgCtx, model)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("nebari.dev/managed"))
		})

		It("should accept a LLMModel with an explicit valid subdomain", func() {
			ns := newManagedNamespace("test-managed-explicit-subdomain")
			Expect(k8sClient.Create(bgCtx, ns)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(bgCtx, ns) })

			model := newBaseLLMModel("my-model-explicit", ns.Name)
			model.Spec.Endpoints.External.Subdomain = "my-custom-subdomain"
			Expect(k8sClient.Create(bgCtx, model)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(bgCtx, model) })
		})

		It("should reject a LLMModel whose effective subdomain exceeds 63 characters", func() {
			ns := newManagedNamespace("test-managed-long-subdomain")
			Expect(k8sClient.Create(bgCtx, ns)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(bgCtx, ns) })

			// An explicit subdomain that is 64 chars long - too long.
			longSubdomain := strings.Repeat("a", 64)
			model := newBaseLLMModel("my-model-long-sub", ns.Name)
			model.Spec.Endpoints.External.Subdomain = longSubdomain
			err := k8sClient.Create(bgCtx, model)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("63"))
		})

		It("should reject a second LLMModel whose subdomain collides with an existing one", func() {
			ns := newManagedNamespace("test-managed-collision")
			Expect(k8sClient.Create(bgCtx, ns)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(bgCtx, ns) })

			// First model: explicit subdomain "shared-subdomain"
			first := newBaseLLMModel("first-model-collision", ns.Name)
			first.Spec.Endpoints.External.Subdomain = "shared-subdomain"
			Expect(k8sClient.Create(bgCtx, first)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(bgCtx, first) })

			// Second model in a different namespace but same subdomain.
			ns2 := newManagedNamespace("test-managed-collision2")
			Expect(k8sClient.Create(bgCtx, ns2)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(bgCtx, ns2) })

			second := newBaseLLMModel("second-model-collision", ns2.Name)
			second.Spec.Endpoints.External.Subdomain = "shared-subdomain"
			err := k8sClient.Create(bgCtx, second)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("subdomain"))
		})

		It("should accept but warn when storage type is emptyDir and preload is true", func() {
			ns := newManagedNamespace("test-managed-emptydir-warn")
			Expect(k8sClient.Create(bgCtx, ns)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(bgCtx, ns) })

			preload := true
			model := newBaseLLMModel("my-model-emptydir", ns.Name)
			model.Spec.Model.Storage.Type = llmv1alpha1.StorageTypeEmptyDir
			model.Spec.Model.Preload = &preload

			// The create should succeed (warnings don't block).
			Expect(k8sClient.Create(bgCtx, model)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(bgCtx, model) })
		})
	})

	Context("ValidateUpdate", func() {
		It("should reject an update that causes a subdomain collision", func() {
			ns := newManagedNamespace("test-managed-update-collision")
			Expect(k8sClient.Create(bgCtx, ns)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(bgCtx, ns) })

			// Create the first model to claim the subdomain.
			first := newBaseLLMModel("update-first", ns.Name)
			first.Spec.Endpoints.External.Subdomain = "taken-subdomain"
			Expect(k8sClient.Create(bgCtx, first)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(bgCtx, first) })

			// Create the second model with a different subdomain.
			second := newBaseLLMModel("update-second", ns.Name)
			second.Spec.Endpoints.External.Subdomain = "free-subdomain"
			Expect(k8sClient.Create(bgCtx, second)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(bgCtx, second) })

			// Now update second to collide with first.
			second.Spec.Endpoints.External.Subdomain = "taken-subdomain"
			err := k8sClient.Update(bgCtx, second)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("subdomain"))
		})

		It("should allow an update that keeps the same subdomain (self-update)", func() {
			ns := newManagedNamespace("test-managed-update-self")
			Expect(k8sClient.Create(bgCtx, ns)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(bgCtx, ns) })

			model := newBaseLLMModel("update-self-model", ns.Name)
			model.Spec.Endpoints.External.Subdomain = "my-own-subdomain"
			Expect(k8sClient.Create(bgCtx, model)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(bgCtx, model) })

			// Update an unrelated field; subdomain stays the same.
			model.Spec.Access.Groups = []string{"updated-group"}
			Expect(k8sClient.Update(bgCtx, model)).To(Succeed())
		})

		It("should reject an update when namespace loses managed label", func() {
			ns := newManagedNamespace("test-managed-update-ns-label")
			Expect(k8sClient.Create(bgCtx, ns)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(bgCtx, ns) })

			model := newBaseLLMModel("update-ns-label-model", ns.Name)
			Expect(k8sClient.Create(bgCtx, model)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(bgCtx, model) })

			// Remove the managed label from the namespace.
			ns.Labels = nil
			Expect(k8sClient.Update(bgCtx, ns)).To(Succeed())

			// Now try to update the model - should be rejected.
			model.Spec.Access.Groups = []string{"some-other-group"}
			err := k8sClient.Update(bgCtx, model)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("nebari.dev/managed"))
		})
	})

	Context("ValidateDelete", func() {
		It("should always accept deletion", func() {
			ns := newManagedNamespace("test-managed-delete")
			Expect(k8sClient.Create(bgCtx, ns)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(bgCtx, ns) })

			model := newBaseLLMModel("delete-model", ns.Name)
			Expect(k8sClient.Create(bgCtx, model)).To(Succeed())

			Expect(k8sClient.Delete(bgCtx, model)).To(Succeed())
		})
	})

	Context("Unit-level validator tests (no envtest)", func() {
		// These test the validator struct directly to verify warning logic
		// without going through the full webhook server round-trip.
		var validator *LLMModelCustomValidator

		BeforeEach(func() {
			validator = &LLMModelCustomValidator{Client: k8sClient}
		})

		It("should return a warning when storage is emptyDir and preload is true", func() {
			preload := true
			model := newBaseLLMModel("warn-unit", "default")
			model.Spec.Model.Storage.Type = llmv1alpha1.StorageTypeEmptyDir
			model.Spec.Model.Preload = &preload

			ns := newManagedNamespace(fmt.Sprintf("warn-unit-ns-%s", "default"))
			// We can't create this without envtest here; just call the
			// validator method directly with an existing managed namespace.
			// Use the pre-existing "default" namespace in envtest (it won't
			// have the label, so we'll test the emptyDir warning path via a
			// managed namespace created specifically for this).
			managedNs := newManagedNamespace("unit-warn-ns")
			Expect(k8sClient.Create(bgCtx, managedNs)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(bgCtx, managedNs) })
			_ = ns

			model.Namespace = managedNs.Name
			warnings, err := validator.ValidateCreate(bgCtx, model)
			Expect(err).NotTo(HaveOccurred())
			Expect(warnings).NotTo(BeEmpty())
			Expect(warnings[0]).To(ContainSubstring("re-download"))
		})

		It("should not warn when storage is emptyDir but preload is false", func() {
			preload := false
			model := newBaseLLMModel("no-warn-unit", "unit-no-warn-ns")
			model.Spec.Model.Storage.Type = llmv1alpha1.StorageTypeEmptyDir
			model.Spec.Model.Preload = &preload

			managedNs := newManagedNamespace("unit-no-warn-ns")
			Expect(k8sClient.Create(bgCtx, managedNs)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(bgCtx, managedNs) })

			warnings, err := validator.ValidateCreate(bgCtx, model)
			Expect(err).NotTo(HaveOccurred())
			Expect(warnings).To(BeEmpty())
		})

		It("should not warn when storage is pvc and preload is true", func() {
			preload := true
			model := newBaseLLMModel("no-warn-pvc", "unit-pvc-warn-ns")
			model.Spec.Model.Storage.Type = llmv1alpha1.StorageTypePVC
			model.Spec.Model.Preload = &preload

			managedNs := newManagedNamespace("unit-pvc-warn-ns")
			Expect(k8sClient.Create(bgCtx, managedNs)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(bgCtx, managedNs) })

			warnings, err := validator.ValidateCreate(bgCtx, model)
			Expect(err).NotTo(HaveOccurred())
			Expect(warnings).To(BeEmpty())
		})
	})
})

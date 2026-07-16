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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	llmv1alpha1 "github.com/nebari-dev/llm-serving-pack/operator/api/v1alpha1"
)

func newBasePassthroughModel(name, namespace string) *llmv1alpha1.PassthroughModel {
	return &llmv1alpha1.PassthroughModel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: llmv1alpha1.PassthroughModelSpec{
			Provider: llmv1alpha1.ProviderSpec{
				Hostname:             "openrouter.ai",
				SchemaVersion:        "api/v1",
				CredentialSecretName: "openrouter-api-key",
			},
			Models: llmv1alpha1.PassthroughModels{
				CatchAll: true,
			},
			Access: llmv1alpha1.AccessSpec{
				Groups: []string{"llm"},
			},
		},
	}
}

var _ = Describe("PassthroughModel Webhook", func() {
	bgCtx := context.Background()

	Context("ValidateCreate", func() {
		It("should accept a valid PassthroughModel in a managed namespace", func() {
			ns := newManagedNamespace("pt-managed-create-valid")
			Expect(k8sClient.Create(bgCtx, ns)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(bgCtx, ns) })

			pm := newBasePassthroughModel("my-passthrough", ns.Name)
			Expect(k8sClient.Create(bgCtx, pm)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(bgCtx, pm) })
		})

		It("should reject a PassthroughModel in a namespace without the managed label", func() {
			ns := newUnmanagedNamespace("pt-unmanaged-create")
			Expect(k8sClient.Create(bgCtx, ns)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(bgCtx, ns) })

			pm := newBasePassthroughModel("my-passthrough-unmanaged", ns.Name)
			err := k8sClient.Create(bgCtx, pm)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("nebari.dev/managed"))
		})

		It("should reject a PassthroughModel with no public flag and no groups", func() {
			ns := newManagedNamespace("pt-managed-empty-access")
			Expect(k8sClient.Create(bgCtx, ns)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(bgCtx, ns) })

			pm := newBasePassthroughModel("pt-empty-access", ns.Name)
			pm.Spec.Access = llmv1alpha1.AccessSpec{}
			err := k8sClient.Create(bgCtx, pm)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.access"))
		})

		It("should accept a public PassthroughModel with no groups", func() {
			ns := newManagedNamespace("pt-managed-public")
			Expect(k8sClient.Create(bgCtx, ns)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(bgCtx, ns) })

			pm := newBasePassthroughModel("pt-public", ns.Name)
			public := true
			pm.Spec.Access = llmv1alpha1.AccessSpec{Public: &public}
			Expect(k8sClient.Create(bgCtx, pm)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(bgCtx, pm) })
		})

		It("should reject a hostname that includes a URL scheme", func() {
			ns := newManagedNamespace("pt-managed-scheme")
			Expect(k8sClient.Create(bgCtx, ns)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(bgCtx, ns) })

			pm := newBasePassthroughModel("pt-scheme", ns.Name)
			pm.Spec.Provider.Hostname = "https://openrouter.ai"
			err := k8sClient.Create(bgCtx, pm)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("bare hostname"))
		})

		It("should reject a hostname that includes a port or path", func() {
			for i, bad := range []string{"openrouter.ai:443", "openrouter.ai/v1", "open router.ai"} {
				ns := newManagedNamespace(fmt.Sprintf("pt-managed-badhost-%d", i))
				Expect(k8sClient.Create(bgCtx, ns)).To(Succeed())
				DeferCleanup(func() { _ = k8sClient.Delete(bgCtx, ns) })

				pm := newBasePassthroughModel(fmt.Sprintf("pt-badhost-%d", i), ns.Name)
				pm.Spec.Provider.Hostname = bad
				err := k8sClient.Create(bgCtx, pm)
				Expect(err).To(HaveOccurred(), "expected hostname %q to be rejected", bad)
				Expect(err.Error()).To(ContainSubstring("bare hostname"))
			}
		})

		It("should reject a PassthroughModel that routes nothing", func() {
			ns := newManagedNamespace("pt-managed-routes-nothing")
			Expect(k8sClient.Create(bgCtx, ns)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(bgCtx, ns) })

			pm := newBasePassthroughModel("pt-routes-nothing", ns.Name)
			pm.Spec.Models = llmv1alpha1.PassthroughModels{}
			err := k8sClient.Create(bgCtx, pm)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("catchAll"))
		})

		It("should reject empty declared model ids", func() {
			ns := newManagedNamespace("pt-managed-empty-declared")
			Expect(k8sClient.Create(bgCtx, ns)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(bgCtx, ns) })

			pm := newBasePassthroughModel("pt-empty-declared", ns.Name)
			pm.Spec.Models = llmv1alpha1.PassthroughModels{Declared: []string{"openai/gpt-5.2", "  "}}
			err := k8sClient.Create(bgCtx, pm)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("declared[1]"))
		})

		It("should reject a PassthroughModel with both endpoints disabled", func() {
			ns := newManagedNamespace("pt-managed-no-endpoints")
			Expect(k8sClient.Create(bgCtx, ns)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(bgCtx, ns) })

			pm := newBasePassthroughModel("pt-no-endpoints", ns.Name)
			disabled := false
			pm.Spec.Endpoints = llmv1alpha1.PassthroughEndpoints{
				External: llmv1alpha1.EndpointToggle{Enabled: &disabled},
				Internal: llmv1alpha1.EndpointToggle{Enabled: &disabled},
			}
			err := k8sClient.Create(bgCtx, pm)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("both external and internal"))
		})

		It("should reject a PassthroughModel whose name collides with an existing LLMModel", func() {
			ns := newManagedNamespace("pt-managed-name-collision")
			Expect(k8sClient.Create(bgCtx, ns)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(bgCtx, ns) })

			model := newBaseLLMModel("shared-name", ns.Name)
			Expect(k8sClient.Create(bgCtx, model)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(bgCtx, model) })

			pm := newBasePassthroughModel("shared-name", ns.Name)
			err := k8sClient.Create(bgCtx, pm)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("already used by an LLMModel"))
		})
	})

	Context("ValidateUpdate", func() {
		It("should reject an update that removes all routing", func() {
			ns := newManagedNamespace("pt-managed-update")
			Expect(k8sClient.Create(bgCtx, ns)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(bgCtx, ns) })

			pm := newBasePassthroughModel("pt-update", ns.Name)
			Expect(k8sClient.Create(bgCtx, pm)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(bgCtx, pm) })

			pm.Spec.Models = llmv1alpha1.PassthroughModels{}
			err := k8sClient.Update(bgCtx, pm)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("catchAll"))
		})
	})

	Context("ValidateDelete", func() {
		It("should always accept deletion", func() {
			ns := newManagedNamespace("pt-managed-delete")
			Expect(k8sClient.Create(bgCtx, ns)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(bgCtx, ns) })

			pm := newBasePassthroughModel("pt-delete", ns.Name)
			Expect(k8sClient.Create(bgCtx, pm)).To(Succeed())
			Expect(k8sClient.Delete(bgCtx, pm)).To(Succeed())
		})
	})
})

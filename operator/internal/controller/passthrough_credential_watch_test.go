package controller

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
)

var _ = Describe("PassthroughModel credential Secret watch", func() {
	It("refreshes status on referenced credential Secret events", func() {
		mgr, err := ctrl.NewManager(cfg, ctrl.Options{
			Scheme:                 k8sClient.Scheme(),
			Metrics:                metricsserver.Options{BindAddress: "0"},
			HealthProbeBindAddress: "0",
		})
		Expect(err).NotTo(HaveOccurred())
		r := &PassthroughModelReconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme(), Config: testConfig()}
		Expect(r.SetupWithManager(mgr)).To(Succeed())
		managerCtx, stop := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- mgr.Start(managerCtx) }()
		DeferCleanup(func() {
			stop()
			Eventually(done, 5*time.Second).Should(Receive(BeNil()))
		})

		pm := newPassthroughModel("credential-watch", "default")
		Expect(k8sClient.Create(ctx, pm)).To(Succeed())
		DeferCleanup(func() {
			Expect(k8sClient.Delete(ctx, pm)).To(Succeed())
			Eventually(func() bool {
				err := k8sClient.Get(ctx, client.ObjectKeyFromObject(pm), &llmv1alpha1.PassthroughModel{})
				return apierrors.IsNotFound(err)
			}, 5*time.Second).Should(BeTrue())
		})
		condition := func() *metav1.Condition {
			stored := &llmv1alpha1.PassthroughModel{}
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(pm), stored); err != nil {
				return nil
			}
			return meta.FindStatusCondition(stored.Status.Conditions, CondUpstreamCredentialResolved)
		}
		assertCondition := func(status metav1.ConditionStatus, reason string) {
			Eventually(condition, 5*time.Second).Should(And(
				Not(BeNil()),
				HaveField("Status", status),
				HaveField("Reason", reason),
			))
		}
		assertCondition(metav1.ConditionFalse, "SecretNotFound")

		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Name: pm.Spec.Provider.CredentialSecretName, Namespace: pm.Namespace,
		}, Data: map[string][]byte{"apiKey": []byte("test-key")}}
		DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, secret))).To(Succeed()) })
		Expect(k8sClient.Create(ctx, secret)).To(Succeed())
		assertCondition(metav1.ConditionTrue, "Resolved")
		secret.Data = map[string][]byte{"apiKey": {}}
		Expect(k8sClient.Update(ctx, secret)).To(Succeed())
		assertCondition(metav1.ConditionFalse, "APIKeyMissing")
		Expect(k8sClient.Delete(ctx, secret)).To(Succeed())
		assertCondition(metav1.ConditionFalse, "SecretNotFound")
	})
})

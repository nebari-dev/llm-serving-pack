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

package controller

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
	"github.com/nebari-dev/nebari-llm-serving-pack/operator/internal/config"
	"github.com/nebari-dev/nebari-llm-serving-pack/operator/internal/controller/reconcilers"
)

const (
	finalizerName       = "llm.nebari.dev/cleanup"
	modelConfigHashAnno = "llm.nebari.dev/model-config-hash"
)

// controllerLogger is the interface for structured logging used within this controller.
type controllerLogger interface {
	Info(msg string, keysAndValues ...interface{})
	Error(err error, msg string, keysAndValues ...interface{})
}

// LLMModelReconciler reconciles a LLMModel object
type LLMModelReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Config *config.OperatorConfig
}

// +kubebuilder:rbac:groups=llm.nebari.dev,resources=llmmodels,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=llm.nebari.dev,resources=llmmodels/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=llm.nebari.dev,resources=llmmodels/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services;serviceaccounts;persistentvolumeclaims;secrets;configmaps;namespaces,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings,verbs=get;list;watch;create;update;patch;delete

// Reconcile implements the main reconciliation loop for LLMModel resources.
func (r *LLMModelReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// 1. Fetch LLMModel
	model := &llmv1alpha1.LLMModel{}
	if err := r.Get(ctx, req.NamespacedName, model); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// 2. Handle deletion
	if !model.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, model)
	}

	// 3. Add finalizer if missing
	if !controllerutil.ContainsFinalizer(model, finalizerName) {
		controllerutil.AddFinalizer(model, finalizerName)
		if err := r.Update(ctx, model); err != nil {
			return ctrl.Result{}, fmt.Errorf("adding finalizer: %w", err)
		}
		// Requeue immediately - the finalizer update changes the resourceVersion,
		// and continuing with the stale object causes status update conflicts.
		return ctrl.Result{Requeue: true}, nil
	}

	// 4. Set phase to Pending if empty
	if model.Status.Phase == "" {
		model.Status.Phase = llmv1alpha1.PhasePending
		if err := r.Status().Update(ctx, model); err != nil {
			log.Error(err, "failed to update status phase to Pending")
			return ctrl.Result{Requeue: true}, nil
		}
	}

	// 5. Ensure the llm-api-keys namespace exists (if Config is available)
	if r.Config != nil {
		if err := r.ensureNamespace(ctx, r.Config.APIKeysNamespace); err != nil {
			return ctrl.Result{}, fmt.Errorf("ensuring api-keys namespace: %w", err)
		}
	}

	// 6. Reconcile storage (PVC)
	storageResult, err := reconcilers.BuildStorageSpec(model)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("building storage spec: %w", err)
	}
	if storageResult.PVC != nil {
		storageResult.PVC.Namespace = model.Namespace
		if err := reconcilers.SetOwnerReference(model, storageResult.PVC, r.Scheme); err != nil {
			return ctrl.Result{}, fmt.Errorf("setting owner on PVC: %w", err)
		}
		if err := r.createOrUpdatePVC(ctx, storageResult.PVC); err != nil {
			return ctrl.Result{}, fmt.Errorf("reconciling PVC: %w", err)
		}
	}

	// 7. Reconcile auth resources (cross-namespace: Secret, ConfigMap, ReferenceGrant)
	var authResources *reconcilers.AuthResources
	if r.Config != nil {
		authResources, err = reconcilers.BuildAuthResources(model, r.Config)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("building auth resources: %w", err)
		}
		if err := r.reconcileCrossNamespaceResources(ctx, log, authResources); err != nil {
			return ctrl.Result{}, err
		}
	}

	// 8. Reconcile NetworkPolicy
	networkPolicy := reconcilers.BuildNetworkPolicy(model, r.Config)
	if err := reconcilers.SetOwnerReference(model, networkPolicy, r.Scheme); err != nil {
		return ctrl.Result{}, fmt.Errorf("setting owner on NetworkPolicy: %w", err)
	}
	if err := r.createOrUpdateNetworkPolicy(ctx, networkPolicy); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling NetworkPolicy: %w", err)
	}

	// 9. Reconcile model service resources (Deployment, Service, SA, PodMonitor)
	modelServiceResources, err := reconcilers.BuildModelServiceResources(model, storageResult, r.Config)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("building model service resources: %w", err)
	}
	if err := r.reconcileModelServiceResources(ctx, log, model, modelServiceResources); err != nil {
		return ctrl.Result{}, err
	}

	// 10. Check deployment readiness to determine phase
	phase := r.determinePhase(ctx, model)
	if phase == llmv1alpha1.PhaseDownloading || phase == llmv1alpha1.PhaseStarting {
		// Update status with phase and replica counts even during intermediate phases
		if err := r.updateStatus(ctx, log, model, phase); err != nil {
			log.Error(err, "failed to update status during intermediate phase", "phase", phase)
		}
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}

	// 11. Once model is (or will be) serving, create downstream resources
	if r.Config != nil {
		// InferencePool + EPP
		poolResources, err := reconcilers.BuildInferencePoolResources(model, r.Config)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("building inference pool resources: %w", err)
		}
		if err := r.reconcileInferencePoolResources(ctx, log, model, poolResources); err != nil {
			return ctrl.Result{}, err
		}

		// Routing (AIGatewayRoutes)
		routingResources, err := reconcilers.BuildRoutingResources(model, r.Config)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("building routing resources: %w", err)
		}
		if err := r.reconcileRoutingResources(ctx, log, model, routingResources); err != nil {
			return ctrl.Result{}, err
		}

		// SecurityPolicies (from auth resources built in step 7)
		if authResources != nil {
			if err := r.reconcileSecurityPolicies(ctx, log, authResources); err != nil {
				return ctrl.Result{}, err
			}
		}
	}

	// 12. Update status
	if err := r.updateStatus(ctx, log, model, phase); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// reconcileDelete handles finalization: cleans up cross-namespace resources and removes the finalizer.
func (r *LLMModelReconciler) reconcileDelete(ctx context.Context, model *llmv1alpha1.LLMModel) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(model, finalizerName) {
		return ctrl.Result{}, nil
	}

	log.Info("cleaning up cross-namespace resources", "model", model.Name)

	if r.Config != nil {
		// Delete Secret in api-keys namespace
		secret := &corev1.Secret{}
		secretKey := types.NamespacedName{
			Name:      reconcilers.APIKeySecretName(model.Name),
			Namespace: r.Config.APIKeysNamespace,
		}
		if err := r.Get(ctx, secretKey, secret); err == nil {
			if err := r.Delete(ctx, secret); err != nil && !apierrors.IsNotFound(err) {
				return ctrl.Result{}, fmt.Errorf("deleting api key secret: %w", err)
			}
		}

		// Delete ConfigMap in api-keys namespace
		cm := &corev1.ConfigMap{}
		cmKey := types.NamespacedName{
			Name:      reconcilers.APIKeyMetadataConfigMapName(model.Name),
			Namespace: r.Config.APIKeysNamespace,
		}
		if err := r.Get(ctx, cmKey, cm); err == nil {
			if err := r.Delete(ctx, cm); err != nil && !apierrors.IsNotFound(err) {
				return ctrl.Result{}, fmt.Errorf("deleting api key metadata configmap: %w", err)
			}
		}

		// Delete ReferenceGrant in api-keys namespace
		refGrant := &unstructured.Unstructured{}
		refGrant.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "gateway.networking.k8s.io",
			Version: "v1beta1",
			Kind:    "ReferenceGrant",
		})
		refGrantKey := types.NamespacedName{
			Name:      model.Name + "-" + model.Namespace + "-ref-grant",
			Namespace: r.Config.APIKeysNamespace,
		}
		if err := r.Get(ctx, refGrantKey, refGrant); err == nil {
			if err := r.Delete(ctx, refGrant); err != nil && !apierrors.IsNotFound(err) {
				// Non-fatal: CRD may not be installed
				log.Error(err, "failed to delete ReferenceGrant during cleanup")
			}
		}
	}

	// Remove finalizer
	controllerutil.RemoveFinalizer(model, finalizerName)
	if err := r.Update(ctx, model); err != nil {
		return ctrl.Result{}, fmt.Errorf("removing finalizer: %w", err)
	}

	return ctrl.Result{}, nil
}

// ensureNamespace creates the namespace if it does not exist.
func (r *LLMModelReconciler) ensureNamespace(ctx context.Context, name string) error {
	ns := &corev1.Namespace{}
	if err := r.Get(ctx, types.NamespacedName{Name: name}, ns); err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: name},
		}
		if err := r.Create(ctx, ns); err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}
	}
	return nil
}

// reconcileCrossNamespaceResources creates or updates the Secret, ConfigMap, and
// ReferenceGrant in the api-keys namespace. These resources have no owner references
// and are cleaned up by the finalizer.
func (r *LLMModelReconciler) reconcileCrossNamespaceResources(
	ctx context.Context,
	log controllerLogger,
	auth *reconcilers.AuthResources,
) error {
	if err := r.createOrUpdateSecret(ctx, auth.APIKeySecret); err != nil {
		return fmt.Errorf("reconciling api key secret: %w", err)
	}
	if err := r.createOrUpdateConfigMap(ctx, auth.APIKeyMetadataCM); err != nil {
		return fmt.Errorf("reconciling api key metadata configmap: %w", err)
	}
	if auth.ReferenceGrant != nil {
		if err := r.createOrUpdateUnstructured(ctx, log, auth.ReferenceGrant); err != nil {
			log.Error(err, "failed to reconcile ReferenceGrant - CRD may not be installed, skipping")
		}
	}
	return nil
}

// reconcileModelServiceResources creates or updates the Deployment, Service, SA, and PodMonitor.
// It includes breaking-change detection for the Deployment.
func (r *LLMModelReconciler) reconcileModelServiceResources(
	ctx context.Context,
	log controllerLogger,
	model *llmv1alpha1.LLMModel,
	resources *reconcilers.ModelServiceResources,
) error {
	// ServiceAccount
	resources.ServiceAccount.Namespace = model.Namespace
	if err := reconcilers.SetOwnerReference(model, resources.ServiceAccount, r.Scheme); err != nil {
		return fmt.Errorf("setting owner on ServiceAccount: %w", err)
	}
	if err := r.createOrUpdateServiceAccount(ctx, resources.ServiceAccount); err != nil {
		return fmt.Errorf("reconciling ServiceAccount: %w", err)
	}

	// Service
	resources.Service.Namespace = model.Namespace
	if err := reconcilers.SetOwnerReference(model, resources.Service, r.Scheme); err != nil {
		return fmt.Errorf("setting owner on Service: %w", err)
	}
	if err := r.createOrUpdateService(ctx, resources.Service); err != nil {
		return fmt.Errorf("reconciling Service: %w", err)
	}

	// Deployment - with breaking-change detection
	resources.Deployment.Namespace = model.Namespace
	if err := reconcilers.SetOwnerReference(model, resources.Deployment, r.Scheme); err != nil {
		return fmt.Errorf("setting owner on Deployment: %w", err)
	}
	hash := modelConfigHash(model)
	if resources.Deployment.Annotations == nil {
		resources.Deployment.Annotations = make(map[string]string)
	}
	resources.Deployment.Annotations[modelConfigHashAnno] = hash

	existing := &appsv1.Deployment{}
	getErr := r.Get(ctx, types.NamespacedName{Name: resources.Deployment.Name, Namespace: resources.Deployment.Namespace}, existing)
	if getErr != nil && !apierrors.IsNotFound(getErr) {
		return fmt.Errorf("getting existing Deployment: %w", getErr)
	}
	if getErr == nil {
		// Deployment exists - check for breaking changes
		existingHash := existing.Annotations[modelConfigHashAnno]
		if existingHash != "" && existingHash != hash {
			log.Info("model config changed - deleting Deployment to trigger re-download",
				"old-hash", existingHash, "new-hash", hash)
			if err := r.Delete(ctx, existing); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("deleting stale Deployment: %w", err)
			}
			// Will be re-created on next reconcile
			return nil
		}
		// Update spec while preserving resource version
		existing.Spec = resources.Deployment.Spec
		if existing.Annotations == nil {
			existing.Annotations = make(map[string]string)
		}
		existing.Annotations[modelConfigHashAnno] = hash
		if err := r.Update(ctx, existing); err != nil {
			return fmt.Errorf("updating Deployment: %w", err)
		}
	} else {
		// Create new deployment
		if err := r.Create(ctx, resources.Deployment); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating Deployment: %w", err)
		}
	}

	// PodMonitor (optional CRD)
	if resources.PodMonitor != nil {
		resources.PodMonitor.SetNamespace(model.Namespace)
		if err := r.createOrUpdateUnstructured(ctx, log, resources.PodMonitor); err != nil {
			log.Error(err, "failed to reconcile PodMonitor - CRD may not be installed, skipping")
		}
	}

	return nil
}

// reconcileInferencePoolResources creates or updates InferencePool and EPP resources.
func (r *LLMModelReconciler) reconcileInferencePoolResources(
	ctx context.Context,
	log controllerLogger,
	model *llmv1alpha1.LLMModel,
	pool *reconcilers.InferencePoolResources,
) error {
	// InferencePool (unstructured CRD - non-fatal if missing)
	pool.InferencePool.SetNamespace(model.Namespace)
	if err := r.createOrUpdateUnstructured(ctx, log, pool.InferencePool); err != nil {
		log.Error(err, "failed to reconcile InferencePool - CRD may not be installed, skipping")
	}

	// EPP ServiceAccount
	pool.EPPServiceAccount.Namespace = model.Namespace
	if err := reconcilers.SetOwnerReference(model, pool.EPPServiceAccount, r.Scheme); err != nil {
		return fmt.Errorf("setting owner on EPP ServiceAccount: %w", err)
	}
	if err := r.createOrUpdateServiceAccount(ctx, pool.EPPServiceAccount); err != nil {
		return fmt.Errorf("reconciling EPP ServiceAccount: %w", err)
	}

	// EPP Role
	pool.EPPRole.Namespace = model.Namespace
	if err := reconcilers.SetOwnerReference(model, pool.EPPRole, r.Scheme); err != nil {
		return fmt.Errorf("setting owner on EPP Role: %w", err)
	}
	if err := r.createOrUpdateRole(ctx, pool.EPPRole); err != nil {
		return fmt.Errorf("reconciling EPP Role: %w", err)
	}

	// EPP RoleBinding
	pool.EPPRoleBinding.Namespace = model.Namespace
	if err := reconcilers.SetOwnerReference(model, pool.EPPRoleBinding, r.Scheme); err != nil {
		return fmt.Errorf("setting owner on EPP RoleBinding: %w", err)
	}
	if err := r.createOrUpdateRoleBinding(ctx, pool.EPPRoleBinding); err != nil {
		return fmt.Errorf("reconciling EPP RoleBinding: %w", err)
	}

	// EPP Service
	pool.EPPService.Namespace = model.Namespace
	if err := reconcilers.SetOwnerReference(model, pool.EPPService, r.Scheme); err != nil {
		return fmt.Errorf("setting owner on EPP Service: %w", err)
	}
	if err := r.createOrUpdateService(ctx, pool.EPPService); err != nil {
		return fmt.Errorf("reconciling EPP Service: %w", err)
	}

	// EPP Deployment
	pool.EPPDeployment.Namespace = model.Namespace
	if err := reconcilers.SetOwnerReference(model, pool.EPPDeployment, r.Scheme); err != nil {
		return fmt.Errorf("setting owner on EPP Deployment: %w", err)
	}
	if err := r.createOrUpdateDeployment(ctx, pool.EPPDeployment); err != nil {
		return fmt.Errorf("reconciling EPP Deployment: %w", err)
	}

	return nil
}

// reconcileRoutingResources creates or updates AIGatewayRoute resources.
func (r *LLMModelReconciler) reconcileRoutingResources(
	ctx context.Context,
	log controllerLogger,
	model *llmv1alpha1.LLMModel,
	routing *reconcilers.RoutingResources,
) error {
	if routing.ExternalRoute != nil {
		routing.ExternalRoute.SetNamespace(model.Namespace)
		if err := r.createOrUpdateUnstructured(ctx, log, routing.ExternalRoute); err != nil {
			log.Error(err, "failed to reconcile external AIGatewayRoute - CRD may not be installed, skipping")
		}
	}
	if routing.InternalRoute != nil {
		routing.InternalRoute.SetNamespace(model.Namespace)
		if err := r.createOrUpdateUnstructured(ctx, log, routing.InternalRoute); err != nil {
			log.Error(err, "failed to reconcile internal AIGatewayRoute - CRD may not be installed, skipping")
		}
	}
	return nil
}

// reconcileSecurityPolicies creates or updates SecurityPolicy resources.
func (r *LLMModelReconciler) reconcileSecurityPolicies(
	ctx context.Context,
	log controllerLogger,
	auth *reconcilers.AuthResources,
) error {
	if auth.ExternalSecurityPolicy != nil {
		if err := r.createOrUpdateUnstructured(ctx, log, auth.ExternalSecurityPolicy); err != nil {
			log.Error(err, "failed to reconcile external SecurityPolicy - CRD may not be installed, skipping")
		}
	}
	if auth.InternalSecurityPolicy != nil {
		if err := r.createOrUpdateUnstructured(ctx, log, auth.InternalSecurityPolicy); err != nil {
			log.Error(err, "failed to reconcile internal SecurityPolicy - CRD may not be installed, skipping")
		}
	}
	return nil
}

// determinePhase inspects the model Deployment and its pods to determine the current lifecycle phase.
func (r *LLMModelReconciler) determinePhase(ctx context.Context, model *llmv1alpha1.LLMModel) llmv1alpha1.LLMModelPhase {
	log := logf.FromContext(ctx)

	dep := &appsv1.Deployment{}
	if err := r.Get(ctx, types.NamespacedName{Name: model.Name, Namespace: model.Namespace}, dep); err != nil {
		if apierrors.IsNotFound(err) {
			return llmv1alpha1.PhasePending
		}
		log.Error(err, "failed to get Deployment for phase determination")
		return llmv1alpha1.PhaseError
	}

	// Check if any pod has a running init container (downloading phase)
	podList := &corev1.PodList{}
	if err := r.List(ctx, podList,
		client.InNamespace(model.Namespace),
		client.MatchingLabels{"app.kubernetes.io/instance": model.Name},
	); err != nil {
		log.Error(err, "failed to list pods for phase determination")
		return llmv1alpha1.PhaseError
	}

	for i := range podList.Items {
		pod := &podList.Items[i]
		for _, cs := range pod.Status.InitContainerStatuses {
			if cs.State.Running != nil {
				return llmv1alpha1.PhaseDownloading
			}
		}
	}

	desired := int32(1)
	if dep.Spec.Replicas != nil {
		desired = *dep.Spec.Replicas
	}

	readyReplicas := dep.Status.ReadyReplicas

	if readyReplicas == 0 {
		return llmv1alpha1.PhaseStarting
	}
	if readyReplicas >= desired {
		return llmv1alpha1.PhaseReady
	}
	return llmv1alpha1.PhaseDegraded
}

// updateStatus writes the final status fields to a fresh copy of the LLMModel.
func (r *LLMModelReconciler) updateStatus(
	ctx context.Context,
	log controllerLogger,
	model *llmv1alpha1.LLMModel,
	phase llmv1alpha1.LLMModelPhase,
) error {
	// Fetch fresh copy to avoid update conflicts
	fresh := &llmv1alpha1.LLMModel{}
	if err := r.Get(ctx, types.NamespacedName{Name: model.Name, Namespace: model.Namespace}, fresh); err != nil {
		return client.IgnoreNotFound(err)
	}

	fresh.Status.Phase = phase
	fresh.Status.ObservedGeneration = fresh.Generation

	// Update replica counts
	dep := &appsv1.Deployment{}
	if err := r.Get(ctx, types.NamespacedName{Name: model.Name, Namespace: model.Namespace}, dep); err == nil {
		desired := int32(1)
		if dep.Spec.Replicas != nil {
			desired = *dep.Spec.Replicas
		}
		fresh.Status.Replicas = llmv1alpha1.ReplicaStatus{
			Ready:   dep.Status.ReadyReplicas,
			Desired: desired,
		}
	}

	// Update endpoint URLs
	if r.Config != nil {
		subdomain := model.Spec.Endpoints.External.Subdomain
		if subdomain == "" {
			subdomain = reconcilers.Slugify(model.Name)
		}
		if r.Config.ExternalGatewayName != "" {
			fresh.Status.Endpoints.External = "https://" + reconcilers.ExternalHostname(subdomain, r.Config.BaseDomain)
		}
		if r.Config.InternalGatewayName != "" {
			fresh.Status.Endpoints.Internal = "https://" + reconcilers.InternalHostname(subdomain, r.Config.BaseDomain)
		}
	}

	if err := r.Status().Update(ctx, fresh); err != nil {
		log.Error(err, "failed to update LLMModel status")
		return err
	}
	return nil
}

// modelConfigHash computes an 8-byte hex hash of the model's breaking configuration fields.
// If the hash changes on an existing Deployment, the Deployment is deleted to trigger re-download.
func modelConfigHash(model *llmv1alpha1.LLMModel) string {
	data := model.Name +
		string(model.Spec.Model.Source) +
		string(model.Spec.Model.Storage.Type) +
		model.Spec.Model.Image +
		model.Spec.Model.Revision +
		model.Spec.Model.Name
	h := sha256.Sum256([]byte(data))
	return fmt.Sprintf("%x", h[:8])
}

// --- createOrUpdate helpers ---

func (r *LLMModelReconciler) createOrUpdatePVC(ctx context.Context, pvc *corev1.PersistentVolumeClaim) error {
	existing := &corev1.PersistentVolumeClaim{}
	err := r.Get(ctx, types.NamespacedName{Name: pvc.Name, Namespace: pvc.Namespace}, existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, pvc)
	}
	if err != nil {
		return err
	}
	// PVC specs are mostly immutable; update labels/annotations only
	existing.Labels = pvc.Labels
	return r.Update(ctx, existing)
}

func (r *LLMModelReconciler) createOrUpdateSecret(ctx context.Context, secret *corev1.Secret) error {
	existing := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Name: secret.Name, Namespace: secret.Namespace}, existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, secret)
	}
	if err != nil {
		return err
	}
	// Preserve existing data (API keys written by other controllers/users); update labels only
	existing.Labels = secret.Labels
	return r.Update(ctx, existing)
}

func (r *LLMModelReconciler) createOrUpdateConfigMap(ctx context.Context, cm *corev1.ConfigMap) error {
	existing := &corev1.ConfigMap{}
	err := r.Get(ctx, types.NamespacedName{Name: cm.Name, Namespace: cm.Namespace}, existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, cm)
	}
	if err != nil {
		return err
	}
	existing.Labels = cm.Labels
	existing.Data = cm.Data
	return r.Update(ctx, existing)
}

func (r *LLMModelReconciler) createOrUpdateNetworkPolicy(ctx context.Context, np *networkingv1.NetworkPolicy) error {
	existing := &networkingv1.NetworkPolicy{}
	err := r.Get(ctx, types.NamespacedName{Name: np.Name, Namespace: np.Namespace}, existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, np)
	}
	if err != nil {
		return err
	}
	existing.Spec = np.Spec
	existing.Labels = np.Labels
	return r.Update(ctx, existing)
}

func (r *LLMModelReconciler) createOrUpdateServiceAccount(ctx context.Context, sa *corev1.ServiceAccount) error {
	existing := &corev1.ServiceAccount{}
	err := r.Get(ctx, types.NamespacedName{Name: sa.Name, Namespace: sa.Namespace}, existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, sa)
	}
	if err != nil {
		return err
	}
	existing.Labels = sa.Labels
	return r.Update(ctx, existing)
}

func (r *LLMModelReconciler) createOrUpdateService(ctx context.Context, svc *corev1.Service) error {
	existing := &corev1.Service{}
	err := r.Get(ctx, types.NamespacedName{Name: svc.Name, Namespace: svc.Namespace}, existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, svc)
	}
	if err != nil {
		return err
	}
	// Preserve ClusterIP assigned by the API server
	svc.Spec.ClusterIP = existing.Spec.ClusterIP
	svc.Spec.ClusterIPs = existing.Spec.ClusterIPs
	existing.Spec = svc.Spec
	existing.Labels = svc.Labels
	return r.Update(ctx, existing)
}

func (r *LLMModelReconciler) createOrUpdateDeployment(ctx context.Context, dep *appsv1.Deployment) error {
	existing := &appsv1.Deployment{}
	err := r.Get(ctx, types.NamespacedName{Name: dep.Name, Namespace: dep.Namespace}, existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, dep)
	}
	if err != nil {
		return err
	}
	existing.Spec = dep.Spec
	existing.Labels = dep.Labels
	return r.Update(ctx, existing)
}

func (r *LLMModelReconciler) createOrUpdateRole(ctx context.Context, role *rbacv1.Role) error {
	existing := &rbacv1.Role{}
	err := r.Get(ctx, types.NamespacedName{Name: role.Name, Namespace: role.Namespace}, existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, role)
	}
	if err != nil {
		return err
	}
	existing.Rules = role.Rules
	existing.Labels = role.Labels
	return r.Update(ctx, existing)
}

func (r *LLMModelReconciler) createOrUpdateRoleBinding(ctx context.Context, rb *rbacv1.RoleBinding) error {
	existing := &rbacv1.RoleBinding{}
	err := r.Get(ctx, types.NamespacedName{Name: rb.Name, Namespace: rb.Namespace}, existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, rb)
	}
	if err != nil {
		return err
	}
	existing.RoleRef = rb.RoleRef
	existing.Subjects = rb.Subjects
	existing.Labels = rb.Labels
	return r.Update(ctx, existing)
}

// createOrUpdateUnstructured creates or updates an unstructured resource.
// Errors for optional CRD-based resources should be logged by the caller rather than
// failing the reconciliation.
func (r *LLMModelReconciler) createOrUpdateUnstructured(
	ctx context.Context,
	log controllerLogger,
	obj *unstructured.Unstructured,
) error {
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(obj.GroupVersionKind())
	err := r.Get(ctx, types.NamespacedName{Name: obj.GetName(), Namespace: obj.GetNamespace()}, existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, obj)
	}
	if err != nil {
		return err
	}
	obj.SetResourceVersion(existing.GetResourceVersion())
	return r.Update(ctx, obj)
}

// SetupWithManager sets up the controller with the Manager.
func (r *LLMModelReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&llmv1alpha1.LLMModel{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Named("llmmodel").
		Complete(r)
}

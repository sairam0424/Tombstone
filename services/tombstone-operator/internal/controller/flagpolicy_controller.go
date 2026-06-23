// Package controller implements reconcile loops for Tombstone Kubernetes CRDs.
package controller

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"go.uber.org/zap"

	v1alpha1 "github.com/tombstone-io/tombstone/services/tombstone-operator/api/v1alpha1"
)

const (
	policyRequeueInterval = 15 * time.Minute
)

// FlagPolicyReconciler evaluates FlagPolicy governance rules against all
// matching FeatureFlag resources in the same namespace.
type FlagPolicyReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Logger *zap.Logger
}

// Reconcile evaluates the FlagPolicy and updates status.violatingFlags.
func (r *FlagPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Logger.With(zap.String("policy", req.Name))

	var policy v1alpha1.FlagPolicy
	if err := r.Get(ctx, req.NamespacedName, &policy); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	violations, err := r.evaluatePolicy(ctx, &policy)
	if err != nil {
		log.Error("policy evaluation failed", zap.Error(err))
		return ctrl.Result{RequeueAfter: policyRequeueInterval}, err
	}

	updated := policy.DeepCopy()
	now := metav1.Now()
	updated.Status.LastEvaluated = &now
	updated.Status.ViolatingFlags = violations
	if len(violations) == 0 {
		updated.Status.Phase = "Compliant"
	} else {
		updated.Status.Phase = "Violation"
		log.Warn("policy violations detected",
			zap.Strings("flags", violations),
			zap.Int("count", len(violations)),
		)
	}

	if err := r.Status().Update(ctx, updated); err != nil {
		log.Warn("failed to update policy status", zap.Error(err))
	}

	return ctrl.Result{RequeueAfter: policyRequeueInterval}, nil
}

// evaluatePolicy lists all matching FeatureFlag resources and checks each
// one against the policy rules. Returns the list of violating flag names.
func (r *FlagPolicyReconciler) evaluatePolicy(
	ctx context.Context,
	policy *v1alpha1.FlagPolicy,
) ([]string, error) {
	listOpts := []client.ListOption{client.InNamespace(policy.Namespace)}

	// Apply label selector if specified.
	if policy.Spec.Selector != nil {
		sel, err := metav1.LabelSelectorAsSelector(policy.Spec.Selector)
		if err != nil {
			return nil, fmt.Errorf("parsing label selector: %w", err)
		}
		if sel != labels.Nothing() {
			listOpts = append(listOpts, client.MatchingLabelsSelector{Selector: sel})
		}
	}

	var flagList v1alpha1.FeatureFlagList
	if err := r.List(ctx, &flagList, listOpts...); err != nil {
		return nil, fmt.Errorf("listing FeatureFlags: %w", err)
	}

	var violations []string
	for i := range flagList.Items {
		ff := &flagList.Items[i]
		if v := r.checkViolations(policy, ff); len(v) > 0 {
			violations = append(violations, fmt.Sprintf("%s/%s: %v", ff.Namespace, ff.Name, v))
		}
	}
	return violations, nil
}

// checkViolations returns a slice of human-readable violation messages for a
// single FeatureFlag against the given FlagPolicy.
func (r *FlagPolicyReconciler) checkViolations(policy *v1alpha1.FlagPolicy, flag *v1alpha1.FeatureFlag) []string {
	var msgs []string

	if policy.Spec.RequireOwner && flag.Spec.Owner == "" {
		msgs = append(msgs, "missing owner")
	}

	if policy.Spec.RequireTags && len(flag.Spec.Tags) == 0 {
		msgs = append(msgs, "missing tags")
	}

	if policy.Spec.MaxBlastRadiusPct > 0 {
		for env, envSpec := range flag.Spec.Environments {
			if envSpec.RolloutPct > policy.Spec.MaxBlastRadiusPct {
				msgs = append(msgs, fmt.Sprintf(
					"env %q rolloutPct %d exceeds maxBlastRadiusPct %d",
					env, envSpec.RolloutPct, policy.Spec.MaxBlastRadiusPct,
				))
			}
		}
	}

	return msgs
}

// SetupWithManager registers the FlagPolicyReconciler with the Manager.
func (r *FlagPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.FlagPolicy{}).
		Complete(r)
}

// Package controller implements the Tombstone Kubernetes operator reconcile loops.
// The FeatureFlagReconciler watches FeatureFlag CRs and mirrors them to the
// Tombstone flag-api, keeping Kubernetes as the GitOps source of truth.
package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"go.uber.org/zap"

	v1alpha1 "github.com/tombstone-io/tombstone/services/tombstone-operator/api/v1alpha1"
	"github.com/tombstone-io/tombstone/services/tombstone-operator/internal/httpclient"
)

const (
	// conditionTypeSynced is the Condition type set when a flag is in sync
	// with the Tombstone API.
	conditionTypeSynced = "Synced"

	// requeueOnError defines the back-off period after a failed sync.
	requeueOnError = 30 * time.Second

	// requeueOnSuccess defines the steady-state reconcile interval.
	requeueOnSuccess = 5 * time.Minute
)

// flagCreateRequest is the payload sent to POST /api/v1/flags.
type flagCreateRequest struct {
	Key         string `json:"key"`
	Type        string `json:"type,omitempty"`
	SafeDefault string `json:"safeDefault,omitempty"`
	Owner       string `json:"owner,omitempty"`
}

// flagEnvUpdateRequest is the payload sent to PATCH /api/v1/flags/{key}/environments/{env}.
type flagEnvUpdateRequest struct {
	Enabled    bool `json:"enabled"`
	RolloutPct int  `json:"rolloutPct"`
}

// flagAPIResponse is the partial response from GET /api/v1/flags/{key}.
type flagAPIResponse struct {
	ID  string `json:"id"`
	Key string `json:"key"`
}

// FeatureFlagReconciler reconciles FeatureFlag objects against the Tombstone
// flag-api. It is the core controller that implements the GitOps loop:
//
//	K8s FeatureFlag CR → reconciler → Tombstone flag-api (REST)
//
// The reconciler is idempotent: running it multiple times produces the same
// result as running it once.
type FeatureFlagReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Logger *zap.Logger

	// APIBase is the base URL of the Tombstone flag-api,
	// e.g. "http://flag-api:8081". Sourced from TOMBSTONE_API_URL.
	APIBase string

	// Token is the Bearer token for flag-api authentication.
	// Sourced from TOMBSTONE_API_TOKEN.
	Token string

	// resilientHTTP wraps the HTTP client used for API calls in a
	// retry+circuit-breaker pipeline. Defaults are set in
	// NewFeatureFlagReconciler; inject a custom httpClient via
	// newFeatureFlagReconcilerWithClient in tests.
	resilientHTTP *httpclient.ResilientClient
}

// NewFeatureFlagReconciler constructs a reconciler with a sensible default
// HTTP client (15 s per-attempt timeout, no redirects on PATCH) wrapped in
// failsafe-go's retry+circuit-breaker pipeline via httpclient.DefaultConfig().
// The operator's reconcile loop already has its own back-off (requeueOnError/
// requeueOnSuccess), so this uses the platform default rather than a
// tightened/loosened deviation.
func NewFeatureFlagReconciler(
	c client.Client,
	scheme *runtime.Scheme,
	logger *zap.Logger,
	apiBase, token string,
) *FeatureFlagReconciler {
	return newFeatureFlagReconcilerWithClient(c, scheme, logger, apiBase, token, nil)
}

// newFeatureFlagReconcilerWithClient allows tests to inject a custom
// *http.Client (e.g. one pointed at an httptest.Server) while still going
// through the resilient retry+circuit-breaker pipeline.
func newFeatureFlagReconcilerWithClient(
	c client.Client,
	scheme *runtime.Scheme,
	logger *zap.Logger,
	apiBase, token string,
	rawClient *http.Client,
) *FeatureFlagReconciler {
	if rawClient == nil {
		rawClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &FeatureFlagReconciler{
		Client:        c,
		Scheme:        scheme,
		Logger:        logger,
		APIBase:       apiBase,
		Token:         token,
		resilientHTTP: httpclient.NewResilientClient(httpclient.DefaultConfig(), rawClient, logger),
	}
}

// Reconcile is the main reconcile loop. It is called by controller-runtime
// whenever a FeatureFlag resource is created, updated, or deleted.
//
// Contract:
//   - On successful sync: status.phase=Synced, status.lastSynced=now
//   - On error:           status.phase=Error,  requeue after requeueOnError
//   - On not-found:       return nil (resource was deleted; no-op)
func (r *FeatureFlagReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Logger.With(
		zap.String("namespace", req.Namespace),
		zap.String("name", req.Name),
	)

	// Fetch the FeatureFlag resource.
	var flag v1alpha1.FeatureFlag
	if err := r.Get(ctx, req.NamespacedName, &flag); err != nil {
		// Not found — resource was deleted. Nothing to reconcile.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log.Info("reconciling FeatureFlag", zap.String("key", flag.Spec.Key))

	// Sync the flag to the Tombstone API.
	flagID, err := r.syncFlag(ctx, &flag)
	if err != nil {
		log.Error("sync failed", zap.String("key", flag.Spec.Key), zap.Error(err))

		// Update status to Error.
		updated := flag.DeepCopy()
		updated.Status.Phase = "Error"
		setCondition(&updated.Status.Conditions, metav1.Condition{
			Type:               conditionTypeSynced,
			Status:             metav1.ConditionFalse,
			Reason:             "SyncFailed",
			Message:            err.Error(),
			ObservedGeneration: flag.Generation,
		})
		if patchErr := r.Status().Update(ctx, updated); patchErr != nil {
			log.Warn("failed to update status after sync error", zap.Error(patchErr))
		}
		return ctrl.Result{RequeueAfter: requeueOnError}, nil
	}

	// Update status to Synced.
	updated := flag.DeepCopy()
	now := metav1.Now()
	updated.Status.Phase = "Synced"
	updated.Status.LastSynced = &now
	if flagID != "" {
		updated.Status.FlagID = flagID
	}
	setCondition(&updated.Status.Conditions, metav1.Condition{
		Type:               conditionTypeSynced,
		Status:             metav1.ConditionTrue,
		Reason:             "SyncSucceeded",
		Message:            fmt.Sprintf("flag %q synced at %s", flag.Spec.Key, now.UTC().Format(time.RFC3339)),
		ObservedGeneration: flag.Generation,
	})
	if patchErr := r.Status().Update(ctx, updated); patchErr != nil {
		log.Warn("failed to update status after successful sync", zap.Error(patchErr))
	}

	log.Info("FeatureFlag synced successfully",
		zap.String("key", flag.Spec.Key),
		zap.String("flagId", updated.Status.FlagID),
	)
	return ctrl.Result{RequeueAfter: requeueOnSuccess}, nil
}

// syncFlag creates or updates the flag in the Tombstone API, then syncs each
// environment configuration. Returns the flag UUID assigned by the API.
func (r *FeatureFlagReconciler) syncFlag(ctx context.Context, flag *v1alpha1.FeatureFlag) (string, error) {
	// Check whether the flag already exists.
	existing, err := r.getFlag(ctx, flag.Spec.Key)
	if err != nil {
		return "", fmt.Errorf("checking flag existence: %w", err)
	}

	var flagID string
	if existing == nil {
		// Flag does not exist — create it.
		created, createErr := r.createFlag(ctx, flag)
		if createErr != nil {
			return "", fmt.Errorf("creating flag: %w", createErr)
		}
		flagID = created.ID
	} else {
		flagID = existing.ID
		// Flag exists — update its metadata.
		if updateErr := r.updateFlag(ctx, flag); updateErr != nil {
			return flagID, fmt.Errorf("updating flag: %w", updateErr)
		}
	}

	// Sync per-environment configuration.
	for env, envSpec := range flag.Spec.Environments {
		if syncErr := r.syncFlagEnvironment(ctx, flag.Spec.Key, env, envSpec); syncErr != nil {
			return flagID, fmt.Errorf("syncing environment %q: %w", env, syncErr)
		}
	}

	return flagID, nil
}

// getFlag fetches the flag from the API. Returns nil if the flag does not exist
// (404), or an error on any other failure.
func (r *FeatureFlagReconciler) getFlag(ctx context.Context, key string) (*flagAPIResponse, error) {
	url := fmt.Sprintf("%s/api/v1/flags/%s", r.APIBase, key)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+r.Token)
	req.Header.Set("Accept", "application/json")

	resp, err := r.resilientHTTP.Do(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GET %s returned %d: %s", url, resp.StatusCode, string(body))
	}

	var result flagAPIResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding GET response: %w", err)
	}
	return &result, nil
}

// createFlag creates a new flag in the Tombstone API via POST /api/v1/flags.
func (r *FeatureFlagReconciler) createFlag(ctx context.Context, flag *v1alpha1.FeatureFlag) (*flagAPIResponse, error) {
	payload := flagCreateRequest{
		Key:         flag.Spec.Key,
		Type:        flag.Spec.Type,
		SafeDefault: flag.Spec.SafeDefault,
		Owner:       flag.Spec.Owner,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("%s/api/v1/flags", r.APIBase)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+r.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := r.resilientHTTP.Do(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("POST %s returned %d: %s", url, resp.StatusCode, string(respBody))
	}

	var result flagAPIResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding POST response: %w", err)
	}
	return &result, nil
}

// updateFlag patches the flag's metadata in the Tombstone API.
func (r *FeatureFlagReconciler) updateFlag(ctx context.Context, flag *v1alpha1.FeatureFlag) error {
	payload := flagCreateRequest{
		Key:         flag.Spec.Key,
		Type:        flag.Spec.Type,
		SafeDefault: flag.Spec.SafeDefault,
		Owner:       flag.Spec.Owner,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	url := fmt.Sprintf("%s/api/v1/flags/%s", r.APIBase, flag.Spec.Key)
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+r.Token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.resilientHTTP.Do(ctx, req)
	if err != nil {
		return fmt.Errorf("PATCH %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("PATCH %s returned %d: %s", url, resp.StatusCode, string(respBody))
	}
	return nil
}

// syncFlagEnvironment patches a single environment's configuration.
func (r *FeatureFlagReconciler) syncFlagEnvironment(
	ctx context.Context,
	key, env string,
	envSpec v1alpha1.FlagEnvSpec,
) error {
	payload := flagEnvUpdateRequest{
		Enabled:    envSpec.Enabled,
		RolloutPct: envSpec.RolloutPct,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	url := fmt.Sprintf("%s/api/v1/flags/%s/environments/%s", r.APIBase, key, env)
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+r.Token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.resilientHTTP.Do(ctx, req)
	if err != nil {
		return fmt.Errorf("PATCH %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("PATCH %s returned %d: %s", url, resp.StatusCode, string(respBody))
	}
	return nil
}

// SetupWithManager registers the controller with the Manager.
// It watches FeatureFlag resources and skips reconcile when only the status
// sub-resource changes (avoids infinite loops).
func (r *FeatureFlagReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.FeatureFlag{}).
		WithEventFilter(predicate.GenerationChangedPredicate{}).
		Complete(r)
}

// setCondition upserts a Condition into the conditions slice, updating the
// LastTransitionTime only when the Status changes.
func setCondition(conditions *[]metav1.Condition, newCond metav1.Condition) {
	now := metav1.Now()
	newCond.LastTransitionTime = now

	for i, c := range *conditions {
		if c.Type == newCond.Type {
			if c.Status != newCond.Status {
				(*conditions)[i] = newCond
			} else {
				// Status unchanged — preserve transition time, update message/reason.
				(*conditions)[i].Reason = newCond.Reason
				(*conditions)[i].Message = newCond.Message
				(*conditions)[i].ObservedGeneration = newCond.ObservedGeneration
			}
			return
		}
	}
	*conditions = append(*conditions, newCond)
}

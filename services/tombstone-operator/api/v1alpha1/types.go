// Package v1alpha1 contains API types for the Tombstone Kubernetes operator.
// These CRDs mirror the YAML GitOps schema for declarative flag management.
package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// FeatureFlag is the Schema for the featureflags API.
// It mirrors the YAML GitOps schema, enabling declarative flag management
// via Kubernetes custom resources.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=ff
// +kubebuilder:printcolumn:name="Key",type=string,JSONPath=`.spec.key`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="LastSynced",type=string,JSONPath=`.status.lastSynced`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type FeatureFlag struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   FeatureFlagSpec   `json:"spec,omitempty"`
	Status FeatureFlagStatus `json:"status,omitempty"`
}

// FeatureFlagSpec defines the desired state of a FeatureFlag.
type FeatureFlagSpec struct {
	// Key is the unique identifier for the flag in the Tombstone API.
	// Immutable after creation.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^[a-z][a-z0-9_.-]{0,253}$`
	Key string `json:"key"`

	// Type controls the evaluation engine's value type.
	// +kubebuilder:validation:Enum=BOOLEAN;STRING;INTEGER;FLOAT;JSON
	// +kubebuilder:default=BOOLEAN
	Type string `json:"type,omitempty"`

	// SafeDefault is the fallback value when evaluation fails or the flag
	// cannot be reached. Always served to callers on error.
	// +kubebuilder:default="false"
	SafeDefault string `json:"safeDefault,omitempty"`

	// Owner is the team or individual responsible for this flag.
	Owner string `json:"owner,omitempty"`

	// Tags are free-form labels for search and grouping.
	// +kubebuilder:validation:MaxItems=20
	Tags []string `json:"tags,omitempty"`

	// Environments maps environment names (e.g. "production", "staging") to
	// per-environment rollout configuration. The operator syncs each entry to
	// the flag-api via PATCH /api/v1/flags/{key}/environments/{env}.
	Environments map[string]FlagEnvSpec `json:"environments,omitempty"`
}

// FlagEnvSpec defines per-environment rollout configuration.
type FlagEnvSpec struct {
	// Enabled controls whether the flag is active in this environment.
	// +kubebuilder:default=false
	Enabled bool `json:"enabled"`

	// RolloutPct is the percentage of traffic (0-100) that receives the
	// flag's active value. 0 means disabled for all users; 100 means all
	// users receive the active value.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	// +kubebuilder:default=0
	RolloutPct int `json:"rolloutPct"`
}

// FeatureFlagStatus reflects the observed state of the FeatureFlag after the
// last reconcile loop.
type FeatureFlagStatus struct {
	// Conditions represent the latest available observations.
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// LastSynced is the timestamp of the last successful sync to the flag-api.
	LastSynced *metav1.Time `json:"lastSynced,omitempty"`

	// FlagID is the UUID assigned by the flag-api after the first successful
	// create. Used for PATCH calls.
	FlagID string `json:"flagId,omitempty"`

	// Phase summarises the reconcile outcome.
	// +kubebuilder:validation:Enum=Pending;Synced;Error
	// +kubebuilder:default=Pending
	Phase string `json:"phase,omitempty"`
}

// FeatureFlagList contains a list of FeatureFlag resources.
//
// +kubebuilder:object:root=true
type FeatureFlagList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FeatureFlag `json:"items"`
}

// FlagEnvironment is the Schema for the flaginvironments API.
// Represents a named environment (production, staging, canary) and its
// global default behaviour within the Tombstone flag system.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=fenv
// +kubebuilder:printcolumn:name="Env",type=string,JSONPath=`.spec.name`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
type FlagEnvironment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   FlagEnvironmentSpec   `json:"spec,omitempty"`
	Status FlagEnvironmentStatus `json:"status,omitempty"`
}

// FlagEnvironmentSpec defines the desired configuration for a named environment.
type FlagEnvironmentSpec struct {
	// Name is the canonical environment identifier (e.g. "production").
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// DefaultRolloutPct is the rollout percentage applied to flags that list
	// this environment without an explicit rolloutPct override.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	// +kubebuilder:default=0
	DefaultRolloutPct int `json:"defaultRolloutPct,omitempty"`

	// Safeguards contains environment-level blast-radius controls.
	Safeguards *EnvironmentSafeguards `json:"safeguards,omitempty"`
}

// EnvironmentSafeguards defines blast-radius gating thresholds for an environment.
type EnvironmentSafeguards struct {
	// MaxRolloutPct is the ceiling for any single flag rollout in this
	// environment. The operator rejects specs that exceed this value.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	// +kubebuilder:default=100
	MaxRolloutPct int `json:"maxRolloutPct,omitempty"`

	// RequireApproval forces the operator to set the flag to Pending instead
	// of Synced until an out-of-band approval is recorded.
	RequireApproval bool `json:"requireApproval,omitempty"`
}

// FlagEnvironmentStatus reflects the observed state of the FlagEnvironment.
type FlagEnvironmentStatus struct {
	// Conditions summarise reconcile outcomes.
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Phase is the latest reconcile outcome.
	// +kubebuilder:validation:Enum=Pending;Synced;Error
	// +kubebuilder:default=Pending
	Phase string `json:"phase,omitempty"`

	// EnvID is the UUID assigned by the flag-api.
	EnvID string `json:"envId,omitempty"`
}

// FlagEnvironmentList contains a list of FlagEnvironment resources.
//
// +kubebuilder:object:root=true
type FlagEnvironmentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FlagEnvironment `json:"items"`
}

// FlagPolicy is the Schema for the flagpolicies API.
// Defines governance policies (blast-radius limits, approval gates,
// stale-flag TTLs) that the operator enforces at admission time.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=fpol
type FlagPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   FlagPolicySpec   `json:"spec,omitempty"`
	Status FlagPolicyStatus `json:"status,omitempty"`
}

// FlagPolicySpec defines the governance rules for a set of flags.
type FlagPolicySpec struct {
	// Selector matches FeatureFlag resources by label. An empty selector
	// matches all flags in the namespace.
	Selector *metav1.LabelSelector `json:"selector,omitempty"`

	// MaxStaleDays is the number of days a flag can remain unchanged before
	// the operator marks it for review / tombstoning.
	// +kubebuilder:validation:Minimum=1
	MaxStaleDays int `json:"maxStaleDays,omitempty"`

	// MaxBlastRadiusPct is the maximum rollout percentage allowed by this
	// policy. The operator blocks reconcile and sets status.phase=Error if
	// any matched flag exceeds this threshold.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	MaxBlastRadiusPct int `json:"maxBlastRadiusPct,omitempty"`

	// RequireOwner ensures every matched flag has a non-empty spec.owner.
	// +kubebuilder:default=true
	RequireOwner bool `json:"requireOwner,omitempty"`

	// RequireTags ensures every matched flag has at least one tag.
	// +kubebuilder:default=false
	RequireTags bool `json:"requireTags,omitempty"`
}

// FlagPolicyStatus reflects the observed enforcement state.
type FlagPolicyStatus struct {
	// ViolatingFlags is the list of FeatureFlag names currently in violation.
	ViolatingFlags []string `json:"violatingFlags,omitempty"`

	// LastEvaluated is the timestamp of the last policy evaluation pass.
	LastEvaluated *metav1.Time `json:"lastEvaluated,omitempty"`

	// Phase is the overall policy health.
	// +kubebuilder:validation:Enum=Compliant;Violation;Error
	// +kubebuilder:default=Compliant
	Phase string `json:"phase,omitempty"`
}

// FlagPolicyList contains a list of FlagPolicy resources.
//
// +kubebuilder:object:root=true
type FlagPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FlagPolicy `json:"items"`
}

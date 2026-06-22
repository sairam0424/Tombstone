// Package v1alpha1 registers Tombstone CRD types with the Kubernetes scheme.
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

// GroupVersion is the group version used to register these objects.
var GroupVersion = schema.GroupVersion{Group: "tombstone.io", Version: "v1alpha1"}

// SchemeBuilder is used to add functions to the scheme.
var SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

// AddToScheme adds the types in this group-version to the given scheme.
var AddToScheme = SchemeBuilder.AddToScheme

func init() {
	SchemeBuilder.Register(
		&FeatureFlag{}, &FeatureFlagList{},
		&FlagEnvironment{}, &FlagEnvironmentList{},
		&FlagPolicy{}, &FlagPolicyList{},
	)
}

// FeatureFlag deep copy methods.

// DeepCopyInto copies all properties of this object into another object of the
// same type that is provided as a pointer.
func (in *FeatureFlag) DeepCopyInto(out *FeatureFlag) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

// DeepCopy creates a deep copy of the FeatureFlag.
func (in *FeatureFlag) DeepCopy() *FeatureFlag {
	if in == nil {
		return nil
	}
	out := new(FeatureFlag)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject implements runtime.Object.
func (in *FeatureFlag) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto for FeatureFlagSpec.
func (in *FeatureFlagSpec) DeepCopyInto(out *FeatureFlagSpec) {
	*out = *in
	if in.Tags != nil {
		out.Tags = make([]string, len(in.Tags))
		copy(out.Tags, in.Tags)
	}
	if in.Environments != nil {
		out.Environments = make(map[string]FlagEnvSpec, len(in.Environments))
		for k, v := range in.Environments {
			out.Environments[k] = v
		}
	}
}

// DeepCopyInto for FeatureFlagStatus.
func (in *FeatureFlagStatus) DeepCopyInto(out *FeatureFlagStatus) {
	*out = *in
	if in.Conditions != nil {
		out.Conditions = make([]metav1.Condition, len(in.Conditions))
		for i := range in.Conditions {
			in.Conditions[i].DeepCopyInto(&out.Conditions[i])
		}
	}
	if in.LastSynced != nil {
		t := *in.LastSynced
		out.LastSynced = &t
	}
}

// DeepCopyInto for FeatureFlagList.
func (in *FeatureFlagList) DeepCopyInto(out *FeatureFlagList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]FeatureFlag, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

// DeepCopy creates a deep copy of FeatureFlagList.
func (in *FeatureFlagList) DeepCopy() *FeatureFlagList {
	if in == nil {
		return nil
	}
	out := new(FeatureFlagList)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject implements runtime.Object.
func (in *FeatureFlagList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// FlagEnvironment deep copy methods.

func (in *FlagEnvironment) DeepCopyInto(out *FlagEnvironment) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

func (in *FlagEnvironment) DeepCopy() *FlagEnvironment {
	if in == nil {
		return nil
	}
	out := new(FlagEnvironment)
	in.DeepCopyInto(out)
	return out
}

func (in *FlagEnvironment) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *FlagEnvironmentSpec) DeepCopyInto(out *FlagEnvironmentSpec) {
	*out = *in
	if in.Safeguards != nil {
		s := *in.Safeguards
		out.Safeguards = &s
	}
}

func (in *FlagEnvironmentStatus) DeepCopyInto(out *FlagEnvironmentStatus) {
	*out = *in
	if in.Conditions != nil {
		out.Conditions = make([]metav1.Condition, len(in.Conditions))
		for i := range in.Conditions {
			in.Conditions[i].DeepCopyInto(&out.Conditions[i])
		}
	}
}

func (in *FlagEnvironmentList) DeepCopyInto(out *FlagEnvironmentList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]FlagEnvironment, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

func (in *FlagEnvironmentList) DeepCopy() *FlagEnvironmentList {
	if in == nil {
		return nil
	}
	out := new(FlagEnvironmentList)
	in.DeepCopyInto(out)
	return out
}

func (in *FlagEnvironmentList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// FlagPolicy deep copy methods.

func (in *FlagPolicy) DeepCopyInto(out *FlagPolicy) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

func (in *FlagPolicy) DeepCopy() *FlagPolicy {
	if in == nil {
		return nil
	}
	out := new(FlagPolicy)
	in.DeepCopyInto(out)
	return out
}

func (in *FlagPolicy) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *FlagPolicySpec) DeepCopyInto(out *FlagPolicySpec) {
	*out = *in
	if in.Selector != nil {
		sel := in.Selector.DeepCopy()
		out.Selector = sel
	}
}

func (in *FlagPolicyStatus) DeepCopyInto(out *FlagPolicyStatus) {
	*out = *in
	if in.ViolatingFlags != nil {
		out.ViolatingFlags = make([]string, len(in.ViolatingFlags))
		copy(out.ViolatingFlags, in.ViolatingFlags)
	}
	if in.LastEvaluated != nil {
		t := *in.LastEvaluated
		out.LastEvaluated = &t
	}
}

func (in *FlagPolicyList) DeepCopyInto(out *FlagPolicyList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]FlagPolicy, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

func (in *FlagPolicyList) DeepCopy() *FlagPolicyList {
	if in == nil {
		return nil
	}
	out := new(FlagPolicyList)
	in.DeepCopyInto(out)
	return out
}

func (in *FlagPolicyList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

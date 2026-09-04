// Package v1alpha1 contains API Schema definitions for the gateway v1alpha1 API group.
// +kubebuilder:object:generate=true
// +groupName=gateway.rsync.zjusct.io
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	// GroupVersion is group version used to register these objects.
	GroupVersion = schema.GroupVersion{Group: "gateway.rsync.zjusct.io", Version: "v1alpha1"}

	// SchemeBuilder registers the gateway.rsync.zjusct.io types with a
	// runtime scheme.
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

	// AddToScheme adds the types in this group-version to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

// addKnownTypes registers RsyncRoute and GatewayConfig with the scheme.
func addKnownTypes(s *runtime.Scheme) error {
	s.AddKnownTypes(GroupVersion,
		&RsyncRoute{}, &RsyncRouteList{},
		&GatewayConfig{}, &GatewayConfigList{},
	)
	metav1.AddToGroupVersion(s, GroupVersion)
	return nil
}

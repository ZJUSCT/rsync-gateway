package gateway

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	v1alpha1 "github.com/ustclug/rsync-proxy/api/v1alpha1"
)

// StableCondition returns cond with the LastTransitionTime taken from an
// existing condition of the same type and status, so rebuilt statuses stay
// byte-identical when nothing changed. When the type/status pair is new,
// LastTransitionTime is set to now.
func StableCondition(existing []metav1.Condition, cond metav1.Condition) metav1.Condition {
	for _, c := range existing {
		if c.Type == cond.Type && c.Status == cond.Status {
			cond.LastTransitionTime = c.LastTransitionTime
			return cond
		}
	}
	cond.LastTransitionTime = metav1.Now()
	return cond
}

// MergeConditions merges want into cur by condition type: existing entries
// of the same type are replaced, new ones appended. The input slices are
// not modified.
func MergeConditions(cur, want []metav1.Condition) []metav1.Condition {
	out := make([]metav1.Condition, 0, len(cur)+len(want))
	for _, c := range cur {
		replaced := false
		for _, w := range want {
			if w.Type == c.Type {
				out = append(out, w)
				replaced = true
				break
			}
		}
		if !replaced {
			out = append(out, c)
		}
	}
	for _, w := range want {
		found := false
		for _, c := range out {
			if c.Type == w.Type {
				found = true
				break
			}
		}
		if !found {
			out = append(out, w)
		}
	}
	return out
}

// conditionContentEqual compares two conditions ignoring LastTransitionTime.
func conditionContentEqual(a, b metav1.Condition) bool {
	return a.Type == b.Type &&
		a.Status == b.Status &&
		a.Reason == b.Reason &&
		a.Message == b.Message &&
		a.ObservedGeneration == b.ObservedGeneration
}

// ConditionsEqual reports whether cur already carries exactly the content
// of want (same order, same types), ignoring LastTransitionTime.
func ConditionsEqual(cur, want []metav1.Condition) bool {
	if len(cur) != len(want) {
		return false
	}
	for i := range want {
		if !conditionContentEqual(cur[i], want[i]) {
			return false
		}
	}
	return true
}

// ListenerStatusesEqual compares listener status lists semantically,
// ignoring LastTransitionTime of conditions.
func ListenerStatusesEqual(cur, want []gatewayv1.ListenerStatus) bool {
	if len(cur) != len(want) {
		return false
	}
	for i := range want {
		if cur[i].Name != want[i].Name || cur[i].AttachedRoutes != want[i].AttachedRoutes {
			return false
		}
		if !routeGroupKindsEqual(cur[i].SupportedKinds, want[i].SupportedKinds) {
			return false
		}
		if !ConditionsEqual(cur[i].Conditions, want[i].Conditions) {
			return false
		}
	}
	return true
}

func routeGroupKindsEqual(cur, want []gatewayv1.RouteGroupKind) bool {
	if len(cur) != len(want) {
		return false
	}
	for i := range want {
		var cg, wg string
		var ck, wk string
		if cur[i].Group != nil {
			cg = string(*cur[i].Group)
		}
		if want[i].Group != nil {
			wg = string(*want[i].Group)
		}
		if cur[i].Kind != "" {
			ck = string(cur[i].Kind)
		}
		if want[i].Kind != "" {
			wk = string(want[i].Kind)
		}
		if cg != wg || ck != wk {
			return false
		}
	}
	return true
}

// ParentStatusesEqual compares route parents status lists semantically,
// ignoring LastTransitionTime of conditions.
func ParentStatusesEqual(cur, want []gatewayv1.RouteParentStatus) bool {
	if len(cur) != len(want) {
		return false
	}
	for i := range want {
		if cur[i].ControllerName != want[i].ControllerName {
			return false
		}
		if !parentRefsEqual(cur[i].ParentRef, want[i].ParentRef) {
			return false
		}
		if !ConditionsEqual(cur[i].Conditions, want[i].Conditions) {
			return false
		}
	}
	return true
}

// FindParentConditions collects the previously written Accepted conditions
// for our own entries, keyed by the index of the matching parent result.
// It backs the change-detection for route events.
func FindParentConditions(existing []gatewayv1.RouteParentStatus, results []ParentResult) map[int]metav1.Condition {
	out := make(map[int]metav1.Condition, len(results))
	for i, res := range results {
		for _, p := range existing {
			if p.ControllerName != v1alpha1.ControllerName {
				continue
			}
			if !parentRefsEqual(p.ParentRef, res.ParentRef) {
				continue
			}
			for _, c := range p.Conditions {
				if c.Type == string(gatewayv1.RouteConditionAccepted) {
					out[i] = c
				}
			}
			break
		}
	}
	return out
}

// ParentRefString renders a canonical, readable form of a ParentReference
// (used in events and messages).
func ParentRefString(ref gatewayv1.ParentReference) string {
	group := "gateway.networking.k8s.io"
	if ref.Group != nil {
		group = string(*ref.Group)
	}
	kind := "Gateway"
	if ref.Kind != nil {
		kind = string(*ref.Kind)
	}
	out := fmtParentRef(group, kind, ref)
	return out
}

func fmtParentRef(group, kind string, ref gatewayv1.ParentReference) string {
	if ref.Namespace != nil && *ref.Namespace != "" {
		return kind + "." + group + "/" + string(*ref.Namespace) + "/" + string(ref.Name)
	}
	return kind + "." + group + "/" + string(ref.Name)
}

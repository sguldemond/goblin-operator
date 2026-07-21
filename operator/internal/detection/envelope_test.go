package detection

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestInEnvelope(t *testing.T) {
	cases := []struct {
		name string
		gvk  schema.GroupVersionKind
		want bool
	}{
		{"pod", schema.GroupVersionKind{Version: "v1", Kind: "Pod"}, true},
		{"deployment", schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, true},
		{"replicaset", schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "ReplicaSet"}, true},
		// Readable but not targetable: the operator holds no standing grant.
		{"secret", schema.GroupVersionKind{Version: "v1", Kind: "Secret"}, false},
		{"statefulset", schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "StatefulSet"}, false},
		// Group matters: a Pod in some other group is not our Pod.
		{"pod in wrong group", schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Pod"}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := InEnvelope(tc.gvk); got != tc.want {
				t.Errorf("InEnvelope(%s) = %v, want %v", tc.gvk, got, tc.want)
			}
		})
	}
}

package detection

import (
	"slices"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

//go:generate go run ./internal/rbacgen

// TargetKind is one kind an IncidentPolicy may target.
//
// Resource is the plural RBAC resource name. It is carried explicitly rather
// than derived from Kind because pluralisation is not mechanical (Endpoints,
// NetworkPolicies, Ingresses) and a wrong guess produces a ClusterRole that
// grants nothing, which fails as a 403 at informer start rather than at build
// time.
type TargetKind struct {
	GVK      schema.GroupVersionKind
	Resource string
}

// Envelope is the set of kinds an IncidentPolicy may target.
//
// It is the single source of truth for two things that must agree: the watches
// the operator starts (cmd/main.go ranges over it) and the read permissions the
// operator holds (zz_generated_rbac.go is generated from it — run `go generate
// ./...` after editing, then `make manifests`).
//
// This is the *detection* envelope: kinds that can be targeted. It is not the
// scout's read envelope (events, nodes, quotas), which is a separate list in
// config/rbac/scout_role.yaml.
var Envelope = []TargetKind{
	{GVK: schema.GroupVersionKind{Version: "v1", Kind: "Pod"}, Resource: "pods"},
	{GVK: schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, Resource: "deployments"},
	{GVK: schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "ReplicaSet"}, Resource: "replicasets"},
}

// GVKs returns just the group-version-kinds, for callers setting up watches.
func GVKs() []schema.GroupVersionKind {
	out := make([]schema.GroupVersionKind, 0, len(Envelope))
	for _, t := range Envelope {
		out = append(out, t.GVK)
	}
	return out
}

// InEnvelope reports whether policies may target gvk.
func InEnvelope(gvk schema.GroupVersionKind) bool {
	return slices.Contains(GVKs(), gvk)
}

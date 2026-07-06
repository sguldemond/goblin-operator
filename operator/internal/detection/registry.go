package detection

import (
	"sync"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// CompiledPolicy is a validated IncidentPolicy ready to evaluate.
type CompiledPolicy struct {
	Name    string
	Trigger string
	Matcher *Matcher
}

// Registry maps target GVK -> active compiled policies, keyed by policy name.
type Registry struct {
	mu    sync.RWMutex
	byGVK map[schema.GroupVersionKind]map[string]CompiledPolicy
}

func NewRegistry() *Registry {
	return &Registry{byGVK: map[schema.GroupVersionKind]map[string]CompiledPolicy{}}
}

// Set upserts a policy under its GVK. If the policy's GVK changed since a prior
// Set, the caller must Delete first; in practice PolicyReconciler Deletes then Sets.
func (r *Registry) Set(name string, gvk schema.GroupVersionKind, p CompiledPolicy) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byGVK[gvk] == nil {
		r.byGVK[gvk] = map[string]CompiledPolicy{}
	}
	r.byGVK[gvk][name] = p
}

// Delete removes a policy by name from every GVK bucket.
func (r *Registry) Delete(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for gvk, m := range r.byGVK {
		delete(m, name)
		if len(m) == 0 {
			delete(r.byGVK, gvk)
		}
	}
}

// ForGVK returns a snapshot slice of active policies for a GVK.
func (r *Registry) ForGVK(gvk schema.GroupVersionKind) []CompiledPolicy {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]CompiledPolicy, 0, len(r.byGVK[gvk]))
	for _, p := range r.byGVK[gvk] {
		out = append(out, p)
	}
	return out
}

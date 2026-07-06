package detection

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestRegistrySetForGVKDelete(t *testing.T) {
	r := NewRegistry()
	pod := schema.GroupVersionKind{Version: "v1", Kind: "Pod"}
	m, _ := Compile("true")
	r.Set("p1", pod, CompiledPolicy{Name: "p1", Trigger: "OOMKilled", Matcher: m})
	if got := r.ForGVK(pod); len(got) != 1 || got[0].Trigger != "OOMKilled" {
		t.Fatalf("expected 1 policy, got %#v", got)
	}
	// Set is upsert by name.
	r.Set("p1", pod, CompiledPolicy{Name: "p1", Trigger: "Changed", Matcher: m})
	if got := r.ForGVK(pod); len(got) != 1 || got[0].Trigger != "Changed" {
		t.Fatalf("expected upsert, got %#v", got)
	}
	r.Delete("p1")
	if got := r.ForGVK(pod); len(got) != 0 {
		t.Fatalf("expected 0 after delete, got %#v", got)
	}
}

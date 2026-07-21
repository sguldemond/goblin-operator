package detection

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// The generated markers are what give the operator permission to watch the
// envelope. If someone edits Envelope and forgets `go generate`, the operator
// still compiles and still starts a watch — it just gets a 403 at runtime. This
// catches that at build time instead.
func TestGeneratedRBACMatchesEnvelope(t *testing.T) {
	raw, err := os.ReadFile("zz_generated_rbac.go")
	if err != nil {
		t.Fatalf("reading generated markers: %v", err)
	}
	got := string(raw)

	for _, target := range Envelope {
		group := target.GVK.Group
		if group == "" {
			group = `""`
		}
		prefix := fmt.Sprintf("+kubebuilder:rbac:groups=%s,resources=", group)

		var found bool
		for _, line := range strings.Split(got, "\n") {
			idx := strings.Index(line, prefix)
			if idx < 0 {
				continue
			}
			rest := line[idx+len(prefix):]
			resources, _, _ := strings.Cut(rest, ",")
			for _, r := range strings.Split(resources, ";") {
				if r == target.Resource {
					found = true
				}
			}
		}
		if !found {
			t.Errorf("no generated RBAC marker grants %s in group %s; run: go generate ./...",
				target.Resource, group)
		}
	}
}

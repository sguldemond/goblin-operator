package scout

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"

	"github.com/sguldemond/goblin/agent/internal/tools"
)

const (
	phaseQueued    = "Queued"
	phaseAssessing = "Assessing"
)

// IncidentWatcher surfaces incidents that are ready to be worked.
//
// The operator moves an incident to Queued once it has granted the scout's
// permissions, so Queued is the only phase worth watching: anything earlier is
// not ready, anything later already belongs to a scout.
type IncidentWatcher struct {
	dynCli    dynamic.Interface
	namespace string // empty means all namespaces
}

func NewIncidentWatcher(dynCli dynamic.Interface, namespace string) *IncidentWatcher {
	return &IncidentWatcher{dynCli: dynCli, namespace: namespace}
}

func (w *IncidentWatcher) resource() dynamic.ResourceInterface {
	ri := w.dynCli.Resource(tools.IncidentGVR)
	if w.namespace == "" {
		return ri
	}
	return ri.Namespace(w.namespace)
}

// Backlog returns incidents that already need attention, newest last. It covers
// restart recovery: anything left in Assessing was claimed by a previous
// process that died holding it, and nothing else will pick it up.
func (w *IncidentWatcher) Backlog(ctx context.Context) ([]*Incident, error) {
	list, err := w.resource().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing incidents: %w", err)
	}

	var out []*Incident
	for i := range list.Items {
		obj := list.Items[i]
		phase := phaseOf(obj.Object)
		if phase != phaseQueued && phase != phaseAssessing {
			continue
		}
		inc, err := incidentFromUnstructured(obj.Object)
		if err != nil {
			continue // malformed incident; nothing useful to investigate
		}
		out = append(out, inc)
	}
	return out, nil
}

// Watch streams newly queued incidents until ctx is cancelled. The channel is
// closed on return.
//
// A dropped watch is re-established rather than fatal: the scout outlives any
// single connection, and an incident missed during a reconnect is caught by the
// periodic resync in Run.
func (w *IncidentWatcher) Watch(ctx context.Context) <-chan *Incident {
	// Buffered so the watch keeps running while the scout is mid-investigation
	// and not reading. An unbuffered channel stalls the watch goroutine until
	// the conversation next reaches its select, which can be many tool calls
	// away — and siblings of the incident being worked on are exactly what
	// arrives during that gap.
	out := make(chan *Incident, 64)

	go func() {
		defer close(out)
		for {
			wi, err := w.resource().Watch(ctx, metav1.ListOptions{})
			if err != nil {
				select {
				case <-ctx.Done():
					return
				case <-time.After(5 * time.Second):
					continue
				}
			}
			w.drain(ctx, wi, out)
			if ctx.Err() != nil {
				return
			}
		}
	}()

	return out
}

func (w *IncidentWatcher) drain(ctx context.Context, wi watch.Interface, out chan<- *Incident) {
	defer wi.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-wi.ResultChan():
			if !ok {
				return // watch closed; caller re-establishes
			}
			if ev.Type != watch.Added && ev.Type != watch.Modified {
				continue
			}
			obj, ok := ev.Object.(interface{ UnstructuredContent() map[string]any })
			if !ok {
				continue
			}
			content := obj.UnstructuredContent()
			if phaseOf(content) != phaseQueued {
				continue
			}
			inc, err := incidentFromUnstructured(content)
			if err != nil {
				continue
			}
			select {
			case <-ctx.Done():
				return
			case out <- inc:
			}
		}
	}
}

// Claim marks an incident as being worked on. It is what distinguishes "nobody
// has looked at this" from "a scout has it", which matters after a restart.
func (w *IncidentWatcher) Claim(ctx context.Context, inc *Incident) error {
	_, err := tools.NewUpdateIncidentStatus(w.dynCli).
		Set(ctx, inc.Namespace, inc.IncidentName, phaseAssessing, "")
	return err
}

func phaseOf(obj map[string]any) string {
	status, _, _ := unstructuredMap(obj, "status")
	return stringField(status, "phase")
}

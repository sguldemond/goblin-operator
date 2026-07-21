package scout

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"

	"github.com/sguldemond/goblin/agent/internal/config"
	"github.com/sguldemond/goblin/agent/internal/llm"
	"github.com/sguldemond/goblin/agent/internal/messenger"
	"github.com/sguldemond/goblin/agent/internal/tools"
)

type Scout struct {
	cfg    *config.Config
	client kubernetes.Interface
	dynCli dynamic.Interface
}

func New(cfg *config.Config, restCfg *rest.Config, client kubernetes.Interface) (*Scout, error) {
	dynCli, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("building dynamic client: %w", err)
	}
	return &Scout{cfg: cfg, client: client, dynCli: dynCli}, nil
}

// resyncInterval bounds how long an incident can go unnoticed if a watch event
// is missed — during a reconnect, for instance.
const resyncInterval = 2 * time.Minute

// Run watches for incidents and works them until the context is cancelled.
//
// The scout is long-lived and holds one conversation covering every open
// incident, which is what lets it notice that several incidents share a root
// cause. It never exits on its own: an empty backlog means idle, not done.
func (s *Scout) Run(ctx context.Context) error {
	gr, err := restmapper.GetAPIGroupResources(s.client.Discovery())
	if err != nil {
		return fmt.Errorf("building REST mapper: %w", err)
	}
	mapper := restmapper.NewDiscoveryRESTMapper(gr)

	m, err := s.messenger()
	if err != nil {
		return err
	}

	var base llm.SendFunc
	switch s.cfg.Provider {
	case "openai":
		base = llm.NewOpenAI(s.cfg.APIKey)
	default:
		base = llm.NewAnthropic(s.cfg.APIKey)
	}
	send := llm.WithRetry(base, 5, func(attempt int, err error, delay time.Duration) {
		fmt.Printf("\r>> API overloaded, retrying in %s (attempt %d)...\n", delay.Round(time.Second), attempt)
	})
	fmt.Printf(">> provider: %s, model: %s\n", s.cfg.Provider, s.cfg.Model)

	watcher := NewIncidentWatcher(s.dynCli, s.cfg.WatchNamespace)

	// Anything left mid-flight belongs to a process that no longer exists. Its
	// conversation and any staged change died with it, so say so rather than
	// leaving a stale approval button that will never do anything.
	backlog, err := watcher.Backlog(ctx)
	if err != nil {
		return fmt.Errorf("reading incident backlog: %w", err)
	}
	if len(backlog) > 0 {
		m.Send(fmt.Sprintf("♻️ Scout restarted with %d open incident(s). Any earlier proposal is void — re-investigating.", len(backlog))) //nolint:errcheck
	}

	sess := &session{
		scout:   s,
		mapper:  mapper,
		send:    send,
		m:       m,
		watcher: watcher,
		open:    map[string]*Incident{},
	}
	return sess.run(ctx, backlog)
}

func (s *Scout) messenger() (messenger.Messenger, error) {
	if token := os.Getenv("TELEGRAM_BOT_TOKEN"); token != "" {
		chatID, _ := strconv.ParseInt(os.Getenv("TELEGRAM_CHAT_ID"), 10, 64)
		tg, err := messenger.NewTelegram(token, chatID)
		if err != nil {
			return nil, fmt.Errorf("telegram: %w", err)
		}
		fmt.Println(">> telegram mode")
		return tg, nil
	}
	return messenger.NewTTY(os.Stdin, os.Stdout), nil
}

// incidentFromUnstructured parses an Incident CR. The scout no longer knows
// which incident it is for at startup, so parsing is driven by whatever the
// watch delivers rather than by configuration.
func incidentFromUnstructured(obj map[string]any) (*Incident, error) {
	meta, _, _ := unstructuredMap(obj, "metadata")
	spec, _, _ := unstructuredMap(obj, "spec")
	targetRef, _, _ := unstructuredMap(spec, "targetRef")

	incident := &Incident{
		IncidentName:     stringField(meta, "name"),
		Namespace:        stringField(meta, "namespace"),
		Trigger:          stringField(spec, "trigger"),
		PolicyRef:        stringField(spec, "policyRef"),
		TargetAPIVersion: stringField(targetRef, "apiVersion"),
		TargetKind:       stringField(targetRef, "kind"),
		TargetName:       stringField(targetRef, "name"),
		TargetNamespace:  stringField(targetRef, "namespace"),
	}

	if incident.IncidentName == "" {
		return nil, fmt.Errorf("incident has no metadata.name")
	}
	if incident.TargetName == "" {
		return nil, fmt.Errorf("targetRef.name is empty in Incident %s", incident.IncidentName)
	}
	// Kind drives which context the scout gathers, so guessing it wrong is
	// worse than refusing: a mis-typed target would be investigated as the
	// wrong kind and silently produce nonsense.
	if incident.TargetKind == "" {
		return nil, fmt.Errorf("targetRef.kind is empty in Incident %s", incident.IncidentName)
	}
	// Empty apiVersion means the core group, per ObjectReference convention.
	if incident.TargetAPIVersion == "" {
		incident.TargetAPIVersion = "v1"
	}
	if incident.TargetNamespace == "" {
		incident.TargetNamespace = incident.Namespace
	}

	return incident, nil
}

// gatherContext seeds the conversation with the target object and the events
// about it — the two things that are meaningful for any kind. Everything else
// (logs, owners, children, node capacity) the scout fetches itself with the
// tools it has; pre-fetching more would mean teaching this function about
// specific kinds, which is exactly what makes it brittle.
func gatherContext(ctx context.Context, incident *Incident, getResource tools.Tool) []tools.ToolResult {
	call := func(t tools.Tool, params any) tools.ToolResult {
		raw, _ := json.Marshal(params)
		out, err := t.Execute(ctx, raw)
		return tools.ToolResult{ToolName: t.Name(), Output: out, Err: err}
	}

	return []tools.ToolResult{
		call(getResource, map[string]string{
			"apiVersion": incident.TargetAPIVersion, "kind": incident.TargetKind,
			"name": incident.TargetName, "namespace": incident.TargetNamespace,
		}),
		// Match on kind as well as name: names are only unique per kind, so a
		// Deployment and a Pod sharing a name would otherwise pick up each
		// other's events.
		call(getResource, map[string]string{
			"apiVersion": "v1", "kind": "Event",
			"namespace": incident.TargetNamespace,
			"fieldSelector": "involvedObject.name=" + incident.TargetName +
				",involvedObject.kind=" + incident.TargetKind,
		}),
	}
}

func unstructuredMap(m map[string]any, key string) (map[string]any, bool, error) {
	v, ok := m[key]
	if !ok {
		return nil, false, nil
	}
	nested, ok := v.(map[string]any)
	return nested, ok, nil
}

func stringField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, _ := m[key].(string)
	return v
}

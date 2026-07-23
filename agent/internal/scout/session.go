package scout

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"

	"github.com/sguldemond/goblin/agent/internal/llm"
	"github.com/sguldemond/goblin/agent/internal/messenger"
	"github.com/sguldemond/goblin/agent/internal/tools"
)

// maxOpenIncidents caps how many incidents share one conversation. Past this
// the context window is the real limit, and a scout that silently truncates is
// worse than one that says it is overloaded.
const maxOpenIncidents = 12

// session owns the single conversation and the set of incidents it covers.
//
// One conversation across all open incidents is what makes correlation
// possible: the model sees three OOMKills on one Deployment together and can
// say so, rather than three isolated processes each proposing the same patch.
type session struct {
	scout   *Scout
	mapper  meta.RESTMapper
	send    llm.SendFunc
	m       messenger.Messenger
	watcher *IncidentWatcher

	open map[string]*Incident

	// broker is the single owner of free-text human input. Every reader of a
	// human reply — the idle loop, the conversation, the approval gate — goes
	// through it, so two goroutines can never compete for one message.
	broker *inputBroker
}

// run is the outer loop: idle until something arrives, converse until every
// incident is closed, then reset and idle again.
//
// The conversation resets on idle because incident history lives in the
// Incident CRs, not in the transcript — the model fetches what it needs with
// listIncidents. That keeps the context bounded without summarising anything.
func (s *session) run(ctx context.Context, backlog []*Incident) error {
	incidents := s.watcher.Watch(ctx)

	// One goroutine owns messenger input; wrap the messenger so every later
	// caller (this loop, the conversation, the approval gate) reads through it.
	s.broker = newInputBroker(ctx, s.m)
	s.m = brokeredMessenger{m: s.m, b: s.broker}

	pending := backlog
	var opening []llm.Message

	for {
		if len(pending) == 0 {
			// Idle, but not deaf: the human can still ask questions with no
			// incident open, and the scout has the tools to answer them.
			select {
			case <-ctx.Done():
				return nil
			case inc, ok := <-incidents:
				if !ok {
					return nil
				}
				// Wait for siblings before starting: replicas of one broken
				// Deployment fail seconds apart, and investigating them
				// together is the whole point of a persistent scout.
				pending = collect(ctx, inc, incidents)
				if len(pending) > 1 {
					fmt.Printf(">> collected %d incidents arriving together\n", len(pending))
				}
			case in := <-s.broker.nextText():
				if in.err != nil {
					return in.err
				}
				opening = []llm.Message{{
					Role:    "user",
					Content: []llm.Content{{Type: "text", Text: in.text}},
				}}
			}
		}

		for _, inc := range pending {
			s.admit(ctx, inc)
		}
		pending = nil

		// A conversation needs something to talk about: an incident, or a
		// question from the human.
		if len(s.open) == 0 && len(opening) == 0 {
			continue
		}
		if err := s.converse(ctx, incidents, opening); err != nil {
			return err
		}
		opening = nil
		// Conversation ended: every incident it covered is closed.
		s.open = map[string]*Incident{}
	}
}

// admit claims an incident and adds it to the open set. A claim that fails is
// logged rather than fatal — another attempt happens on the next resync.
func (s *session) admit(ctx context.Context, inc *Incident) {
	if _, dup := s.open[inc.Key()]; dup {
		return
	}
	if len(s.open) >= maxOpenIncidents {
		s.m.Send(fmt.Sprintf("⚠️ %d incidents already open; %s will wait.", len(s.open), inc.Key())) //nolint:errcheck
		return
	}
	if err := s.watcher.Claim(ctx, inc); err != nil {
		fmt.Printf(">> could not claim %s: %v\n", inc.Key(), err)
		return
	}
	s.open[inc.Key()] = inc
	fmt.Printf(">> claimed %s: %s on %s %s/%s\n",
		inc.Key(), inc.Trigger, inc.TargetKind, inc.TargetNamespace, inc.TargetName)
}

// openKeys is the snapshot a staged change is validated against.
func (s *session) openKeys() []string {
	keys := make([]string, 0, len(s.open))
	for k := range s.open {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// arrivedSince reports incidents opened after a staged change was proposed.
// It is what makes an approval a decision about the current world rather than
// the world as it was when the human was asked.
func (s *session) arrivedSince(snapshot []string) []*Incident {
	was := make(map[string]bool, len(snapshot))
	for _, k := range snapshot {
		was[k] = true
	}
	var out []*Incident
	for k, inc := range s.open {
		if !was[k] {
			out = append(out, inc)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	return out
}

// seedFor builds the opening message for a newly admitted incident.
func (s *session) seedFor(ctx context.Context, inc *Incident) string {
	getResource := tools.NewGetResource(s.scout.dynCli, s.mapper)
	return BuildContext(*inc, gatherContext(ctx, inc, getResource))
}

// describeArrivals renders newly arrived incidents for the model.
func describeArrivals(incs []*Incident) string {
	var sb strings.Builder
	for _, inc := range incs {
		fmt.Fprintf(&sb, "- %s: %s on %s %s/%s\n",
			inc.Key(), inc.Trigger, inc.TargetKind, inc.TargetNamespace, inc.TargetName)
	}
	return sb.String()
}

// resyncTicker bounds how long a missed watch event can hide an incident.
func resyncTicker() *time.Ticker { return time.NewTicker(resyncInterval) }

package scout

import (
	"context"
	"time"
)

// Vars rather than consts so tests can shorten them; the debounce is timing
// behaviour, and a suite that waits out real windows stops being run.
var (
	// settleWindow is how long the scout waits for siblings after an incident
	// arrives. One failing Deployment produces one incident per replica,
	// seconds apart; investigating the first alone and the rest separately
	// wastes work and hides the fact that they are one fault.
	settleWindow = 5 * time.Second

	// settleCeiling bounds the wait when incidents keep arriving, so a noisy
	// cluster cannot postpone the investigation indefinitely.
	settleCeiling = 30 * time.Second
)

// collect gathers an incident and any siblings that arrive shortly after it.
//
// It debounces rather than using a fixed window: a lone incident starts after
// settleWindow, while a stream of related ones keeps the batch open until it
// settles or hits settleCeiling. The cost is a floor on time-to-first-response,
// which is negligible next to how long an investigation takes.
func collect(ctx context.Context, first *Incident, incidents <-chan *Incident) []*Incident {
	batch := []*Incident{first}
	batch = append(batch, drain(incidents)...)

	deadline := time.NewTimer(settleCeiling)
	defer deadline.Stop()
	quiet := time.NewTimer(settleWindow)
	defer quiet.Stop()

	for {
		select {
		case <-ctx.Done():
			return batch

		case <-deadline.C:
			return batch

		case <-quiet.C:
			return batch

		case inc, ok := <-incidents:
			if !ok {
				return batch
			}
			batch = append(batch, inc)
			batch = append(batch, drain(incidents)...)
			// Something arrived, so wait again for things to go quiet.
			if !quiet.Stop() {
				select {
				case <-quiet.C:
				default:
				}
			}
			quiet.Reset(settleWindow)
		}
	}
}

// drain takes everything already queued without waiting. Incidents that arrived
// while the scout was busy are free to collect and belong in the same batch.
func drain(incidents <-chan *Incident) []*Incident {
	var out []*Incident
	for {
		select {
		case inc, ok := <-incidents:
			if !ok {
				return out
			}
			out = append(out, inc)
		default:
			return out
		}
	}
}

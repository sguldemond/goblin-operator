package controller

import (
	"context"
	"time"

	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// GrantSweeper periodically removes scout permissions whose incident is gone or
// finished.
//
// The finalizer handles ordinary deletion; this catches what it cannot: an
// incident force-deleted with --grace-period=0, a crash between creating a
// binding and persisting the finalizer, or a binding deleted and recreated by
// hand. Without it those grants would persist until someone noticed.
type GrantSweeper struct {
	Grants   *GrantManager
	Interval time.Duration
}

// Start implements manager.Runnable. It sweeps once at startup — that is when
// orphans from a previous process are most likely — then on a ticker.
func (s *GrantSweeper) Start(ctx context.Context) error {
	log := logf.FromContext(ctx).WithName("grant-sweeper")

	interval := s.Interval
	if interval <= 0 {
		interval = 5 * time.Minute
	}

	if err := s.Grants.Sweep(ctx); err != nil {
		log.Error(err, "Initial grant sweep failed")
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			// A failed sweep is logged, never fatal: the next tick retries, and
			// killing the operator over a stale binding would be worse.
			if err := s.Grants.Sweep(ctx); err != nil {
				log.Error(err, "Grant sweep failed")
			}
		}
	}
}

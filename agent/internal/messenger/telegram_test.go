package messenger

import (
	"context"
	"testing"
	"time"
)

// The approval gate is safety-critical: a change is applied only when the human
// answers the exact question that proposed it. Button presses queued before the
// question was asked — idle taps, or presses on an earlier question's keyboard —
// must not be read as its answer. This is the bug that auto-applied and
// auto-rejected changes nobody was looking at.
func TestAwaitCallbackIgnoresStalePresses(t *testing.T) {
	ch := make(chan string, 8)
	ch <- "1:y" // left over from an earlier question
	ch <- "1:n"
	ch <- "2:n" // the answer to THIS question

	got, err := awaitCallback(context.Background(), ch, "2")
	if err != nil {
		t.Fatal(err)
	}
	if got != "n" {
		t.Errorf("got %q; the stale y must be skipped and this question's n returned", got)
	}
}

// With only stale presses available, the question stays unanswered rather than
// borrowing an answer from another keyboard.
func TestAwaitCallbackBlocksOnStaleOnly(t *testing.T) {
	ch := make(chan string, 8)
	ch <- "1:y"

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	if _, err := awaitCallback(ctx, ch, "2"); err != context.DeadlineExceeded {
		t.Errorf("err = %v; want DeadlineExceeded — a stale press must not answer", err)
	}
}

// poll() is a single goroutine serving both chat messages and button
// callbacks. A blocking send on a full channel would stop button presses
// arriving too, leaving the scout unable to receive an approval — silently and
// permanently, until the pod restarts. Dropping stale chat is the lesser evil.
func TestOfferNeverBlocksThePoller(t *testing.T) {
	m := &TelegramMessenger{textCh: make(chan string, 2)}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 20 {
			m.offer(string(rune('a' + i%26)))
		}
	}()

	select {
	case <-done:
	case <-timeout():
		t.Fatal("offer blocked; the poller would stop handling button presses")
	}
}

// The newest message is the one worth keeping: it is what the human just said.
func TestOfferKeepsTheMostRecentMessages(t *testing.T) {
	m := &TelegramMessenger{textCh: make(chan string, 2)}

	m.offer("first")
	m.offer("second")
	m.offer("third")

	got := []string{<-m.textCh, <-m.textCh}
	if got[1] != "third" {
		t.Errorf("buffer = %v; want the newest message retained", got)
	}
}

func timeout() <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		defer close(ch)
		<-time.After(2 * time.Second)
	}()
	return ch
}

package messenger

import (
	"testing"
	"time"
)

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

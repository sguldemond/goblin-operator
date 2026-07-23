package scout

import (
	"context"

	"github.com/sguldemond/goblin/agent/internal/messenger"
)

// inputBroker is the single owner of free-text human input. Exactly one
// goroutine reads the messenger, so no two callers can ever race for one reply.
//
// The bug this removes: the idle listener and the approval gate each opened
// their own read of the same channel. A rejection reason typed by the human
// went to whichever won the race; when the idle listener won, the gate waited
// forever and the scout went silent.
type inputBroker struct {
	lines chan humanInput
}

func newInputBroker(ctx context.Context, m messenger.Messenger) *inputBroker {
	b := &inputBroker{lines: make(chan humanInput, 1)}
	go b.read(ctx, m)
	return b
}

// read is the one and only reader of free-text input. Every line goes to lines;
// a caller that has since moved on simply leaves the line buffered for the next
// reader, so nothing is lost and a second competing reader is never started.
func (b *inputBroker) read(ctx context.Context, m messenger.Messenger) {
	for {
		text, err := m.Ask(ctx, "", nil)
		select {
		case b.lines <- humanInput{text, err}:
		case <-ctx.Done():
			return
		}
		if err != nil {
			// The input is gone (context cancelled, transport closed). No
			// further replies will arrive, so stop rather than spin.
			return
		}
	}
}

// nextText is the channel delivering the next human line. Callers select on it
// alongside incidents or context cancellation, so waiting on the human never
// blocks reacting to an incident.
func (b *inputBroker) nextText() <-chan humanInput { return b.lines }

// brokeredMessenger routes free-text reads through the broker while leaving
// button reads and sends on the underlying messenger. Handing this to the
// approval gate makes its rejection-reason prompt use the one shared reader
// instead of opening a second one that would race the session's.
type brokeredMessenger struct {
	m messenger.Messenger
	b *inputBroker
}

func (bm brokeredMessenger) Send(text string) error { return bm.m.Send(text) }
func (bm brokeredMessenger) StartThinking() func()  { return bm.m.StartThinking() }

func (bm brokeredMessenger) Ask(ctx context.Context, question string, rows [][]messenger.Button) (string, error) {
	if rows != nil {
		// Buttons arrive on their own channel with a single waiter, so they
		// never race the text reader — read them directly.
		return bm.m.Ask(ctx, question, rows)
	}
	if question != "" {
		if err := bm.m.Send(question); err != nil {
			return "", err
		}
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case in := <-bm.b.nextText():
		return in.text, in.err
	}
}

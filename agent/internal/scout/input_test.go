package scout

import (
	"context"
	"testing"
	"time"

	"github.com/sguldemond/goblin/agent/internal/messenger"
)

// scriptedMessenger feeds queued text and button replies, and records sends.
type scriptedMessenger struct {
	text    chan string
	buttons chan string
	sends   []string
}

func newScripted() *scriptedMessenger {
	return &scriptedMessenger{text: make(chan string, 8), buttons: make(chan string, 8)}
}

func (f *scriptedMessenger) Send(t string) error { f.sends = append(f.sends, t); return nil }
func (f *scriptedMessenger) StartThinking() func() { return func() {} }

func (f *scriptedMessenger) Ask(ctx context.Context, _ string, rows [][]messenger.Button) (string, error) {
	src := f.text
	if rows != nil {
		src = f.buttons
	}
	select {
	case v := <-src:
		return v, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// The deadlock this replaces: a human reply was lost when the reader waiting for
// it had moved on. With a single broker, a line produced while nobody is
// waiting is buffered and handed to whoever reads next — the gate's rejection
// prompt gets the reason even though the idle loop opened the read first.
func TestBrokerDeliversToWhoeverReadsNext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	f := newScripted()
	b := newInputBroker(ctx, f)

	f.text <- "some more memory" // arrives while no one is selecting on it

	select {
	case in := <-b.nextText():
		if in.err != nil {
			t.Fatalf("err = %v", in.err)
		}
		if in.text != "some more memory" {
			t.Fatalf("got %q; want the buffered line delivered to the next reader", in.text)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("line lost: the broker did not deliver a reply nobody was yet waiting for")
	}
}

// Sequential reads see sequential lines, proving there is a single reader rather
// than several each taking a message.
func TestBrokerReadsAreSequential(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	f := newScripted()
	b := newInputBroker(ctx, f)

	f.text <- "first"
	if got := (<-b.nextText()).text; got != "first" {
		t.Fatalf("got %q; want first", got)
	}
	f.text <- "second"
	if got := (<-b.nextText()).text; got != "second" {
		t.Fatalf("got %q; want second", got)
	}
}

// The wrapper sends the prompt and returns the human's typed reason through the
// broker — the path the approval gate takes for a rejection reason.
func TestBrokeredMessengerRoutesTextThroughBroker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	f := newScripted()
	bm := brokeredMessenger{m: f, b: newInputBroker(ctx, f)}

	f.text <- "needs more headroom"
	got, err := bm.Ask(ctx, "Rejection reason?", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "needs more headroom" {
		t.Fatalf("got %q; want the typed reason", got)
	}
	if len(f.sends) != 1 || f.sends[0] != "Rejection reason?" {
		t.Fatalf("sends = %v; want the question sent once", f.sends)
	}
}

// Button prompts bypass the broker and read the button channel directly, so an
// approval press is never confused with a typed message.
func TestBrokeredMessengerRoutesButtonsToUnderlying(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	f := newScripted()
	bm := brokeredMessenger{m: f, b: newInputBroker(ctx, f)}

	f.buttons <- "y"
	got, err := bm.Ask(ctx, "Apply this change?", [][]messenger.Button{
		{{Text: "✅ Apply", Data: "y"}, {Text: "❌ Reject", Data: "n"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "y" {
		t.Fatalf("got %q; want the button data", got)
	}
}

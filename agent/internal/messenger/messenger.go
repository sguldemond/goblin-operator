package messenger

import "context"

// Button is a labelled action offered to the human.
type Button struct {
	Text string // display label
	Data string // value returned when the human picks this button
}

// Messenger abstracts all human I/O so the scout and tools are independent of
// whether the session runs in a terminal or over Telegram.
type Messenger interface {
	// Send delivers a plain-text (or HTML) message to the human.
	Send(text string) error

	// Ask poses a question and blocks until the human responds.
	// rows == nil  →  free-text reply (the typed string is returned).
	// rows != nil  →  inline button choice; returns the chosen Button.Data.
	Ask(ctx context.Context, question string, rows [][]Button) (string, error)

	// StartThinking signals that the agent is processing.
	// The returned function must be called when processing finishes.
	StartThinking() func()
}

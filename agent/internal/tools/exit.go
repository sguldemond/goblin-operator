package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type Exit struct{ triggered bool }

func NewExit() *Exit { return &Exit{} }

func (t *Exit) Name() string { return "exit" }

func (t *Exit) Description() string {
	return "Signal that the conversation should end. Only call this when the human explicitly says they are done — " +
		"e.g. 'thanks', 'all good', 'bye', 'we're done'. " +
		"Do NOT call this because the task is complete. Always wait for the human to close the conversation."
}

func (t *Exit) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type": "object", "properties": {}}`)
}

func (t *Exit) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	t.triggered = true
	return "Exit requested — awaiting human confirmation.", nil
}

func (t *Exit) Active() bool { return t.triggered }

func (t *Exit) AfterTurn(_ context.Context, scanner *bufio.Scanner, out io.Writer) ([]string, bool, error) {
	fmt.Fprint(out, "Goblin wants to exit. OK? [y/n]: ")
	if !scanner.Scan() {
		t.triggered = false
		return nil, false, scanner.Err()
	}
	if strings.TrimSpace(strings.ToLower(scanner.Text())) == "y" {
		fmt.Fprintln(out, "Goodbye.")
		return nil, true, nil
	}
	t.triggered = false
	return []string{"Human declined the exit. Continue the conversation."}, false, nil
}

var _ Tool = (*Exit)(nil)
var _ AfterTurnHook = (*Exit)(nil)

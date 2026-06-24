package messenger

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"time"
)

// TTYMessenger implements Messenger over a terminal (stdin/stdout).
type TTYMessenger struct {
	scanner *bufio.Scanner
	out     io.Writer
}

func NewTTY(in io.Reader, out io.Writer) *TTYMessenger {
	return &TTYMessenger{scanner: bufio.NewScanner(in), out: out}
}

func (m *TTYMessenger) Send(text string) error {
	_, err := fmt.Fprintln(m.out, text)
	return err
}

func (m *TTYMessenger) Ask(_ context.Context, question string, rows [][]Button) (string, error) {
	if rows == nil {
		// Free-text prompt.
		for {
			if question != "" {
				fmt.Fprintf(m.out, "\n%s ", question)
			} else {
				fmt.Fprint(m.out, "\n> ")
			}
			if !m.scanner.Scan() {
				return "", m.scanner.Err()
			}
			if line := strings.TrimSpace(m.scanner.Text()); line != "" {
				return line, nil
			}
		}
	}

	// Button choice — render as numbered list, accept the Data value directly.
	if question != "" {
		fmt.Fprintln(m.out, question)
	}
	for _, row := range rows {
		for _, btn := range row {
			fmt.Fprintf(m.out, "  [%s] %s\n", btn.Data, btn.Text)
		}
	}
	for {
		fmt.Fprint(m.out, "Choice: ")
		if !m.scanner.Scan() {
			return "", m.scanner.Err()
		}
		answer := strings.TrimSpace(strings.ToLower(m.scanner.Text()))
		for _, row := range rows {
			for _, btn := range row {
				if answer == strings.ToLower(btn.Data) || answer == strings.ToLower(btn.Text) {
					return btn.Data, nil
				}
			}
		}
	}
}

func (m *TTYMessenger) StartThinking() func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		n := 0
		for {
			select {
			case <-stop:
				fmt.Fprint(m.out, "\r                \r")
				return
			case <-ticker.C:
				n++
				fmt.Fprintf(m.out, "\r>> %-3s", strings.Repeat(".", n%4))
			}
		}
	}()
	return func() {
		close(stop)
		<-done
	}
}

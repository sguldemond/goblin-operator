package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sguldemond/goblin/telegram/internal/pipe"
	"github.com/sguldemond/goblin/telegram/internal/tgbot"
)

// Prompts emitted by the scout that require a human response.
// Order matters: check longer suffixes first to avoid false partial matches.
var promptPatterns = []string{
	"Goblin wants to exit. OK? [y/n]: ",
	"Rejection reason (optional): ",
	"Apply? [y/n]: ",
	"\n> ",
}

type state int

const (
	stateNormal        state = iota
	stateAwaitingYN          // waiting for approve/reject button
	stateAwaitingReason      // waiting for rejection reason text
	stateAwaitingChat        // waiting for free-form text (> prompt)
	stateAwaitingExit        // waiting for exit confirmation
)

type Bot struct {
	tg      *tgbot.Client
	remName string
	pipeDir string

	mu      sync.Mutex
	cur     state
	textBuf strings.Builder
	inbox   *os.File // goblin-horn → scout; nil until scout connects

	updateOffset int
}

// --- outbox tailing ---

func (b *Bot) tailOutbox(outbox io.Reader) {
	buf := make([]byte, 1)
	var acc strings.Builder

	for {
		_, err := outbox.Read(buf)
		if err != nil {
			log.Printf("outbox read: %v", err)
			return
		}
		acc.WriteByte(buf[0])
		s := acc.String()

		// Complete line
		if buf[0] == '\n' {
			b.handleToken(s)
			acc.Reset()
			continue
		}

		// Known prompt suffix (no trailing newline)
		for _, p := range promptPatterns {
			if strings.HasSuffix(s, p) {
				b.handleToken(s)
				acc.Reset()
				break
			}
		}
	}
}

func (b *Bot) handleToken(token string) {
	log.Printf("← scout: %q", token)
	b.mu.Lock()
	defer b.mu.Unlock()

	switch {
	case strings.HasSuffix(token, "Goblin wants to exit. OK? [y/n]: "):
		b.flushLocked()
		if err := b.tg.SendMessage("🚪 Goblin wants to exit.", &tgbot.InlineKeyboard{
			InlineKeyboard: [][]tgbot.InlineButton{{
				{Text: "👋 Let it go", CallbackData: b.remName + ":y"},
				{Text: "🚫 Stay", CallbackData: b.remName + ":n"},
			}},
		}); err != nil {
			log.Printf("sendMessage (exit prompt): %v", err)
		}
		b.cur = stateAwaitingExit

	case strings.HasSuffix(token, "Apply? [y/n]: "):
		b.textBuf.WriteString(token)
		b.flushLocked()
		if err := b.tg.SendMessage("Approve this patch?", &tgbot.InlineKeyboard{
			InlineKeyboard: [][]tgbot.InlineButton{{
				{Text: "✅ Apply", CallbackData: b.remName + ":y"},
				{Text: "❌ Reject", CallbackData: b.remName + ":n"},
			}},
		}); err != nil {
			log.Printf("sendMessage (apply prompt): %v", err)
		}
		b.cur = stateAwaitingYN

	case strings.HasSuffix(token, "Rejection reason (optional): "):
		b.flushLocked()
		if err := b.tg.SendMessage("Rejection reason? (reply with text, or /skip)", nil); err != nil {
			log.Printf("sendMessage (reason prompt): %v", err)
		}
		b.cur = stateAwaitingReason

	case strings.HasSuffix(token, "\n> "):
		// Strip the trailing "> " — we send our own cue
		b.textBuf.WriteString(strings.TrimSuffix(token, "\n> "))
		b.flushLocked()
		if err := b.tg.SendMessage("💬 Your turn — reply to continue:", nil); err != nil {
			log.Printf("sendMessage (chat prompt): %v", err)
		}
		b.cur = stateAwaitingChat

	default:
		b.textBuf.WriteString(token)
		// Flush on paragraph break so long output doesn't arrive as one giant blob
		if strings.HasSuffix(b.textBuf.String(), "\n\n") {
			b.flushLocked()
		}
	}
}

// flushLocked sends the accumulated text buffer as a Telegram message.
// Must be called with b.mu held.
func (b *Bot) flushLocked() {
	text := strings.TrimRight(b.textBuf.String(), "\n")
	b.textBuf.Reset()
	if strings.TrimSpace(text) == "" {
		return
	}
	// Telegram max message size is 4096 chars.
	for len(text) > 4000 {
		if err := b.tg.SendMessage(text[:4000], nil); err != nil {
			log.Printf("sendMessage: %v", err)
		}
		text = text[4000:]
	}
	if text != "" {
		if err := b.tg.SendMessage(text, nil); err != nil {
			log.Printf("sendMessage: %v", err)
		}
	}
}

// --- inbox writing ---

func (b *Bot) connectInbox() {
	p := filepath.Join(b.pipeDir, "inbox")
	// O_WRONLY blocks until scout opens inbox for reading. That's fine — we're
	// in a goroutine and the scout will open it shortly after startup.
	f, err := os.OpenFile(p, os.O_WRONLY, 0)
	if err != nil {
		log.Printf("inbox connect failed: %v", err)
		return
	}
	b.mu.Lock()
	b.inbox = f
	b.mu.Unlock()
	log.Println("goblin-horn: scout connected")
}

func (b *Bot) writeToScout(s string) {
	b.mu.Lock()
	f := b.inbox
	b.mu.Unlock()
	if f == nil {
		log.Printf("writeToScout: scout not connected, dropping %q", s)
		return
	}
	log.Printf("→ scout: %q", s)
	if _, err := io.WriteString(f, s); err != nil {
		log.Printf("writeToScout: %v", err)
	}
}

// --- Telegram polling ---

func (b *Bot) pollTelegram() {
	for {
		updates, err := b.tg.GetUpdates(b.updateOffset)
		if err != nil {
			log.Printf("getUpdates: %v", err)
			time.Sleep(2 * time.Second)
			continue
		}
		for _, u := range updates {
			b.updateOffset = u.UpdateID + 1
			b.handleUpdate(u)
		}
	}
}

func (b *Bot) handleUpdate(u tgbot.Update) {
	if cb := u.CallbackQuery; cb != nil {
		if !strings.HasPrefix(cb.Data, b.remName+":") {
			return // belongs to a different scout's goblin-horn instance
		}
		b.tg.AnswerCallbackQuery(cb.ID)
		action := strings.TrimPrefix(cb.Data, b.remName+":")

		b.mu.Lock()
		cur := b.cur
		b.mu.Unlock()

		switch cur {
		case stateAwaitingYN:
			b.writeToScout(action + "\n")
			if action == "n" {
				b.mu.Lock()
				b.cur = stateAwaitingReason
				b.mu.Unlock()
				b.tg.SendMessage("Rejection reason? (reply with text, or /skip)", nil)
			} else {
				b.mu.Lock()
				b.cur = stateNormal
				b.mu.Unlock()
			}

		case stateAwaitingExit:
			b.writeToScout(action + "\n")
			b.mu.Lock()
			b.cur = stateNormal
			b.mu.Unlock()
		}
		return
	}

	if msg := u.Message; msg != nil {
		b.mu.Lock()
		cur := b.cur
		b.mu.Unlock()

		switch cur {
		case stateAwaitingReason:
			reason := msg.Text
			if reason == "/skip" {
				reason = ""
			}
			b.writeToScout(reason + "\n")
			b.mu.Lock()
			b.cur = stateNormal
			b.mu.Unlock()

		case stateAwaitingChat:
			b.writeToScout(msg.Text + "\n")
			b.mu.Lock()
			b.cur = stateNormal
			b.mu.Unlock()
		}
	}
}

// --- main ---

func mustEnv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		log.Fatalf("required env var %s not set", k)
	}
	return v
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	token := mustEnv("TELEGRAM_BOT_TOKEN")
	chatID := mustEnv("TELEGRAM_CHAT_ID")
	remName := mustEnv("REMEDIATION_NAME")
	pipeDir := envOr("PIPE_DIR", "/shared")

	if err := pipe.Setup(pipeDir); err != nil {
		log.Fatalf("pipe setup: %v", err)
	}

	tg := tgbot.New(token, chatID)
	b := &Bot{tg: tg, remName: remName, pipeDir: pipeDir, cur: stateNormal}

	// Connect inbox (blocks until scout opens its end — run in background).
	go b.connectInbox()

	// Connect outbox (blocks until scout opens its end — run in background).
	// Once connected, start tailing.
	go func() {
		outboxPath := filepath.Join(pipeDir, "outbox")
		// O_RDWR avoids blocking on open regardless of when the scout connects,
		// same trick the scout uses. Reads still block when the pipe is empty.
		f, err := os.OpenFile(outboxPath, os.O_RDWR, 0)
		if err != nil {
			log.Fatalf("outbox open: %v", err)
		}
		defer f.Close()
		log.Println("goblin-horn: outbox open, tailing scout output")
		b.tailOutbox(f)
		// Scout process exited — flush any remaining buffer and notify Telegram.
		b.mu.Lock()
		b.flushLocked()
		b.mu.Unlock()
		b.tg.SendMessage("👻 Goblin scout session ended.", nil)
		log.Println("goblin-horn: scout disconnected")
	}()

	log.Printf("goblin-horn: waiting for scout (%s)…", remName)
	b.tg.SendMessage(fmt.Sprintf("🔔 Goblin scout dispatched: <b>%s</b>", remName), nil)

	b.pollTelegram()
}

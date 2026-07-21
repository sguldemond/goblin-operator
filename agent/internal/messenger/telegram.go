package messenger

import (
	"context"
	"fmt"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// TelegramMessenger implements Messenger over a Telegram bot.
type TelegramMessenger struct {
	bot    *tgbotapi.BotAPI
	chatID int64

	textCh     chan string
	callbackCh chan string // delivers Button.Data from inline keyboard taps
}

func NewTelegram(token string, chatID int64) (*TelegramMessenger, error) {
	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		return nil, fmt.Errorf("telegram bot init: %w", err)
	}

	m := &TelegramMessenger{
		bot:        bot,
		chatID:     chatID,
		textCh:     make(chan string, 8),
		callbackCh: make(chan string, 8),
	}
	go m.poll()
	return m, nil
}

// poll long-polls Telegram and routes updates to the appropriate channels.
func (m *TelegramMessenger) poll() {
	cfg := tgbotapi.NewUpdate(0)
	cfg.Timeout = 30
	updates := m.bot.GetUpdatesChan(cfg)
	for u := range updates {
		if u.CallbackQuery != nil {
			cb := u.CallbackQuery
			fmt.Printf("[tg in] (button) %s\n", cb.Data)
			m.bot.Send(tgbotapi.NewCallback(cb.ID, "")) //nolint:errcheck
			m.callbackCh <- cb.Data
			continue
		}
		if u.Message != nil && u.Message.Chat.ID == m.chatID {
			fmt.Printf("[tg in] %s\n", u.Message.Text)
			m.offer(u.Message.Text)
		}
	}
}

// offer queues an incoming message without ever blocking the poller.
//
// poll() is a single goroutine serving both text and button callbacks, so a
// blocking send on a full channel would stop button presses arriving too — the
// scout would go silently and permanently deaf, unable to receive an approval
// even when a real incident needed one. Dropping the oldest chat message is far
// less costly than that.
func (m *TelegramMessenger) offer(text string) {
	for {
		select {
		case m.textCh <- text:
			return
		default:
		}
		select {
		case dropped := <-m.textCh:
			fmt.Printf("[tg] dropped unread message: %s\n", dropped)
		default:
			// Drained by a reader in between; try the send again.
		}
	}
}

func (m *TelegramMessenger) Send(text string) error {
	fmt.Printf("[tg out] %s\n", text)
	msg := tgbotapi.NewMessage(m.chatID, text)
	msg.ParseMode = tgbotapi.ModeHTML
	_, err := m.bot.Send(msg)
	return err
}

func (m *TelegramMessenger) Ask(ctx context.Context, question string, rows [][]Button) (string, error) {
	if rows == nil {
		if question != "" {
			if err := m.Send(question); err != nil {
				return "", err
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case text := <-m.textCh:
			return text, nil
		}
	}

	// Build inline keyboard.
	keyboard := make([][]tgbotapi.InlineKeyboardButton, len(rows))
	for i, row := range rows {
		keyboard[i] = make([]tgbotapi.InlineKeyboardButton, len(row))
		for j, btn := range row {
			keyboard[i][j] = tgbotapi.NewInlineKeyboardButtonData(btn.Text, btn.Data)
		}
	}
	msg := tgbotapi.NewMessage(m.chatID, question)
	msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(keyboard...)
	if _, err := m.bot.Send(msg); err != nil {
		return "", err
	}

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case data := <-m.callbackCh:
		return data, nil
	}
}

func (m *TelegramMessenger) StartThinking() func() {
	ctx, cancel := context.WithCancel(context.Background())
	sendTyping := func() {
		m.bot.Request(tgbotapi.NewChatAction(m.chatID, tgbotapi.ChatTyping)) //nolint:errcheck
	}
	go func() {
		fmt.Println("[tg out] typing...")
		sendTyping()
		ticker := time.NewTicker(4 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sendTyping()
			}
		}
	}()
	return cancel
}

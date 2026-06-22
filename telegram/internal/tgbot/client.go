package tgbot

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Client struct {
	token  string
	chatID string
	http   *http.Client
}

func New(token, chatID string) *Client {
	return &Client{
		token:  token,
		chatID: chatID,
		// Timeout must exceed the long-poll timeout (25s) used in GetUpdates.
		http: &http.Client{Timeout: 35 * time.Second},
	}
}

// --- types ---

type InlineButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

type InlineKeyboard struct {
	InlineKeyboard [][]InlineButton `json:"inline_keyboard"`
}

type Update struct {
	UpdateID      int            `json:"update_id"`
	Message       *Message       `json:"message"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
}

type Message struct {
	MessageID int    `json:"message_id"`
	Text      string `json:"text"`
}

type CallbackQuery struct {
	ID      string   `json:"id"`
	Data    string   `json:"data"`
	Message *Message `json:"message"`
}

// --- API calls ---

func (c *Client) apiURL(method string) string {
	return fmt.Sprintf("https://api.telegram.org/bot%s/%s", c.token, method)
}

func (c *Client) post(method string, body any) ([]byte, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Post(c.apiURL(method), "application/json", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func (c *Client) SendMessage(text string, keyboard *InlineKeyboard) error {
	body := map[string]any{
		"chat_id":    c.chatID,
		"text":       text,
		"parse_mode": "HTML",
	}
	if keyboard != nil {
		body["reply_markup"] = keyboard
	}
	_, err := c.post("sendMessage", body)
	return err
}

func (c *Client) AnswerCallbackQuery(callbackID string) error {
	_, err := c.post("answerCallbackQuery", map[string]string{"callback_query_id": callbackID})
	return err
}

func (c *Client) GetUpdates(offset int) ([]Update, error) {
	url := fmt.Sprintf("%s?offset=%d&timeout=25&allowed_updates=[\"message\",\"callback_query\"]",
		c.apiURL("getUpdates"), offset)
	resp, err := c.http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result struct {
		OK     bool     `json:"ok"`
		Result []Update `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	if !result.OK {
		return nil, fmt.Errorf("getUpdates returned ok=false")
	}
	return result.Result, nil
}

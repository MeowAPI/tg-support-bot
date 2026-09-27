package telegram

import (
	"context"
	"encoding/json"
	"time"
)

func (c *Client) GetMe(ctx context.Context) (*User, error) {
	var u User
	if err := c.Call(ctx, "getMe", nil, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// DeleteWebhook switches the bot to long polling; getUpdates fails while a
// webhook is set.
func (c *Client) DeleteWebhook(ctx context.Context) error {
	return c.Call(ctx, "deleteWebhook", map[string]any{"drop_pending_updates": false}, nil)
}

// GetUpdates long-polls for up to timeout seconds. Passing offset confirms
// every update below it.
func (c *Client) GetUpdates(ctx context.Context, offset int64, timeout int) ([]Update, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second+20*time.Second)
	defer cancel()
	var updates []Update
	err := c.Call(ctx, "getUpdates", map[string]any{
		"offset":          offset,
		"timeout":         timeout,
		"allowed_updates": AllowedUpdates,
	}, &updates)
	return updates, err
}

type SendMessageParams struct {
	ChatID          int64                 `json:"chat_id"`
	Text            string                `json:"text"`
	ParseMode       string                `json:"parse_mode,omitempty"`
	Entities        json.RawMessage       `json:"entities,omitempty"`
	ReplyParameters *ReplyParameters      `json:"reply_parameters,omitempty"`
	ReplyMarkup     *InlineKeyboardMarkup `json:"reply_markup,omitempty"`
}

func (c *Client) SendMessage(ctx context.Context, p SendMessageParams) (*Message, error) {
	var m Message
	if err := c.Call(ctx, "sendMessage", p, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

type CopyMessageParams struct {
	ChatID          int64                 `json:"chat_id"`
	FromChatID      int64                 `json:"from_chat_id"`
	MessageID       int64                 `json:"message_id"`
	ReplyParameters *ReplyParameters      `json:"reply_parameters,omitempty"`
	ReplyMarkup     *InlineKeyboardMarkup `json:"reply_markup,omitempty"`
}

// CopyMessage re-sends a message without a "forwarded from" header and
// returns the new message's ID.
func (c *Client) CopyMessage(ctx context.Context, p CopyMessageParams) (int64, error) {
	var r struct {
		MessageID int64 `json:"message_id"`
	}
	if err := c.Call(ctx, "copyMessage", p, &r); err != nil {
		return 0, err
	}
	return r.MessageID, nil
}

type EditMessageTextParams struct {
	ChatID      int64                 `json:"chat_id"`
	MessageID   int64                 `json:"message_id"`
	Text        string                `json:"text"`
	ParseMode   string                `json:"parse_mode,omitempty"`
	ReplyMarkup *InlineKeyboardMarkup `json:"reply_markup,omitempty"`
}

// EditMessageText treats "message is not modified" as success.
func (c *Client) EditMessageText(ctx context.Context, p EditMessageTextParams) error {
	if err := c.Call(ctx, "editMessageText", p, nil); err != nil && !isNotModified(err) {
		return err
	}
	return nil
}

// EditMessageReplyMarkup treats "message is not modified" as success.
func (c *Client) EditMessageReplyMarkup(ctx context.Context, chatID, messageID int64, markup *InlineKeyboardMarkup) error {
	err := c.Call(ctx, "editMessageReplyMarkup", map[string]any{
		"chat_id":      chatID,
		"message_id":   messageID,
		"reply_markup": markup,
	}, nil)
	if err != nil && !isNotModified(err) {
		return err
	}
	return nil
}

func (c *Client) AnswerCallbackQuery(ctx context.Context, id, text string, alert bool) error {
	return c.Call(ctx, "answerCallbackQuery", map[string]any{
		"callback_query_id": id,
		"text":              text,
		"show_alert":        alert,
	}, nil)
}

// GetChat accepts a numeric chat ID or an "@username" string.
func (c *Client) GetChat(ctx context.Context, chatID any) (*Chat, error) {
	var chat Chat
	if err := c.Call(ctx, "getChat", map[string]any{"chat_id": chatID}, &chat); err != nil {
		return nil, err
	}
	return &chat, nil
}

func (c *Client) GetChatMember(ctx context.Context, chatID, userID int64) (*ChatMember, error) {
	var m ChatMember
	if err := c.Call(ctx, "getChatMember", map[string]any{"chat_id": chatID, "user_id": userID}, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func (c *Client) SetMessageReaction(ctx context.Context, chatID, messageID int64, emoji string) error {
	return c.Call(ctx, "setMessageReaction", map[string]any{
		"chat_id":    chatID,
		"message_id": messageID,
		"reaction":   []map[string]string{{"type": "emoji", "emoji": emoji}},
	}, nil)
}

func (c *Client) SetMyCommands(ctx context.Context, commands []BotCommand, scope BotCommandScope) error {
	return c.Call(ctx, "setMyCommands", map[string]any{"commands": commands, "scope": scope}, nil)
}

func (c *Client) DeleteMyCommands(ctx context.Context, scope BotCommandScope) error {
	return c.Call(ctx, "deleteMyCommands", map[string]any{"scope": scope}, nil)
}

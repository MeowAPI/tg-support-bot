// Package telegram is a minimal client for the parts of the Telegram Bot API
// the support bot needs.
package telegram

import (
	"encoding/json"
	"strings"
)

// AllowedUpdates lists the update types the bot subscribes to.
var AllowedUpdates = []string{"message", "callback_query", "my_chat_member"}

type Update struct {
	UpdateID      int64              `json:"update_id"`
	Message       *Message           `json:"message,omitempty"`
	CallbackQuery *CallbackQuery     `json:"callback_query,omitempty"`
	MyChatMember  *ChatMemberUpdated `json:"my_chat_member,omitempty"`
}

type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot,omitempty"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name,omitempty"`
	Username  string `json:"username,omitempty"`
}

// FullName joins the first and last name.
func (u *User) FullName() string {
	return strings.TrimSpace(u.FirstName + " " + u.LastName)
}

type Chat struct {
	ID       int64  `json:"id"`
	Type     string `json:"type"`
	Title    string `json:"title,omitempty"`
	Username string `json:"username,omitempty"`
}

type Message struct {
	MessageID int64 `json:"message_id"`
	From      *User `json:"from,omitempty"`
	Chat      Chat  `json:"chat"`
	// Date is 0 for an InaccessibleMessage inside a callback query.
	Date int64  `json:"date"`
	Text string `json:"text,omitempty"`
	// Entities is passed through untouched so formatting survives a round trip.
	Entities        json.RawMessage       `json:"entities,omitempty"`
	ReplyToMessage  *Message              `json:"reply_to_message,omitempty"`
	ForwardOrigin   *MessageOrigin        `json:"forward_origin,omitempty"`
	ReplyMarkup     *InlineKeyboardMarkup `json:"reply_markup,omitempty"`
	MigrateToChatID int64                 `json:"migrate_to_chat_id,omitempty"`
}

// MessageOrigin describes where a forwarded message came from.
type MessageOrigin struct {
	Type           string `json:"type"`
	SenderUser     *User  `json:"sender_user,omitempty"`
	SenderUserName string `json:"sender_user_name,omitempty"`
}

type CallbackQuery struct {
	ID      string   `json:"id"`
	From    User     `json:"from"`
	Message *Message `json:"message,omitempty"`
	Data    string   `json:"data,omitempty"`
}

type ChatMemberUpdated struct {
	Chat          Chat       `json:"chat"`
	From          User       `json:"from"`
	OldChatMember ChatMember `json:"old_chat_member"`
	NewChatMember ChatMember `json:"new_chat_member"`
}

type ChatMember struct {
	Status string `json:"status"`
	User   User   `json:"user"`
	// CanSendMessages is only present for restricted members.
	CanSendMessages *bool `json:"can_send_messages,omitempty"`
}

type InlineKeyboardMarkup struct {
	InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"`
}

type InlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data,omitempty"`
}

type ReplyParameters struct {
	MessageID                int64 `json:"message_id"`
	AllowSendingWithoutReply bool  `json:"allow_sending_without_reply,omitempty"`
}

type BotCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

type BotCommandScope struct {
	Type   string `json:"type"`
	ChatID int64  `json:"chat_id,omitempty"`
}

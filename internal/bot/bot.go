// Package bot implements a Telegram support desk: private messages from users
// are copied into a staff group, and staff replies to those copies are copied
// back to the user.
package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log/slog"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/MeowAPI/tg-support-bot/internal/store"
	"github.com/MeowAPI/tg-support-bot/internal/telegram"
)

const (
	pollTimeout = 50 // seconds
	pendingTTL  = 10 * time.Minute
)

// Settings keys owned by this package.
const (
	keyWelcome = "welcome"
	keyAck     = "ack"
	keyAckOff  = "ack_off"
)

const (
	defaultWelcome = "👋 你好！请直接发送你的问题（文字、图片、文件都可以），客服会尽快回复你。"
	defaultAck     = "✅ 消息已转交客服，请耐心等待回复。"
)

// Config tunes the bot's behaviour.
type Config struct {
	// SuperAdmins come from the environment and cannot be removed in the panel.
	SuperAdmins []int64
	// AckCooldown is the minimum gap between two auto-replies to one user.
	AckCooldown time.Duration
	// LinkRetention is how long reply mappings are kept.
	LinkRetention time.Duration
}

type Bot struct {
	api   *telegram.Client
	store *store.Store
	cfg   Config
	log   *slog.Logger
	super map[int64]bool
	me    telegram.User

	// Updates are handled one at a time on the polling goroutine, so the maps
	// below need no locking.
	pending map[int64]pendingInput // admin ID → prompt page awaiting input
	lastAck map[int64]time.Time
}

type pendingInput struct {
	page    string
	expires time.Time
}

// richText is a stored message text with its formatting entities.
type richText struct {
	Text     string          `json:"text"`
	Entities json.RawMessage `json:"entities,omitempty"`
}

func New(api *telegram.Client, st *store.Store, cfg Config, log *slog.Logger) *Bot {
	if cfg.AckCooldown <= 0 {
		cfg.AckCooldown = 10 * time.Minute
	}
	if cfg.LinkRetention <= 0 {
		cfg.LinkRetention = 90 * 24 * time.Hour
	}
	super := make(map[int64]bool, len(cfg.SuperAdmins))
	var ids []int64
	for _, id := range cfg.SuperAdmins {
		if !super[id] {
			super[id] = true
			ids = append(ids, id)
		}
	}
	cfg.SuperAdmins = ids
	return &Bot{
		api:     api,
		store:   st,
		cfg:     cfg,
		log:     log,
		super:   super,
		pending: make(map[int64]pendingInput),
		lastAck: make(map[int64]time.Time),
	}
}

// Run long-polls for updates until ctx is cancelled.
func (b *Bot) Run(ctx context.Context) error {
	if err := b.init(ctx); err != nil {
		return err
	}
	b.log.Info("bot started", "username", b.me.Username, "id", b.me.ID, "super_admins", len(b.super))
	if len(b.super) == 0 {
		b.log.Warn("ADMIN_IDS is empty, so nobody can open the admin panel; send /id to the bot to learn your user ID")
	}
	go b.pruneLoop(ctx)

	var offset int64
	for ctx.Err() == nil {
		updates, err := b.api.GetUpdates(ctx, offset, pollTimeout)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			b.log.Warn("getUpdates failed", "err", err)
			sleepCtx(ctx, 3*time.Second)
			continue
		}
		for _, u := range updates {
			offset = u.UpdateID + 1
			b.dispatch(ctx, u)
		}
	}
	if offset != 0 {
		// Confirm what was handled so a restart does not replay it.
		cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, _ = b.api.GetUpdates(cctx, offset, 0)
		cancel()
	}
	return nil
}

func (b *Bot) init(ctx context.Context) error {
	me, err := b.api.GetMe(ctx)
	if err != nil {
		return fmt.Errorf("getMe (check BOT_TOKEN): %w", err)
	}
	b.me = *me
	if err := b.api.DeleteWebhook(ctx); err != nil {
		return fmt.Errorf("deleteWebhook: %w", err)
	}
	b.setupCommands(ctx)
	return nil
}

func (b *Bot) dispatch(parent context.Context, u telegram.Update) {
	// Finish the update even during shutdown so it is not half-handled.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), time.Minute)
	defer cancel()
	defer func() {
		if r := recover(); r != nil {
			b.log.Error("panic while handling update", "update_id", u.UpdateID, "panic", r, "stack", string(debug.Stack()))
		}
	}()
	switch {
	case u.Message != nil:
		b.onMessage(ctx, u.Message)
	case u.CallbackQuery != nil:
		b.onCallback(ctx, u.CallbackQuery)
	case u.MyChatMember != nil:
		b.onMyChatMember(ctx, u.MyChatMember)
	}
}

func (b *Bot) onCallback(ctx context.Context, q *telegram.CallbackQuery) {
	kind, arg, _ := strings.Cut(q.Data, ":")
	switch kind {
	case "adm":
		b.onAdminCallback(ctx, q, arg)
	case "u", "b", "bc", "bx", "ub":
		b.onUserCallback(ctx, q, kind, arg)
	default:
		b.answer(ctx, q, "", false)
	}
}

func (b *Bot) setupCommands(ctx context.Context) {
	if err := b.api.SetMyCommands(ctx, userCommands, telegram.BotCommandScope{Type: "default"}); err != nil {
		b.log.Warn("setMyCommands (default) failed", "err", err)
	}
	if err := b.api.SetMyCommands(ctx, groupCommands, telegram.BotCommandScope{Type: "all_group_chats"}); err != nil {
		b.log.Warn("setMyCommands (groups) failed", "err", err)
	}
	for _, id := range b.adminIDs() {
		b.setAdminCommands(ctx, id)
	}
}

var (
	userCommands = []telegram.BotCommand{
		{Command: "start", Description: "开始咨询"},
		{Command: "id", Description: "查看我的用户 ID"},
	}
	adminCommands = append(append([]telegram.BotCommand{}, userCommands...),
		telegram.BotCommand{Command: "admin", Description: "打开管理面板"},
		telegram.BotCommand{Command: "cancel", Description: "取消当前操作"},
	)
	groupCommands = []telegram.BotCommand{
		{Command: "id", Description: "查看本群 ID"},
		{Command: "bind", Description: "将本群设为客服群组（管理员）"},
		{Command: "info", Description: "回复用户消息：查看用户信息"},
		{Command: "ban", Description: "回复用户消息：封禁用户"},
		{Command: "unban", Description: "回复用户消息：解除封禁"},
	}
)

// setAdminCommands shows the admin commands in that admin's private chat. It
// fails harmlessly for admins who have never started the bot.
func (b *Bot) setAdminCommands(ctx context.Context, id int64) {
	if err := b.api.SetMyCommands(ctx, adminCommands, telegram.BotCommandScope{Type: "chat", ChatID: id}); err != nil {
		b.log.Debug("setMyCommands for admin failed", "admin_id", id, "err", err)
	}
}

func (b *Bot) isAdmin(id int64) bool {
	if b.super[id] {
		return true
	}
	ok, err := b.store.IsAdmin(id)
	if err != nil {
		b.log.Error("admin lookup failed", "user_id", id, "err", err)
	}
	return ok
}

// adminIDs returns every admin, configured ones first.
func (b *Bot) adminIDs() []int64 {
	ids := append([]int64{}, b.cfg.SuperAdmins...)
	admins, err := b.store.Admins()
	if err != nil {
		b.log.Error("list admins failed", "err", err)
	}
	for _, a := range admins {
		if !b.super[a.UserID] {
			ids = append(ids, a.UserID)
		}
	}
	return ids
}

func (b *Bot) notifyAdmins(ctx context.Context, text string) {
	for _, id := range b.adminIDs() {
		if _, err := b.send(ctx, id, text, nil); err != nil {
			b.log.Debug("notify admin failed", "admin_id", id, "err", err)
		}
	}
}

func (b *Bot) groupID() int64 {
	id, _, err := b.store.Group()
	if err != nil {
		b.log.Error("load support group failed", "err", err)
	}
	return id
}

// pendingFor returns the prompt an admin is answering, if any.
func (b *Bot) pendingFor(id int64) (string, bool) {
	p, ok := b.pending[id]
	if !ok {
		return "", false
	}
	if time.Now().After(p.expires) {
		delete(b.pending, id)
		return "", false
	}
	return p.page, true
}

// command parses a leading bot command. name is lower-cased and empty when
// the text is not a command; foreign is true for commands addressed to
// another bot ("/start@other_bot").
func (b *Bot) command(m *telegram.Message) (name, args string, foreign bool) {
	if !strings.HasPrefix(m.Text, "/") {
		return "", "", false
	}
	head, rest := m.Text, ""
	if i := strings.IndexFunc(m.Text, unicode.IsSpace); i >= 0 {
		head, rest = m.Text[:i], m.Text[i:]
	}
	name = head[1:]
	if at := strings.IndexByte(name, '@'); at >= 0 {
		if !strings.EqualFold(name[at+1:], b.me.Username) {
			return "", "", true
		}
		name = name[:at]
	}
	return strings.ToLower(name), strings.TrimSpace(rest), false
}

func (b *Bot) send(ctx context.Context, chatID int64, text string, kb *telegram.InlineKeyboardMarkup) (*telegram.Message, error) {
	return b.api.SendMessage(ctx, telegram.SendMessageParams{ChatID: chatID, Text: text, ParseMode: "HTML", ReplyMarkup: kb})
}

// reply answers m with an HTML message, logging failures.
func (b *Bot) reply(ctx context.Context, m *telegram.Message, text string) {
	_, err := b.api.SendMessage(ctx, telegram.SendMessageParams{
		ChatID:          m.Chat.ID,
		Text:            text,
		ParseMode:       "HTML",
		ReplyParameters: &telegram.ReplyParameters{MessageID: m.MessageID, AllowSendingWithoutReply: true},
	})
	if err != nil {
		b.log.Warn("reply failed", "chat_id", m.Chat.ID, "err", err)
	}
}

func (b *Bot) answer(ctx context.Context, q *telegram.CallbackQuery, text string, alert bool) {
	if err := b.api.AnswerCallbackQuery(ctx, q.ID, truncate(text, 200), alert); err != nil {
		b.log.Debug("answerCallbackQuery failed", "err", err)
	}
}

func (b *Bot) loadText(key, fallback string) richText {
	raw, err := b.store.Setting(key)
	if err != nil {
		b.log.Error("load setting failed", "key", key, "err", err)
	}
	var t richText
	if raw == "" || json.Unmarshal([]byte(raw), &t) != nil || t.Text == "" {
		return richText{Text: fallback}
	}
	return t
}

func (b *Bot) saveText(key string, m *telegram.Message) error {
	raw, err := json.Marshal(richText{Text: m.Text, Entities: m.Entities})
	if err != nil {
		return err
	}
	return b.store.SetSetting(key, string(raw))
}

func (b *Bot) sendRich(ctx context.Context, chatID int64, t richText) {
	if _, err := b.api.SendMessage(ctx, telegram.SendMessageParams{ChatID: chatID, Text: t.Text, Entities: t.Entities}); err != nil {
		b.log.Warn("send message failed", "chat_id", chatID, "err", err)
	}
}

func (b *Bot) ackOff() bool {
	v, err := b.store.Setting(keyAckOff)
	if err != nil {
		b.log.Error("load setting failed", "key", keyAckOff, "err", err)
	}
	return v == "1"
}

// userName returns a known user's display name, or "" when unknown.
func (b *Bot) userName(uid int64) string {
	u, err := b.store.User(uid)
	if err != nil {
		b.log.Error("load user failed", "user_id", uid, "err", err)
	}
	if u == nil {
		return ""
	}
	return displayName(u.FirstName, u.LastName, u.Username)
}

// userLabel is userName with the numeric ID as fallback.
func (b *Bot) userLabel(uid int64) string {
	if name := b.userName(uid); name != "" {
		return name
	}
	return strconv.FormatInt(uid, 10)
}

func (b *Bot) pruneLoop(ctx context.Context) {
	t := time.NewTicker(6 * time.Hour)
	defer t.Stop()
	for {
		n, err := b.store.PruneLinks(time.Now().Add(-b.cfg.LinkRetention))
		if err != nil {
			b.log.Warn("prune links failed", "err", err)
		} else if n > 0 {
			b.log.Info("pruned old message links", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func displayName(first, last, username string) string {
	name := truncate(strings.TrimSpace(first+" "+last), 24)
	switch {
	case name != "" && username != "":
		return name + " @" + username
	case name != "":
		return name
	case username != "":
		return "@" + username
	}
	return ""
}

func esc(s string) string { return html.EscapeString(s) }

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

func fmtTime(t time.Time) string {
	if t.IsZero() || t.Unix() == 0 {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04")
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func btn(text, data string) telegram.InlineKeyboardButton {
	return telegram.InlineKeyboardButton{Text: text, CallbackData: data}
}

func row(buttons ...telegram.InlineKeyboardButton) []telegram.InlineKeyboardButton { return buttons }

func keyboard(rows ...[]telegram.InlineKeyboardButton) *telegram.InlineKeyboardMarkup {
	return &telegram.InlineKeyboardMarkup{InlineKeyboard: rows}
}

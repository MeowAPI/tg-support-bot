package bot

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/MeowAPI/tg-support-bot/internal/telegram"
)

// Panel pages. Pages listed in promptParent wait for the admin to type
// something; the admin has a pending input exactly while one is shown.
const (
	pageMain        = "main"
	pageGroup       = "group"
	pageAdmins      = "admins"
	pageAddAdmin    = "addadmin"
	pageBans        = "bans"
	pageBan         = "ban"
	pageWelcome     = "welcome"
	pageWelcomeEdit = "welcome_edit"
	pageAck         = "ack"
	pageAckEdit     = "ack_edit"
)

// promptParent maps each prompt page to the page shown once its input is
// accepted.
var promptParent = map[string]string{
	pageGroup:       pageMain,
	pageAddAdmin:    pageAdmins,
	pageBan:         pageBans,
	pageWelcomeEdit: pageWelcome,
	pageAckEdit:     pageAck,
}

const retryHint = "\n\n请重新发送，或发送 /cancel 取消。"

// listLimit caps list pages so the keyboard stays within Telegram's limits.
const listLimit = 20

// showPanel renders page into the panel message msgID, or into a new message
// when msgID is 0 or the edit fails. In a private chat the chat ID is the
// admin's user ID.
func (b *Bot) showPanel(ctx context.Context, chatID, msgID int64, page string) {
	if _, ok := promptParent[page]; ok {
		b.pending[chatID] = pendingInput{page: page, expires: time.Now().Add(pendingTTL)}
	} else {
		delete(b.pending, chatID)
	}
	text, kb := b.renderPage(page)
	if msgID != 0 {
		err := b.api.EditMessageText(ctx, telegram.EditMessageTextParams{
			ChatID: chatID, MessageID: msgID, Text: text, ParseMode: "HTML", ReplyMarkup: kb,
		})
		if err == nil {
			return
		}
		b.log.Debug("edit panel failed, sending a new one", "err", err)
	}
	if _, err := b.send(ctx, chatID, text, kb); err != nil {
		b.log.Warn("send panel failed", "chat_id", chatID, "err", err)
	}
}

func (b *Bot) onAdminCallback(ctx context.Context, q *telegram.CallbackQuery, data string) {
	if !b.isAdmin(q.From.ID) {
		b.answer(ctx, q, "⛔ 无权限", true)
		return
	}
	m := q.Message
	if m == nil || m.Date == 0 || m.Chat.Type != "private" {
		b.answer(ctx, q, "面板已失效，请重新发送 /admin", true)
		return
	}
	action, arg, _ := strings.Cut(data, ":")
	page, notice, alert := action, "", false
	switch action {
	case pageMain, pageGroup, pageAdmins, pageAddAdmin, pageBans, pageBan,
		pageWelcome, pageWelcomeEdit, pageAck, pageAckEdit:
		// Plain navigation.
	case "check":
		page, notice, alert = pageGroup, b.checkGroup(ctx), true
	case "unbind":
		page, notice = pageGroup, "已解除客服群组绑定"
		if err := b.store.SetGroup(0, ""); err != nil {
			b.log.Error("clear support group failed", "err", err)
			notice = "❌ 操作失败"
		}
	case "deladmin":
		page, notice = pageAdmins, b.removeAdmin(ctx, arg)
	case "unban":
		page = pageBans
		if uid, err := strconv.ParseInt(arg, 10, 64); err == nil {
			notice = b.unban(uid)
		}
	case "welcome_reset":
		page, notice = pageWelcome, b.resetSetting(keyWelcome)
	case "ack_reset":
		page, notice = pageAck, b.resetSetting(keyAck)
	case "ack_toggle":
		page, notice = pageAck, "已关闭自动回复"
		var err error
		if b.ackOff() {
			notice = "已开启自动回复"
			err = b.store.DeleteSetting(keyAckOff)
		} else {
			err = b.store.SetSetting(keyAckOff, "1")
		}
		if err != nil {
			b.log.Error("toggle auto-reply failed", "err", err)
			notice = "❌ 操作失败"
		}
	case "close":
		delete(b.pending, m.Chat.ID)
		b.answer(ctx, q, "", false)
		if err := b.api.EditMessageText(ctx, telegram.EditMessageTextParams{
			ChatID: m.Chat.ID, MessageID: m.MessageID, Text: "管理面板已关闭，发送 /admin 重新打开。",
		}); err != nil {
			b.log.Debug("close panel failed", "err", err)
		}
		return
	default:
		page = pageMain
	}
	b.answer(ctx, q, notice, alert)
	b.showPanel(ctx, m.Chat.ID, m.MessageID, page)
}

// onAdminInput handles a message typed while a prompt page is open.
func (b *Bot) onAdminInput(ctx context.Context, m *telegram.Message, page string) {
	switch page {
	case pageGroup:
		b.submitGroup(ctx, m, m.Text)

	case pageAddAdmin:
		uid, _, err := b.parseTarget(m)
		switch {
		case err != nil:
			b.reply(ctx, m, "❌ "+esc(err.Error())+retryHint)
			return
		case b.isAdmin(uid):
			b.reply(ctx, m, "该用户已经是管理员。"+retryHint)
			return
		}
		if err := b.store.AddAdmin(uid, m.From.ID); err != nil {
			b.log.Error("add admin failed", "err", err)
			b.reply(ctx, m, "❌ 添加失败，请查看日志。")
			return
		}
		b.log.Info("admin added", "user_id", uid, "by", m.From.ID)
		b.setAdminCommands(ctx, uid)
		b.reply(ctx, m, "✅ 已添加管理员 "+esc(b.userLabel(uid)))
		b.showPanel(ctx, m.Chat.ID, 0, pageAdmins)

	case pageBan:
		uid, reason, err := b.parseTarget(m)
		if err != nil {
			b.reply(ctx, m, "❌ "+esc(err.Error())+retryHint)
			return
		}
		text, ok := b.ban(uid, m.From.ID, reason)
		if !ok {
			b.reply(ctx, m, esc(text)+retryHint)
			return
		}
		b.reply(ctx, m, esc(text))
		b.showPanel(ctx, m.Chat.ID, 0, pageBans)

	case pageWelcomeEdit, pageAckEdit:
		if strings.TrimSpace(m.Text) == "" {
			b.reply(ctx, m, "请发送一条文字消息。"+retryHint)
			return
		}
		key := keyWelcome
		if page == pageAckEdit {
			key = keyAck
		}
		if err := b.saveText(key, m); err != nil {
			b.log.Error("save text failed", "key", key, "err", err)
			b.reply(ctx, m, "❌ 保存失败，请查看日志。")
			return
		}
		b.reply(ctx, m, "✅ 已保存。")
		b.showPanel(ctx, m.Chat.ID, 0, promptParent[page])
	}
}

// submitGroup binds the group an admin typed in (ID or @username).
func (b *Bot) submitGroup(ctx context.Context, m *telegram.Message, input string) {
	chat, err := b.bindGroup(ctx, input)
	if err != nil {
		text := "❌ " + esc(err.Error())
		if _, ok := b.pending[m.Chat.ID]; ok {
			text += retryHint
		}
		b.reply(ctx, m, text)
		return
	}
	b.reply(ctx, m, fmt.Sprintf("✅ 客服群组已设置为 <b>%s</b>（<code>%d</code>）", esc(chat.Title), chat.ID))
	b.showPanel(ctx, m.Chat.ID, 0, pageMain)
}

// bindGroup resolves input to a group and makes it the support group.
func (b *Bot) bindGroup(ctx context.Context, input string) (*telegram.Chat, error) {
	input = strings.TrimSpace(input)
	var candidates []any
	if strings.HasPrefix(input, "@") {
		candidates = []any{input}
	} else {
		n, err := strconv.ParseInt(input, 10, 64)
		if err != nil || n == 0 {
			return nil, errors.New("格式不正确，请发送数字群组 ID（例如 -1001234567890）")
		}
		if n < 0 {
			candidates = []any{n}
		} else {
			// Group IDs are negative; people often copy them without the
			// "-100" supergroup prefix or the minus sign.
			if v, err := strconv.ParseInt("-100"+input, 10, 64); err == nil {
				candidates = append(candidates, v)
			}
			candidates = append(candidates, -n)
		}
	}
	var chat *telegram.Chat
	for _, c := range candidates {
		got, err := b.api.GetChat(ctx, c)
		if err == nil {
			chat = got
			break
		}
		b.log.Debug("getChat failed", "chat_id", c, "err", err)
	}
	if chat == nil {
		return nil, errors.New("找不到该群组：请确认 ID 正确，并且已经把机器人拉进群")
	}
	if err := b.useGroup(ctx, chat); err != nil {
		return nil, err
	}
	return chat, nil
}

// useGroup checks the bot can post in chat, announces the binding there and
// saves it.
func (b *Bot) useGroup(ctx context.Context, chat *telegram.Chat) error {
	if chat.Type != "group" && chat.Type != "supergroup" {
		return errors.New("该 ID 不是群组（可能是用户或频道）")
	}
	member, err := b.api.GetChatMember(ctx, chat.ID, b.me.ID)
	if err != nil {
		return fmt.Errorf("无法获取机器人在群内的状态：%s", telegram.Description(err))
	}
	switch {
	case !inChat(member.Status):
		return errors.New("机器人不在该群组中，请先把机器人拉进群")
	case member.Status == "restricted" && member.CanSendMessages != nil && !*member.CanSendMessages:
		return errors.New("机器人在该群组被禁言，请解除限制后重试")
	}
	if _, err := b.send(ctx, chat.ID,
		"✅ 本群已设置为客服群组。\n\n用户发给机器人的消息会转发到这里，直接<b>回复</b>转发过来的消息即可回复用户。", nil); err != nil {
		return fmt.Errorf("机器人无法在该群组发言：%s", telegram.Description(err))
	}
	if err := b.store.SetGroup(chat.ID, chat.Title); err != nil {
		b.log.Error("save support group failed", "err", err)
		return errors.New("保存失败，请查看日志")
	}
	b.log.Info("support group set", "chat_id", chat.ID, "title", chat.Title)
	return nil
}

// checkGroup reports, as plain text, whether the bot can reach the group.
func (b *Bot) checkGroup(ctx context.Context) string {
	gid, title, err := b.store.Group()
	if err != nil || gid == 0 {
		return "尚未设置客服群组。"
	}
	chat, err := b.api.GetChat(ctx, gid)
	if err != nil {
		return "❌ 无法访问群组：" + telegram.Description(err)
	}
	member, err := b.api.GetChatMember(ctx, gid, b.me.ID)
	if err != nil {
		return "❌ 无法获取机器人状态：" + telegram.Description(err)
	}
	if !inChat(member.Status) {
		return "❌ 机器人已不在群组中，请重新拉进群。"
	}
	if chat.Title != title {
		if err := b.store.SetGroup(gid, chat.Title); err != nil {
			b.log.Warn("update group title failed", "err", err)
		}
	}
	return fmt.Sprintf("✅ 连接正常\n群组：%s\n机器人身份：%s", chat.Title, memberStatus(member.Status))
}

func (b *Bot) removeAdmin(ctx context.Context, arg string) string {
	uid, err := strconv.ParseInt(arg, 10, 64)
	if err != nil {
		return ""
	}
	if b.super[uid] {
		return "该管理员来自 ADMIN_IDS 配置，无法在这里移除。"
	}
	if err := b.store.RemoveAdmin(uid); err != nil {
		b.log.Error("remove admin failed", "err", err)
		return "❌ 操作失败"
	}
	b.log.Info("admin removed", "user_id", uid)
	delete(b.pending, uid)
	if err := b.api.DeleteMyCommands(ctx, telegram.BotCommandScope{Type: "chat", ChatID: uid}); err != nil {
		b.log.Debug("deleteMyCommands failed", "err", err)
	}
	return "已移除管理员 " + b.userLabel(uid)
}

func (b *Bot) resetSetting(key string) string {
	if err := b.store.DeleteSetting(key); err != nil {
		b.log.Error("reset setting failed", "key", key, "err", err)
		return "❌ 操作失败"
	}
	return "已恢复默认"
}

// parseTarget reads a user from a forwarded message or from "<id> [reason]".
func (b *Bot) parseTarget(m *telegram.Message) (int64, string, error) {
	if o := m.ForwardOrigin; o != nil {
		if o.Type == "user" && o.SenderUser != nil {
			u := o.SenderUser
			if err := b.store.TouchUser(u.ID, u.FirstName, u.LastName, u.Username, 0); err != nil {
				b.log.Warn("save user failed", "err", err)
			}
			return u.ID, "", nil
		}
		return 0, "", errors.New("对方隐藏了转发来源，请改为发送数字用户 ID")
	}
	fields := strings.Fields(m.Text)
	if len(fields) > 0 {
		if uid, err := strconv.ParseInt(fields[0], 10, 64); err == nil && uid > 0 {
			return uid, strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(m.Text), fields[0])), nil
		}
	}
	return 0, "", errors.New("请发送数字用户 ID，或转发一条该用户的消息")
}

func (b *Bot) renderPage(page string) (string, *telegram.InlineKeyboardMarkup) {
	switch page {
	case pageGroup:
		return b.pageGroup()
	case pageAdmins:
		return b.pageAdmins()
	case pageAddAdmin:
		return "👮 <b>添加管理员</b>\n\n请发送对方的<b>用户 ID</b>，或直接<b>转发</b>一条对方的消息给我。\n\n" +
				"对方私聊机器人发送 /id 即可查看自己的 ID。\n发送 /cancel 取消。",
			keyboard(row(btn("« 返回", "adm:"+pageAdmins)))
	case pageBans:
		return b.pageBans()
	case pageBan:
		return "🚫 <b>封禁用户</b>\n\n请发送要封禁的<b>用户 ID</b>（可在后面加空格写原因），或<b>转发</b>一条该用户的消息。\n\n" +
				"被封禁用户的消息不会再转发到客服群。\n发送 /cancel 取消。",
			keyboard(row(btn("« 返回", "adm:"+pageBans)))
	case pageWelcome:
		t := b.loadText(keyWelcome, defaultWelcome)
		return "👋 <b>欢迎语</b>\n\n用户发送 /start 时回复：\n<blockquote>" + esc(truncate(t.Text, 800)) + "</blockquote>",
			keyboard(
				row(btn("✏️ 修改", "adm:"+pageWelcomeEdit), btn("♻️ 恢复默认", "adm:welcome_reset")),
				row(btn("« 返回", "adm:"+pageMain)),
			)
	case pageWelcomeEdit:
		return "✏️ <b>修改欢迎语</b>\n\n请直接发送新的欢迎语，支持粗体、链接等格式。\n发送 /cancel 取消。",
			keyboard(row(btn("« 返回", "adm:"+pageWelcome)))
	case pageAck:
		return b.pageAck()
	case pageAckEdit:
		return "✏️ <b>修改自动回复</b>\n\n请直接发送新的自动回复内容，支持粗体、链接等格式。\n发送 /cancel 取消。",
			keyboard(row(btn("« 返回", "adm:"+pageAck)))
	}
	return b.pageMain()
}

func (b *Bot) pageMain() (string, *telegram.InlineKeyboardMarkup) {
	bans, err := b.store.CountBans()
	if err != nil {
		b.log.Error("count bans failed", "err", err)
	}
	ack := "🔔 开启"
	if b.ackOff() {
		ack = "🔕 关闭"
	}
	text := fmt.Sprintf("🛠 <b>客服机器人管理面板</b>\n\n📍 客服群组：%s\n👮 管理员：%d 人\n🚫 黑名单：%d 人\n💬 自动回复：%s",
		b.groupLabel(), len(b.adminIDs()), bans, ack)
	return text, keyboard(
		row(btn("📍 设置客服群组", "adm:"+pageGroup)),
		row(btn("👮 管理员", "adm:"+pageAdmins), btn("🚫 黑名单", "adm:"+pageBans)),
		row(btn("👋 欢迎语", "adm:"+pageWelcome), btn("💬 自动回复", "adm:"+pageAck)),
		row(btn("✖️ 关闭", "adm:close")),
	)
}

func (b *Bot) pageGroup() (string, *telegram.InlineKeyboardMarkup) {
	text := "📍 <b>设置客服群组</b>\n\n当前：" + b.groupLabel() + "\n\n" +
		"请直接发送<b>群组 ID</b>（例如 <code>-1001234567890</code>），公开群也可以发送 @用户名。\n\n" +
		"获取群组 ID：先把机器人拉进群，在群里发送 /id。\n" +
		"也可以在目标群里直接发送 /bind 一键绑定。\n\n发送 /cancel 取消。"
	var rows [][]telegram.InlineKeyboardButton
	if b.groupID() != 0 {
		rows = append(rows, row(btn("🔍 检测连通性", "adm:check"), btn("🗑 解除绑定", "adm:unbind")))
	}
	rows = append(rows, row(btn("« 返回", "adm:"+pageMain)))
	return text, keyboard(rows...)
}

func (b *Bot) pageAdmins() (string, *telegram.InlineKeyboardMarkup) {
	var sb strings.Builder
	sb.WriteString("👮 <b>管理员</b>\n\n")
	for _, id := range b.cfg.SuperAdmins {
		fmt.Fprintf(&sb, "🔒 <code>%d</code> %s\n", id, esc(b.userName(id)))
	}
	admins, err := b.store.Admins()
	if err != nil {
		b.log.Error("list admins failed", "err", err)
	}
	var rows [][]telegram.InlineKeyboardButton
	for _, a := range admins {
		if b.super[a.UserID] {
			continue
		}
		fmt.Fprintf(&sb, "• <code>%d</code> %s\n", a.UserID, esc(b.userName(a.UserID)))
		if len(rows) < listLimit {
			rows = append(rows, row(btn("❌ 移除 "+truncate(b.userLabel(a.UserID), 24), fmt.Sprintf("adm:deladmin:%d", a.UserID))))
		}
	}
	if len(b.cfg.SuperAdmins) == 0 && len(rows) == 0 {
		sb.WriteString("暂无管理员。\n")
	}
	sb.WriteString("\n🔒 来自 ADMIN_IDS 环境变量，只能通过修改配置移除。")
	rows = append(rows, row(btn("➕ 添加管理员", "adm:"+pageAddAdmin)), row(btn("« 返回", "adm:"+pageMain)))
	return sb.String(), keyboard(rows...)
}

func (b *Bot) pageBans() (string, *telegram.InlineKeyboardMarkup) {
	total, err := b.store.CountBans()
	if err != nil {
		b.log.Error("count bans failed", "err", err)
	}
	bans, err := b.store.Bans(listLimit)
	if err != nil {
		b.log.Error("list bans failed", "err", err)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "🚫 <b>黑名单</b>（共 %d 人）\n\n", total)
	var rows [][]telegram.InlineKeyboardButton
	for _, ban := range bans {
		fmt.Fprintf(&sb, "• <code>%d</code> %s · %s", ban.UserID, esc(b.userName(ban.UserID)), fmtTime(ban.BannedAt))
		if ban.Reason != "" {
			sb.WriteString(" · " + esc(truncate(ban.Reason, 40)))
		}
		sb.WriteString("\n")
		rows = append(rows, row(btn("✅ 解封 "+truncate(b.userLabel(ban.UserID), 24), fmt.Sprintf("adm:unban:%d", ban.UserID))))
	}
	switch {
	case total == 0:
		sb.WriteString("黑名单为空。\n")
	case total > len(bans):
		fmt.Fprintf(&sb, "\n仅显示最近 %d 人。其他用户可在客服群里回复其消息发送 /unban 解封。\n", len(bans))
	}
	rows = append(rows, row(btn("➕ 封禁用户", "adm:"+pageBan)), row(btn("« 返回", "adm:"+pageMain)))
	return sb.String(), keyboard(rows...)
}

func (b *Bot) pageAck() (string, *telegram.InlineKeyboardMarkup) {
	t := b.loadText(keyAck, defaultAck)
	status, toggle := "🔔 开启", btn("🔕 关闭", "adm:ack_toggle")
	if b.ackOff() {
		status, toggle = "🔕 关闭", btn("🔔 开启", "adm:ack_toggle")
	}
	text := fmt.Sprintf("💬 <b>自动回复</b>\n\n状态：%s\n用户发消息后自动回复以下内容（同一用户 %s内只回复一次）：\n<blockquote>%s</blockquote>",
		status, humanDuration(b.cfg.AckCooldown), esc(truncate(t.Text, 800)))
	return text, keyboard(
		row(btn("✏️ 修改", "adm:"+pageAckEdit), toggle),
		row(btn("♻️ 恢复默认", "adm:ack_reset")),
		row(btn("« 返回", "adm:"+pageMain)),
	)
}

// groupLabel is HTML.
func (b *Bot) groupLabel() string {
	id, title, err := b.store.Group()
	if err != nil {
		b.log.Error("load support group failed", "err", err)
	}
	if id == 0 {
		return "未设置 ⚠️"
	}
	return fmt.Sprintf("<b>%s</b>（<code>%d</code>）", esc(title), id)
}

func memberStatus(s string) string {
	switch s {
	case "creator":
		return "群主"
	case "administrator":
		return "管理员"
	case "restricted":
		return "受限成员"
	}
	return "普通成员"
}

func humanDuration(d time.Duration) string {
	switch {
	case d >= time.Hour && d%time.Hour == 0:
		return fmt.Sprintf(" %d 小时", d/time.Hour)
	case d >= time.Minute:
		return fmt.Sprintf(" %d 分钟", d/time.Minute)
	}
	return fmt.Sprintf(" %d 秒", d/time.Second)
}

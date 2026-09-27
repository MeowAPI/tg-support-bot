package bot

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/MeowAPI/tg-support-bot/internal/store"
	"github.com/MeowAPI/tg-support-bot/internal/telegram"
)

func (b *Bot) onMessage(ctx context.Context, m *telegram.Message) {
	switch m.Chat.Type {
	case "private":
		if m.From != nil {
			b.onPrivate(ctx, m)
		}
	case "group", "supergroup":
		b.onGroup(ctx, m)
	}
}

func (b *Bot) onPrivate(ctx context.Context, m *telegram.Message) {
	uid := m.From.ID
	cmd, args, _ := b.command(m)
	admin := b.isAdmin(uid)

	if admin {
		switch cmd {
		case "admin":
			b.showPanel(ctx, m.Chat.ID, 0, pageMain)
			return
		case "cancel":
			if _, ok := b.pending[uid]; ok {
				delete(b.pending, uid)
				b.reply(ctx, m, "已取消。")
			} else {
				b.reply(ctx, m, "当前没有进行中的操作。")
			}
			return
		case "setgroup":
			if args == "" {
				b.showPanel(ctx, m.Chat.ID, 0, pageGroup)
			} else {
				b.submitGroup(ctx, m, args)
			}
			return
		case "":
			if page, ok := b.pendingFor(uid); ok {
				b.onAdminInput(ctx, m, page)
				return
			}
		}
	}

	switch cmd {
	case "start":
		b.sendRich(ctx, m.Chat.ID, b.loadText(keyWelcome, defaultWelcome))
		if admin {
			b.setAdminCommands(ctx, uid)
			b.reply(ctx, m, "🛠 你是管理员，发送 /admin 打开管理面板。")
		}
		return
	case "id":
		b.reply(ctx, m, fmt.Sprintf("你的用户 ID：<code>%d</code>", uid))
		return
	case "admin", "cancel", "setgroup":
		b.reply(ctx, m, "⛔ 仅管理员可用。")
		return
	}
	b.relayToGroup(ctx, m)
}

// relayToGroup copies a user's private message into the support group.
func (b *Bot) relayToGroup(ctx context.Context, m *telegram.Message) {
	u := m.From
	if banned, err := b.store.IsBanned(u.ID); err != nil {
		b.log.Error("ban lookup failed", "user_id", u.ID, "err", err)
	} else if banned {
		return
	}
	gid := b.groupID()
	if gid == 0 {
		b.reply(ctx, m, "⏳ 客服暂未开放，请稍后再试。")
		return
	}
	if err := b.store.TouchUser(u.ID, u.FirstName, u.LastName, u.Username, 1); err != nil {
		b.log.Error("save user failed", "user_id", u.ID, "err", err)
	}

	p := telegram.CopyMessageParams{
		ChatID:      gid,
		FromChatID:  m.Chat.ID,
		MessageID:   m.MessageID,
		ReplyMarkup: b.userKeyboard(u.ID),
	}
	if r := m.ReplyToMessage; r != nil {
		// The user answered something that has a counterpart in the group:
		// keep the thread there too.
		if l, err := b.store.LinkByUserMsg(u.ID, r.MessageID); err == nil && l != nil && l.GroupChatID == gid {
			p.ReplyParameters = &telegram.ReplyParameters{MessageID: l.GroupMsgID, AllowSendingWithoutReply: true}
		}
	}
	id, err := b.api.CopyMessage(ctx, p)
	var apiErr *telegram.Error
	if errors.As(err, &apiErr) && apiErr.MigrateTo != 0 {
		gid = b.migrateGroup(gid, apiErr.MigrateTo)
		p.ChatID, p.ReplyParameters = gid, nil
		id, err = b.api.CopyMessage(ctx, p)
	}
	if err != nil {
		b.log.Error("relay to group failed", "user_id", u.ID, "group_id", gid, "err", err)
		b.reply(ctx, m, "⚠️ 消息发送失败，请稍后再试。")
		return
	}
	if err := b.store.SaveLink(store.Link{
		GroupChatID: gid, GroupMsgID: id, UserID: u.ID, UserMsgID: m.MessageID, Kind: store.KindInbound,
	}); err != nil {
		b.log.Error("save link failed", "err", err)
	}
	b.maybeAck(ctx, u.ID)
}

// maybeAck sends the auto-reply, at most once per cooldown per user.
func (b *Bot) maybeAck(ctx context.Context, uid int64) {
	if b.ackOff() {
		return
	}
	now := time.Now()
	if last, ok := b.lastAck[uid]; ok && now.Sub(last) < b.cfg.AckCooldown {
		return
	}
	if len(b.lastAck) > 10000 {
		for id, t := range b.lastAck {
			if now.Sub(t) >= b.cfg.AckCooldown {
				delete(b.lastAck, id)
			}
		}
	}
	b.lastAck[uid] = now
	b.sendRich(ctx, uid, b.loadText(keyAck, defaultAck))
}

// migrateGroup follows a group → supergroup upgrade, which changes the chat
// ID, and returns the new ID.
func (b *Bot) migrateGroup(from, to int64) int64 {
	id, title, err := b.store.Group()
	if err == nil && id == from {
		if err := b.store.SetGroup(to, title); err != nil {
			b.log.Error("save migrated group failed", "err", err)
		} else {
			b.log.Info("support group migrated to supergroup", "from", from, "to", to)
		}
	}
	return to
}

func (b *Bot) onGroup(ctx context.Context, m *telegram.Message) {
	gid := b.groupID()
	if m.MigrateToChatID != 0 {
		if m.Chat.ID == gid {
			b.migrateGroup(gid, m.MigrateToChatID)
		}
		return
	}
	if m.From == nil {
		return
	}
	cmd, args, foreign := b.command(m)
	if foreign {
		return
	}
	switch cmd {
	case "id":
		text := fmt.Sprintf("本群 ID：<code>%d</code>\n你的用户 ID：<code>%d</code>", m.Chat.ID, m.From.ID)
		if m.Chat.ID == gid {
			text += "\n\n✅ 本群是当前的客服群组。"
		}
		b.reply(ctx, m, text)
		return
	case "bind":
		b.bindHere(ctx, m, gid)
		return
	}
	if gid == 0 || m.Chat.ID != gid || m.ReplyToMessage == nil {
		return
	}

	target := m.ReplyToMessage
	link, err := b.store.InboundLink(gid, target.MessageID)
	if err != nil {
		b.log.Error("link lookup failed", "err", err)
		return
	}
	if link == nil {
		// Only our forwarded copies carry a keyboard; tell staff when the
		// mapping for one of them is gone.
		if cmd == "" && target.From != nil && target.From.ID == b.me.ID && target.ReplyMarkup != nil {
			b.reply(ctx, m, "⚠️ 找不到这条消息对应的用户，记录可能已过期。")
		}
		return
	}
	switch cmd {
	case "info":
		b.reply(ctx, m, esc(b.userInfo(link.UserID)))
		return
	case "ban", "unban":
		text := b.unban(link.UserID)
		if cmd == "ban" {
			text, _ = b.ban(link.UserID, m.From.ID, args)
		}
		b.reply(ctx, m, esc(text))
		b.editMarkup(ctx, &target.Chat, target.MessageID, b.userKeyboard(link.UserID))
		return
	}
	b.relayToUser(ctx, m, link)
}

// relayToUser copies a staff reply to the user it answers.
func (b *Bot) relayToUser(ctx context.Context, m *telegram.Message, link *store.Link) {
	id, err := b.api.CopyMessage(ctx, telegram.CopyMessageParams{
		ChatID:          link.UserID,
		FromChatID:      m.Chat.ID,
		MessageID:       m.MessageID,
		ReplyParameters: &telegram.ReplyParameters{MessageID: link.UserMsgID, AllowSendingWithoutReply: true},
	})
	if err != nil {
		reason := telegram.Description(err)
		if telegram.IsForbidden(err) {
			reason = "用户已屏蔽机器人或删除了对话"
		}
		b.log.Warn("relay to user failed", "user_id", link.UserID, "err", err)
		b.reply(ctx, m, "❌ 发送失败："+esc(reason))
		return
	}
	if err := b.store.SaveLink(store.Link{
		GroupChatID: m.Chat.ID, GroupMsgID: m.MessageID, UserID: link.UserID, UserMsgID: id, Kind: store.KindOutbound,
	}); err != nil {
		b.log.Error("save link failed", "err", err)
	}
	if err := b.api.SetMessageReaction(ctx, m.Chat.ID, m.MessageID, "👌"); err != nil {
		b.reply(ctx, m, "✅ 已发送")
	}
}

// bindHere handles /bind inside a group.
func (b *Bot) bindHere(ctx context.Context, m *telegram.Message, gid int64) {
	if !b.isAdmin(m.From.ID) {
		b.reply(ctx, m, "⛔ 只有机器人管理员可以绑定客服群组。")
		return
	}
	if m.Chat.ID == gid {
		b.reply(ctx, m, "✅ 本群已经是客服群组。")
		return
	}
	chat := m.Chat
	if err := b.useGroup(ctx, &chat); err != nil {
		b.reply(ctx, m, "❌ "+esc(err.Error()))
	}
}

// onUserCallback handles the buttons under forwarded user messages.
func (b *Bot) onUserCallback(ctx context.Context, q *telegram.CallbackQuery, action, arg string) {
	uid, err := strconv.ParseInt(arg, 10, 64)
	m := q.Message
	if err != nil || m == nil || m.Date == 0 {
		b.answer(ctx, q, "", false)
		return
	}
	if m.Chat.ID != b.groupID() && !b.isAdmin(q.From.ID) {
		b.answer(ctx, q, "⛔ 无权限", true)
		return
	}
	switch action {
	case "u":
		b.answer(ctx, q, b.userInfo(uid), true)
	case "b":
		b.answer(ctx, q, "", false)
		b.editMarkup(ctx, &m.Chat, m.MessageID, keyboard(row(
			btn("⚠️ 确认封禁", "bc:"+arg),
			btn("↩️ 取消", "bx:"+arg),
		)))
	case "bc":
		text, _ := b.ban(uid, q.From.ID, "")
		b.answer(ctx, q, text, false)
		b.editMarkup(ctx, &m.Chat, m.MessageID, b.userKeyboard(uid))
	case "ub":
		b.answer(ctx, q, b.unban(uid), false)
		b.editMarkup(ctx, &m.Chat, m.MessageID, b.userKeyboard(uid))
	default: // "bx": cancel
		b.answer(ctx, q, "", false)
		b.editMarkup(ctx, &m.Chat, m.MessageID, b.userKeyboard(uid))
	}
}

func (b *Bot) editMarkup(ctx context.Context, chat *telegram.Chat, msgID int64, kb *telegram.InlineKeyboardMarkup) {
	if err := b.api.EditMessageReplyMarkup(ctx, chat.ID, msgID, kb); err != nil {
		b.log.Debug("edit reply markup failed", "err", err)
	}
}

// userKeyboard is attached to every forwarded user message.
func (b *Bot) userKeyboard(uid int64) *telegram.InlineKeyboardMarkup {
	id := strconv.FormatInt(uid, 10)
	label, action := "👤 "+b.userLabel(uid), btn("🚫 封禁", "b:"+id)
	if banned, _ := b.store.IsBanned(uid); banned {
		label, action = "🚫 "+b.userLabel(uid), btn("✅ 解封", "ub:"+id)
	}
	return keyboard(row(btn(label, "u:"+id), action))
}

// userInfo is plain text short enough for a callback alert.
func (b *Bot) userInfo(uid int64) string {
	var sb strings.Builder
	u, err := b.store.User(uid)
	if err != nil {
		b.log.Error("load user failed", "user_id", uid, "err", err)
	}
	if u != nil {
		if name := displayName(u.FirstName, u.LastName, u.Username); name != "" {
			sb.WriteString(name + "\n")
		}
		fmt.Fprintf(&sb, "ID：%d\n首次联系：%s\n最近联系：%s\n消息数：%d",
			uid, fmtTime(u.FirstSeen), fmtTime(u.LastSeen), u.Messages)
	} else {
		fmt.Fprintf(&sb, "ID：%d", uid)
	}
	if banned, _ := b.store.IsBanned(uid); banned {
		sb.WriteString("\n状态：🚫 已封禁")
	} else {
		sb.WriteString("\n状态：正常")
	}
	return sb.String()
}

// ban blocks uid and returns a plain-text result.
func (b *Bot) ban(uid, by int64, reason string) (string, bool) {
	if b.isAdmin(uid) {
		return "⛔ 不能封禁管理员。", false
	}
	if err := b.store.Ban(uid, by, reason); err != nil {
		b.log.Error("ban failed", "user_id", uid, "err", err)
		return "❌ 封禁失败，请查看日志。", false
	}
	b.log.Info("user banned", "user_id", uid, "by", by)
	return "🚫 已封禁 " + b.userLabel(uid), true
}

// unban returns a plain-text result.
func (b *Bot) unban(uid int64) string {
	ok, err := b.store.Unban(uid)
	switch {
	case err != nil:
		b.log.Error("unban failed", "user_id", uid, "err", err)
		return "❌ 操作失败，请查看日志。"
	case !ok:
		return b.userLabel(uid) + " 不在黑名单中。"
	}
	return "✅ 已解封 " + b.userLabel(uid)
}

func (b *Bot) onMyChatMember(ctx context.Context, u *telegram.ChatMemberUpdated) {
	if u.Chat.Type != "group" && u.Chat.Type != "supergroup" {
		return
	}
	wasIn, isIn := inChat(u.OldChatMember.Status), inChat(u.NewChatMember.Status)
	switch {
	case isIn && !wasIn && b.isAdmin(u.From.ID):
		text := fmt.Sprintf("👋 机器人已加入本群。\n群组 ID：<code>%d</code>\n\n管理员发送 /bind 即可将本群设为客服群组。", u.Chat.ID)
		if _, err := b.send(ctx, u.Chat.ID, text, nil); err != nil {
			b.log.Debug("greet group failed", "err", err)
		}
	case wasIn && !isIn && u.Chat.ID == b.groupID():
		b.log.Warn("bot was removed from the support group", "chat_id", u.Chat.ID)
		b.notifyAdmins(ctx, fmt.Sprintf("⚠️ 机器人已被移出客服群组「%s」，用户消息将无法转发。请发送 /admin 重新设置客服群组。", esc(u.Chat.Title)))
	}
}

func inChat(status string) bool {
	switch status {
	case "creator", "administrator", "member", "restricted":
		return true
	}
	return false
}

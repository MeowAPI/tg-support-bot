package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MeowAPI/tg-support-bot/internal/store"
	"github.com/MeowAPI/tg-support-bot/internal/telegram"
)

const (
	testToken = "123:TEST"
	botID     = int64(9999)
	adminID   = int64(1000)
	userID    = int64(2000)
	staffID   = int64(3000)
	staff2ID  = int64(3001)
	groupID   = int64(-1001111)
)

var groupChat = telegram.Chat{ID: groupID, Type: "supergroup", Title: "Support"}

type apiCall struct {
	method string
	params map[string]any
	// resultID is the message_id the fake returned, if any.
	resultID int64
}

func (c apiCall) num(path ...string) int64 {
	var v any = c.params
	for _, k := range path {
		m, _ := v.(map[string]any)
		v = m[k]
	}
	n, _ := v.(json.Number).Int64()
	return n
}

func (c apiCall) str(key string) string {
	s, _ := c.params[key].(string)
	return s
}

// callbacks lists the callback_data of every button in reply_markup.
func (c apiCall) callbacks() []string {
	var out []string
	markup, _ := c.params["reply_markup"].(map[string]any)
	rows, _ := markup["inline_keyboard"].([]any)
	for _, r := range rows {
		buttons, _ := r.([]any)
		for _, b := range buttons {
			m, _ := b.(map[string]any)
			if d, ok := m["callback_data"].(string); ok {
				out = append(out, d)
			}
		}
	}
	return out
}

// fakeTG is an in-memory Bot API that records every call.
type fakeTG struct {
	srv *httptest.Server

	mu      sync.Mutex
	calls   []apiCall
	nextID  int64
	chats   map[string]map[string]any   // getChat results by chat_id
	fails   map[string][]map[string]any // queued error responses by method
	updates []telegram.Update           // served once by getUpdates
}

func newFakeTG(t *testing.T) *fakeTG {
	f := &fakeTG{nextID: 500, chats: map[string]map[string]any{}, fails: map[string][]map[string]any{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeTG) serve(w http.ResponseWriter, r *http.Request) {
	method, ok := strings.CutPrefix(r.URL.Path, "/bot"+testToken+"/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	params := map[string]any{}
	dec := json.NewDecoder(r.Body)
	dec.UseNumber()
	_ = dec.Decode(&params)

	f.mu.Lock()
	call := apiCall{method: method, params: params}
	var resp map[string]any
	if q := f.fails[method]; len(q) > 0 {
		resp, f.fails[method] = q[0], q[1:]
	} else {
		resp = f.respond(&call)
	}
	f.calls = append(f.calls, call)
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (f *fakeTG) respond(c *apiCall) map[string]any {
	ok := func(result any) map[string]any { return map[string]any{"ok": true, "result": result} }
	switch c.method {
	case "getMe":
		return ok(map[string]any{"id": botID, "is_bot": true, "first_name": "Support", "username": "support_bot"})
	case "getUpdates":
		out := f.updates
		f.updates = nil
		if out == nil {
			out = []telegram.Update{}
		}
		return ok(out)
	case "copyMessage":
		f.nextID++
		c.resultID = f.nextID
		return ok(map[string]any{"message_id": f.nextID})
	case "sendMessage":
		f.nextID++
		c.resultID = f.nextID
		return ok(map[string]any{"message_id": f.nextID, "date": 1, "chat": map[string]any{"id": c.params["chat_id"], "type": "private"}})
	case "getChat":
		if chat, found := f.chats[fmt.Sprint(c.params["chat_id"])]; found {
			return ok(chat)
		}
		return map[string]any{"ok": false, "error_code": 400, "description": "Bad Request: chat not found"}
	case "getChatMember":
		return ok(map[string]any{"status": "member", "user": map[string]any{"id": botID, "is_bot": true, "first_name": "Support"}})
	}
	return ok(true)
}

func (f *fakeTG) failNext(method string, code int, desc string, params map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	resp := map[string]any{"ok": false, "error_code": code, "description": desc}
	if params != nil {
		resp["parameters"] = params
	}
	f.fails[method] = append(f.fails[method], resp)
}

func (f *fakeTG) addChat(id int64, typ, title string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.chats[fmt.Sprint(id)] = map[string]any{"id": id, "type": typ, "title": title}
}

// take returns and clears the recorded calls.
func (f *fakeTG) take() []apiCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.calls
	f.calls = nil
	return out
}

func newTestBot(t *testing.T) (*Bot, *fakeTG) {
	t.Helper()
	f := newFakeTG(t)
	st, err := store.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	b := New(telegram.NewClient(f.srv.URL, testToken), st, Config{
		SuperAdmins: []int64{adminID},
		AckCooldown: time.Minute,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := b.init(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.take()
	return b, f
}

func privateMsg(from, id int64, text string) *telegram.Message {
	return &telegram.Message{
		MessageID: id,
		From:      &telegram.User{ID: from, FirstName: fmt.Sprintf("User%d", from)},
		Chat:      telegram.Chat{ID: from, Type: "private"},
		Date:      1,
		Text:      text,
	}
}

func groupMsg(chat telegram.Chat, from, id int64, text string, replyTo *telegram.Message) *telegram.Message {
	return &telegram.Message{
		MessageID:      id,
		From:           &telegram.User{ID: from, FirstName: "Staff"},
		Chat:           chat,
		Date:           1,
		Text:           text,
		ReplyToMessage: replyTo,
	}
}

// botCopy is how a forwarded user message appears inside reply_to_message.
func botCopy(id int64) *telegram.Message {
	return &telegram.Message{
		MessageID:   id,
		From:        &telegram.User{ID: botID, IsBot: true},
		Chat:        groupChat,
		Date:        1,
		ReplyMarkup: keyboard(row(btn("👤", "u:1"))),
	}
}

func callback(from int64, chat telegram.Chat, msgID int64, data string) telegram.Update {
	return telegram.Update{CallbackQuery: &telegram.CallbackQuery{
		ID:      "cb",
		From:    telegram.User{ID: from},
		Message: &telegram.Message{MessageID: msgID, Chat: chat, Date: 1},
		Data:    data,
	}}
}

func msg(m *telegram.Message) telegram.Update { return telegram.Update{Message: m} }

func only(t *testing.T, calls []apiCall, method string) apiCall {
	t.Helper()
	var found []apiCall
	for _, c := range calls {
		if c.method == method {
			found = append(found, c)
		}
	}
	if len(found) != 1 {
		t.Fatalf("want exactly one %s call, got %d in %v", method, len(found), methods(calls))
	}
	return found[0]
}

func none(t *testing.T, calls []apiCall, method string) {
	t.Helper()
	for _, c := range calls {
		if c.method == method {
			t.Fatalf("unexpected %s call: %v", method, c.params)
		}
	}
}

func methods(calls []apiCall) []string {
	out := make([]string, len(calls))
	for i, c := range calls {
		out[i] = c.method
	}
	return out
}

func wantNum(t *testing.T, c apiCall, want int64, path ...string) {
	t.Helper()
	if got := c.num(path...); got != want {
		t.Fatalf("%s %s = %d, want %d", c.method, strings.Join(path, "."), got, want)
	}
}

func TestConversationRoundTrip(t *testing.T) {
	b, f := newTestBot(t)
	ctx := context.Background()
	if err := b.store.SetGroup(groupID, "Support"); err != nil {
		t.Fatal(err)
	}

	// The user's message is copied into the group with the user buttons.
	b.dispatch(ctx, msg(privateMsg(userID, 10, "hello")))
	calls := f.take()
	cp := only(t, calls, "copyMessage")
	wantNum(t, cp, groupID, "chat_id")
	wantNum(t, cp, userID, "from_chat_id")
	wantNum(t, cp, 10, "message_id")
	if cbs := cp.callbacks(); !slices.Equal(cbs, []string{"u:2000", "b:2000"}) {
		t.Fatalf("user keyboard = %v", cbs)
	}
	ack := only(t, calls, "sendMessage")
	wantNum(t, ack, userID, "chat_id")
	if ack.str("text") != defaultAck {
		t.Fatalf("ack text = %q", ack.str("text"))
	}
	groupCopy := cp.resultID

	// Staff replying to that copy reaches the user, threaded under the
	// original message, and the staff message gets a reaction.
	b.dispatch(ctx, msg(groupMsg(groupChat, staffID, 20, "hi, how can I help?", botCopy(groupCopy))))
	calls = f.take()
	cp = only(t, calls, "copyMessage")
	wantNum(t, cp, userID, "chat_id")
	wantNum(t, cp, groupID, "from_chat_id")
	wantNum(t, cp, 20, "message_id")
	wantNum(t, cp, 10, "reply_parameters", "message_id")
	react := only(t, calls, "setMessageReaction")
	wantNum(t, react, 20, "message_id")
	none(t, calls, "sendMessage")
	userCopy := cp.resultID

	// The user replying to the answer is threaded under the staff message,
	// and the auto-reply is not repeated within the cooldown.
	reply := privateMsg(userID, 11, "thanks")
	reply.ReplyToMessage = &telegram.Message{MessageID: userCopy, Chat: reply.Chat, Date: 1}
	b.dispatch(ctx, msg(reply))
	calls = f.take()
	cp = only(t, calls, "copyMessage")
	wantNum(t, cp, groupID, "chat_id")
	wantNum(t, cp, 20, "reply_parameters", "message_id")
	none(t, calls, "sendMessage")

	// Staff replying to a colleague's answer is internal and must not leak.
	colleague := groupMsg(groupChat, staffID, 20, "hi, how can I help?", nil)
	b.dispatch(ctx, msg(groupMsg(groupChat, staff2ID, 21, "nice answer", colleague)))
	if calls := f.take(); len(calls) != 0 {
		t.Fatalf("internal staff reply triggered %v", methods(calls))
	}

	// Plain group chatter is ignored too.
	b.dispatch(ctx, msg(groupMsg(groupChat, staffID, 22, "lunch?", nil)))
	if calls := f.take(); len(calls) != 0 {
		t.Fatalf("group chatter triggered %v", methods(calls))
	}

	u, err := b.store.User(userID)
	if err != nil || u == nil || u.Messages != 2 {
		t.Fatalf("stored user = %+v, %v", u, err)
	}
}

func TestRunPollsAndConfirmsOnShutdown(t *testing.T) {
	b, f := newTestBot(t)
	b.store.SetGroup(groupID, "Support")
	u := msg(privateMsg(userID, 10, "hello"))
	u.UpdateID = 41
	f.mu.Lock()
	f.updates = []telegram.Update{u}
	f.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	var calls []apiCall
	deadline := time.Now().Add(5 * time.Second)
	for !slices.ContainsFunc(calls, func(c apiCall) bool { return c.method == "copyMessage" }) {
		if time.Now().After(deadline) {
			t.Fatalf("update was not relayed; calls: %v", methods(calls))
		}
		time.Sleep(10 * time.Millisecond)
		calls = append(calls, f.take()...)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
	calls = append(calls, f.take()...)

	var polls []apiCall
	for _, c := range calls {
		if c.method == "getUpdates" {
			polls = append(polls, c)
		}
	}
	last := polls[len(polls)-1]
	wantNum(t, last, 42, "offset")
	wantNum(t, last, 0, "timeout")
}

func TestNoGroupConfigured(t *testing.T) {
	b, f := newTestBot(t)
	b.dispatch(context.Background(), msg(privateMsg(userID, 10, "hello")))
	calls := f.take()
	none(t, calls, "copyMessage")
	if !strings.Contains(only(t, calls, "sendMessage").str("text"), "客服暂未开放") {
		t.Fatal("user was not told support is unavailable")
	}
}

func TestAdminSetsGroupFromPanel(t *testing.T) {
	b, f := newTestBot(t)
	ctx := context.Background()
	f.addChat(-1004242, "supergroup", "客服群")
	adminChat := telegram.Chat{ID: adminID, Type: "private"}

	b.dispatch(ctx, msg(privateMsg(adminID, 1, "/admin")))
	panel := only(t, f.take(), "sendMessage")
	if !slices.Contains(panel.callbacks(), "adm:group") {
		t.Fatalf("panel buttons = %v", panel.callbacks())
	}

	b.dispatch(ctx, callback(adminID, adminChat, panel.resultID, "adm:group"))
	calls := f.take()
	only(t, calls, "answerCallbackQuery")
	if !strings.Contains(only(t, calls, "editMessageText").str("text"), "群组 ID") {
		t.Fatal("group prompt not shown")
	}

	// A bad ID keeps the prompt open.
	b.dispatch(ctx, msg(privateMsg(adminID, 2, "not-an-id")))
	if !strings.Contains(only(t, f.take(), "sendMessage").str("text"), "格式不正确") {
		t.Fatal("bad ID not rejected")
	}

	// The ID without the -100 prefix is still resolved.
	b.dispatch(ctx, msg(privateMsg(adminID, 3, "4242")))
	calls = f.take()
	var announced bool
	for _, c := range calls {
		if c.method == "sendMessage" && c.num("chat_id") == -1004242 {
			announced = true
		}
	}
	if !announced {
		t.Fatalf("group was not told it is bound: %v", methods(calls))
	}
	if gid := b.groupID(); gid != -1004242 {
		t.Fatalf("group = %d, want -1004242", gid)
	}
	if _, ok := b.pendingFor(adminID); ok {
		t.Fatal("prompt still pending after success")
	}

	// Later messages from the admin are relayed like any user's.
	b.dispatch(ctx, msg(privateMsg(adminID, 4, "4242")))
	wantNum(t, only(t, f.take(), "copyMessage"), -1004242, "chat_id")
}

func TestSetGroupRejectsChannel(t *testing.T) {
	b, f := newTestBot(t)
	f.addChat(-1007777, "channel", "News")
	b.dispatch(context.Background(), msg(privateMsg(adminID, 1, "/setgroup -1007777")))
	if !strings.Contains(only(t, f.take(), "sendMessage").str("text"), "不是群组") {
		t.Fatal("channel was not rejected")
	}
	if b.groupID() != 0 {
		t.Fatal("channel was saved as the group")
	}
}

func TestNonAdminIsLockedOut(t *testing.T) {
	b, f := newTestBot(t)
	ctx := context.Background()

	b.dispatch(ctx, msg(privateMsg(userID, 1, "/admin")))
	calls := f.take()
	if !strings.Contains(only(t, calls, "sendMessage").str("text"), "仅管理员") {
		t.Fatal("non-admin not refused")
	}

	b.dispatch(ctx, callback(userID, telegram.Chat{ID: userID, Type: "private"}, 5, "adm:group"))
	calls = f.take()
	ans := only(t, calls, "answerCallbackQuery")
	if ans.params["show_alert"] != true {
		t.Fatal("refusal should be an alert")
	}
	none(t, calls, "editMessageText")
	if _, ok := b.pendingFor(userID); ok {
		t.Fatal("non-admin got a pending prompt")
	}
}

func TestBindInGroup(t *testing.T) {
	b, f := newTestBot(t)
	ctx := context.Background()
	other := telegram.Chat{ID: -1005555, Type: "supergroup", Title: "Staff"}

	b.dispatch(ctx, msg(groupMsg(other, staffID, 1, "/bind@support_bot", nil)))
	if !strings.Contains(only(t, f.take(), "sendMessage").str("text"), "只有机器人管理员") {
		t.Fatal("non-admin bind not refused")
	}

	b.dispatch(ctx, msg(groupMsg(other, adminID, 2, "/bind@other_bot", nil)))
	if calls := f.take(); len(calls) != 0 {
		t.Fatalf("command for another bot was handled: %v", methods(calls))
	}

	b.dispatch(ctx, msg(groupMsg(other, adminID, 3, "/bind", nil)))
	f.take()
	if gid, title, _ := b.store.Group(); gid != other.ID || title != "Staff" {
		t.Fatalf("group = %d %q", gid, title)
	}
}

func TestBanFlow(t *testing.T) {
	b, f := newTestBot(t)
	ctx := context.Background()
	b.store.SetGroup(groupID, "Support")

	b.dispatch(ctx, msg(privateMsg(userID, 10, "spam")))
	copyID := only(t, f.take(), "copyMessage").resultID

	// First click asks for confirmation.
	b.dispatch(ctx, callback(staffID, groupChat, copyID, "b:2000"))
	edit := only(t, f.take(), "editMessageReplyMarkup")
	if cbs := edit.callbacks(); !slices.Equal(cbs, []string{"bc:2000", "bx:2000"}) {
		t.Fatalf("confirm keyboard = %v", cbs)
	}

	// Confirming bans and flips the button to unban.
	b.dispatch(ctx, callback(staffID, groupChat, copyID, "bc:2000"))
	edit = only(t, f.take(), "editMessageReplyMarkup")
	if cbs := edit.callbacks(); !slices.Equal(cbs, []string{"u:2000", "ub:2000"}) {
		t.Fatalf("banned keyboard = %v", cbs)
	}
	if banned, _ := b.store.IsBanned(userID); !banned {
		t.Fatal("user not banned")
	}

	// Banned users are dropped silently.
	b.dispatch(ctx, msg(privateMsg(userID, 11, "more spam")))
	if calls := f.take(); len(calls) != 0 {
		t.Fatalf("banned user triggered %v", methods(calls))
	}

	// Staff can still unban with a reply command.
	b.dispatch(ctx, msg(groupMsg(groupChat, staffID, 30, "/unban", botCopy(copyID))))
	f.take()
	if banned, _ := b.store.IsBanned(userID); banned {
		t.Fatal("user still banned")
	}

	// Buttons clicked outside the support group need an admin.
	outsider := telegram.Chat{ID: -1009999, Type: "supergroup"}
	b.dispatch(ctx, callback(staffID, outsider, 1, "bc:2000"))
	none(t, f.take(), "editMessageReplyMarkup")
	if banned, _ := b.store.IsBanned(userID); banned {
		t.Fatal("outsider could ban")
	}
}

func TestAdminsCannotBeBanned(t *testing.T) {
	b, f := newTestBot(t)
	b.store.SetGroup(groupID, "Support")
	b.dispatch(context.Background(), msg(privateMsg(adminID, 10, "test")))
	copyID := only(t, f.take(), "copyMessage").resultID
	b.dispatch(context.Background(), callback(staffID, groupChat, copyID, "bc:1000"))
	f.take()
	if banned, _ := b.store.IsBanned(adminID); banned {
		t.Fatal("admin was banned")
	}
}

func TestGroupMigration(t *testing.T) {
	b, f := newTestBot(t)
	b.store.SetGroup(groupID, "Support")
	f.failNext("copyMessage", 400, "Bad Request: group chat was upgraded to a supergroup chat",
		map[string]any{"migrate_to_chat_id": -1002222})

	b.dispatch(context.Background(), msg(privateMsg(userID, 10, "hello")))
	calls := f.take()
	var targets []int64
	for _, c := range calls {
		if c.method == "copyMessage" {
			targets = append(targets, c.num("chat_id"))
		}
	}
	if !slices.Equal(targets, []int64{groupID, -1002222}) {
		t.Fatalf("copy targets = %v", targets)
	}
	if gid, title, _ := b.store.Group(); gid != -1002222 || title != "Support" {
		t.Fatalf("group after migration = %d %q", gid, title)
	}

	// The migration service message is followed too.
	b.dispatch(context.Background(), msg(&telegram.Message{
		MessageID: 1, Chat: telegram.Chat{ID: -1002222, Type: "group"}, Date: 1, MigrateToChatID: -1003333,
	}))
	if gid := b.groupID(); gid != -1003333 {
		t.Fatalf("group after service message = %d", gid)
	}
}

func TestBlockedUserIsReportedInGroup(t *testing.T) {
	b, f := newTestBot(t)
	b.store.SetGroup(groupID, "Support")
	b.dispatch(context.Background(), msg(privateMsg(userID, 10, "hello")))
	copyID := only(t, f.take(), "copyMessage").resultID

	f.failNext("copyMessage", 403, "Forbidden: bot was blocked by the user", nil)
	b.dispatch(context.Background(), msg(groupMsg(groupChat, staffID, 20, "answer", botCopy(copyID))))
	calls := f.take()
	note := only(t, calls, "sendMessage")
	wantNum(t, note, groupID, "chat_id")
	if !strings.Contains(note.str("text"), "屏蔽") {
		t.Fatalf("report = %q", note.str("text"))
	}
	none(t, calls, "setMessageReaction")
}

func TestAddAdminByForward(t *testing.T) {
	b, f := newTestBot(t)
	ctx := context.Background()
	adminChat := telegram.Chat{ID: adminID, Type: "private"}

	b.dispatch(ctx, callback(adminID, adminChat, 7, "adm:"+pageAddAdmin))
	f.take()
	fwd := privateMsg(adminID, 2, "some text")
	fwd.ForwardOrigin = &telegram.MessageOrigin{Type: "user", SenderUser: &telegram.User{ID: 4444, FirstName: "New"}}
	b.dispatch(ctx, msg(fwd))
	f.take()
	if !b.isAdmin(4444) {
		t.Fatal("forwarded user not added as admin")
	}

	// Configured admins cannot be removed from the panel; added ones can.
	b.dispatch(ctx, callback(adminID, adminChat, 7, "adm:deladmin:1000"))
	f.take()
	b.dispatch(ctx, callback(adminID, adminChat, 7, "adm:deladmin:4444"))
	f.take()
	if !b.isAdmin(adminID) || b.isAdmin(4444) {
		t.Fatalf("admin state wrong: %v %v", b.isAdmin(adminID), b.isAdmin(4444))
	}
}

func TestCustomWelcomeKeepsFormatting(t *testing.T) {
	b, f := newTestBot(t)
	ctx := context.Background()
	b.dispatch(ctx, callback(adminID, telegram.Chat{ID: adminID, Type: "private"}, 7, "adm:"+pageWelcomeEdit))
	f.take()
	in := privateMsg(adminID, 2, "Hi there")
	in.Entities = json.RawMessage(`[{"type":"bold","offset":0,"length":2}]`)
	b.dispatch(ctx, msg(in))
	f.take()

	b.dispatch(ctx, msg(privateMsg(userID, 1, "/start")))
	welcome := only(t, f.take(), "sendMessage")
	if welcome.str("text") != "Hi there" {
		t.Fatalf("welcome = %q", welcome.str("text"))
	}
	ents, _ := welcome.params["entities"].([]any)
	if len(ents) != 1 {
		t.Fatalf("entities = %v", welcome.params["entities"])
	}
}

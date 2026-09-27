// Package store keeps the support bot's state in SQLite.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	_ "modernc.org/sqlite"
)

// Link kinds.
const (
	// KindInbound: the group message is the bot's copy of a user message.
	KindInbound = 0
	// KindOutbound: the group message is a staff reply and the user message
	// is the bot's copy of it.
	KindOutbound = 1
)

// Link ties a message in the support group to one in a user's private chat.
type Link struct {
	GroupChatID int64
	GroupMsgID  int64
	UserID      int64
	UserMsgID   int64
	Kind        int
}

// User is what the bot remembers about someone who wrote to it.
type User struct {
	ID        int64
	FirstName string
	LastName  string
	Username  string
	FirstSeen time.Time
	LastSeen  time.Time
	Messages  int64
}

// Admin is an administrator added through the panel.
type Admin struct {
	UserID  int64
	AddedBy int64
	AddedAt time.Time
}

// Ban is a blocked user.
type Ban struct {
	UserID   int64
	Reason   string
	BannedBy int64
	BannedAt time.Time
}

const schema = `
CREATE TABLE IF NOT EXISTS settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS admins (
	user_id  INTEGER PRIMARY KEY,
	added_by INTEGER NOT NULL DEFAULT 0,
	added_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS bans (
	user_id   INTEGER PRIMARY KEY,
	reason    TEXT NOT NULL DEFAULT '',
	banned_by INTEGER NOT NULL DEFAULT 0,
	banned_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS users (
	user_id    INTEGER PRIMARY KEY,
	first_name TEXT NOT NULL DEFAULT '',
	last_name  TEXT NOT NULL DEFAULT '',
	username   TEXT NOT NULL DEFAULT '',
	first_seen INTEGER NOT NULL,
	last_seen  INTEGER NOT NULL,
	messages   INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS links (
	group_chat_id INTEGER NOT NULL,
	group_msg_id  INTEGER NOT NULL,
	user_id       INTEGER NOT NULL,
	user_msg_id   INTEGER NOT NULL,
	kind          INTEGER NOT NULL,
	created_at    INTEGER NOT NULL,
	PRIMARY KEY (group_chat_id, group_msg_id)
);
CREATE INDEX IF NOT EXISTS links_by_user ON links (user_id, user_msg_id);
CREATE INDEX IF NOT EXISTS links_by_created ON links (created_at);
`

const upsertSetting = `INSERT INTO settings (key, value) VALUES (?, ?)
	ON CONFLICT(key) DO UPDATE SET value = excluded.value`

const (
	keyGroupID    = "group_id"
	keyGroupTitle = "group_title"
)

// Store is safe for concurrent use.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the database at path.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	// Without a "file:" prefix the driver strips the query and applies the
	// pragmas itself, which keeps plain Windows paths working.
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Setting returns "" for a missing key.
func (s *Store) Setting(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(upsertSetting, key, value)
	return err
}

func (s *Store) DeleteSetting(key string) error {
	_, err := s.db.Exec(`DELETE FROM settings WHERE key = ?`, key)
	return err
}

// Group returns the support group; id is 0 when none is configured.
func (s *Store) Group() (id int64, title string, err error) {
	raw, err := s.Setting(keyGroupID)
	if err != nil || raw == "" {
		return 0, "", err
	}
	if id, err = strconv.ParseInt(raw, 10, 64); err != nil {
		return 0, "", fmt.Errorf("bad %s setting %q: %w", keyGroupID, raw, err)
	}
	title, err = s.Setting(keyGroupTitle)
	return id, title, err
}

// SetGroup stores the support group. An id of 0 clears it.
func (s *Store) SetGroup(id int64, title string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if id == 0 {
		_, err = tx.Exec(`DELETE FROM settings WHERE key IN (?, ?)`, keyGroupID, keyGroupTitle)
	} else if _, err = tx.Exec(upsertSetting, keyGroupID, strconv.FormatInt(id, 10)); err == nil {
		_, err = tx.Exec(upsertSetting, keyGroupTitle, title)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) AddAdmin(id, by int64) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO admins (user_id, added_by, added_at) VALUES (?, ?, ?)`,
		id, by, time.Now().Unix())
	return err
}

func (s *Store) RemoveAdmin(id int64) error {
	_, err := s.db.Exec(`DELETE FROM admins WHERE user_id = ?`, id)
	return err
}

func (s *Store) IsAdmin(id int64) (bool, error) {
	return s.exists(`SELECT 1 FROM admins WHERE user_id = ?`, id)
}

func (s *Store) Admins() ([]Admin, error) {
	rows, err := s.db.Query(`SELECT user_id, added_by, added_at FROM admins ORDER BY added_at, user_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Admin
	for rows.Next() {
		var a Admin
		var at int64
		if err := rows.Scan(&a.UserID, &a.AddedBy, &at); err != nil {
			return nil, err
		}
		a.AddedAt = time.Unix(at, 0)
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) Ban(id, by int64, reason string) error {
	_, err := s.db.Exec(`INSERT INTO bans (user_id, reason, banned_by, banned_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET
			reason = excluded.reason, banned_by = excluded.banned_by, banned_at = excluded.banned_at`,
		id, reason, by, time.Now().Unix())
	return err
}

// Unban reports whether the user was banned.
func (s *Store) Unban(id int64) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM bans WHERE user_id = ?`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (s *Store) IsBanned(id int64) (bool, error) {
	return s.exists(`SELECT 1 FROM bans WHERE user_id = ?`, id)
}

// Bans returns the most recent bans first.
func (s *Store) Bans(limit int) ([]Ban, error) {
	rows, err := s.db.Query(`SELECT user_id, reason, banned_by, banned_at FROM bans
		ORDER BY banned_at DESC, user_id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Ban
	for rows.Next() {
		var b Ban
		var at int64
		if err := rows.Scan(&b.UserID, &b.Reason, &b.BannedBy, &at); err != nil {
			return nil, err
		}
		b.BannedAt = time.Unix(at, 0)
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) CountBans() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM bans`).Scan(&n)
	return n, err
}

// TouchUser records a user's current profile and adds messages to their
// message count. last_seen only moves when messages > 0.
func (s *Store) TouchUser(id int64, firstName, lastName, username string, messages int) error {
	now := time.Now().Unix()
	_, err := s.db.Exec(`INSERT INTO users (user_id, first_name, last_name, username, first_seen, last_seen, messages)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET
			first_name = excluded.first_name,
			last_name  = excluded.last_name,
			username   = excluded.username,
			last_seen  = CASE WHEN excluded.messages > 0 THEN excluded.last_seen ELSE users.last_seen END,
			messages   = users.messages + excluded.messages`,
		id, firstName, lastName, username, now, now, messages)
	return err
}

// User returns nil when the user is unknown.
func (s *Store) User(id int64) (*User, error) {
	var u User
	var first, last int64
	err := s.db.QueryRow(`SELECT user_id, first_name, last_name, username, first_seen, last_seen, messages
		FROM users WHERE user_id = ?`, id).
		Scan(&u.ID, &u.FirstName, &u.LastName, &u.Username, &first, &last, &u.Messages)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	u.FirstSeen, u.LastSeen = time.Unix(first, 0), time.Unix(last, 0)
	return &u, nil
}

func (s *Store) SaveLink(l Link) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO links
		(group_chat_id, group_msg_id, user_id, user_msg_id, kind, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		l.GroupChatID, l.GroupMsgID, l.UserID, l.UserMsgID, l.Kind, time.Now().Unix())
	return err
}

// InboundLink finds the user behind a bot copy in the support group. Staff
// messages are deliberately not matched, so staff replying to each other
// never reaches the user. Returns nil when there is no match.
func (s *Store) InboundLink(groupChatID, groupMsgID int64) (*Link, error) {
	return s.link(`WHERE group_chat_id = ? AND group_msg_id = ? AND kind = ?`,
		groupChatID, groupMsgID, KindInbound)
}

// LinkByUserMsg finds the group counterpart of a message in a user's chat.
// Returns nil when there is no match.
func (s *Store) LinkByUserMsg(userID, userMsgID int64) (*Link, error) {
	return s.link(`WHERE user_id = ? AND user_msg_id = ? ORDER BY rowid DESC LIMIT 1`, userID, userMsgID)
}

// PruneLinks deletes links created before t.
func (s *Store) PruneLinks(t time.Time) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM links WHERE created_at < ?`, t.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) link(where string, args ...any) (*Link, error) {
	var l Link
	err := s.db.QueryRow(`SELECT group_chat_id, group_msg_id, user_id, user_msg_id, kind FROM links `+where, args...).
		Scan(&l.GroupChatID, &l.GroupMsgID, &l.UserID, &l.UserMsgID, &l.Kind)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &l, nil
}

func (s *Store) exists(query string, args ...any) (bool, error) {
	var one int
	err := s.db.QueryRow(query, args...).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

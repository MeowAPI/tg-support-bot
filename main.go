// Command tg-support-bot runs a Telegram customer-support relay bot.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/MeowAPI/tg-support-bot/internal/bot"
	"github.com/MeowAPI/tg-support-bot/internal/store"
	"github.com/MeowAPI/tg-support-bot/internal/telegram"
)

type config struct {
	token   string
	apiBase string
	dbPath  string
	debug   bool
	bot     bot.Config
}

func main() {
	if err := loadDotEnv(".env"); err != nil {
		fmt.Fprintln(os.Stderr, "read .env:", err)
		os.Exit(1)
	}
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}
	level := slog.LevelInfo
	if cfg.debug {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	st, err := store.Open(cfg.dbPath)
	if err != nil {
		logger.Error("open database failed", "path", cfg.dbPath, "err", err)
		os.Exit(1)
	}
	defer st.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	b := bot.New(telegram.NewClient(cfg.apiBase, cfg.token), st, cfg.bot, logger)
	if err := b.Run(ctx); err != nil {
		logger.Error("bot stopped", "err", err)
		st.Close()
		os.Exit(1)
	}
	logger.Info("bot stopped")
}

func loadConfig() (config, error) {
	c := config{
		token:   strings.TrimSpace(os.Getenv("BOT_TOKEN")),
		apiBase: envOr("TELEGRAM_API_BASE", "https://api.telegram.org"),
		dbPath:  envOr("DB_PATH", filepath.Join("data", "bot.db")),
		debug:   os.Getenv("LOG_LEVEL") == "debug",
	}
	if c.token == "" {
		return c, errors.New("BOT_TOKEN is required (get one from @BotFather)")
	}
	sep := func(r rune) bool { return r == ',' || unicode.IsSpace(r) }
	for _, f := range strings.FieldsFunc(os.Getenv("ADMIN_IDS"), sep) {
		id, err := strconv.ParseInt(f, 10, 64)
		if err != nil || id <= 0 {
			return c, fmt.Errorf("ADMIN_IDS: %q is not a user ID", f)
		}
		c.bot.SuperAdmins = append(c.bot.SuperAdmins, id)
	}
	if v := os.Getenv("ACK_COOLDOWN"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return c, fmt.Errorf("ACK_COOLDOWN: %q is not a positive duration such as 10m", v)
		}
		c.bot.AckCooldown = d
	}
	if v := os.Getenv("LINK_RETENTION_DAYS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return c, fmt.Errorf("LINK_RETENTION_DAYS: %q is not a positive number", v)
		}
		c.bot.LinkRetention = time.Duration(n) * 24 * time.Hour
	}
	return c, nil
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

// loadDotEnv sets KEY=VALUE pairs from path unless already in the environment.
// A missing file is not an error.
func loadDotEnv(path string) error {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(strings.TrimPrefix(key, "export "))
		val = strings.TrimSpace(val)
		if len(val) >= 2 && (val[0] == '"' || val[0] == '\'') && val[len(val)-1] == val[0] {
			val = val[1 : len(val)-1]
		} else if i := strings.Index(val, " #"); i >= 0 {
			val = strings.TrimSpace(val[:i]) // inline comment
		}
		if _, set := os.LookupEnv(key); !set {
			os.Setenv(key, val)
		}
	}
	return sc.Err()
}

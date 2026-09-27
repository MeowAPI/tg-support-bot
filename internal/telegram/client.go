package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultTimeout = 30 * time.Second

// Client calls the Bot API. Create one with NewClient.
type Client struct {
	endpoint string
	http     *http.Client
}

// NewClient returns a client for apiBase (normally https://api.telegram.org).
// Requests honour HTTPS_PROXY / HTTP_PROXY from the environment.
func NewClient(apiBase, token string) *Client {
	return &Client{
		endpoint: strings.TrimRight(apiBase, "/") + "/bot" + token + "/",
		http:     &http.Client{},
	}
}

// Error is a Bot API call that returned ok=false.
type Error struct {
	Method      string
	Code        int
	Description string
	// MigrateTo is set when a group has been upgraded to a supergroup.
	MigrateTo int64
	// RetryAfter is the flood-control wait in seconds.
	RetryAfter int
}

func (e *Error) Error() string {
	return fmt.Sprintf("telegram %s: %d %s", e.Method, e.Code, e.Description)
}

// IsForbidden reports whether err means the bot may not write to the chat,
// e.g. because the user blocked it.
func IsForbidden(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == http.StatusForbidden
}

// Description returns the Bot API's error text, or err's own message.
func Description(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Description
	}
	return err.Error()
}

func isNotModified(err error) bool {
	var e *Error
	return errors.As(err, &e) && strings.Contains(e.Description, "message is not modified")
}

type response struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
	Parameters  *struct {
		MigrateToChatID int64 `json:"migrate_to_chat_id"`
		RetryAfter      int   `json:"retry_after"`
	} `json:"parameters"`
}

// Call invokes method with params (marshalled as JSON) and decodes the result
// into result when it is non-nil. Flood-control errors are retried a couple of
// times. Calls without a deadline get a 30s timeout.
func (c *Client) Call(ctx context.Context, method string, params, result any) error {
	body := []byte("{}")
	if params != nil {
		var err error
		if body, err = json.Marshal(params); err != nil {
			return fmt.Errorf("telegram %s: encode params: %w", method, err)
		}
	}
	for attempt := 0; ; attempt++ {
		err := c.do(ctx, method, body, result)
		var apiErr *Error
		if !errors.As(err, &apiErr) || apiErr.RetryAfter <= 0 || apiErr.RetryAfter > 60 || attempt >= 2 {
			return err
		}
		t := time.NewTimer(time.Duration(apiErr.RetryAfter) * time.Second)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return err
		}
	}
}

func (c *Client) do(ctx context.Context, method string, body []byte, result any) error {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultTimeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+method, bytes.NewReader(body))
	if err != nil {
		// The URL embeds the bot token, so the parse error is not surfaced.
		return fmt.Errorf("telegram %s: invalid API base URL", method)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		// url.Error prints the request URL, which embeds the bot token.
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err
		}
		return fmt.Errorf("telegram %s: %w", method, err)
	}
	defer resp.Body.Close()

	var r response
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&r); err != nil {
		return fmt.Errorf("telegram %s: HTTP %d: undecodable response", method, resp.StatusCode)
	}
	if !r.OK {
		e := &Error{Method: method, Code: r.ErrorCode, Description: r.Description}
		if p := r.Parameters; p != nil {
			e.MigrateTo, e.RetryAfter = p.MigrateToChatID, p.RetryAfter
		}
		return e
	}
	if result == nil {
		return nil
	}
	if err := json.Unmarshal(r.Result, result); err != nil {
		return fmt.Errorf("telegram %s: decode result: %w", method, err)
	}
	return nil
}

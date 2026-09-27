package telegram

import (
	"context"
	"strings"
	"testing"
)

func TestTransportErrorsDoNotLeakToken(t *testing.T) {
	c := NewClient("http://127.0.0.1:1", "123:SECRET")
	_, err := c.GetMe(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("token leaked into error: %v", err)
	}
}

package telegram

import (
	"fmt"
	"strings"
	"testing"
)

func TestTelegramErrorDetailPrefersDescription(t *testing.T) {
	got := telegramErrorDetail(strings.NewReader(`{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`))
	if got != "Bad Request: chat not found" {
		t.Fatalf("detail = %q, want description", got)
	}
}

func TestTelegramErrorDetailFallsBackToRawBody(t *testing.T) {
	got := telegramErrorDetail(strings.NewReader(`plain failure`))
	if got != "plain failure" {
		t.Fatalf("detail = %q, want raw body", got)
	}
}

func TestRedactRemovesBotTokenFromErrors(t *testing.T) {
	token := "123456:super-secret-token"
	a := NewWithID("telegram-test", token, "agent", nil)
	got := a.redact(fmt.Errorf(`Get "https://api.telegram.org/bot%s/getUpdates": context canceled`, token))
	if strings.Contains(got, token) {
		t.Fatalf("redacted error leaked token: %s", got)
	}
	if !strings.Contains(got, "<telegram-bot-token>") {
		t.Fatalf("redacted error missing placeholder: %s", got)
	}
}

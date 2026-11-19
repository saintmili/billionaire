package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"time"
	"unicode/utf8"
)

// Telegram holds resolved credentials only in the delivery adapter. Transport is
// injectable for local protocol fixtures; nil uses the standard HTTPS transport.
type Telegram struct {
	token, chat string
	client      *http.Client
}

var telegramToken = regexp.MustCompile(`^[0-9]+:[A-Za-z0-9_-]+$`)
var telegramChat = regexp.MustCompile(`^(-?[0-9]+|@[A-Za-z0-9_]+)$`)

func NewTelegram(token, chat string, transport http.RoundTripper) *Telegram {
	return &Telegram{token: token, chat: chat, client: &http.Client{Transport: transport, Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (t *Telegram) Send(ctx context.Context, e Event) error {
	failure := func(category string, permanent bool) error {
		return &DeliveryError{Category: category, Permanent: permanent}
	}
	if len(t.token) > 256 || len(t.chat) > 256 || !telegramToken.MatchString(t.token) || !telegramChat.MatchString(t.chat) {
		return failure("telegram_unavailable", true)
	}
	// JSON escapes control characters and preserves the common event context. The
	// byte limit is conservative for Telegram's character limit, including emoji.
	data, _ := json.Marshal(e)
	text := string(data)
	if len(text) > 4096 {
		text = text[:4093]
		for !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
		text += "..."
	}
	body, _ := json.Marshal(struct {
		Chat    string `json:"chat_id"`
		Text    string `json:"text"`
		Preview struct {
			Disabled bool `json:"is_disabled"`
		} `json:"link_preview_options"`
	}{Chat: t.chat, Text: text, Preview: struct {
		Disabled bool `json:"is_disabled"`
	}{true}})
	request, err := http.NewRequestWithContext(ctx, "POST", "https://api.telegram.org/bot"+t.token+"/sendMessage", bytes.NewReader(body))
	if err != nil {
		return failure("telegram_request", true)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := t.client.Do(request)
	if err != nil {
		return failure("telegram_transport", false)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil || len(payload) > 65536 {
		return failure("telegram_response", false)
	}
	var result struct {
		OK         bool `json:"ok"`
		Code       int  `json:"error_code"`
		Parameters struct {
			Retry json.Number `json:"retry_after"`
		} `json:"parameters"`
	}
	decodeErr := json.Unmarshal(payload, &result)
	code := response.StatusCode
	if decodeErr == nil && result.Code != 0 {
		code = result.Code
	}
	if code == 429 {
		delay := time.Second
		seconds, _ := strconv.ParseUint(string(result.Parameters.Retry), 10, 64)
		if seconds > 0 {
			const maxSeconds = uint64((1<<63 - 1) / int64(time.Second))
			delay = time.Duration(min(seconds, maxSeconds)) * time.Second
		}
		return &DeliveryError{Category: "telegram_rate_limited", RetryAfter: delay}
	}
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		return failure("telegram_redirect", true)
	}
	if code >= 400 && code < 500 {
		return failure("telegram_rejected", true)
	}
	if code >= 500 {
		return failure("telegram_server", false)
	}
	if decodeErr != nil || !result.OK || response.StatusCode < 200 || response.StatusCode >= 300 {
		return failure("telegram_response", false)
	}
	return nil
}

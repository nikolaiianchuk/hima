package notify

import (
	"bytes"
	"context"
	"errors"
	"mime/multipart"
	"net/http"
	"strings"
)

// TelegramConfig sends Events through a Telegram bot to one chat.
type TelegramConfig struct {
	BotToken string
	ChatID   string
	// AttachICS sends a Booking's .ics as a document with new-Booking and
	// reschedule notifications.
	AttachICS bool
	// APIBase is https://api.telegram.org unless overridden in tests.
	APIBase string
}

func (t *TelegramConfig) url(method string) string {
	base := t.APIBase
	if base == "" {
		base = "https://api.telegram.org"
	}
	return base + "/bot" + t.BotToken + "/" + method
}

// sendTelegram delivers ev. Errors are scrubbed of the bot token, which
// Telegram puts in the request URL and Go's network errors repeat.
func (d *Dispatcher) sendTelegram(ctx context.Context, ev Event) error {
	if err := d.sendTelegramRaw(ctx, ev); err != nil {
		return redactedError{err: err, secret: d.Telegram.BotToken}
	}
	return nil
}

type redactedError struct {
	err    error
	secret string
}

func (e redactedError) Error() string { return strings.ReplaceAll(e.err.Error(), e.secret, "…") }
func (e redactedError) Unwrap() error { return e.err }

func (d *Dispatcher) sendTelegramRaw(ctx context.Context, ev Event) error {
	t := d.Telegram
	text := ev.Title
	if ev.Message != "" {
		text += "\n" + ev.Message
	}
	if t.AttachICS && ev.Attachment != nil {
		err := d.telegramDocument(ctx, text, ev.Attachment)
		var status *statusError
		if !errors.As(err, &status) || status.code >= 500 {
			return err
		}
		d.Log.Warn("telegram refused the attachment, sending text", "err", err)
	}
	body := bytes.NewBufferString(`{"chat_id":` + jsonString(t.ChatID) + `,"text":` + jsonString(text) + `,"disable_web_page_preview":true}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url("sendMessage"), body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return do(d.client(), req)
}

func (d *Dispatcher) telegramDocument(ctx context.Context, caption string, att *Attachment) error {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("chat_id", d.Telegram.ChatID)
	mw.WriteField("caption", caption)
	fw, err := mw.CreateFormFile("document", att.Filename)
	if err != nil {
		return err
	}
	fw.Write(att.Data)
	mw.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.Telegram.url("sendDocument"), &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return do(d.client(), req)
}

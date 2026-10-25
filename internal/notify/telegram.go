package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"html"
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

// telegramIcons are emoji with U+FE0F where needed (🗓️, 🏷️, ⚠️, ✏️, ➡️):
// without it some apps draw a narrow text glyph that swallows the space.
var telegramIcons = map[Kind]string{
	Booked:            "🗓️",
	Rescheduled:       "🔄",
	CancelledByGuest:  "❌",
	CancelledByOwner:  "❌",
	EditedByOwner:     "✏️",
	CalendarFailing:   "⚠️",
	CalendarRecovered: "✅",
	WriteBackFailed:   "⚠️",
}

// telegramHTML renders ev in Telegram's HTML subset: a bold title, then one
// line per Booking detail.
func telegramHTML(ev Event) string {
	e := html.EscapeString
	var b strings.Builder
	if icon := telegramIcons[ev.Kind]; icon != "" {
		b.WriteString(icon + " ")
	}
	b.WriteString("<b>" + e(ev.Title) + "</b>")
	bk := ev.Booking
	if bk == nil {
		if ev.Message != "" {
			b.WriteString("\n" + e(ev.Message))
		}
		return b.String()
	}
	line := func(icon, text string) { b.WriteString("\n" + icon + " " + text) }
	switch {
	case bk.PreviousWhen != "":
		line("🕐", "<s>"+e(bk.PreviousWhen)+"</s>")
		line("➡️", "<b>"+e(bk.When)+"</b>")
	case ev.Kind == CancelledByGuest || ev.Kind == CancelledByOwner:
		line("🕐", "<s>"+e(bk.When)+"</s>")
	default:
		line("🕐", e(bk.When))
	}
	if loc := bk.Location; loc != "" {
		if strings.HasPrefix(loc, "https://") || strings.HasPrefix(loc, "http://") {
			line("📍", `<a href="`+e(loc)+`">`+e(loc)+"</a>")
		} else {
			line("📍", e(loc))
		}
	}
	if bk.Note != "" {
		// Captions on documents are capped at 1024 characters.
		note := []rune(bk.Note)
		if len(note) > 300 {
			note = append(note[:299], '…')
		}
		line("💬", "<i>"+e(string(note))+"</i>")
	}
	if bk.InviteLabel != "" {
		line("🏷️", "Invite “"+e(bk.InviteLabel)+"”")
	}
	return b.String()
}

// telegramButton links to the admin; Telegram only accepts https buttons.
func (d *Dispatcher) telegramButton() any {
	if !strings.HasPrefix(d.ClickURL, "https://") {
		return nil
	}
	return map[string]any{"inline_keyboard": [][]map[string]string{{{"text": "Open in hima", "url": d.ClickURL}}}}
}

func (d *Dispatcher) sendTelegramRaw(ctx context.Context, ev Event) error {
	t := d.Telegram
	text := telegramHTML(ev)
	if t.AttachICS && ev.Attachment != nil {
		err := d.telegramDocument(ctx, text, ev.Attachment)
		var status *statusError
		if !errors.As(err, &status) || status.code >= 500 {
			return err
		}
		d.Log.Warn("telegram refused the attachment, sending text", "err", err)
	}
	msg := map[string]any{
		"chat_id":              t.ChatID,
		"text":                 text,
		"parse_mode":           "HTML",
		"link_preview_options": map[string]bool{"is_disabled": true},
	}
	if btn := d.telegramButton(); btn != nil {
		msg["reply_markup"] = btn
	}
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url("sendMessage"), bytes.NewReader(body))
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
	mw.WriteField("parse_mode", "HTML")
	if btn := d.telegramButton(); btn != nil {
		b, _ := json.Marshal(btn)
		mw.WriteField("reply_markup", string(b))
	}
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

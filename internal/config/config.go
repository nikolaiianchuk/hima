// Package config loads hima's configuration from a YAML file and HIMA_*
// environment variables, and refuses configurations that would expose an
// unprotected admin (see docs/adr/0002).
package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/nikolaiianchuk/hima/internal/totp"
)

const (
	AuthModeProxy    = "proxy"
	AuthModePassword = "password"
)

type Config struct {
	// Listen is the address the HTTP server binds to. Defaults to
	// 127.0.0.1:8080 in proxy mode and :8080 in password mode.
	Listen string `yaml:"listen"`
	// BaseURL is the public URL used when building Invite and Manage links.
	BaseURL  string `yaml:"base_url"`
	Database string `yaml:"database"`
	Owner    Owner  `yaml:"owner"`
	Auth     Auth   `yaml:"auth"`

	AvailabilityConfig AvailabilityConfig `yaml:"availability"`
	InviteDefaults     InviteDefaults     `yaml:"invite_defaults"`
	Notifications      Notifications      `yaml:"notifications"`
	// RetentionDays is how long past Bookings, dead Invites and activity are
	// kept before being deleted.
	RetentionDays int `yaml:"retention_days"`
	// Calendars are the Calendar sources whose events are Busy blocks.
	Calendars []CalendarConfig `yaml:"calendars"`
	// CalDAV is an optional account to read calendars from and write
	// Bookings to.
	CalDAV CalDAVConfig `yaml:"caldav"`
	// Google and Microsoft connect through OAuth apps the Owner registers.
	Google    GoogleConfig    `yaml:"google"`
	Microsoft MicrosoftConfig `yaml:"microsoft"`
}

type Owner struct {
	Name string `yaml:"name"`
	// Emails identify the Owner among meeting attendees so invitations they
	// declined are not Busy blocks. Never shown or sent anywhere.
	Emails []string `yaml:"emails"`
}

type Auth struct {
	// Mode is mandatory: "proxy" or "password". There is no default.
	Mode string `yaml:"mode"`
	// ProxyHeader, in proxy mode, is a header the reverse proxy must set
	// (e.g. Remote-User). Empty means every request reaching hima is trusted.
	ProxyHeader string `yaml:"proxy_header"`
	// PasswordHash, in password mode, is a bcrypt hash produced by
	// `hima hash-password`.
	PasswordHash string `yaml:"password_hash"`
	// TOTPSecret, in password mode, adds a one-time code from an
	// authenticator app; generate it with `hima totp-setup`.
	TOTPSecret string `yaml:"totp_secret"`
}

// Load reads path (if non-empty and present), applies environment overrides,
// fills defaults and validates the result.
func Load(path string) (*Config, error) {
	var c Config
	if path != "" {
		b, err := os.ReadFile(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
		case err != nil:
			return nil, fmt.Errorf("read config: %w", err)
		default:
			if err := yaml.Unmarshal(b, &c); err != nil {
				return nil, fmt.Errorf("parse config %s: %w", path, err)
			}
		}
	}
	c.applyEnv(os.Getenv)
	if f := c.CalDAV.PasswordFile; f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("caldav.password_file: %w", err)
		}
		c.CalDAV.Password = strings.TrimSpace(string(b))
	}
	c.applyDefaults()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) applyEnv(getenv func(string) string) {
	set := func(dst *string, key string) {
		if v := getenv(key); v != "" {
			*dst = v
		}
	}
	set(&c.Listen, "HIMA_LISTEN")
	set(&c.BaseURL, "HIMA_BASE_URL")
	set(&c.Database, "HIMA_DATABASE")
	set(&c.Owner.Name, "HIMA_OWNER_NAME")
	if v := getenv("HIMA_OWNER_EMAILS"); v != "" {
		c.Owner.Emails = strings.Split(v, ",")
	}
	set(&c.Auth.Mode, "HIMA_AUTH_MODE")
	set(&c.Auth.ProxyHeader, "HIMA_AUTH_PROXY_HEADER")
	set(&c.Auth.PasswordHash, "HIMA_AUTH_PASSWORD_HASH")
	set(&c.Auth.TOTPSecret, "HIMA_AUTH_TOTP_SECRET")
	set(&c.Notifications.Ntfy.URL, "HIMA_NTFY_URL")
	set(&c.Notifications.Ntfy.Token, "HIMA_NTFY_TOKEN")
	set(&c.Notifications.Webhook.URL, "HIMA_WEBHOOK_URL")
	set(&c.Notifications.Telegram.BotToken, "HIMA_TELEGRAM_BOT_TOKEN")
	set(&c.Notifications.Telegram.ChatID, "HIMA_TELEGRAM_CHAT_ID")
	set(&c.CalDAV.URL, "HIMA_CALDAV_URL")
	set(&c.CalDAV.Username, "HIMA_CALDAV_USERNAME")
	set(&c.CalDAV.Password, "HIMA_CALDAV_PASSWORD")
	set(&c.CalDAV.PasswordFile, "HIMA_CALDAV_PASSWORD_FILE")
	set(&c.CalDAV.Write, "HIMA_CALDAV_WRITE")
	set(&c.Google.ClientID, "HIMA_GOOGLE_CLIENT_ID")
	set(&c.Google.ClientSecret, "HIMA_GOOGLE_CLIENT_SECRET")
	set(&c.Microsoft.ClientID, "HIMA_MICROSOFT_CLIENT_ID")
	set(&c.Microsoft.ClientSecret, "HIMA_MICROSOFT_CLIENT_SECRET")
	set(&c.Microsoft.Tenant, "HIMA_MICROSOFT_TENANT")
	if v := getenv("HIMA_NTFY_ATTACH_ICS"); v != "" {
		c.Notifications.Ntfy.AttachICS = v == "true" || v == "1"
	}
}

func (c *Config) applyDefaults() {
	if c.Listen == "" {
		if c.Auth.Mode == AuthModeProxy {
			c.Listen = "127.0.0.1:8080"
		} else {
			c.Listen = ":8080"
		}
	}
	if c.Database == "" {
		c.Database = "hima.db"
	}
	if c.Owner.Name == "" {
		c.Owner.Name = "the owner"
	}
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	c.AvailabilityConfig.applyDefaults()
	c.InviteDefaults.applyDefaults()
	if c.RetentionDays == 0 {
		c.RetentionDays = 90
	}
}

func (c *Config) Validate() error {
	switch c.Auth.Mode {
	case "":
		return errors.New(`auth.mode is required: set it to "proxy" (admin protected by your reverse proxy) or "password" (built-in login)`)
	case AuthModeProxy:
	case AuthModePassword:
		if c.Auth.PasswordHash == "" {
			return errors.New("auth.mode is password but auth.password_hash is empty; generate one with `hima hash-password`")
		}
	default:
		return fmt.Errorf(`auth.mode %q is invalid: use "proxy" or "password"`, c.Auth.Mode)
	}
	if c.Auth.TOTPSecret != "" {
		if c.Auth.Mode != AuthModePassword {
			return errors.New("auth.totp_secret only applies to password mode; in proxy mode, use your proxy's second factor")
		}
		if _, err := totp.ParseSecret(c.Auth.TOTPSecret); err != nil {
			return fmt.Errorf("auth.totp_secret: %w", err)
		}
	}
	if _, err := c.Availability(); err != nil {
		return err
	}
	if err := c.InviteDefaults.validate(); err != nil {
		return err
	}
	// Manage links work for 30 days after a Booking ends; keep it at least that long.
	if c.RetentionDays < 31 {
		return errors.New("retention_days must be at least 31")
	}
	for name, u := range map[string]string{"notifications.ntfy.url": c.Notifications.Ntfy.URL, "notifications.webhook.url": c.Notifications.Webhook.URL} {
		if u != "" && !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
			return fmt.Errorf("%s must be an http(s) URL", name)
		}
	}
	if tg := c.Notifications.Telegram; (tg.BotToken == "") != (tg.ChatID == "") {
		return errors.New("notifications.telegram needs both bot_token and chat_id")
	}
	if dav := c.CalDAV; dav.URL != "" {
		if !strings.HasPrefix(dav.URL, "https://") && !strings.HasPrefix(dav.URL, "http://") {
			return errors.New("caldav.url must be an http(s) URL")
		}
		if dav.Username == "" || dav.Password == "" {
			return errors.New("caldav needs username and password (or password_file); use an app password where your provider offers one")
		}
	}
	if (c.Google.ClientID != "" || c.Microsoft.ClientID != "") && !strings.HasPrefix(c.BaseURL, "http") {
		return errors.New("google and microsoft need base_url: the OAuth redirect URI is built from it")
	}
	if c.Google.ClientID != "" && c.Google.ClientSecret == "" {
		return errors.New("google.client_secret is required")
	}
	if c.Microsoft.ClientID != "" && c.Microsoft.ClientSecret == "" {
		return errors.New("microsoft.client_secret is required")
	}
	writers := 0
	for _, on := range []bool{c.CalDAV.URL != "" && c.CalDAV.Write != "", c.Google.ClientID != "" && c.Google.Write != "", c.Microsoft.ClientID != "" && c.Microsoft.Write} {
		if on {
			writers++
		}
	}
	if writers > 1 {
		return errors.New("Bookings can be written to one calendar only: set write on just one of caldav, google and microsoft")
	}
	for i, cal := range c.Calendars {
		if !strings.HasPrefix(cal.URL, "https://") && !strings.HasPrefix(cal.URL, "http://") {
			return fmt.Errorf("calendars[%d].url must be an http(s) URL (webcal:// links: replace webcal with https)", i)
		}
	}
	return nil
}

// InviteDefaults pre-fill the admin's "new Invite" form.
type InviteDefaults struct {
	ExpiresInDays int             `yaml:"expires_in_days"`
	MaxUses       int             `yaml:"max_uses"`
	Durations     []time.Duration `yaml:"durations"`
	Location      string          `yaml:"location"`
}

func (d *InviteDefaults) applyDefaults() {
	if d.ExpiresInDays == 0 {
		d.ExpiresInDays = 7
	}
	if d.MaxUses == 0 {
		d.MaxUses = 1
	}
	if len(d.Durations) == 0 {
		d.Durations = []time.Duration{30 * time.Minute}
	}
}

func (d InviteDefaults) validate() error {
	if d.ExpiresInDays < 0 || d.MaxUses < 0 {
		return errors.New("invite_defaults: expires_in_days and max_uses must be positive")
	}
	for _, dur := range d.Durations {
		if dur <= 0 || dur%time.Minute != 0 {
			return fmt.Errorf("invite_defaults.durations: %v is not a whole number of minutes", dur)
		}
	}
	return nil
}

// Notifications tell the Owner about Bookings, reschedules, Guest
// Cancellations and failing Calendar sources.
type Notifications struct {
	Ntfy struct {
		// URL is the full topic URL, e.g. https://ntfy.sh/some-secret-topic.
		URL   string `yaml:"url"`
		Token string `yaml:"token"`
		// AttachICS attaches the .ics to new-Booking notifications.
		AttachICS bool `yaml:"attach_ics"`
	} `yaml:"ntfy"`
	Webhook struct {
		URL string `yaml:"url"`
	} `yaml:"webhook"`
	Telegram struct {
		BotToken string `yaml:"bot_token"`
		ChatID   string `yaml:"chat_id"`
		// AttachICS sends the .ics with new-Booking and reschedule messages.
		AttachICS bool `yaml:"attach_ics"`
	} `yaml:"telegram"`
}

type CalDAVConfig struct {
	URL          string `yaml:"url"`
	Username     string `yaml:"username"`
	Password     string `yaml:"password"`
	PasswordFile string `yaml:"password_file"`
	// Read names the calendars that are Calendar sources. Omitted reads every
	// calendar; an empty list reads none.
	Read         []string `yaml:"read"`
	AllDayBlocks bool     `yaml:"all_day_blocks"`
	// Write names the calendar Bookings are written to; empty disables it.
	Write string `yaml:"write"`
}

type GoogleConfig struct {
	ClientID     string `yaml:"client_id"`
	ClientSecret string `yaml:"client_secret"`
	// Read lists calendar IDs used as Calendar sources. Omitted reads the
	// primary calendar; an empty list reads none.
	Read []string `yaml:"read"`
	// Write is the calendar ID Bookings are written to ("primary" for the
	// main one); empty disables it.
	Write string `yaml:"write"`
}

type MicrosoftConfig struct {
	ClientID     string `yaml:"client_id"`
	ClientSecret string `yaml:"client_secret"`
	// Tenant restricts sign-in to one directory; default "common".
	Tenant string `yaml:"tenant"`
	// Read uses the mailbox's free/busy as a Calendar source (default true).
	Read *bool `yaml:"read"`
	// Write puts Bookings in the default calendar.
	Write bool `yaml:"write"`
}

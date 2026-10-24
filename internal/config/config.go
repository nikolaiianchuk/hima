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
	// Calendars are the Calendar sources whose events are Busy blocks.
	Calendars []CalendarConfig `yaml:"calendars"`
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
	set(&c.Notifications.Ntfy.URL, "HIMA_NTFY_URL")
	set(&c.Notifications.Ntfy.Token, "HIMA_NTFY_TOKEN")
	set(&c.Notifications.Webhook.URL, "HIMA_WEBHOOK_URL")
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
	if _, err := c.Availability(); err != nil {
		return err
	}
	if err := c.InviteDefaults.validate(); err != nil {
		return err
	}
	for name, u := range map[string]string{"notifications.ntfy.url": c.Notifications.Ntfy.URL, "notifications.webhook.url": c.Notifications.Webhook.URL} {
		if u != "" && !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
			return fmt.Errorf("%s must be an http(s) URL", name)
		}
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
}

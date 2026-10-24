// Package config loads hima's configuration from a YAML file and HIMA_*
// environment variables, and refuses configurations that would expose an
// unprotected admin (see docs/adr/0002).
package config

import (
	"errors"
	"fmt"
	"os"
	"strings"

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
	// Calendars are the Calendar sources whose events are Busy blocks.
	Calendars []CalendarConfig `yaml:"calendars"`
}

type Owner struct {
	Name string `yaml:"name"`
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
	set(&c.Auth.Mode, "HIMA_AUTH_MODE")
	set(&c.Auth.ProxyHeader, "HIMA_AUTH_PROXY_HEADER")
	set(&c.Auth.PasswordHash, "HIMA_AUTH_PASSWORD_HASH")
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
	for i, cal := range c.Calendars {
		if !strings.HasPrefix(cal.URL, "https://") && !strings.HasPrefix(cal.URL, "http://") {
			return fmt.Errorf("calendars[%d].url must be an http(s) URL (webcal:// links: replace webcal with https)", i)
		}
	}
	return nil
}

package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	tests := []struct {
		name       string
		yaml       string
		env        map[string]string
		wantErr    bool
		wantListen string
	}{
		{name: "missing auth mode refuses to start", yaml: "owner: {name: Nikolai}", wantErr: true},
		{name: "unknown auth mode", yaml: "auth: {mode: none}", wantErr: true},
		{name: "password mode without hash", yaml: "auth: {mode: password}", wantErr: true},
		{name: "proxy mode binds localhost by default", yaml: "auth: {mode: proxy}", wantListen: "127.0.0.1:8080"},
		{name: "proxy mode honours explicit listen", yaml: "listen: ':9000'\nauth: {mode: proxy}", wantListen: ":9000"},
		{name: "password mode binds all interfaces", yaml: "auth: {mode: password, password_hash: x}", wantListen: ":8080"},
		{name: "bad weekday", yaml: "auth: {mode: proxy}\navailability: {weekly: {monday: ['10:00-12:00']}}", wantErr: true},
		{name: "bad range", yaml: "auth: {mode: proxy}\navailability: {weekly: {mon: ['12:00-10:00']}}", wantErr: true},
		{name: "unknown timezone", yaml: "auth: {mode: proxy}\navailability: {timezone: Mars/Olympus}", wantErr: true},
		{name: "webcal url", yaml: "auth: {mode: proxy}\ncalendars: [{url: 'webcal://x/y.ics'}]", wantErr: true},
		{name: "retention too short", yaml: "auth: {mode: proxy}\nretention_days: 7", wantErr: true},
		{name: "caldav without password", yaml: "auth: {mode: proxy}\ncaldav: {url: 'https://dav.example.com', username: me}", wantErr: true},
		{name: "caldav ok", yaml: "auth: {mode: proxy}\ncaldav: {url: 'https://dav.example.com', username: me, password: x}", wantListen: "127.0.0.1:8080"},
		{name: "oauth needs base_url", yaml: "auth: {mode: proxy}\ngoogle: {client_id: a, client_secret: b}", wantErr: true},
		{name: "one write target", yaml: "auth: {mode: proxy}\nbase_url: https://h.test\ngoogle: {client_id: a, client_secret: b, write: primary}\nmicrosoft: {client_id: a, client_secret: b, write: true}", wantErr: true},
		{name: "google ok", yaml: "auth: {mode: proxy}\nbase_url: https://h.test\ngoogle: {client_id: a, client_secret: b, write: primary}", wantListen: "127.0.0.1:8080"},
		{name: "email invites need from", yaml: "auth: {mode: proxy}\nowner: {emails: [me@x.com]}\nemail_invites: {smtp: {host: smtp.x.com}}", wantErr: true},
		{name: "email invites from = to", yaml: "auth: {mode: proxy}\nowner: {emails: [me@x.com]}\nemail_invites: {from: me@x.com, smtp: {host: smtp.x.com}}", wantErr: true},
		{name: "email invites and caldav write", yaml: "auth: {mode: proxy}\nowner: {emails: [me@x.com]}\nemail_invites: {from: hima@x.com, smtp: {host: smtp.x.com}}\ncaldav: {url: 'https://d.x', username: u, password: p, write: P}", wantErr: true},
		{name: "email invites ok", yaml: "auth: {mode: proxy}\nowner: {emails: [me@x.com]}\nemail_invites: {from: hima@x.com, smtp: {host: smtp.x.com}}", wantListen: "127.0.0.1:8080"},
		{name: "env overrides file", yaml: "auth: {mode: password}", env: map[string]string{"HIMA_AUTH_MODE": "proxy"}, wantListen: "127.0.0.1:8080"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "hima.yaml")
			if err := os.WriteFile(path, []byte(tt.yaml), 0o600); err != nil {
				t.Fatal(err)
			}
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			c, err := Load(path)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.Listen != tt.wantListen {
				t.Errorf("Listen = %q, want %q", c.Listen, tt.wantListen)
			}
		})
	}
}

func TestAvailabilityFromYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hima.yaml")
	os.WriteFile(path, []byte(`
auth: {mode: proxy}
availability:
  timezone: Europe/Lisbon
  weekly:
    mon: ["09:00-12:00", "14:00-18:00"]
    sat: ["10:00-13:00"]
  buffer: 15m
  min_notice: 12h
  horizon_days: 14
`), 0o600)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	a, err := c.Availability()
	if err != nil {
		t.Fatal(err)
	}
	if a.Location.String() != "Europe/Lisbon" || len(a.Weekly[time.Monday]) != 2 || len(a.Weekly[time.Saturday]) != 1 ||
		len(a.Weekly[time.Tuesday]) != 0 || a.Buffer != 15*time.Minute || a.MinNotice != 12*time.Hour ||
		a.Horizon != 14*24*time.Hour || a.Step != 30*time.Minute {
		t.Errorf("unexpected availability: %+v", a)
	}
}

func TestAvailabilityDefaultsToWeekdayOfficeHours(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hima.yaml")
	os.WriteFile(path, []byte("auth: {mode: proxy}"), 0o600)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := c.Availability()
	if len(a.Weekly[time.Friday]) != 1 || len(a.Weekly[time.Sunday]) != 0 || a.Horizon != 30*24*time.Hour {
		t.Errorf("unexpected defaults: %+v", a)
	}
}

func TestCalDAVPasswordFileAndRead(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "pw"), []byte("s3cret\n"), 0o600)
	path := filepath.Join(dir, "hima.yaml")
	os.WriteFile(path, []byte("auth: {mode: proxy}\ncaldav: {url: 'https://dav.example.com', username: me, password_file: '"+filepath.Join(dir, "pw")+"', read: [], write: Personal}"), 0o600)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.CalDAV.Password != "s3cret" {
		t.Errorf("password = %q", c.CalDAV.Password)
	}
	// read: [] turns reading off, so only write-back remains.
	if got := c.Sources(c.CalDAVAccount(), time.Hour); len(got) != 0 {
		t.Errorf("sources = %d, want 0", len(got))
	}
	c.CalDAV.Read = nil
	if got := c.Sources(c.CalDAVAccount(), time.Hour); len(got) != 1 || got[0].Fetcher.Name() != "CalDAV dav.example.com (all calendars)" {
		t.Errorf("sources = %+v", got)
	}
}

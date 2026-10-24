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

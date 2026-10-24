package config

import (
	"os"
	"path/filepath"
	"testing"
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

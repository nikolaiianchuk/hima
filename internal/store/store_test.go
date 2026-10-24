package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenMigratesAndReopens(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "hima.db")

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()

	// Reopening must not re-run applied migrations.
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	now := time.Now()
	if err := s.CreateSession(ctx, "h", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.SessionValid(ctx, "h", now); err != nil || !ok {
		t.Fatalf("SessionValid = %v, %v; want true", ok, err)
	}
	if ok, _ := s.SessionValid(ctx, "h", now.Add(2*time.Hour)); ok {
		t.Error("expired session reported valid")
	}
	if err := s.DeleteSession(ctx, "h"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.SessionValid(ctx, "h", now); ok {
		t.Error("deleted session reported valid")
	}
}

// Command hima runs the hima server.
//
//	hima [-config hima.yaml]   start the server
//	hima hash-password         print a bcrypt hash for auth.password_hash
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // the distroless image has no zoneinfo

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/term"

	"github.com/nikolaiianchuk/hima/internal/booking"
	"github.com/nikolaiianchuk/hima/internal/calsource"
	"github.com/nikolaiianchuk/hima/internal/config"
	"github.com/nikolaiianchuk/hima/internal/ics"
	"github.com/nikolaiianchuk/hima/internal/store"
	"github.com/nikolaiianchuk/hima/internal/web"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "hash-password" {
		if err := hashPassword(); err != nil {
			fmt.Fprintln(os.Stderr, "hima:", err)
			os.Exit(1)
		}
		return
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(log); err != nil {
		log.Error("exiting", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	defaultConfig := os.Getenv("HIMA_CONFIG")
	if defaultConfig == "" {
		defaultConfig = "hima.yaml"
	}
	configPath := flag.String("config", defaultConfig, "path to the config file (optional if HIMA_* env vars are set)")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.Database)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer st.Close()

	avail, err := cfg.Availability()
	if err != nil {
		return err
	}
	cals := calsource.New(cfg.Sources(), ics.Options{Location: avail.Location, OwnerEmails: cfg.Owner.Emails}, nil)
	go cals.Run(ctx, 5*time.Minute, func(err error) {
		log.Warn("calendar source refresh failed", "err", err)
	})
	svc := &booking.Service{Store: st, Availability: avail, Calendars: cals, Now: time.Now}

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           web.New(cfg, st, svc, log),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.Listen, "auth", cfg.Auth.Mode)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// hashPassword reads a password (hidden on a terminal, or one line from a pipe)
// and prints its bcrypt hash.
func hashPassword() error {
	var pw []byte
	if term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprint(os.Stderr, "Password: ")
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return err
		}
		pw = b
	} else {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return err
		}
		pw = []byte(strings.TrimRight(line, "\r\n"))
	}
	if len(pw) == 0 {
		return errors.New("empty password")
	}
	hash, err := bcrypt.GenerateFromPassword(pw, bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	fmt.Println(string(hash))
	return nil
}

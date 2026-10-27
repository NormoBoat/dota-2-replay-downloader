package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/normo/dota-collector/internal/config"
	"github.com/normo/dota-collector/internal/secret"
	"github.com/normo/dota-collector/internal/steamx"
	"github.com/normo/dota-collector/internal/store"
	steam "github.com/paralin/go-steam"
	"golang.org/x/term"
)

var stdin = bufio.NewReader(os.Stdin)

// ask reads env or one line from stdin; secret input is not echoed on a terminal.
func ask(env, prompt string, hidden bool) string {
	if v := os.Getenv(env); v != "" {
		return v
	}
	fmt.Print(prompt)
	if hidden && term.IsTerminal(int(os.Stdin.Fd())) {
		b, _ := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		return strings.TrimSpace(string(b))
	}
	line, _ := stdin.ReadString('\n')
	return strings.TrimSpace(line)
}

// login exchanges the password (and Steam Guard code) for a token once; only the
// encrypted token is stored. One attempt, one retry with a Guard code: never a loop.
func login(ctx context.Context, cfg *config.Config, s *store.Store, box *secret.Box) error {
	user := ask("STEAM_USER", "Steam login: ", false)
	pass := ask("STEAM_PASS", "Steam password: ", true)
	if user == "" || pass == "" {
		return errors.New("Steam login and password are required")
	}
	code := os.Getenv("STEAM_GUARD_CODE")
	client := steam.NewClient()
	defer client.Disconnect()
	authenticate := func() (string, error) {
		actx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		return client.Auth.Authenticate(actx, &steam.LogOnDetails{Username: user, Password: pass, AuthCode: code, TwoFactorCode: code, DeviceFriendlyName: "dota-collector"})
	}
	token, err := authenticate()
	var authErr *steam.AuthSessionError
	if err != nil && code == "" && errors.As(err, &authErr) && len(authErr.Confirmations) > 0 {
		if code = ask("", "Steam Guard code: ", false); code != "" {
			token, err = authenticate()
		}
	}
	if err != nil {
		return fmt.Errorf("Steam authentication failed (%w); not retried to avoid login throttling", err)
	}
	if err = s.SaveLogin(ctx, user, box.Seal(token), cfg.GC.DailyLimit); err != nil {
		return err
	}
	fmt.Println("token saved; checking GC session...")
	session, err := steamx.Open(ctx, user, token, cfg.GC)
	if errors.Is(err, steamx.ErrPlayingElsewhere) {
		fmt.Println("account is playing elsewhere: GC check postponed, the collector waits until it is free")
		return nil
	}
	if err != nil {
		return fmt.Errorf("token saved but GC session failed: %w", err)
	}
	session.Close()
	fmt.Println("GC session OK")
	return nil
}

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/normo/dota-collector/internal/app"
	"github.com/normo/dota-collector/internal/config"
	"github.com/normo/dota-collector/internal/secret"
	"github.com/normo/dota-collector/internal/store"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := command(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func command(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: collector init|migrate|login|run|status|healthcheck [-config path]")
	}
	command := args[0]
	args = args[1:]
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	path := flags.String("config", filepath.Join(filepath.Dir(exe), "settings.toml"), "settings path")
	if err = flags.Parse(args); err != nil {
		return err
	}
	if command == "init" {
		if err = config.Init(*path); err != nil {
			return err
		}
		fmt.Println("Created settings; API token is stored in the private file.")
		return nil
	}
	cfg, err := config.Load(*path)
	if err != nil {
		if os.IsNotExist(err) {
			return errors.New("settings file missing: run collector init")
		}
		return err
	}
	if command == "status" || command == "healthcheck" {
		return remote(command, cfg)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	// Restore the default handler after the first signal so a second signal forces exit.
	go func() { <-ctx.Done(); cancel() }()
	if command == "run" {
		return app.Run(ctx, *path, cfg)
	}
	key, err := cfg.SecretKey()
	if err != nil {
		return err
	}
	box, err := secret.New(key)
	if err != nil {
		return err
	}
	s, err := store.Open(cfg.Database.DSN, cfg.Database.MaxOpenConns)
	if err != nil {
		return errors.New("cannot connect to PostgreSQL")
	}
	defer s.Close()
	if err = s.Migrate(ctx); err != nil {
		return err
	}
	if command == "migrate" {
		return nil
	}
	if command == "login" {
		return login(ctx, cfg, s, box)
	}
	return errors.New("unknown command")
}
func remote(command string, cfg *config.Config) error {
	host, port, err := net.SplitHostPort(cfg.API.Listen)
	if err != nil {
		return err
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	path := "status"
	if command == "healthcheck" {
		path = "health"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", "http://"+net.JoinHostPort(host, port)+"/api/v1/"+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.API.Token)
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}}
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("collector API unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("collector API HTTP %d", resp.StatusCode)
	}
	_, err = io.Copy(os.Stdout, resp.Body)
	return err
}

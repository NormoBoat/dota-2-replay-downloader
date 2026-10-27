// Package config loads, validates and saves settings.toml.
package config

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/jackc/pgx/v5"
)

// Duration is a time.Duration written as "10s" in TOML and JSON.
type Duration struct{ time.Duration }

func (d Duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

func D(v time.Duration) Duration { return Duration{v} }

type Config struct {
	App        App        `toml:"app" json:"app"`
	Database   Database   `toml:"database" json:"database"`
	Security   Security   `toml:"security" json:"-"`
	API        API        `toml:"api" json:"api"`
	GC         GC         `toml:"gc" json:"gc"`
	Downloader Downloader `toml:"downloader" json:"downloader"`
}

type App struct {
	ShutdownTimeout Duration `toml:"shutdown_timeout" json:"shutdown_timeout"`
}

type Database struct {
	DSN          string `toml:"dsn" json:"dsn"`
	MaxOpenConns int    `toml:"max_open_conns" json:"max_open_conns"`
}

type Security struct {
	SecretKey string `toml:"secret_key"`
}

type API struct {
	Listen      string   `toml:"listen" json:"listen"`
	Token       string   `toml:"token" json:"token"`
	CORSOrigins []string `toml:"cors_origins" json:"cors_origins"`
}

type GC struct {
	DailyLimit     int      `toml:"daily_limit" json:"daily_limit"`
	RequestTimeout Duration `toml:"request_timeout" json:"request_timeout"`
	WelcomeTimeout Duration `toml:"welcome_timeout" json:"welcome_timeout"`
}

type Downloader struct {
	Workers int      `toml:"workers" json:"workers"`
	Timeout Duration `toml:"timeout" json:"timeout"`
}

// Default returns the values documented in settings.example.toml.
func Default() Config {
	return Config{
		App:        App{ShutdownTimeout: D(45 * time.Second)},
		Database:   Database{DSN: "host=localhost user=collector password=collector dbname=dota sslmode=disable", MaxOpenConns: 20},
		API:        API{Listen: "127.0.0.1:8080", CORSOrigins: []string{}},
		GC:         GC{DailyLimit: 100, RequestTimeout: D(10 * time.Second), WelcomeTimeout: D(60 * time.Second)},
		Downloader: Downloader{Workers: 4, Timeout: D(10 * time.Minute)},
	}
}

// Load reads path over the defaults; unknown keys are an error.
func Load(path string) (*Config, error) {
	cfg := Default()
	md, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if und := md.Undecoded(); len(und) > 0 {
		keys := make([]string, len(und))
		for i, k := range und {
			keys[i] = k.String()
		}
		return nil, fmt.Errorf("%s: unknown keys: %s", path, strings.Join(keys, ", "))
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &cfg, nil
}

// SecretKey decodes security.secret_key (32 bytes, base64).
func (c *Config) SecretKey() ([]byte, error) {
	k, err := base64.StdEncoding.DecodeString(c.Security.SecretKey)
	if err != nil || len(k) != 32 {
		return nil, errors.New("security.secret_key must be 32 bytes in base64 (run `collector init`)")
	}
	return k, nil
}

func (c *Config) Validate() error {
	var errs []error
	bad := func(key, msg string) { errs = append(errs, fmt.Errorf("%s: %s", key, msg)) }
	pos := func(key string, d Duration) {
		if d.Duration <= 0 {
			bad(key, "must be > 0")
		}
	}
	if _, err := pgx.ParseConfig(c.Database.DSN); err != nil {
		bad("database.dsn", "invalid PostgreSQL connection string")
	}
	if c.Database.MaxOpenConns < 1 {
		bad("database.max_open_conns", "must be >= 1")
	}
	if _, err := c.SecretKey(); err != nil {
		bad("security.secret_key", err.Error())
	}
	if _, _, err := net.SplitHostPort(c.API.Listen); err != nil {
		bad("api.listen", err.Error())
	}
	if len(c.API.Token) < 16 {
		bad("api.token", "must be at least 16 characters")
	}
	pos("app.shutdown_timeout", c.App.ShutdownTimeout)
	if c.GC.DailyLimit < 1 {
		bad("gc.daily_limit", "must be >= 1")
	}
	pos("gc.request_timeout", c.GC.RequestTimeout)
	pos("gc.welcome_timeout", c.GC.WelcomeTimeout)
	if c.Downloader.Workers < 0 || c.Downloader.Workers > 64 {
		bad("downloader.workers", "must be 0..64")
	}
	pos("downloader.timeout", c.Downloader.Timeout)
	return errors.Join(errs...)
}

// Save writes cfg atomically (tmp + fsync + rename) and keeps the previous file as .bak.
func Save(path string, cfg *Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	var buf bytes.Buffer
	buf.WriteString("# Written by collector. Comments are not preserved; see settings.example.toml.\n\n")
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if old, err := os.ReadFile(path); err == nil {
		if err := os.WriteFile(path+".bak", old, 0o600); err != nil {
			return err
		}
	}
	return os.Rename(tmp, path)
}

// Mask is the placeholder for secrets in API responses; sending it back keeps the old value.
const Mask = "***"

// Masked returns a copy safe to show: token and the DSN hidden.
func (c Config) Masked() Config {
	c.Security = Security{}
	if c.API.Token != "" {
		c.API.Token = Mask
	}
	c.Database.DSN = Mask
	return c
}

// Merge applies an edited config from the admin API onto c: security is never
// changed and masked secrets keep their current values.
func (c Config) Merge(edit Config) Config {
	edit.Security = c.Security
	if edit.API.Token == Mask {
		edit.API.Token = c.API.Token
	}
	if edit.Database.DSN == Mask {
		edit.Database.DSN = c.Database.DSN
	}
	return edit
}

// NeedsRestart reports whether switching from c to n needs an in-process restart.
func (c Config) NeedsRestart(n Config) bool {
	return !reflect.DeepEqual(c.Database, n.Database) || !reflect.DeepEqual(c.API, n.API) || !reflect.DeepEqual(c.App, n.App)
}

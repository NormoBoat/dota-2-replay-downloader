package config

import (
	"bytes"
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
	"github.com/normo/dota-collector/internal/secret"
)

// Init creates a private settings file once. It never replaces an existing key.
func Init(path string) error {
	c := Default()
	c.Security.SecretKey = secret.NewKey()
	c.API.Token = secret.NewKey()
	var b bytes.Buffer
	if err := toml.NewEncoder(&b).Encode(c); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("create settings (existing files are never overwritten): %w", err)
	}
	_, err = f.Write(b.Bytes())
	if err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

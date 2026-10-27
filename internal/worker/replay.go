package worker

import (
	"bytes"
	"compress/bzip2"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/normo/dota-collector/internal/store"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ReplayURL is the Valve CDN address (cluster, match_id, replay_salt); tests point it at httptest.
var ReplayURL = "http://replay%d.valve.net/570/%d_%d.dem.bz2"

func ValidateReplay(data []byte) (string, error) {
	var reader io.Reader
	compression := ""
	if bytes.HasPrefix(data, []byte("BZh")) {
		reader = bzip2.NewReader(bytes.NewReader(data))
		compression = "bzip2"
	} else if bytes.HasPrefix(data, []byte{0x28, 0xb5, 0x2f, 0xfd}) {
		z, err := zstd.NewReader(bytes.NewReader(data), zstd.WithDecoderMaxMemory(512<<20))
		if err != nil {
			return "", err
		}
		defer z.Close()
		reader = z
		compression = "zstd"
	} else {
		return "", errors.New("unsupported replay compression")
	}
	head := make([]byte, 8)
	if _, err := io.ReadFull(reader, head); err != nil {
		return "", err
	}
	if !bytes.Equal(head, []byte("PBDEMS2\x00")) {
		return "", errors.New("invalid replay header")
	}
	// Read to EOF to detect truncated streams, without allocating decompressed data.
	n, err := io.Copy(io.Discard, io.LimitReader(reader, 4<<30))
	if err != nil {
		return "", err
	}
	if n >= 4<<30 {
		return "", errors.New("replay exceeds decompression limit")
	}
	return compression, nil
}
func Download(ctx context.Context, s *store.Store, t store.Task, client *http.Client) error {
	if t.TargetID == nil {
		return errors.New("missing match id")
	}
	var m store.Match
	if err := s.DB.WithContext(ctx).First(&m, "match_id = ?", *t.TargetID).Error; err != nil {
		return err
	}
	if m.MatchID <= 0 || m.Cluster == nil || *m.Cluster <= 0 || m.ReplaySalt == nil || *m.ReplaySalt <= 0 {
		return errors.New("match has no replay coordinates")
	}
	u := fmt.Sprintf(ReplayURL, *m.Cluster, m.MatchID, *m.ReplaySalt)
	data, compression, err := FetchReplay(ctx, client, u)
	if err != nil {
		return err
	}
	hash := sha256.Sum256(data)
	r := store.Replay{ReplayMeta: store.ReplayMeta{MatchID: m.MatchID, URL: u, Compression: compression, SizeBytes: int64(len(data)), Sha256: hash[:], DownloadedAt: time.Now()}, Data: data}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&r).Error; err != nil {
			return err
		}
		return store.Finish(tx, t.ID, store.StatusDone, "")
	})
}

// FetchReplay keeps the original compressed bytes and rejects incomplete files.
func FetchReplay(ctx context.Context, client *http.Client, u string) ([]byte, string, error) {
	if client == nil {
		return nil, "", errors.New("replay HTTP client is required")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", errors.Join(errors.New("replay download network error"), ctx.Err())
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, "", fmt.Errorf("replay CDN HTTP %d", resp.StatusCode)
	}
	// shortcut: compressed replays are capped at 256 MiB; use streaming storage if larger files become necessary.
	if resp.ContentLength > 256<<20 {
		return nil, "", errors.New("replay exceeds 256 MiB")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (256<<20)+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) > 256<<20 {
		return nil, "", errors.New("replay exceeds 256 MiB")
	}
	compression, err := ValidateReplay(data)
	if err != nil {
		return nil, "", err
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	return data, compression, nil
}

// DownloadFile is the spike's local check; production Download stores bytes in PostgreSQL.
func DownloadFile(ctx context.Context, client *http.Client, u, path string) error {
	data, _, err := FetchReplay(ctx, client, u)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		f.Close()
		if !ok {
			os.Remove(path)
		}
	}()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}

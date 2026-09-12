// Package cache fetches a URL to a local file and reuses the file until it is
// older than a max age. It is the one place the "download once a day" logic
// lives for the player dump and the id crosswalk.
package cache

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type Options struct {
	URL    string
	Path   string
	MaxAge time.Duration
	HTTP   *http.Client // defaults to http.DefaultClient
	// Validate, if set, must accept the downloaded bytes before they replace
	// the cache. A bad response then never overwrites a good cache.
	Validate func([]byte) error
}

// Info describes where the bytes came from.
type Info struct {
	FetchedAt time.Time // download time, or the cache file's mtime
	FromCache bool      // no download happened this run
	Stale     bool      // a refresh was due but failed; old cache used
	StaleErr  error     // why the refresh failed, when Stale
}

func (i Info) String() string {
	src := "fetched"
	if i.FromCache {
		src = "cache"
	}
	if i.Stale {
		src = "stale cache"
	}
	return src + ", " + i.FetchedAt.Format(time.RFC3339)
}

// Load returns the cached bytes if the cache is fresh, otherwise downloads.
// A failed download falls back to a stale cache when one exists.
func Load(ctx context.Context, o Options) ([]byte, Info, error) {
	if o.URL == "" || o.Path == "" || o.MaxAge <= 0 {
		return nil, Info{}, errors.New("cache: URL, Path and MaxAge are required")
	}
	if o.HTTP == nil {
		o.HTTP = http.DefaultClient
	}
	if o.Validate == nil {
		o.Validate = func([]byte) error { return nil }
	}

	var cacheTime time.Time
	haveCache := false
	if fi, err := os.Stat(o.Path); err == nil {
		cacheTime, haveCache = fi.ModTime(), true
	}

	if haveCache && time.Since(cacheTime) < o.MaxAge {
		data, err := os.ReadFile(o.Path)
		if err != nil {
			return nil, Info{}, fmt.Errorf("cache: read %s: %w", o.Path, err)
		}
		if err := o.Validate(data); err != nil {
			return nil, Info{}, fmt.Errorf("cache: %s is corrupt (delete it to refetch): %w", o.Path, err)
		}
		return data, Info{FetchedAt: cacheTime, FromCache: true}, nil
	}

	data, fetchErr := fetch(ctx, o.HTTP, o.URL)
	if fetchErr == nil {
		if err := o.Validate(data); err != nil {
			fetchErr = fmt.Errorf("bad response from %s: %w", o.URL, err)
		} else if err := writeAtomic(o.Path, data); err != nil {
			return nil, Info{}, fmt.Errorf("cache: write %s: %w", o.Path, err)
		} else {
			return data, Info{FetchedAt: time.Now()}, nil
		}
	}

	if !haveCache {
		return nil, Info{}, fmt.Errorf("cache: %w (and no cache at %s)", fetchErr, o.Path)
	}
	data, err := os.ReadFile(o.Path)
	if err != nil {
		return nil, Info{}, fmt.Errorf("cache: %w; stale cache unreadable: %v", fetchErr, err)
	}
	if err := o.Validate(data); err != nil {
		return nil, Info{}, fmt.Errorf("cache: %w; stale cache corrupt: %v", fetchErr, err)
	}
	return data, Info{FetchedAt: cacheTime, FromCache: true, Stale: true, StaleErr: fetchErr}, nil
}

func fetch(ctx context.Context, c *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "sunday-board")
	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: HTTP %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// WriteAtomic writes data to path via a temp file and rename, mode 0644.
func WriteAtomic(path string, data []byte) error { return writeAtomic(path, data) }

func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	fail := func(err error) error {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		return fail(err)
	}
	if err := os.Chmod(name, 0o644); err != nil {
		return fail(err)
	}
	return os.Rename(name, path)
}

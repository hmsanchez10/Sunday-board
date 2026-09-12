// Package creds is the local-file implementation of ledger.CredStore.
//
// The file is JSON keyed by owner, then platform, then whatever that platform
// needs:
//
//	{
//	  "hector": {
//	    "espn":  {"swid": "{...}", "espn_s2": "..."},
//	    "yahoo": {"client_id": "...", "client_secret": "...", "refresh_token": "..."}
//	  }
//	}
//
// Sleeper's API is public, so no entry is needed for it and none is read.
package creds

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/hmsanchez10/Sunday-board/internal/ledger"
)

// FileCredStore reads credentials from a JSON file on every call. The file is
// small and re-reading means a rotated token is picked up without a restart.
type FileCredStore struct {
	Path string
}

var _ ledger.CredStore = (*FileCredStore)(nil)

func NewFileCredStore(path string) *FileCredStore {
	return &FileCredStore{Path: path}
}

// CredsFor returns the credentials for one owner on one platform. Sleeper
// always gets an empty, non-nil Creds without touching the file.
func (s *FileCredStore) CredsFor(owner ledger.Owner, p ledger.Platform) (ledger.Creds, error) {
	if p == ledger.PlatformSleeper {
		return ledger.Creds{}, nil
	}

	raw, err := os.ReadFile(s.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("creds: %s does not exist; %s/%s needs credentials", s.Path, owner, p)
		}
		return nil, fmt.Errorf("creds: %w", err)
	}

	var file map[ledger.Owner]map[ledger.Platform]map[string]string
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("creds: %s: %w", s.Path, err)
	}

	byPlatform, ok := file[owner]
	if !ok {
		return nil, fmt.Errorf("creds: no entry for owner %q in %s", owner, s.Path)
	}
	entry, ok := byPlatform[p]
	if !ok {
		return nil, fmt.Errorf("creds: no %s entry for owner %q in %s", p, owner, s.Path)
	}

	out := make(ledger.Creds, len(entry))
	for k, v := range entry {
		out[k] = v
	}
	return out, nil
}

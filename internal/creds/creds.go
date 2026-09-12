// Package creds is the local-file implementation of ledger.CredStore.
//
// The file is JSON. The full shape is keyed by owner, then platform, then
// whatever that platform needs:
//
//	{
//	  "hector": {
//	    "espn":  {"espn_s2": "...", "SWID": "{...}"},
//	    "yahoo": {"client_id": "...", "client_secret": "...", "refresh_token": "..."}
//	  }
//	}
//
// A single-owner file may omit the owner level and key on platform directly:
//
//	{"espn": {"espn_s2": "...", "SWID": "{...}"}}
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

	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, fmt.Errorf("creds: %s: %w", s.Path, err)
	}

	var entry map[string]string
	switch {
	case top[string(owner)] != nil:
		var byPlatform map[ledger.Platform]map[string]string
		if err := json.Unmarshal(top[string(owner)], &byPlatform); err != nil {
			return nil, fmt.Errorf("creds: %s: owner %q: %w", s.Path, owner, err)
		}
		entry = byPlatform[p]
	case top[string(p)] != nil:
		if err := json.Unmarshal(top[string(p)], &entry); err != nil {
			return nil, fmt.Errorf("creds: %s: %s: %w", s.Path, p, err)
		}
	}
	if entry == nil {
		return nil, fmt.Errorf("creds: no %s entry for owner %q in %s", p, owner, s.Path)
	}

	out := make(ledger.Creds, len(entry))
	for k, v := range entry {
		out[k] = v
	}
	return out, nil
}

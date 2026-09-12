// Package players builds the canonical player table from Sleeper's
// /v1/players/nfl dump (~14MB, cached for 24h) and resolves each player's
// cross-platform ids.
//
// Resolution order for ESPN/Yahoo ids:
//  1. the DynastyProcess crosswalk keyed on the Sleeper player_id
//  2. Sleeper's own espn_id/yahoo_id when the crosswalk has no usable row
//  3. a crosswalk row matched on normalized name + position + team
//
// Team defenses are the exception: no crosswalk carries them, so their ESPN
// id is derived from the team code (path "team").
//
// Whatever path wins, any id still blank is filled from the other sources.
// A player with no ESPN and no Yahoo id after all three is "unmatched"; it
// stays in the table so nothing is silently dropped.
package players

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hmsanchez10/Sunday-board/internal/cache"
	"github.com/hmsanchez10/Sunday-board/internal/idmap"
	"github.com/hmsanchez10/Sunday-board/internal/ledger"
)

const (
	DefaultURL    = "https://api.sleeper.app/v1/players/nfl"
	DefaultMaxAge = 24 * time.Hour
)

type Options struct {
	URL       string        // defaults to DefaultURL
	CachePath string        // required; e.g. players_nfl.json
	MaxAge    time.Duration // defaults to DefaultMaxAge
	HTTP      *http.Client
	// IDMap is the crosswalk. nil disables paths 1 and 3.
	IDMap *idmap.Map
}

// Store is the loaded, resolved player table.
type Store struct {
	byID   map[ledger.PlayerID]ledger.Player
	byESPN map[string]ledger.PlayerID
	idmap  *idmap.Map
	Info   cache.Info
	// Resolved counts every player in the dump by ResolvedVia.
	Resolved map[string]int
}

// Load returns the player table, downloading a fresh dump if the cache is
// missing or older than MaxAge.
func Load(ctx context.Context, opts Options) (*Store, error) {
	if opts.CachePath == "" {
		return nil, errors.New("players: CachePath is required")
	}
	if opts.URL == "" {
		opts.URL = DefaultURL
	}
	if opts.MaxAge == 0 {
		opts.MaxAge = DefaultMaxAge
	}
	var dump map[string]rawPlayer
	_, info, err := cache.Load(ctx, cache.Options{
		URL: opts.URL, Path: opts.CachePath, MaxAge: opts.MaxAge, HTTP: opts.HTTP,
		Validate: func(b []byte) error {
			dump = nil
			if err := json.Unmarshal(b, &dump); err != nil {
				return err
			}
			if len(dump) == 0 {
				return errors.New("empty player dump")
			}
			return nil
		},
	})
	if err != nil {
		return nil, fmt.Errorf("players: %w", err)
	}
	s := build(dump, opts.IDMap)
	s.Info = info
	return s, nil
}

// Lookup returns the resolved record for a Sleeper player_id.
func (s *Store) Lookup(id ledger.PlayerID) (ledger.Player, bool) {
	p, ok := s.byID[id]
	return p, ok
}

// Len is the number of players in the table.
func (s *Store) Len() int { return len(s.byID) }

// SleeperIDForESPN is the reverse join used by the ESPN adapter. The
// crosswalk's own espn_id -> sleeper_id row wins; otherwise the resolved
// table is searched, which also covers Sleeper's own espn_id and name matches.
func (s *Store) SleeperIDForESPN(espnID string) (ledger.PlayerID, bool) {
	if espnID == "" {
		return "", false
	}
	if s.idmap != nil {
		if r, ok := s.idmap.ByESPN(espnID); ok && r.SleeperID != "" {
			if _, in := s.byID[ledger.PlayerID(r.SleeperID)]; in {
				return ledger.PlayerID(r.SleeperID), true
			}
		}
	}
	id, ok := s.byESPN[espnID]
	return id, ok
}

// TeamDefense returns the Sleeper id for a team defense, which Sleeper keys
// on the team abbreviation ("PIT"). Defenses have no espn_id anywhere.
func (s *Store) TeamDefense(team string) (ledger.PlayerID, bool) {
	p, ok := s.byID[ledger.PlayerID(team)]
	if !ok || p.Position != "DEF" {
		return "", false
	}
	return p.ID, true
}

// String is a short summary for logs.
func (s *Store) String() string {
	return fmt.Sprintf("%d players (%s)", len(s.byID), s.Info)
}

// flexID accepts a JSON number, string, or null. Sleeper serves espn_id and
// yahoo_id as integers today; a foreign key is a string on our side.
type flexID string

func (f *flexID) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || bytes.Equal(b, []byte("null")) {
		*f = ""
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = flexID(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*f = flexID(n.String())
	return nil
}

type rawPlayer struct {
	PlayerID              string `json:"player_id"`
	FullName              string `json:"full_name"`
	FirstName             string `json:"first_name"`
	LastName              string `json:"last_name"`
	Position              string `json:"position"`
	Team                  string `json:"team"`
	ESPNID                flexID `json:"espn_id"`
	YahooID               flexID `json:"yahoo_id"`
	InjuryStatus          string `json:"injury_status"`
	PracticeParticipation string `json:"practice_participation"`
	DepthChartPosition    string `json:"depth_chart_position"`
	DepthChartOrder       *int   `json:"depth_chart_order"`
}

func build(dump map[string]rawPlayer, m *idmap.Map) *Store {
	s := &Store{
		byID:     make(map[ledger.PlayerID]ledger.Player, len(dump)),
		byESPN:   map[string]ledger.PlayerID{},
		idmap:    m,
		Resolved: map[string]int{},
	}
	for key, rp := range dump {
		id := rp.PlayerID
		if id == "" {
			id = key
		}
		name := rp.FullName
		if name == "" {
			// Team defenses carry city/mascot in first/last and no full_name.
			name = strings.TrimSpace(rp.FirstName + " " + rp.LastName)
		}
		p := ledger.Player{
			ID:                    ledger.PlayerID(id),
			Name:                  name,
			Position:              rp.Position,
			Team:                  rp.Team,
			InjuryStatus:          rp.InjuryStatus,
			PracticeParticipation: rp.PracticeParticipation,
			DepthChartPosition:    rp.DepthChartPosition,
		}
		if rp.DepthChartOrder != nil {
			p.DepthChartOrder = *rp.DepthChartOrder
		}
		resolve(&p, string(rp.ESPNID), string(rp.YahooID), m)
		s.Resolved[p.ResolvedVia]++
		s.byID[p.ID] = p
		if p.ESPNID != "" {
			if prev, dup := s.byESPN[p.ESPNID]; !dup || prefer(p, s.byID[prev]) {
				s.byESPN[p.ESPNID] = p.ID
			}
		}
	}
	return s
}

var viaRank = map[string]int{
	ledger.ResolvedViaCrosswalk: 0,
	ledger.ResolvedViaTeam:      0,
	ledger.ResolvedViaSleeper:   1,
	ledger.ResolvedViaName:      2,
}

// prefer decides which of two Sleeper records sharing an espn_id owns the
// reverse index: the stronger resolution path, then the one on a team, then
// the lower id for determinism.
func prefer(a, b ledger.Player) bool {
	if viaRank[a.ResolvedVia] != viaRank[b.ResolvedVia] {
		return viaRank[a.ResolvedVia] < viaRank[b.ResolvedVia]
	}
	if (a.Team != "") != (b.Team != "") {
		return a.Team != ""
	}
	return a.ID < b.ID
}

// resolve fills ESPNID, YahooID, GSISID and ResolvedVia. See the package doc
// for the order.
func resolve(p *ledger.Player, sleeperESPN, sleeperYahoo string, m *idmap.Map) {
	hasIDs := func(espn, yahoo string) bool { return espn != "" || yahoo != "" }

	var row idmap.Row
	haveRow := false
	if m != nil {
		row, haveRow = m.BySleeper(string(p.ID))
	}
	if haveRow {
		p.GSISID = row.GSISID
	}

	switch {
	case p.Position == "DEF":
		p.ResolvedVia = ledger.ResolvedViaUnmatched
		if id, ok := idmap.ESPNTeamDefenseID(p.Team); ok {
			p.ESPNID = id
			p.ResolvedVia = ledger.ResolvedViaTeam
		}
	case haveRow && hasIDs(row.ESPNID, row.YahooID):
		p.ESPNID, p.YahooID = row.ESPNID, row.YahooID
		p.ResolvedVia = ledger.ResolvedViaCrosswalk
	case hasIDs(sleeperESPN, sleeperYahoo):
		p.ESPNID, p.YahooID = sleeperESPN, sleeperYahoo
		p.ResolvedVia = ledger.ResolvedViaSleeper
	default:
		p.ResolvedVia = ledger.ResolvedViaUnmatched
		if m == nil {
			break
		}
		if r, ok := m.ByName(p.Name, p.Position, p.Team); ok && hasIDs(r.ESPNID, r.YahooID) {
			p.ESPNID, p.YahooID = r.ESPNID, r.YahooID
			if p.GSISID == "" {
				p.GSISID = r.GSISID
			}
			p.ResolvedVia = ledger.ResolvedViaName
		}
	}

	// Fill whatever is still blank from the sources that did not win.
	if p.ESPNID == "" {
		p.ESPNID = sleeperESPN
	}
	if p.YahooID == "" {
		p.YahooID = sleeperYahoo
	}
}

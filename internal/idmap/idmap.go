// Package idmap loads the DynastyProcess player id crosswalk
// (nflverse ff_playerids) and indexes it by Sleeper id and by normalized
// name + position + team. It exists because Sleeper's own espn_id/yahoo_id
// columns are too sparse to join on.
package idmap

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/hmsanchez10/Sunday-board/internal/cache"
)

const (
	// DefaultURL is files/db_playerids.csv on the master branch of
	// github.com/dynastyprocess/data. Verified resolving on 2026-09-12.
	DefaultURL    = "https://raw.githubusercontent.com/dynastyprocess/data/master/files/db_playerids.csv"
	DefaultMaxAge = 7 * 24 * time.Hour
)

type Options struct {
	URL       string        // defaults to DefaultURL
	CachePath string        // required; e.g. idmap.csv
	MaxAge    time.Duration // defaults to DefaultMaxAge
	HTTP      *http.Client
}

// Row is one crosswalk record. Team is normalized to Sleeper's codes, with
// "" for free agents; Position is normalized to Sleeper's (K, not PK).
type Row struct {
	SleeperID string
	ESPNID    string
	YahooID   string
	GSISID    string
	Name      string
	Position  string
	Team      string
}

// Map is the loaded crosswalk.
type Map struct {
	bySleeper map[string]Row
	byName    map[string][]Row

	Info cache.Info
	Rows int // rows parsed, including rows with no sleeper_id
}

// Load returns the crosswalk, downloading if the cache is older than MaxAge.
func Load(ctx context.Context, opts Options) (*Map, error) {
	if opts.CachePath == "" {
		return nil, errors.New("idmap: CachePath is required")
	}
	if opts.URL == "" {
		opts.URL = DefaultURL
	}
	if opts.MaxAge == 0 {
		opts.MaxAge = DefaultMaxAge
	}
	var m *Map
	data, info, err := cache.Load(ctx, cache.Options{
		URL: opts.URL, Path: opts.CachePath, MaxAge: opts.MaxAge, HTTP: opts.HTTP,
		Validate: func(b []byte) error {
			var err error
			m, err = Parse(bytes.NewReader(b))
			return err
		},
	})
	if err != nil {
		return nil, fmt.Errorf("idmap: %w", err)
	}
	_ = data
	m.Info = info
	return m, nil
}

// Parse reads the CSV. Columns are located by header name, and the literal
// "NA" is treated as empty. When two rows share a sleeper_id the one with
// more of espn/yahoo/gsis populated wins.
func Parse(r io.Reader) (*Map, error) {
	cr := csv.NewReader(r)
	cr.LazyQuotes = true
	cr.ReuseRecord = true

	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	col := map[string]int{}
	for i, h := range header {
		col[strings.TrimSpace(h)] = i
	}
	for _, need := range []string{"sleeper_id", "espn_id", "yahoo_id", "gsis_id", "name", "position", "team"} {
		if _, ok := col[need]; !ok {
			return nil, fmt.Errorf("column %q missing; header is %v", need, header)
		}
	}
	get := func(rec []string, name string) string {
		i := col[name]
		if i >= len(rec) {
			return ""
		}
		v := strings.TrimSpace(rec[i])
		if v == "NA" {
			return ""
		}
		return v
	}

	m := &Map{bySleeper: map[string]Row{}, byName: map[string][]Row{}}
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("row %d: %w", m.Rows+2, err)
		}
		m.Rows++
		row := Row{
			SleeperID: get(rec, "sleeper_id"),
			ESPNID:    get(rec, "espn_id"),
			YahooID:   get(rec, "yahoo_id"),
			GSISID:    get(rec, "gsis_id"),
			Name:      get(rec, "name"),
			Position:  NormalizePosition(get(rec, "position")),
			Team:      NormalizeTeam(get(rec, "team")),
		}
		if row.Name != "" {
			k := NameKey(row.Name, row.Position, row.Team)
			m.byName[k] = append(m.byName[k], row)
		}
		if row.SleeperID == "" {
			continue
		}
		if prev, dup := m.bySleeper[row.SleeperID]; !dup || score(row) > score(prev) {
			m.bySleeper[row.SleeperID] = row
		}
	}
	if len(m.bySleeper) == 0 {
		return nil, errors.New("no rows with a sleeper_id")
	}
	return m, nil
}

func score(r Row) int {
	n := 0
	for _, v := range []string{r.ESPNID, r.YahooID, r.GSISID} {
		if v != "" {
			n++
		}
	}
	return n
}

// BySleeper looks up a row by Sleeper player_id.
func (m *Map) BySleeper(id string) (Row, bool) {
	r, ok := m.bySleeper[id]
	return r, ok
}

// ByName looks up a row by normalized name, position and team. It only
// returns a match when exactly one row has that key; ambiguity is a miss.
func (m *Map) ByName(name, position, team string) (Row, bool) {
	rows := m.byName[NameKey(name, position, team)]
	if len(rows) != 1 {
		return Row{}, false
	}
	return rows[0], true
}

// Len is the number of distinct sleeper_ids indexed.
func (m *Map) Len() int { return len(m.bySleeper) }

// NameKey builds the path-c lookup key. Position and team are normalized to
// Sleeper's vocabulary so both sides agree.
func NameKey(name, position, team string) string {
	return NormalizeName(name) + "|" + NormalizePosition(position) + "|" + NormalizeTeam(team)
}

var suffixes = map[string]bool{"jr": true, "sr": true, "ii": true, "iii": true, "iv": true}

// NormalizeName lowercases, strips punctuation, drops trailing Jr/Sr/II/III/IV
// and collapses whitespace. "A.J. Brown" -> "aj brown",
// "Kenneth Walker III" -> "kenneth walker", "Amon-Ra St. Brown" -> "amonra st brown".
func NormalizeName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		case unicode.IsSpace(r):
			b.WriteByte(' ')
		}
	}
	parts := strings.Fields(b.String())
	for len(parts) > 1 && suffixes[parts[len(parts)-1]] {
		parts = parts[:len(parts)-1]
	}
	return strings.Join(parts, " ")
}

// mflTeams maps the crosswalk's MFL-style team codes to Sleeper's.
var mflTeams = map[string]string{
	"GBP": "GB", "KCC": "KC", "JAC": "JAX", "LVR": "LV", "NEP": "NE", "NOS": "NO",
	"SFO": "SF", "TBB": "TB", "RAM": "LAR", "SDC": "LAC", "STL": "LAR", "OAK": "LV",
	"FA": "", "FA*": "",
}

// NormalizeTeam maps any known team code to Sleeper's code; free agents are "".
func NormalizeTeam(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	if t, ok := mflTeams[s]; ok {
		return t
	}
	return s
}

// NormalizePosition maps crosswalk position codes to Sleeper's.
func NormalizePosition(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	switch s {
	case "PK":
		return "K"
	case "DST", "D/ST":
		return "DEF"
	}
	return s
}

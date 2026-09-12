// Package espn implements ledger.Adapter against ESPN Fantasy's v3 read API.
// Auth is two cookies, espn_s2 and SWID, supplied through creds; the adapter
// never reads them from anywhere else. The owner's team is found by matching
// the SWID against each team's owners, never by a configured team id.
package espn

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hmsanchez10/Sunday-board/internal/idmap"
	"github.com/hmsanchez10/Sunday-board/internal/ledger"
)

const DefaultBaseURL = "https://lm-api-reads.fantasy.espn.com/apis/v3/games/ffl"

// Resolver joins ESPN players into the Sleeper-keyed ledger.
type Resolver interface {
	// SleeperIDForESPN maps an ESPN player id to the ledger key.
	SleeperIDForESPN(espnID string) (ledger.PlayerID, bool)
	// TeamDefense maps a team abbreviation ("PIT") to its D/ST ledger key.
	// ESPN's D/ST ids are synthetic (-16000 minus proTeamId) and exist in no
	// crosswalk, so defenses join on team instead.
	TeamDefense(team string) (ledger.PlayerID, bool)
}

// Slots maps ESPN lineupSlotId to a slot name. Bench and IR are handled by
// SlotBench/SlotIR; every other id is a starter slot. An id not listed here is
// an error rather than a guess.
var Slots = map[int]string{
	0:  "QB",
	2:  "RB",
	4:  "WR",
	6:  "TE",
	23: "FLEX",
	16: "D/ST",
	17: "K",
	20: "Bench",
	21: "IR",
}

const (
	slotBench = 20
	slotIR    = 21
)

// slotOrder is the display order for roster entries.
var slotOrder = []int{0, 2, 4, 6, 23, 16, 17, slotBench, slotIR}

// positions maps ESPN defaultPositionId to Sleeper's position vocabulary.
var positions = map[int]string{1: "QB", 2: "RB", 3: "WR", 4: "TE", 5: "K", 16: "DEF"}

// UnjoinedPrefix marks a ledger key synthesized for an ESPN player that could
// not be joined to a Sleeper id. The entry stays on the roster; the key is
// "espn:<espn player id>".
const UnjoinedPrefix = "espn:"

// Adapter fetches one ESPN league for the owner identified by the SWID cookie.
type Adapter struct {
	HTTP    *http.Client
	BaseURL string

	// Season selects the seasons/{season} path segment.
	Season int
	// Starters are the lineup slot names from the league YAML; Fetch checks
	// them against the league's lineupSlotCounts.
	Starters []string
	Resolver Resolver

	mu       sync.Mutex
	unjoined map[ledger.PlayerID]ledger.Player
}

var _ ledger.Adapter = (*Adapter)(nil)

func New(r Resolver, season int, starters []string) *Adapter {
	return &Adapter{
		HTTP:     &http.Client{Timeout: 30 * time.Second},
		BaseURL:  DefaultBaseURL,
		Season:   season,
		Starters: starters,
		Resolver: r,
		unjoined: map[ledger.PlayerID]ledger.Player{},
	}
}

func (a *Adapter) Platform() ledger.Platform { return ledger.PlatformESPN }

// Unjoined returns ledger records, built from ESPN's own data, for every
// roster entry Fetch could not join to a Sleeper id. Each is marked
// ResolvedViaUnmatched so it lands in unmatched.json instead of vanishing.
func (a *Adapter) Unjoined() map[ledger.PlayerID]ledger.Player {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make(map[ledger.PlayerID]ledger.Player, len(a.unjoined))
	for k, v := range a.unjoined {
		out[k] = v
	}
	return out
}

// --- wire types (only the fields we read) ---

type apiLeague struct {
	ID              int       `json:"id"`
	SeasonID        int       `json:"seasonId"`
	ScoringPeriodID int       `json:"scoringPeriodId"`
	Teams           []apiTeam `json:"teams"`
	Settings        struct {
		Name           string `json:"name"`
		RosterSettings struct {
			LineupSlotCounts map[string]int `json:"lineupSlotCounts"`
		} `json:"rosterSettings"`
	} `json:"settings"`
}

type apiTeam struct {
	ID           int      `json:"id"`
	Abbrev       string   `json:"abbrev"`
	Name         string   `json:"name"`
	Owners       []string `json:"owners"`
	PrimaryOwner string   `json:"primaryOwner"`
	WaiverRank   int      `json:"waiverRank"`
	Record       struct {
		Overall struct {
			Wins   int `json:"wins"`
			Losses int `json:"losses"`
			Ties   int `json:"ties"`
		} `json:"overall"`
	} `json:"record"`
	Roster struct {
		Entries []apiEntry `json:"entries"`
	} `json:"roster"`
}

type apiEntry struct {
	PlayerID        int64  `json:"playerId"`
	LineupSlotID    int    `json:"lineupSlotId"`
	InjuryStatus    string `json:"injuryStatus"`
	PlayerPoolEntry struct {
		Player struct {
			ID                int64  `json:"id"`
			FullName          string `json:"fullName"`
			DefaultPositionID int    `json:"defaultPositionId"`
			ProTeamID         int    `json:"proTeamId"`
			InjuryStatus      string `json:"injuryStatus"`
		} `json:"player"`
	} `json:"playerPoolEntry"`
}

// Fetch implements ledger.Adapter.
func (a *Adapter) Fetch(ctx context.Context, cfg ledger.LeagueConfig, creds ledger.Creds) (ledger.LeagueState, error) {
	if cfg.Platform != ledger.PlatformESPN {
		return ledger.LeagueState{}, fmt.Errorf("espn: league %s is platform %q", cfg.ID, cfg.Platform)
	}
	if cfg.LeagueID == "" {
		return ledger.LeagueState{}, fmt.Errorf("espn: league %s has no league_id", cfg.ID)
	}
	if a.Resolver == nil {
		return ledger.LeagueState{}, fmt.Errorf("espn: no Resolver configured")
	}
	s2, swid, err := cookies(creds)
	if err != nil {
		return ledger.LeagueState{}, fmt.Errorf("espn: league %s: %w", cfg.ID, err)
	}

	url := fmt.Sprintf("%s/seasons/%d/segments/0/leagues/%s?view=mRoster&view=mTeam&view=mSettings", a.BaseURL, a.Season, cfg.LeagueID)
	var league apiLeague
	if err := a.getJSON(ctx, url, s2, swid, &league); err != nil {
		return ledger.LeagueState{}, fmt.Errorf("espn: league %s: %w", cfg.ID, err)
	}
	if err := checkStarters(a.Starters, league.Settings.RosterSettings.LineupSlotCounts); err != nil {
		return ledger.LeagueState{}, fmt.Errorf("espn: league %s: %w", cfg.ID, err)
	}

	me := normalizeSWID(swid)
	var mine *apiTeam
	for i := range league.Teams {
		if ownsTeam(me, &league.Teams[i]) {
			mine = &league.Teams[i]
			break
		}
	}
	if mine == nil {
		return ledger.LeagueState{}, fmt.Errorf("espn: league %s: no team among %d is owned by the SWID in creds", cfg.ID, len(league.Teams))
	}

	entries, err := a.mapRoster(mine)
	if err != nil {
		return ledger.LeagueState{}, fmt.Errorf("espn: league %s: %w", cfg.ID, err)
	}

	return ledger.LeagueState{
		Owner:     cfg.Owner,
		LeagueID:  cfg.ID,
		Platform:  ledger.PlatformESPN,
		TeamID:    strconv.Itoa(mine.ID),
		Record:    record(mine),
		Roster:    entries,
		Waivers:   waivers(&league, mine),
		FetchedAt: time.Now().UTC(),
	}, nil
}

// cookies pulls espn_s2 and SWID from creds, matching key names case-insensitively.
func cookies(creds ledger.Creds) (s2, swid string, err error) {
	for k, v := range creds {
		switch strings.ToLower(k) {
		case "espn_s2":
			s2 = v
		case "swid":
			swid = v
		}
	}
	if s2 == "" || swid == "" {
		return "", "", fmt.Errorf("creds must contain espn_s2 and SWID")
	}
	return s2, swid, nil
}

func normalizeSWID(s string) string {
	return strings.ToUpper(strings.Trim(strings.TrimSpace(s), "{}"))
}

func ownsTeam(me string, t *apiTeam) bool {
	if normalizeSWID(t.PrimaryOwner) == me {
		return true
	}
	for _, o := range t.Owners {
		if normalizeSWID(o) == me {
			return true
		}
	}
	return false
}

// checkStarters compares the configured starter slots with the league's
// lineupSlotCounts as multisets (ESPN has no slot order).
func checkStarters(configured []string, counts map[string]int) error {
	want := map[string]int{}
	for _, s := range configured {
		want[s]++
	}
	got := map[string]int{}
	for k, n := range counts {
		id, err := strconv.Atoi(k)
		if err != nil || n == 0 || id == slotBench || id == slotIR {
			continue
		}
		name, ok := Slots[id]
		if !ok {
			return fmt.Errorf("league uses lineupSlotId %d, which is not in the slot map", id)
		}
		got[name] += n
	}
	if len(want) != len(got) {
		return fmt.Errorf("config roster.starters %v does not match league lineup slots %v", configured, got)
	}
	for k, n := range want {
		if got[k] != n {
			return fmt.Errorf("config roster.starters %v does not match league lineup slots %v", configured, got)
		}
	}
	return nil
}

func (a *Adapter) mapRoster(t *apiTeam) ([]ledger.RosterEntry, error) {
	type item struct {
		entry ledger.RosterEntry
		slot  int
		name  string
	}
	var items []item
	for _, e := range t.Roster.Entries {
		p := e.PlayerPoolEntry.Player
		if _, ok := Slots[e.LineupSlotID]; !ok {
			return nil, fmt.Errorf("player %d (%s) is in lineupSlotId %d, which is not in the slot map", e.PlayerID, p.FullName, e.LineupSlotID)
		}
		id := a.join(&e)
		re := ledger.RosterEntry{Player: id}
		switch e.LineupSlotID {
		case slotBench:
			re.Slot = ledger.SlotBench
		case slotIR:
			re.Slot = ledger.SlotIR
		default:
			re.Slot = ledger.SlotStarter
			re.SlotName = Slots[e.LineupSlotID]
		}
		items = append(items, item{entry: re, slot: e.LineupSlotID, name: p.FullName})
	}
	rank := func(slot int) int {
		for i, s := range slotOrder {
			if s == slot {
				return i
			}
		}
		return len(slotOrder)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if rank(items[i].slot) != rank(items[j].slot) {
			return rank(items[i].slot) < rank(items[j].slot)
		}
		return items[i].name < items[j].name
	})
	out := make([]ledger.RosterEntry, len(items))
	for i, it := range items {
		out[i] = it.entry
	}
	return out, nil
}

// join returns the ledger key for one ESPN roster entry, recording an
// unjoined placeholder when no Sleeper id can be found.
func (a *Adapter) join(e *apiEntry) ledger.PlayerID {
	p := e.PlayerPoolEntry.Player
	team := idmap.TeamForESPNProTeamID(p.ProTeamID)
	pos := positions[p.DefaultPositionID]

	// Every entry, defenses included, is first joined on its ESPN id; the
	// team-code join is the fallback for a defense the table lacks.
	if id, ok := a.Resolver.SleeperIDForESPN(strconv.FormatInt(e.PlayerID, 10)); ok {
		return id
	}
	if pos == "DEF" {
		if id, ok := a.Resolver.TeamDefense(team); ok {
			return id
		}
	}

	id := ledger.PlayerID(UnjoinedPrefix + strconv.FormatInt(e.PlayerID, 10))
	inj := p.InjuryStatus
	if inj == "" {
		inj = e.InjuryStatus
	}
	if inj == "ACTIVE" || inj == "NORMAL" {
		inj = ""
	}
	a.mu.Lock()
	a.unjoined[id] = ledger.Player{
		ID:           id,
		Name:         p.FullName,
		Position:     pos,
		Team:         team,
		ESPNID:       strconv.FormatInt(e.PlayerID, 10),
		InjuryStatus: inj,
		ResolvedVia:  ledger.ResolvedViaUnmatched,
	}
	a.mu.Unlock()
	return id
}

func record(t *apiTeam) string {
	o := t.Record.Overall
	s := fmt.Sprintf("%d-%d", o.Wins, o.Losses)
	if o.Ties > 0 {
		s += fmt.Sprintf("-%d", o.Ties)
	}
	return s
}

func waivers(l *apiLeague, t *apiTeam) ledger.WaiverState {
	size := 0
	for k, n := range l.Settings.RosterSettings.LineupSlotCounts {
		if k != strconv.Itoa(slotIR) {
			size += n
		}
	}
	held := 0
	for _, e := range t.Roster.Entries {
		if e.LineupSlotID != slotIR {
			held++
		}
	}
	open := size - held
	if open < 0 {
		open = 0
	}
	rank := t.WaiverRank
	return ledger.WaiverState{
		Type:            "priority_reset_weekly",
		Priority:        &rank,
		OpenRosterSpots: open,
	}
}

func (a *Adapter) getJSON(ctx context.Context, url, s2, swid string, v any) error {
	c := a.HTTP
	if c == nil {
		c = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "sunday-board")
	req.Header.Set("Accept", "application/json")
	req.AddCookie(&http.Cookie{Name: "espn_s2", Value: s2})
	req.AddCookie(&http.Cookie{Name: "SWID", Value: swid})
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return fmt.Errorf("HTTP %s: espn_s2/SWID rejected (cookies expire; refresh them from a logged-in browser)", resp.Status)
		}
		return fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	return nil
}

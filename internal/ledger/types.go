// Package ledger defines the single state file that every adapter writes into
// and every consumer reads from.
//
// Design rules that are cheap now and expensive later (see the plan):
//   - Every record carries an Owner, even though there is exactly one today.
//   - Credentials arrive as an argument. No adapter reads a global or a file.
//   - Consumers iterate over State.Leagues. Nothing hardcodes "my four leagues".
//   - Players are keyed on the Sleeper player ID, which is the only ID space
//     that maps cleanly to ESPN and Yahoo.
package ledger

import (
	"context"
	"time"
)

// Owner identifies whose leagues these are. One value today; a real identity later.
type Owner string

// PlayerID is a Sleeper player_id, the canonical key across all platforms.
// Sleeper's /v1/players/nfl dump carries espn_id and yahoo_id on every record,
// which is what makes this the join key rather than player names.
type PlayerID string

type Platform string

const (
	PlatformSleeper Platform = "sleeper"
	PlatformESPN    Platform = "espn"
	PlatformYahoo   Platform = "yahoo"
)

// Player is the canonical player record, built once per run from the Sleeper dump.
type Player struct {
	ID       PlayerID `json:"id"`
	Name     string   `json:"name"`
	Position string   `json:"position"`
	Team     string   `json:"team"`

	ESPNID  string `json:"espn_id,omitempty"`
	YahooID string `json:"yahoo_id,omitempty"`

	InjuryStatus         string `json:"injury_status,omitempty"`         // Questionable, Out, IR, ...
	PracticeParticipation string `json:"practice_participation,omitempty"` // DNP, LP, FP
	DepthChartPosition   string `json:"depth_chart_position,omitempty"`
	DepthChartOrder      int    `json:"depth_chart_order,omitempty"`

	// GSISID is the NFL/nflverse player id, from the DynastyProcess crosswalk.
	GSISID string `json:"gsis_id,omitempty"`
	// ResolvedVia records which path produced ESPNID/YahooID: one of the
	// ResolvedVia* constants below.
	ResolvedVia string `json:"resolved_via,omitempty"`
}

// Resolution paths for Player.ResolvedVia, in the order they are tried.
const (
	ResolvedViaCrosswalk = "crosswalk" // DynastyProcess crosswalk row keyed on the Sleeper id
	ResolvedViaSleeper   = "sleeper"   // Sleeper's own espn_id/yahoo_id
	ResolvedViaName      = "name"      // crosswalk row matched on normalized name + position + team
	ResolvedViaUnmatched = "unmatched" // no ESPN or Yahoo id found by any path
)

// Slot is the lineup position a player occupies on one roster.
type Slot string

const (
	SlotStarter Slot = "starter" // in the lineup, position detail in SlotName
	SlotBench   Slot = "bench"
	SlotIR      Slot = "ir"
	SlotTaxi    Slot = "taxi"
)

// RosterEntry is one player on one of the owner's teams.
type RosterEntry struct {
	Player   PlayerID `json:"player"`
	Slot     Slot     `json:"slot"`
	SlotName string   `json:"slot_name,omitempty"` // QB, FLEX, SUPER_FLEX, W/R/T, ...
	// KickoffAt matters because every one of these leagues locks players
	// individually at their own game time, not once per week.
	KickoffAt *time.Time `json:"kickoff_at,omitempty"`
}

// WaiverState is deliberately a union of the three mechanisms in play:
// FAAB (Sleeper), rolling priority (both Yahoo), and weekly inverse-standings
// reset (ESPN). A claim costs something different in each.
type WaiverState struct {
	Type            string `json:"type"` // faab | rolling_priority | priority_reset_weekly
	BudgetRemaining *int   `json:"budget_remaining,omitempty"`
	Priority        *int   `json:"priority,omitempty"`
	OpenRosterSpots int    `json:"open_roster_spots"`
}

// LeagueState is one league's slice of the world, as of FetchedAt.
type LeagueState struct {
	Owner      Owner         `json:"owner"`
	LeagueID   string        `json:"league_id"`   // matches the id: field in leagues/*.yaml
	Platform   Platform      `json:"platform"`
	TeamID     string        `json:"team_id"`
	Record     string        `json:"record,omitempty"` // "1-0"
	Roster     []RosterEntry `json:"roster"`
	Waivers    WaiverState   `json:"waivers"`
	FetchedAt  time.Time     `json:"fetched_at"`
}

// Holding is the inverted index and the reason this tool exists: one player,
// every place the owner has him, so a single injury resolves across all leagues.
type Holding struct {
	LeagueID string `json:"league_id"`
	Slot     Slot   `json:"slot"`
	SlotName string `json:"slot_name,omitempty"`
}

// State is the whole file. Written once per run to state.json.
type State struct {
	GeneratedAt time.Time                `json:"generated_at"`
	Week        int                      `json:"week"`
	Leagues     []LeagueState            `json:"leagues"`
	Players     map[PlayerID]Player      `json:"players"`  // only players the owner holds or is watching
	Holdings    map[PlayerID][]Holding   `json:"holdings"` // derived from Leagues; the cross-league view
}

// Creds is whatever one platform needs. Opaque on purpose so the shape can
// change per platform without touching the adapter interface.
type Creds map[string]string

// CredStore is the single credential lookup. Today it reads a local file.
// Swapping it for per-user storage changes this implementation and nothing else.
type CredStore interface {
	CredsFor(owner Owner, p Platform) (Creds, error)
}

// LeagueConfig is the parsed leagues/*.yaml for one league. Adapters need the
// identifiers; the scoring and waiver rules are for the brief, not the fetch.
type LeagueConfig struct {
	ID       string   `yaml:"id"`
	Name     string   `yaml:"name"`
	Platform Platform `yaml:"platform"`
	LeagueID string   `yaml:"league_id"`
	TeamID   string   `yaml:"team_id"`
	Owner    Owner    `yaml:"owner"`
}

// Adapter is implemented once per platform. Credentials are an argument, never
// read from inside, which is what makes the Yahoo browser-to-API swap a
// one-file change later.
type Adapter interface {
	Platform() Platform
	Fetch(ctx context.Context, cfg LeagueConfig, creds Creds) (LeagueState, error)
}

// Package sleeper implements ledger.Adapter against the public Sleeper API.
// No credentials are needed; the creds argument is accepted and ignored so
// the call shape matches every other platform.
package sleeper

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hmsanchez10/Sunday-board/internal/ledger"
)

const DefaultBaseURL = "https://api.sleeper.app/v1"

// Adapter fetches one Sleeper league for one Sleeper user.
type Adapter struct {
	HTTP    *http.Client
	BaseURL string

	// Username is the Sleeper handle without the leading @. It is resolved to
	// a user_id once and cached for the life of the adapter.
	Username string

	// Starters are the lineup slot names in order (QB, RB, RB, ..., SUPER_FLEX)
	// from the league's YAML. Fetch checks them against the league's actual
	// roster_positions and refuses to label slots from a config that has
	// drifted.
	Starters []string

	mu     sync.Mutex
	userID string
}

var _ ledger.Adapter = (*Adapter)(nil)

// New returns an adapter for username using the given starter slot names.
func New(username string, starters []string) *Adapter {
	return &Adapter{
		HTTP:     &http.Client{Timeout: 30 * time.Second},
		BaseURL:  DefaultBaseURL,
		Username: strings.TrimPrefix(username, "@"),
		Starters: starters,
	}
}

func (a *Adapter) Platform() ledger.Platform { return ledger.PlatformSleeper }

// --- wire types (only the fields we read) ---

type apiUser struct {
	UserID      string `json:"user_id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
}

type apiLeague struct {
	Name            string   `json:"name"`
	Season          string   `json:"season"`
	Status          string   `json:"status"`
	RosterPositions []string `json:"roster_positions"`
	Settings        struct {
		WaiverType   int `json:"waiver_type"` // 0 rolling, 1 reset weekly, 2 FAAB
		WaiverBudget int `json:"waiver_budget"`
		ReserveSlots int `json:"reserve_slots"`
		TaxiSlots    int `json:"taxi_slots"`
	} `json:"settings"`
}

type apiRoster struct {
	RosterID int      `json:"roster_id"`
	OwnerID  string   `json:"owner_id"`
	CoOwners []string `json:"co_owners"`
	Players  []string `json:"players"`
	Starters []string `json:"starters"`
	Reserve  []string `json:"reserve"`
	Taxi     []string `json:"taxi"`
	Settings struct {
		Wins             int `json:"wins"`
		Losses           int `json:"losses"`
		Ties             int `json:"ties"`
		WaiverBudgetUsed int `json:"waiver_budget_used"`
		WaiverPosition   int `json:"waiver_position"`
	} `json:"settings"`
}

// NFLState is /v1/state/nfl.
type NFLState struct {
	Week        int    `json:"week"`
	DisplayWeek int    `json:"display_week"`
	Season      string `json:"season"`
	SeasonType  string `json:"season_type"` // pre | regular | post
}

// CurrentNFLState returns Sleeper's view of the NFL calendar.
func CurrentNFLState(ctx context.Context, c *http.Client, baseURL string) (NFLState, error) {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	var st NFLState
	if err := getJSON(ctx, c, baseURL+"/state/nfl", &st); err != nil {
		return NFLState{}, err
	}
	return st, nil
}

// Fetch implements ledger.Adapter. creds is unused: Sleeper is public.
func (a *Adapter) Fetch(ctx context.Context, cfg ledger.LeagueConfig, _ ledger.Creds) (ledger.LeagueState, error) {
	if cfg.Platform != ledger.PlatformSleeper {
		return ledger.LeagueState{}, fmt.Errorf("sleeper: league %s is platform %q", cfg.ID, cfg.Platform)
	}
	if cfg.LeagueID == "" {
		return ledger.LeagueState{}, fmt.Errorf("sleeper: league %s has no league_id", cfg.ID)
	}

	userID, err := a.resolveUser(ctx)
	if err != nil {
		return ledger.LeagueState{}, err
	}

	base := a.BaseURL + "/league/" + cfg.LeagueID

	var league apiLeague
	if err := getJSON(ctx, a.HTTP, base, &league); err != nil {
		return ledger.LeagueState{}, fmt.Errorf("sleeper: league %s: %w", cfg.ID, err)
	}
	if err := checkStarters(a.Starters, league.RosterPositions); err != nil {
		return ledger.LeagueState{}, fmt.Errorf("sleeper: league %s: %w", cfg.ID, err)
	}

	var users []apiUser
	if err := getJSON(ctx, a.HTTP, base+"/users", &users); err != nil {
		return ledger.LeagueState{}, fmt.Errorf("sleeper: league %s users: %w", cfg.ID, err)
	}
	member := false
	for _, u := range users {
		if u.UserID == userID {
			member = true
			break
		}
	}
	if !member {
		return ledger.LeagueState{}, fmt.Errorf("sleeper: league %s: @%s (user_id %s) is not a member", cfg.ID, a.Username, userID)
	}

	var rosters []apiRoster
	if err := getJSON(ctx, a.HTTP, base+"/rosters", &rosters); err != nil {
		return ledger.LeagueState{}, fmt.Errorf("sleeper: league %s rosters: %w", cfg.ID, err)
	}
	var mine *apiRoster
	for i := range rosters {
		if ownsRoster(userID, &rosters[i]) {
			mine = &rosters[i]
			break
		}
	}
	if mine == nil {
		return ledger.LeagueState{}, fmt.Errorf("sleeper: league %s: no roster owned by @%s", cfg.ID, a.Username)
	}

	entries, err := mapRoster(a.Starters, mine)
	if err != nil {
		return ledger.LeagueState{}, fmt.Errorf("sleeper: league %s: %w", cfg.ID, err)
	}

	return ledger.LeagueState{
		Owner:     cfg.Owner,
		LeagueID:  cfg.ID,
		Platform:  ledger.PlatformSleeper,
		TeamID:    strconv.Itoa(mine.RosterID),
		Record:    record(mine),
		Roster:    entries,
		Waivers:   waivers(&league, mine),
		FetchedAt: time.Now().UTC(),
	}, nil
}

func (a *Adapter) resolveUser(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.userID != "" {
		return a.userID, nil
	}
	if a.Username == "" {
		return "", fmt.Errorf("sleeper: no username configured")
	}
	var u apiUser
	if err := getJSON(ctx, a.HTTP, a.BaseURL+"/user/"+a.Username, &u); err != nil {
		return "", fmt.Errorf("sleeper: resolve @%s: %w", a.Username, err)
	}
	if u.UserID == "" {
		return "", fmt.Errorf("sleeper: resolve @%s: no such user", a.Username)
	}
	a.userID = u.UserID
	return u.UserID, nil
}

func ownsRoster(userID string, r *apiRoster) bool {
	if r.OwnerID == userID {
		return true
	}
	for _, co := range r.CoOwners {
		if co == userID {
			return true
		}
	}
	return false
}

// checkStarters verifies the configured starter slots equal the league's
// non-bench roster_positions, in order.
func checkStarters(configured, positions []string) error {
	var actual []string
	for _, p := range positions {
		if p != "BN" {
			actual = append(actual, p)
		}
	}
	if len(actual) != len(configured) {
		return fmt.Errorf("config roster.starters %v does not match league roster_positions %v", configured, actual)
	}
	for i := range actual {
		if actual[i] != configured[i] {
			return fmt.Errorf("config roster.starters %v does not match league roster_positions %v", configured, actual)
		}
	}
	return nil
}

// mapRoster turns Sleeper's four lists into RosterEntries. Sleeper puts every
// rostered player in players[], including IR and taxi, and uses "0" for an
// empty starter slot.
func mapRoster(slots []string, r *apiRoster) ([]ledger.RosterEntry, error) {
	if len(r.Starters) > len(slots) {
		return nil, fmt.Errorf("roster has %d starters but config has %d starter slots", len(r.Starters), len(slots))
	}

	var out []ledger.RosterEntry
	placed := map[string]bool{}

	for i, id := range r.Starters {
		if id == "" || id == "0" {
			continue // empty slot
		}
		placed[id] = true
		out = append(out, ledger.RosterEntry{Player: ledger.PlayerID(id), Slot: ledger.SlotStarter, SlotName: slots[i]})
	}
	for _, id := range r.Reserve {
		if id == "" || id == "0" || placed[id] {
			continue
		}
		placed[id] = true
		out = append(out, ledger.RosterEntry{Player: ledger.PlayerID(id), Slot: ledger.SlotIR})
	}
	for _, id := range r.Taxi {
		if id == "" || id == "0" || placed[id] {
			continue
		}
		placed[id] = true
		out = append(out, ledger.RosterEntry{Player: ledger.PlayerID(id), Slot: ledger.SlotTaxi})
	}
	for _, id := range r.Players {
		if id == "" || id == "0" || placed[id] {
			continue
		}
		placed[id] = true
		out = append(out, ledger.RosterEntry{Player: ledger.PlayerID(id), Slot: ledger.SlotBench})
	}
	return out, nil
}

func record(r *apiRoster) string {
	s := fmt.Sprintf("%d-%d", r.Settings.Wins, r.Settings.Losses)
	if r.Settings.Ties > 0 {
		s += fmt.Sprintf("-%d", r.Settings.Ties)
	}
	return s
}

func waivers(l *apiLeague, r *apiRoster) ledger.WaiverState {
	// Active roster capacity is every roster_positions entry (starters + BN).
	// IR and taxi are separate pools and do not count against it.
	active := len(r.Players) - len(r.Reserve) - len(r.Taxi)
	open := len(l.RosterPositions) - active
	if open < 0 {
		open = 0
	}
	w := ledger.WaiverState{OpenRosterSpots: open}
	switch l.Settings.WaiverType {
	case 2:
		remaining := l.Settings.WaiverBudget - r.Settings.WaiverBudgetUsed
		w.Type = "faab"
		w.BudgetRemaining = &remaining
	case 1:
		pos := r.Settings.WaiverPosition
		w.Type = "priority_reset_weekly"
		w.Priority = &pos
	default:
		pos := r.Settings.WaiverPosition
		w.Type = "rolling_priority"
		w.Priority = &pos
	}
	return w
}

func getJSON(ctx context.Context, c *http.Client, url string, v any) error {
	if c == nil {
		c = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "sunday-board")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: HTTP %s", url, resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		return fmt.Errorf("GET %s: decode: %w", url, err)
	}
	return nil
}

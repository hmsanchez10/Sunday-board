// Package config loads leagues/*.yaml. Each file yields the identifiers an
// adapter needs (ledger.LeagueConfig) plus the full rule set the weekly brief
// needs. Nothing here knows which leagues exist; it loads whatever is in the
// directory.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"

	"github.com/hmsanchez10/Sunday-board/internal/ledger"
)

// League is one parsed leagues/*.yaml.
type League struct {
	ledger.LeagueConfig `yaml:",inline"`
	Rules               `yaml:",inline"`

	// Path is the file this league was loaded from. Diagnostics only.
	Path string `yaml:"-"`
}

// Rules is everything in the YAML beyond the identifiers. The sections whose
// keys are stable across platforms are typed; scoring, trades, deadlines and
// keepers vary per platform and stay as maps.
type Rules struct {
	Season     int    `yaml:"season"`
	LeagueType string `yaml:"league_type"` // dynasty, redraft, ...
	TeamName   string `yaml:"team_name"`   // ESPN identifies the team by name

	Format    Format                    `yaml:"format"`
	Roster    Roster                    `yaml:"roster"`
	Scoring   map[string]map[string]any `yaml:"scoring"`
	Waivers   Waivers                   `yaml:"waivers"`
	Trades    map[string]any            `yaml:"trades"`
	Deadlines map[string]any            `yaml:"deadlines"`
	Keepers   map[string]any            `yaml:"keepers"`
	Posture   string                    `yaml:"posture"`
}

type Format struct {
	Teams                 int    `yaml:"teams"`
	Superflex             bool   `yaml:"superflex"`
	Divisions             int    `yaml:"divisions"`
	FractionalPoints      bool   `yaml:"fractional_points"`
	NegativePoints        bool   `yaml:"negative_points"`
	LeagueFormat          string `yaml:"league_format"`
	RegularSeasonMatchups int    `yaml:"regular_season_matchups"`
	MatchupTiebreaker     string `yaml:"matchup_tiebreaker"`
	LineupProtection      bool   `yaml:"lineup_protection"`
}

type Roster struct {
	Size             int            `yaml:"size"`
	Starters         []string       `yaml:"starters"` // lineup slots in order: QB, RB, RB, ..., SUPER_FLEX
	Bench            int            `yaml:"bench"`
	IR               int            `yaml:"ir"`
	TaxiSlots        int            `yaml:"taxi_slots"`
	TaxiYears        int            `yaml:"taxi_years"`
	TaxiAllowVets    bool           `yaml:"taxi_allow_vets"`
	TaxiDeadlineWeek int            `yaml:"taxi_deadline_week"`
	IREligibility    string         `yaml:"ir_eligibility"`
	PositionMaximums map[string]int `yaml:"position_maximums"`
}

type Waivers struct {
	Type            string `yaml:"type"` // faab | rolling_priority | priority_reset_weekly
	Budget          int    `yaml:"budget"`
	MinBid          int    `yaml:"min_bid"`
	Daily           bool   `yaml:"daily"`
	ClearDays       int    `yaml:"clear_days"`
	ClaimPeriodDays int    `yaml:"claim_period_days"`
	WeeklyRun       string `yaml:"weekly_run"`
	LineupLock      string `yaml:"lineup_lock"`
	OrderRule       string `yaml:"order_rule"`
}

var validPlatforms = map[ledger.Platform]bool{
	ledger.PlatformSleeper: true,
	ledger.PlatformESPN:    true,
	ledger.PlatformYahoo:   true,
}

var validWaiverTypes = map[string]bool{
	"faab":                  true,
	"rolling_priority":      true,
	"priority_reset_weekly": true,
}

// Load reads every *.yaml in dir, sorted by filename. Unknown keys inside the
// typed sections are an error so a typo in a config fails loudly instead of
// silently defaulting.
func Load(dir string) ([]League, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("config: no *.yaml files in %s", dir)
	}
	sort.Strings(paths)

	seen := map[string]string{}
	var out []League
	for _, p := range paths {
		l, err := LoadFile(p)
		if err != nil {
			return nil, err
		}
		if prev, dup := seen[l.ID]; dup {
			return nil, fmt.Errorf("config: duplicate league id %q in %s and %s", l.ID, prev, p)
		}
		seen[l.ID] = p
		out = append(out, l)
	}
	return out, nil
}

// LoadFile parses and validates a single league YAML.
func LoadFile(path string) (League, error) {
	f, err := os.Open(path)
	if err != nil {
		return League{}, err
	}
	defer f.Close()

	var l League
	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	if err := dec.Decode(&l); err != nil {
		return League{}, fmt.Errorf("config: %s: %w", path, err)
	}
	l.Path = path
	if err := l.validate(); err != nil {
		return League{}, fmt.Errorf("config: %s: %w", path, err)
	}
	return l, nil
}

func (l League) validate() error {
	var errs []error
	req := func(field, v string) {
		if v == "" {
			errs = append(errs, fmt.Errorf("%s is required", field))
		}
	}
	req("id", l.ID)
	req("name", l.Name)
	req("league_id", l.LeagueID)
	req("owner", string(l.Owner))
	if !validPlatforms[l.Platform] {
		errs = append(errs, fmt.Errorf("platform %q must be one of sleeper, espn, yahoo", l.Platform))
	}
	if l.Season == 0 {
		errs = append(errs, errors.New("season is required"))
	}
	if len(l.Roster.Starters) == 0 {
		errs = append(errs, errors.New("roster.starters is required"))
	}
	if !validWaiverTypes[l.Waivers.Type] {
		errs = append(errs, fmt.Errorf("waivers.type %q must be one of faab, rolling_priority, priority_reset_weekly", l.Waivers.Type))
	}
	return errors.Join(errs...)
}

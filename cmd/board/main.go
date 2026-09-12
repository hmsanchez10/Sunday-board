// Command board fetches every league in leagues/*.yaml through its platform
// adapter and writes the consolidated state.json.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/hmsanchez10/Sunday-board/internal/adapters/sleeper"
	"github.com/hmsanchez10/Sunday-board/internal/cache"
	"github.com/hmsanchez10/Sunday-board/internal/config"
	"github.com/hmsanchez10/Sunday-board/internal/creds"
	"github.com/hmsanchez10/Sunday-board/internal/idmap"
	"github.com/hmsanchez10/Sunday-board/internal/ledger"
	"github.com/hmsanchez10/Sunday-board/internal/players"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "board:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		leaguesDir   = flag.String("leagues", "leagues", "directory of league YAML files")
		outPath      = flag.String("out", "state.json", "where to write the state file")
		playersCache = flag.String("players-cache", "players_nfl.json", "cache path for the Sleeper player dump")
		playersAge   = flag.Duration("players-max-age", players.DefaultMaxAge, "refetch the player dump when the cache is older than this")
		idmapCache   = flag.String("idmap-cache", "idmap.csv", "cache path for the DynastyProcess player id crosswalk")
		idmapAge     = flag.Duration("idmap-max-age", idmap.DefaultMaxAge, "refetch the crosswalk when the cache is older than this")
		unmatched    = flag.String("unmatched", "unmatched.json", "where to write held players with no ESPN or Yahoo id")
		credsPath    = flag.String("creds", filepath.Join("secrets", "creds.json"), "credential file for platforms that need one")
		sleeperUser  = flag.String("sleeper-user", "hect0reo", "Sleeper handle whose rosters to fetch")
		timeout      = flag.Duration("timeout", 2*time.Minute, "overall run timeout")
	)
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	httpClient := &http.Client{Timeout: 60 * time.Second}

	leagues, err := config.Load(*leaguesDir)
	if err != nil {
		return err
	}
	logf("loaded %d league configs from %s", len(leagues), *leaguesDir)

	nfl, err := sleeper.CurrentNFLState(ctx, httpClient, sleeper.DefaultBaseURL)
	if err != nil {
		return fmt.Errorf("nfl state: %w", err)
	}
	logf("nfl: season %s (%s), week %d", nfl.Season, nfl.SeasonType, nfl.Week)

	xwalk, err := idmap.Load(ctx, idmap.Options{CachePath: *idmapCache, MaxAge: *idmapAge, HTTP: httpClient})
	if err != nil {
		return err
	}
	logf("idmap: %d rows, %d with a sleeper_id (%s)", xwalk.Rows, xwalk.Len(), xwalk.Info)
	if xwalk.Info.Stale {
		logf("warning: crosswalk refetch failed, using stale cache: %v", xwalk.Info.StaleErr)
	}

	table, err := players.Load(ctx, players.Options{CachePath: *playersCache, MaxAge: *playersAge, HTTP: httpClient, IDMap: xwalk})
	if err != nil {
		return err
	}
	logf("players: %s; dump-wide resolution crosswalk %d, sleeper %d, name %d, unmatched %d", table,
		table.Resolved[ledger.ResolvedViaCrosswalk], table.Resolved[ledger.ResolvedViaSleeper],
		table.Resolved[ledger.ResolvedViaName], table.Resolved[ledger.ResolvedViaUnmatched])
	if table.Info.Stale {
		logf("warning: player refetch failed, using stale cache: %v", table.Info.StaleErr)
	}

	store := creds.NewFileCredStore(*credsPath)

	state := ledger.State{
		GeneratedAt: time.Now().UTC(),
		Week:        nfl.Week,
		Leagues:     []ledger.LeagueState{},
		Players:     map[ledger.PlayerID]ledger.Player{},
		Holdings:    map[ledger.PlayerID][]ledger.Holding{},
	}

	var failures []error
	for _, l := range leagues {
		adapter := adapterFor(l, *sleeperUser)
		if adapter == nil {
			logf("%s (%s): skipped, no adapter for platform %q yet", l.ID, l.Name, l.Platform)
			continue
		}
		c, err := store.CredsFor(l.Owner, l.Platform)
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", l.ID, err))
			continue
		}
		ls, err := adapter.Fetch(ctx, l.LeagueConfig, c)
		if err != nil {
			failures = append(failures, err)
			logf("%s: FAILED: %v", l.ID, err)
			continue
		}
		state.Leagues = append(state.Leagues, ls)
		logf("%s (%s): team %s, record %s, %d rostered, waivers %s", l.ID, l.Name, ls.TeamID, ls.Record, len(ls.Roster), describeWaivers(ls.Waivers))
	}

	index(&state, table)

	if err := writeJSON(*outPath, state); err != nil {
		return err
	}
	logf("wrote %s: %d leagues, %d players, %d holdings", *outPath, len(state.Leagues), len(state.Players), len(state.Holdings))

	un := report(&state)
	if err := writeJSON(*unmatched, un); err != nil {
		return err
	}
	if len(un) > 0 {
		logf("wrote %s: %d players", *unmatched, len(un))
	}

	if len(failures) > 0 {
		return fmt.Errorf("%d league(s) failed: %w", len(failures), errors.Join(failures...))
	}
	return nil
}

// adapterFor returns a fresh adapter for one league, or nil if the platform
// has no adapter yet. Per-league construction is what lets slot names come
// from that league's own config.
func adapterFor(l config.League, sleeperUser string) ledger.Adapter {
	switch l.Platform {
	case ledger.PlatformSleeper:
		return sleeper.New(sleeperUser, l.Roster.Starters)
	default:
		return nil
	}
}

// index fills State.Players and State.Holdings from State.Leagues.
func index(state *ledger.State, table *players.Store) {
	for _, ls := range state.Leagues {
		for _, e := range ls.Roster {
			if _, seen := state.Players[e.Player]; !seen {
				p, ok := table.Lookup(e.Player)
				if !ok {
					logf("warning: %s: player %s not in Sleeper dump", ls.LeagueID, e.Player)
					p = ledger.Player{ID: e.Player}
				}
				state.Players[e.Player] = p
			}
			state.Holdings[e.Player] = append(state.Holdings[e.Player], ledger.Holding{
				LeagueID: ls.LeagueID, Slot: e.Slot, SlotName: e.SlotName,
			})
		}
	}
	for _, hs := range state.Holdings {
		sort.Slice(hs, func(i, j int) bool {
			if hs[i].LeagueID != hs[j].LeagueID {
				return hs[i].LeagueID < hs[j].LeagueID
			}
			return hs[i].SlotName < hs[j].SlotName
		})
	}
}

// Unmatched is one held player with no ESPN or Yahoo id by any path.
type Unmatched struct {
	SleeperID ledger.PlayerID `json:"sleeper_id"`
	Name      string          `json:"name"`
	Position  string          `json:"position"`
	Team      string          `json:"team"`
	Leagues   []string        `json:"leagues"`
}

// report prints the id-resolution summary for the held players and returns
// the ones still unmatched. The slice is never nil so the file is always a
// JSON array.
func report(state *ledger.State) []Unmatched {
	counts := map[string]int{}
	noESPN, noYahoo := 0, 0
	un := []Unmatched{}
	ids := make([]ledger.PlayerID, 0, len(state.Players))
	for id := range state.Players {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		p := state.Players[id]
		counts[p.ResolvedVia]++
		if p.ESPNID == "" {
			noESPN++
		}
		if p.YahooID == "" {
			noYahoo++
		}
		if p.ResolvedVia == ledger.ResolvedViaUnmatched || p.ResolvedVia == "" {
			u := Unmatched{SleeperID: id, Name: p.Name, Position: p.Position, Team: p.Team, Leagues: []string{}}
			for _, h := range state.Holdings[id] {
				u.Leagues = append(u.Leagues, h.LeagueID)
			}
			un = append(un, u)
		}
	}
	logf("id resolution (%d held players): crosswalk %d, sleeper %d, name %d, unmatched %d; still missing espn_id %d, yahoo_id %d",
		len(ids), counts[ledger.ResolvedViaCrosswalk], counts[ledger.ResolvedViaSleeper], counts[ledger.ResolvedViaName],
		counts[ledger.ResolvedViaUnmatched], noESPN, noYahoo)
	return un
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return cache.WriteAtomic(path, append(data, '\n'))
}

func describeWaivers(w ledger.WaiverState) string {
	switch {
	case w.BudgetRemaining != nil:
		return fmt.Sprintf("%s $%d left, %d open spots", w.Type, *w.BudgetRemaining, w.OpenRosterSpots)
	case w.Priority != nil:
		return fmt.Sprintf("%s #%d, %d open spots", w.Type, *w.Priority, w.OpenRosterSpots)
	default:
		return fmt.Sprintf("%s, %d open spots", w.Type, w.OpenRosterSpots)
	}
}

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

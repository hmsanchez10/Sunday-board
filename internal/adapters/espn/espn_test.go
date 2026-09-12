package espn

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hmsanchez10/Sunday-board/internal/ledger"
)

type fakeResolver struct{}

func (fakeResolver) SleeperIDForESPN(id string) (ledger.PlayerID, bool) {
	m := map[string]ledger.PlayerID{"100": "s100", "200": "s200", "300": "s300"}
	v, ok := m[id]
	return v, ok
}
func (fakeResolver) TeamDefense(team string) (ledger.PlayerID, bool) {
	if team == "PIT" {
		return "PIT", true
	}
	return "", false
}

const league = `{
 "id": 1, "seasonId": 2026,
 "settings": {"rosterSettings": {"lineupSlotCounts": {"0":1,"2":1,"16":1,"20":2,"21":1,"23":0}}},
 "teams": [
  {"id": 3, "owners": ["{OTHER}"], "roster": {"entries": []}},
  {"id": 7, "owners": ["{abc-123}"], "primaryOwner": "{abc-123}", "waiverRank": 5,
   "record": {"overall": {"wins": 3, "losses": 1, "ties": 1}},
   "roster": {"entries": [
     {"playerId": 100, "lineupSlotId": 0, "playerPoolEntry": {"player": {"id": 100, "fullName": "Quarter Back", "defaultPositionId": 1, "proTeamId": 17}}},
     {"playerId": 200, "lineupSlotId": 2, "playerPoolEntry": {"player": {"id": 200, "fullName": "Running Back", "defaultPositionId": 2, "proTeamId": 12}}},
     {"playerId": -16023, "lineupSlotId": 16, "playerPoolEntry": {"player": {"id": -16023, "fullName": "Steelers D/ST", "defaultPositionId": 16, "proTeamId": 23}}},
     {"playerId": 300, "lineupSlotId": 20, "playerPoolEntry": {"player": {"id": 300, "fullName": "Bench Guy", "defaultPositionId": 3, "proTeamId": 6}}},
     {"playerId": 999, "lineupSlotId": 20, "injuryStatus": "NORMAL", "playerPoolEntry": {"player": {"id": 999, "fullName": "Mystery Man", "defaultPositionId": 3, "proTeamId": 30, "injuryStatus": "QUESTIONABLE"}}},
     {"playerId": 400, "lineupSlotId": 21, "playerPoolEntry": {"player": {"id": 400, "fullName": "Hurt Guy", "defaultPositionId": 4, "proTeamId": 1}}}
   ]}}
 ]
}`

func TestFetch(t *testing.T) {
	var gotCookies string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCookies = r.Header.Get("Cookie")
		if r.URL.Path != "/seasons/2026/segments/0/leagues/258236" {
			t.Errorf("path %s", r.URL.Path)
		}
		w.Write([]byte(league))
	}))
	defer srv.Close()

	a := New(fakeResolver{}, 2026, []string{"QB", "RB", "D/ST"})
	a.BaseURL = srv.URL
	cfg := ledger.LeagueConfig{ID: "the-league", Platform: ledger.PlatformESPN, LeagueID: "258236", Owner: "hector"}
	st, err := a.Fetch(context.Background(), cfg, ledger.Creds{"espn_s2": "S2", "SWID": "{ABC-123}"})
	if err != nil {
		t.Fatal(err)
	}
	if gotCookies != "espn_s2=S2; SWID={ABC-123}" {
		t.Errorf("cookies: %q", gotCookies)
	}
	if st.TeamID != "7" || st.Record != "3-1-1" {
		t.Errorf("identity: %+v", st)
	}
	want := []ledger.RosterEntry{
		{Player: "s100", Slot: ledger.SlotStarter, SlotName: "QB"},
		{Player: "s200", Slot: ledger.SlotStarter, SlotName: "RB"},
		{Player: "PIT", Slot: ledger.SlotStarter, SlotName: "D/ST"},
		{Player: "s300", Slot: ledger.SlotBench},
		{Player: "espn:999", Slot: ledger.SlotBench},
		{Player: "espn:400", Slot: ledger.SlotIR},
	}
	if len(st.Roster) != len(want) {
		t.Fatalf("roster: %+v", st.Roster)
	}
	for i := range want {
		if st.Roster[i] != want[i] {
			t.Errorf("entry %d: %+v, want %+v", i, st.Roster[i], want[i])
		}
	}
	if st.Waivers.Type != "priority_reset_weekly" || *st.Waivers.Priority != 5 {
		t.Errorf("waivers: %+v", st.Waivers)
	}
	// size 5 (1+1+1+2, IR excluded), held 5 (IR excluded) -> 0 open
	if st.Waivers.OpenRosterSpots != 0 {
		t.Errorf("open: %d", st.Waivers.OpenRosterSpots)
	}
	un := a.Unjoined()
	if len(un) != 2 {
		t.Fatalf("unjoined: %+v", un)
	}
	m := un["espn:999"]
	if m.Name != "Mystery Man" || m.Position != "WR" || m.Team != "JAX" || m.ESPNID != "999" || m.InjuryStatus != "QUESTIONABLE" || m.ResolvedVia != ledger.ResolvedViaUnmatched {
		t.Errorf("unjoined record: %+v", m)
	}
}

func TestFetchNeedsCreds(t *testing.T) {
	a := New(fakeResolver{}, 2026, nil)
	_, err := a.Fetch(context.Background(), ledger.LeagueConfig{ID: "x", Platform: ledger.PlatformESPN, LeagueID: "1"}, ledger.Creds{})
	if err == nil {
		t.Fatal("expected missing-creds error")
	}
}

func TestFetchNoOwnedTeam(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(league)) }))
	defer srv.Close()
	a := New(fakeResolver{}, 2026, []string{"QB", "RB", "D/ST"})
	a.BaseURL = srv.URL
	_, err := a.Fetch(context.Background(), ledger.LeagueConfig{ID: "x", Platform: ledger.PlatformESPN, LeagueID: "1"}, ledger.Creds{"espn_s2": "s", "swid": "{nobody}"})
	if err == nil {
		t.Fatal("expected no-team error")
	}
}

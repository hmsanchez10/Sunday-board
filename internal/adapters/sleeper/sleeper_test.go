package sleeper

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hmsanchez10/Sunday-board/internal/ledger"
)

func TestFetchMapsRoster(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/user/hect0reo", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"user_id":"u1","username":"hect0reo"}`))
	})
	mux.HandleFunc("/league/L1", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"roster_positions":["QB","RB","FLEX","BN","BN"],"settings":{"waiver_type":2,"waiver_budget":200}}`))
	})
	mux.HandleFunc("/league/L1/users", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"user_id":"u9"},{"user_id":"u1"}]`))
	})
	mux.HandleFunc("/league/L1/rosters", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[
		 {"roster_id":1,"owner_id":"u9","players":["1"],"starters":["1"],"settings":{}},
		 {"roster_id":7,"owner_id":"u1",
		  "players":["10","11","12","13","14","15"],
		  "starters":["10","0","11"],
		  "reserve":["14"],
		  "taxi":["15"],
		  "settings":{"wins":2,"losses":1,"ties":0,"waiver_budget_used":35,"waiver_position":4}}
		]`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	a := New("@hect0reo", []string{"QB", "RB", "FLEX"})
	a.BaseURL = srv.URL

	cfg := ledger.LeagueConfig{ID: "test", Platform: ledger.PlatformSleeper, LeagueID: "L1", Owner: "hector"}
	st, err := a.Fetch(context.Background(), cfg, ledger.Creds{})
	if err != nil {
		t.Fatal(err)
	}

	if st.LeagueID != "test" || st.TeamID != "7" || st.Record != "2-1" {
		t.Errorf("identity: %+v", st)
	}
	want := map[ledger.PlayerID]ledger.RosterEntry{
		"10": {Slot: ledger.SlotStarter, SlotName: "QB"},
		"11": {Slot: ledger.SlotStarter, SlotName: "FLEX"},
		"12": {Slot: ledger.SlotBench},
		"13": {Slot: ledger.SlotBench},
		"14": {Slot: ledger.SlotIR},
		"15": {Slot: ledger.SlotTaxi},
	}
	if len(st.Roster) != len(want) {
		t.Fatalf("got %d entries, want %d: %+v", len(st.Roster), len(want), st.Roster)
	}
	for _, e := range st.Roster {
		w, ok := want[e.Player]
		if !ok || w.Slot != e.Slot || w.SlotName != e.SlotName {
			t.Errorf("entry %+v, want %+v", e, w)
		}
	}
	if st.Waivers.Type != "faab" || st.Waivers.BudgetRemaining == nil || *st.Waivers.BudgetRemaining != 165 {
		t.Errorf("waivers: %+v", st.Waivers)
	}
	// 5 roster_positions, 4 active players (6 minus IR minus taxi) -> 1 open.
	if st.Waivers.OpenRosterSpots != 1 {
		t.Errorf("open spots: %d", st.Waivers.OpenRosterSpots)
	}
}

func TestFetchRejectsDriftedConfig(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/user/x", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"user_id":"u1"}`)) })
	mux.HandleFunc("/league/L1", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"roster_positions":["QB","RB","BN"],"settings":{}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	a := New("x", []string{"QB", "WR"})
	a.BaseURL = srv.URL
	_, err := a.Fetch(context.Background(), ledger.LeagueConfig{ID: "t", Platform: ledger.PlatformSleeper, LeagueID: "L1"}, nil)
	if err == nil {
		t.Fatal("expected config drift error")
	}
}

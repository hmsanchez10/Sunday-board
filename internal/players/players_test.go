package players

import (
	"strings"
	"testing"

	"github.com/hmsanchez10/Sunday-board/internal/idmap"
	"github.com/hmsanchez10/Sunday-board/internal/ledger"
)

const crosswalk = `sleeper_id,espn_id,yahoo_id,gsis_id,name,position,team
1,100,NA,00-1,Cross Walk,RB,KCC
2,NA,NA,00-2,Gsis Only,WR,DAL
NA,300,301,00-4,Name Match,WR,GBP
NA,500,NA,NA,Twin Player,TE,FA
NA,501,NA,NA,Twin Player,TE,FA
`

func TestResolveOrder(t *testing.T) {
	m, err := idmap.Parse(strings.NewReader(crosswalk))
	if err != nil {
		t.Fatal(err)
	}
	dump := map[string]rawPlayer{
		"1": {PlayerID: "1", FullName: "Cross Walk", Position: "RB", Team: "KC", ESPNID: "999", YahooID: "111"},
		"2": {PlayerID: "2", FullName: "Gsis Only", Position: "WR", Team: "DAL", ESPNID: "200"},
		"3": {PlayerID: "3", FullName: "Sleeper Only", Position: "QB", Team: "NYJ", YahooID: "333"},
		"4": {PlayerID: "4", FullName: "Name Match Jr.", Position: "WR", Team: "GB"},
		"5": {PlayerID: "5", FullName: "Twin Player", Position: "TE"},
		"6": {PlayerID: "6", FullName: "Nobody", Position: "RB", Team: "SEA"},
	}
	s := build(dump, m)

	want := map[ledger.PlayerID]ledger.Player{
		"1": {ESPNID: "100", YahooID: "111", GSISID: "00-1", ResolvedVia: ledger.ResolvedViaCrosswalk}, // yahoo filled from sleeper
		"2": {ESPNID: "200", GSISID: "00-2", ResolvedVia: ledger.ResolvedViaSleeper},                   // gsis kept from crosswalk
		"3": {YahooID: "333", ResolvedVia: ledger.ResolvedViaSleeper},
		"4": {ESPNID: "300", YahooID: "301", GSISID: "00-4", ResolvedVia: ledger.ResolvedViaName},
		"5": {ResolvedVia: ledger.ResolvedViaUnmatched}, // ambiguous name
		"6": {ResolvedVia: ledger.ResolvedViaUnmatched},
	}
	for id, w := range want {
		g, ok := s.Lookup(id)
		if !ok {
			t.Fatalf("%s missing", id)
		}
		if g.ESPNID != w.ESPNID || g.YahooID != w.YahooID || g.GSISID != w.GSISID || g.ResolvedVia != w.ResolvedVia {
			t.Errorf("%s: got espn=%q yahoo=%q gsis=%q via=%q, want espn=%q yahoo=%q gsis=%q via=%q",
				id, g.ESPNID, g.YahooID, g.GSISID, g.ResolvedVia, w.ESPNID, w.YahooID, w.GSISID, w.ResolvedVia)
		}
	}
	if s.Resolved[ledger.ResolvedViaUnmatched] != 2 || s.Resolved[ledger.ResolvedViaCrosswalk] != 1 {
		t.Errorf("counts: %v", s.Resolved)
	}

	// Reverse join: crosswalk row first, then the resolved table.
	if id, ok := s.SleeperIDForESPN("100"); !ok || id != "1" {
		t.Errorf("ESPN 100 -> %q %v, want 1", id, ok)
	}
	if id, ok := s.SleeperIDForESPN("333"); ok {
		t.Errorf("ESPN 333 is a yahoo id, got %q", id)
	}
	if id, ok := s.SleeperIDForESPN("200"); !ok || id != "2" {
		t.Errorf("ESPN 200 (sleeper's own) -> %q %v, want 2", id, ok)
	}
	if id, ok := s.SleeperIDForESPN("300"); !ok || id != "4" {
		t.Errorf("ESPN 300 (name path) -> %q %v, want 4", id, ok)
	}
	if _, ok := s.SleeperIDForESPN("nope"); ok {
		t.Error("unknown espn id should miss")
	}
}

func TestTeamDefense(t *testing.T) {
	s := build(map[string]rawPlayer{
		"PIT": {PlayerID: "PIT", FirstName: "Pittsburgh", LastName: "Steelers", Position: "DEF", Team: "PIT"},
		"1":   {PlayerID: "1", FullName: "Not A Defense", Position: "RB", Team: "KC"},
	}, nil)
	if id, ok := s.TeamDefense("PIT"); !ok || id != "PIT" {
		t.Errorf("PIT -> %q %v", id, ok)
	}
	if p, _ := s.Lookup("PIT"); p.ESPNID != "-16023" || p.ResolvedVia != ledger.ResolvedViaTeam {
		t.Errorf("DEF should resolve via team with a derived espn id: %+v", p)
	}
	if id, ok := s.SleeperIDForESPN("-16023"); !ok || id != "PIT" {
		t.Errorf("ESPN D/ST id -16023 -> %q %v, want PIT", id, ok)
	}
	if _, ok := s.TeamDefense("KC"); ok {
		t.Error("KC has no DEF record here")
	}
	if p, _ := s.Lookup("PIT"); p.Name != "Pittsburgh Steelers" {
		t.Errorf("DEF name: %q", p.Name)
	}
}

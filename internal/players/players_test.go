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
}

package config

import (
	"testing"

	"github.com/hmsanchez10/Sunday-board/internal/ledger"
)

func TestLoadRepoLeagues(t *testing.T) {
	leagues, err := Load("../../leagues")
	if err != nil {
		t.Fatal(err)
	}
	if len(leagues) == 0 {
		t.Fatal("no leagues loaded")
	}
	for _, l := range leagues {
		if l.ID == "" || l.LeagueID == "" || l.Owner == "" {
			t.Errorf("%s: missing identifiers: %+v", l.Path, l.LeagueConfig)
		}
		if len(l.Roster.Starters) == 0 {
			t.Errorf("%s: no starters", l.Path)
		}
		if l.Platform == ledger.PlatformSleeper && l.Waivers.Type == "faab" && l.Waivers.Budget == 0 {
			t.Errorf("%s: faab league with no budget", l.Path)
		}
		if l.Scoring["passing"] == nil {
			t.Errorf("%s: scoring.passing missing", l.Path)
		}
	}
}

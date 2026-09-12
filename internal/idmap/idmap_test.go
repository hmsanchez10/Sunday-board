package idmap

import (
	"strings"
	"testing"
)

func TestNormalizeName(t *testing.T) {
	cases := map[string]string{
		"A.J. Brown":            "aj brown",
		"Kenneth Walker III":    "kenneth walker",
		"Marvin Harrison Jr.":   "marvin harrison",
		"Amon-Ra St. Brown":     "amonra st brown",
		"Odell Beckham Jr":      "odell beckham",
		"D'Andre Swift":         "dandre swift",
		"  Patrick   Mahomes  ": "patrick mahomes",
		"Robert Griffin III":    "robert griffin",
		"Jr.":                   "jr", // never strip the only token
	}
	for in, want := range cases {
		if got := NormalizeName(in); got != want {
			t.Errorf("NormalizeName(%q) = %q, want %q", in, got, want)
		}
	}
}

const sample = `sleeper_id,espn_id,yahoo_id,gsis_id,name,position,team,extra
8151,4567048,33996,00-0038134,Kenneth Walker III,RB,KCC,x
2859,NA,NA,NA,Old Row,RB,FA,x
2859,111,NA,NA,Old Row,RB,FA,x
NA,222,333,NA,Jacoby Jones,WR,FA,x
NA,444,555,NA,Jacoby Jones,WR,FA,x
NA,666,NA,00-1,Sam Kicker,PK,GBP,x
`

func TestESPNTeamDefenseID(t *testing.T) {
	if id, ok := ESPNTeamDefenseID("PIT"); !ok || id != "-16023" {
		t.Errorf("PIT -> %q %v", id, ok)
	}
	if id, ok := ESPNTeamDefenseID("KCC"); !ok || id != "-16012" {
		t.Errorf("KCC (MFL code) -> %q %v", id, ok)
	}
	if _, ok := ESPNTeamDefenseID("XYZ"); ok {
		t.Error("unknown team should miss")
	}
	if TeamForESPNProTeamID(30) != "JAX" || TeamForESPNProTeamID(0) != "" {
		t.Error("proTeamId mapping")
	}
}

func TestParse(t *testing.T) {
	m, err := Parse(strings.NewReader(sample))
	if err != nil {
		t.Fatal(err)
	}
	r, ok := m.BySleeper("8151")
	if !ok || r.ESPNID != "4567048" || r.Team != "KC" || r.Position != "RB" {
		t.Errorf("8151: %+v", r)
	}
	if r, _ := m.BySleeper("2859"); r.ESPNID != "111" {
		t.Errorf("duplicate sleeper_id should keep the populated row, got %+v", r)
	}
	if _, ok := m.ByName("Jacoby Jones", "WR", ""); ok {
		t.Error("ambiguous name should not match")
	}
	if r, ok := m.ByName("kenneth walker", "RB", "KC"); !ok || r.SleeperID != "8151" {
		t.Errorf("name match: %+v %v", r, ok)
	}
	if r, ok := m.ByName("Sam Kicker", "K", "GB"); !ok || r.ESPNID != "666" {
		t.Errorf("PK/GBP should normalize to K/GB: %+v %v", r, ok)
	}
}

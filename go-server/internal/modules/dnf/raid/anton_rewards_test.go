package raid

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestAntonResultDomainRankProducesNativeRanks(t *testing.T) {
	for rank := byte(0); rank < 4; rank++ {
		body, err := BuildAntonResultBody(false, 0, 1872, 7, rank, 0)
		if err != nil || len(body) != 10 || body[8] != rank+2 {
			t.Fatal("domain/native result rank", rank, body, err)
		}
		if out := os.Getenv("DNF90_ANTON_NATIVE_DIR"); out != "" {
			if err := os.MkdirAll(out, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(out, fmt.Sprintf("go-anton-result-rank%d.bin", rank+2)), body, 0644); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := BuildAntonResultBody(false, 0, 1, 0, 4, 0); err == nil {
		t.Fatal("unsupported domain rank accepted")
	}
	if _, err := BuildResultBody(false, 0, 1, 0, 6, 0); err == nil {
		t.Fatal("unsupported wire rank accepted")
	}
}

func TestRealPVFAntonRewardsUseExactContainersAndWeights(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires actual production PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	config, err := LoadAntonRewardConfiguration(archive)
	if err != nil {
		t.Fatal(err)
	}
	for phase, expected := range [][]struct {
		draw uint32
		id   uint32
		flag byte
	}{
		{{0, 10304479, 0}, {97, 10304479, 0}, {98, 10304483, 1}, {99, 10304484, 1}},
		{{0, 10304480, 0}, {664, 10304480, 0}, {665, 10157783, 1}, {964, 10157783, 1}, {965, 10304485, 1}, {969, 10304485, 1}, {970, 10304486, 1}, {994, 10304486, 1}, {995, 10304487, 1}, {999, 10304487, 1}},
	} {
		p := config.Phases[phase]
		if p.MinimumClears != 1 {
			t.Fatal("participation requirement differs from actual PVF", p)
		}
		for _, entry := range expected {
			for rank := byte(0); rank <= 3; rank++ {
				got, err := p.Select("squad_item", rank, entry.draw)
				if err != nil || got.ContainerID != entry.id || got.Flags != entry.flag {
					t.Fatal("weighted reward differs from actual PVF", phase, entry, got, err)
				}
			}
		}
		for deaths, rank := range map[uint32]byte{0: 0, 1: 0, 2: 1, 5: 1, 6: 2, 9: 2, 10: 3, 14: 3, 15: 3, 65535: 3, ^uint32(0): 3} {
			got, err := p.RankForDeaths(deaths)
			if err != nil || got != rank {
				t.Fatal("configured rank range", deaths, got, err)
			}
		}
		if _, err := (AntonPhaseRewards{}).RankForDeaths(15); err == nil {
			t.Fatal("missing rank table accepted")
		}
		_, total, err := p.Pool("squad_item", 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.Select("squad_item", 0, total); err == nil {
			t.Fatal("out-of-range draw accepted")
		}
		if _, _, err := p.Pool("gold", 0); err == nil {
			t.Fatal("missing gold reward replaced by old container")
		}
		party, err := p.Select("party_card", 0, 0)
		want := uint32(10304474)
		if phase == 1 {
			want = 10304475
		}
		if err != nil || party.ContainerID != want {
			t.Fatal("party reward does not follow actual phase", party, err)
		}
	}
	text, err := archive.ReadText("etc/raid/anton.etc")
	if err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []string{
		strings.Replace(text, "`squad_item` -1 98 10304479 0", "`squad_item` -1 0 10304479 0", 1),
		strings.Replace(text, "`party_card` -1 100 10304474 0", "`party_card` -1 100 0 0", 1),
		strings.Replace(text, "`pcroom_card`", "`unknown_kind`", 1),
		strings.Replace(text, "[MINIMAL DUNGEON CLEAR COUNT]\n1", "[MINIMAL DUNGEON CLEAR COUNT]\n0", 1),
		strings.Replace(text, "2 5 1", "1 5 1", 1),
		strings.Replace(text, "[/CLEAR REWARD ITEM]", "", 1),
	} {
		if mutation == text {
			t.Fatal("test mutation did not match actual PVF")
		}
		if _, err := LoadAntonRewardConfiguration(admissionSource{"etc/raid/anton.etc": mutation}); err == nil {
			t.Fatal("malformed reward dependency accepted")
		}
	}
}

func TestAntonResultAndNestedRewardsEmitNativeFixtures(t *testing.T) {
	result, err := BuildResultBody(false, 0, 1872, 7, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	cards := []RewardDisplayCard{
		{CharacterID: 31, CardType: 1, Items: []RewardDisplayItem{{ItemID: 10304475, Quantity: 1}, {ItemID: 10304481, Quantity: 2, Flags: 1}}},
		{CharacterID: 32, CardType: 2, Items: []RewardDisplayItem{{ItemID: 10304480, Quantity: 1}}},
	}
	body, err := BuildRewardListBody(3, cards)
	if err != nil {
		t.Fatal(err)
	}
	if directory := os.Getenv("DNF90_ANTON_NATIVE_DIR"); directory != "" {
		if err := os.MkdirAll(directory, 0755); err != nil {
			t.Fatal(err)
		}
		for name, wire := range map[string][]byte{"go-anton-result.bin": result, "go-anton-reward-list.bin": body} {
			if err := os.WriteFile(filepath.Join(directory, name), wire, 0644); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, invalid := range [][]RewardDisplayCard{
		{{CharacterID: 0}},
		{{CharacterID: 31}, {CharacterID: 31}},
		{{CharacterID: 31, Items: make([]RewardDisplayItem, 256)}},
		{{CharacterID: 31, Items: []RewardDisplayItem{{ItemID: 0, Quantity: 1}}}},
		{{CharacterID: 31, Items: []RewardDisplayItem{{ItemID: 10304474, Quantity: 0}}}},
	} {
		if _, err := BuildRewardListBody(0, invalid); err == nil {
			t.Fatal("invalid reward projection accepted", invalid)
		}
	}
}

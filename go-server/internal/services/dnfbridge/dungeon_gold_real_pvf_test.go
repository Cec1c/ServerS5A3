package dnfbridge

import (
	"context"
	"os"
	"testing"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/progression"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFQuestGoldEveryLevelMatchesFlatSource(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF not configured")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	text, err := archive.ReadText(progression.QuestParameterPath)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := dnfpvf.Parse(progression.QuestParameterPath, text)
	if err != nil {
		t.Fatal(err)
	}
	values := doc.Ints("gold reward table")
	if len(values) == 0 {
		t.Fatal("empty gold source")
	}
	tables, err := progression.Load(context.Background(), archive)
	if err != nil {
		t.Fatal(err)
	}
	for i, value := range values {
		got, err := tables.QuestGold(i+1, i+1, 100, false)
		if err != nil || int64(got) != value {
			t.Fatalf("level=%d gold=%d source=%d err=%v", i+1, got, value, err)
		}
	}
	t.Logf("checked all %d gold levels against exact PVF rows", len(values))
}

func TestRealPVFLevel90FreeCardUsesKillsPartyAndRank(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF not configured")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonMonsterCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := currentDungeonGoldReferences(catalog)
	if err != nil {
		t.Fatal(err)
	}
	reference, found := refs[90]
	if !found || reference.Base != 1438 || reference.VariancePct != 9 {
		t.Fatalf("unexpected actual level90 reference=%+v found=%t", reference, found)
	}
	for _, scenario := range []struct {
		members   int
		partyRate float64
		amount    int64
	}{
		{1, 1, 7247}, {2, 1.2, 4348}, {4, 1.6, 2899},
	} {
		runtime := &runtimeDungeonState{
			entryPartyCount:              scenario.members,
			Request:                      dungeoncmd.SelectDungeonRequest{Difficulty: 4},
			Room:                         &runtimeDungeonRoom{cardGoldKills: currentDungeonCardKillCounts{Normal: 20, Boss: 1}},
			settlementPlayResultReceived: true,
			settlementPlayResultBody:     make([]byte, currentDungeonPlayResultBaseSize),
		}
		runtime.settlementPlayResultBody[currentDungeonPlayResultClientRankPointOffset] = 99
		input, err := loadCurrentDungeonFreeCardGoldInputs(archive, runtime)
		if err != nil {
			t.Fatal(err)
		}
		if input.DifficultyRate != 2 || input.PartyRate != scenario.partyRate || input.RankBonus != .2 {
			t.Fatalf("PVF rates differ: %+v", input)
		}
		noVariance := reference
		noVariance.VariancePct = 0
		got, err := input.amount(newCurrentDungeonDropLCG(37), noVariance)
		if err != nil || got != scenario.amount {
			t.Fatalf("members=%d amount=%d want=%d err=%v", scenario.members, got, scenario.amount, err)
		}
		got, err = input.amount(newCurrentDungeonDropLCG(37), reference)
		if err != nil || got < scenario.amount*91/100 || got > scenario.amount*109/100 {
			t.Fatalf("actual variance result=%d err=%v", got, err)
		}
		t.Logf("members=%d normal=20 boss=1 rank=99 prevariance=%d actual=%d", scenario.members, scenario.amount, got)
		for _, rank := range []struct {
			point byte
			bonus float64
		}{
			{255, .2}, {99, .2}, {98, .15}, {90, .15}, {89, .12}, {80, .12},
			{79, .10}, {60, .10}, {59, .05}, {50, .05}, {49, 0}, {30, 0}, {20, 0}, {10, 0}, {0, 0},
		} {
			runtime.settlementPlayResultBody[currentDungeonPlayResultClientRankPointOffset] = rank.point
			input, err := loadCurrentDungeonFreeCardGoldInputs(archive, runtime)
			if err != nil || input.RankBonus != rank.bonus {
				t.Fatalf("rank=%d bonus=%v want=%v err=%v", rank.point, input.RankBonus, rank.bonus, err)
			}
		}
	}
}

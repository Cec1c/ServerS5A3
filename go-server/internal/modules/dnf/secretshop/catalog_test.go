package secretshop

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

type catalogTestSource string

func (s catalogTestSource) ReadText(relativePath string) (string, error) {
	if relativePath != PVFPath {
		return "", fmt.Errorf("unexpected path %q", relativePath)
	}
	return string(s), nil
}

func syntheticCatalogText() string {
	return `
[eplp wait time]
3
[price random]
10
[cash user stackable count]
200
[add slot cash user stackable count]
100
[dungeon npc]
7146 1000 9465 9445 9425 9405 9385
7146 1002 30 40 50 60 70
7146 1003 500 510 520 530 540
7146 1004 5 5 5 5 5
[/dungeon npc]
[level section]
0 1 19
6 66 73
14 88 90
[/level section]
[level npc]
14 1000 9489 9477 9465 9453 9441
14 1002 6 8 10 12 14
14 1003 500 510 520 530 540
14 1004 5 5 5 5 5
[/level npc]
[npc]
1002
[level]
14 2
100 0 50 0 1 10
101 1 75 2 3 20
102 0 125 0 2 30
[/level]
[cash item]
14 2
110 0 60 0 1 11
111 1 85 3 4 21
[/cash item]
[dungeon]
7146 2
200 0 70 0 1 12
201 1 95 4 5 22
202 0 145 0 6 32
[/dungeon item]
[dungeon cash item]
7146 2
210 0 80 0 1 13
211 1 105 5 6 23
[/dungeon cash item]
[/npc]
[npc]
1003
[level]
14 2
3033 0 100 0 100 100
3034 0 100 0 100 100
[/level]
[cash item]
14 2
3035 0 100 0 100 100
3036 0 100 0 100 100
[/cash item]
[dungeon]
7146 2
3033 0 100 0 100 100
3034 0 100 0 100 100
[/dungeon]
[dungeon cash item]
7146 2
3035 0 100 0 100 100
3036 0 100 0 100 100
[/dungeon cash item]
[/npc]
[npc]
1004
[level]
6 1
[/level]
[cash item]
6 1
[/cash item]
[level]
14 1
4000 0 500 0 1 10
[/level]
[cash item]
14 1
4001 0 600 0 1 11
[/cash item]
[dungeon]
7146 1
4002 0 700 0 1 12
[/dungeon]
[dungeon cash item]
7146 1
4003 0 800 0 1 13
[/dungeon cash item]
[/npc]
[event item]
1 10 0 150000 0 1 2500
[/event item]
[secret shop end]
`
}

func loadSyntheticCatalog(t *testing.T) *Catalog {
	t.Helper()
	cat, err := Load(catalogTestSource(syntheticCatalogText()))
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

func TestLoadRealTableShapeCopiesAndLookupPriority(t *testing.T) {
	cat := loadSyntheticCatalog(t)
	if cat.WaitSeconds != 3 || cat.PriceRandomPercent != 10 || cat.StackablePercent != 200 || cat.ExtraStackablePercent != 100 {
		t.Fatalf("unexpected scalar settings: %+v", cat)
	}
	if got, want := cat.SupportedNPCIDs(), []uint32{1002, 1003, 1004}; !reflect.DeepEqual(got, want) {
		t.Fatalf("supported NPCs %v, want %v", got, want)
	}

	wantWeights := []NPCWeight{
		{NPCID: 1000, Weights: [5]uint32{9465, 9445, 9425, 9405, 9385}},
		{NPCID: 1002, Weights: [5]uint32{30, 40, 50, 60, 70}},
		{NPCID: 1003, Weights: [5]uint32{500, 510, 520, 530, 540}},
		{NPCID: 1004, Weights: [5]uint32{5, 5, 5, 5, 5}},
	}
	weights, err := cat.NPCWeights(7146, 89)
	if err != nil || !reflect.DeepEqual(weights, wantWeights) {
		t.Fatalf("dungeon weights=%+v err=%v", weights, err)
	}
	levelWeights, err := cat.NPCWeights(9999, 89)
	if err != nil || len(levelWeights) != 4 || levelWeights[1].Weights != ([5]uint32{6, 8, 10, 12, 14}) {
		t.Fatalf("level fallback weights=%+v err=%v", levelWeights, err)
	}
	pairs, err := cat.WeightPairs(7146, 89, 1)
	if err != nil || !reflect.DeepEqual(pairs, []WeightPair{
		{NPCID: 1000, Weight: 9445},
		{NPCID: 1002, Weight: 40},
		{NPCID: 1003, Weight: 510},
		{NPCID: 1004, Weight: 5},
	}) {
		t.Fatalf("weight pairs=%+v err=%v", pairs, err)
	}
	if _, err := cat.WeightPairs(7146, 89, 5); !errors.Is(err, ErrInvalidColumn) {
		t.Fatalf("invalid column error %v", err)
	}

	normal, extra, err := cat.Pools(1002, 7146, 89)
	if err != nil {
		t.Fatal(err)
	}
	if normal.SelectionCount != 2 || len(normal.Candidates) != 3 || normal.Candidates[0] != (Candidate{200, 0, 70, 0, 1, 12}) {
		t.Fatalf("unexpected dungeon normal pool %+v", normal)
	}
	if extra.SelectionCount != 2 || len(extra.Candidates) != 2 || extra.Candidates[1] != (Candidate{211, 1, 105, 5, 6, 23}) {
		t.Fatalf("unexpected dungeon-cash pool %+v", extra)
	}

	normal, extra, err = cat.Pools(1002, 9999, 89)
	if err != nil {
		t.Fatal(err)
	}
	if normal.SelectionCount != 2 || len(normal.Candidates) != 3 || normal.Candidates[1].CostMode != 1 {
		t.Fatalf("unexpected level normal pool %+v", normal)
	}
	if extra.SelectionCount != 2 || len(extra.Candidates) != 2 || extra.Candidates[1].CostItemID != 3 {
		t.Fatalf("unexpected level cash pool %+v", extra)
	}
	normal, extra, err = cat.Pools(1002, 7146, 9999)
	if err != nil || normal.Candidates[0].ItemID != 200 || extra.Candidates[0].ItemID != 210 {
		t.Fatalf("dungeon priority depends on level: normal=%+v extra=%+v err=%v", normal, extra, err)
	}
	if defaultNormal, defaultExtra, err := cat.Pools(DefaultNPCID, 7146, 89); err != nil || defaultNormal.SelectionCount != 0 || len(defaultNormal.Candidates) != 0 || defaultExtra.SelectionCount != 0 || len(defaultExtra.Candidates) != 0 {
		t.Fatalf("default NPC pool=%+v/%+v err=%v", defaultNormal, defaultExtra, err)
	}
	if _, _, err := cat.Pools(1005, 7146, 89); !errors.Is(err, ErrUnsupportedNPC) {
		t.Fatalf("unknown NPC error %v", err)
	}
	if _, _, err := cat.Pools(1002, 9999, 87); !errors.Is(err, ErrLevelNotFound) {
		t.Fatalf("missing level error %v", err)
	}
	emptyNormal, emptyExtra, err := cat.Pools(1004, 0, 70)
	if err != nil || emptyNormal.SelectionCount != 1 || len(emptyNormal.Candidates) != 0 || emptyExtra.SelectionCount != 1 || len(emptyExtra.Candidates) != 0 {
		t.Fatalf("zero-candidate pool was rejected: normal=%+v extra=%+v err=%v", emptyNormal, emptyExtra, err)
	}

	normal.Candidates[0].ItemID = 999999
	normalAgain, _, err := cat.Pools(1002, 7146, 89)
	if err != nil || normalAgain.Candidates[0].ItemID != 200 {
		t.Fatalf("pool was mutated through returned slice: %+v err=%v", normalAgain, err)
	}
	weights[0].Weights[0] = 1
	weightsAgain, err := cat.NPCWeights(7146, 89)
	if err != nil || weightsAgain[0].Weights[0] != 9465 {
		t.Fatalf("weights were mutated through returned slice: %+v err=%v", weightsAgain, err)
	}
	pairs[0].Weight = 1
	pairsAgain, err := cat.WeightPairs(7146, 89, 1)
	if err != nil || pairsAgain[0].Weight != 9445 {
		t.Fatalf("pairs were mutated through returned slice: %+v err=%v", pairsAgain, err)
	}
}

func TestLoadRejectsMalformedTables(t *testing.T) {
	base := syntheticCatalogText()
	tests := []struct {
		name   string
		mutate func(string) string
	}{
		{"missing scalar section", func(s string) string { return strings.Replace(s, "[eplp wait time]\n3\n", "", 1) }},
		{"duplicate dungeon weight key", func(s string) string {
			line := "7146 1002 30 40 50 60 70\n"
			return strings.Replace(s, line, line+line, 1)
		}},
		{"odd dungeon weight row", func(s string) string {
			return strings.Replace(s, "[/dungeon npc]", "1\n[/dungeon npc]", 1)
		}},
		{"duplicate level section key", func(s string) string {
			return strings.Replace(s, "14 88 90\n", "14 88 90\n14 91 100\n", 1)
		}},
		{"overlapping level section", func(s string) string {
			return strings.Replace(s, "14 88 90\n", "15 10 20\n", 1)
		}},
		{"unknown NPC block", func(s string) string {
			return strings.Replace(s, "\n1003\n[level]", "\n1005\n[level]", 1)
		}},
		{"duplicate pool selector", func(s string) string {
			old := "102 0 125 0 2 30\n[/level]"
			return strings.Replace(s, old, "102 0 125 0 2 30\n[/level]\n[level]\n14 2\n103 0 5 0 1 1\n[/level]", 1)
		}},
		{"selection count overflow", func(s string) string {
			return strings.Replace(s, "14 2\n100 0 50", "14 9\n100 0 50", 1)
		}},
		{"cost mode invalid", func(s string) string {
			return strings.Replace(s, "100 0 50 0 1 10", "100 2 50 0 1 10", 1)
		}},
		{"negative price", func(s string) string {
			return strings.Replace(s, "100 0 50 0 1 10", "100 0 -50 0 1 10", 1)
		}},
		{"negative count", func(s string) string {
			return strings.Replace(s, "100 0 50 0 1 10", "100 0 50 0 -1 10", 1)
		}},
		{"negative weight", func(s string) string {
			return strings.Replace(s, "100 0 50 0 1 10", "100 0 50 0 1 -10", 1)
		}},
		{"uint32 item overflow", func(s string) string {
			return strings.Replace(s, "100 0 50 0 1 10", "4294967296 0 50 0 1 10", 1)
		}},
		{"uint32 weight overflow", func(s string) string {
			return strings.Replace(s, "100 0 50 0 1 10", "100 0 50 0 1 4294967296", 1)
		}},
		{"truncated candidate row", func(s string) string {
			return strings.Replace(s, "100 0 50 0 1 10\n", "100 0 50 0 1\n", 1)
		}},
		{"unknown subsection", func(s string) string {
			return strings.Replace(s, "[cash item]\n14 2\n110", "[cash mystery]\n14 2\n110", 1)
		}},
		{"bad dungeon close", func(s string) string {
			return strings.Replace(s, "[/dungeon item]", "[/wrong close]", 1)
		}},
		{"bad event width", func(s string) string {
			return strings.Replace(s, "1 10 0 150000 0 1 2500", "1 10 0 150000 0 1", 1)
		}},
		{"tokens after end", func(s string) string {
			return s + "[unknown]\n"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cat, err := Load(catalogTestSource(test.mutate(base)))
			if err == nil || cat != nil {
				t.Fatalf("malformed catalog accepted: catalog=%+v err=%v", cat, err)
			}
			if !errors.Is(err, ErrInvalidPVF) {
				t.Fatalf("error does not identify invalid PVF: %v", err)
			}
		})
	}
	if cat, err := Load(nil); err == nil || cat != nil || !errors.Is(err, ErrSourceRequired) {
		t.Fatalf("nil source result catalog=%+v err=%v", cat, err)
	}
}

func candidateBudgetCatalog(t *testing.T, count int) string {
	t.Helper()
	var rows strings.Builder
	for i := 0; i < count; i++ {
		fmt.Fprintf(&rows, "%d 0 50 0 1 10\n", 100+i)
	}
	old := "14 2\n100 0 50 0 1 10\n101 1 75 2 3 20\n102 0 125 0 2 30\n"
	replacement := "14 0\n" + rows.String()
	malformed := strings.Replace(syntheticCatalogText(), old, replacement, 1)
	if malformed == syntheticCatalogText() {
		t.Fatal("candidate-budget fixture replacement did not apply")
	}
	return malformed
}

func TestCandidateBudgetBoundary(t *testing.T) {
	boundary, err := Load(catalogTestSource(candidateBudgetCatalog(t, 4096)))
	if err != nil {
		t.Fatal(err)
	}
	pool, _, err := boundary.Pools(1002, 0, 89)
	if err != nil || len(pool.Candidates) != 4096 {
		t.Fatalf("4096-candidate pool: rows=%d err=%v", len(pool.Candidates), err)
	}

	cat, err := Load(catalogTestSource(candidateBudgetCatalog(t, 4097)))
	if err == nil || cat != nil || !errors.Is(err, ErrInvalidPVF) {
		t.Fatalf("oversized pool accepted: catalog=%+v err=%v", cat, err)
	}
}

func TestRealPVFSecretShopSmoke(t *testing.T) {
	path := strings.TrimSpace(os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE"))
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the paired Script.pvf")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	cat, err := Load(archive)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cat.SupportedNPCIDs(), []uint32{1002, 1003, 1004}; !reflect.DeepEqual(got, want) {
		t.Fatalf("supported NPCs %v, want %v", got, want)
	}

	weights, err := cat.NPCWeights(7146, 89)
	if err != nil || len(weights) != 4 {
		t.Fatalf("dungeon 7146 weights=%+v err=%v", weights, err)
	}
	for column := 0; column < 5; column++ {
		total := uint32(0)
		for _, row := range weights {
			total += row.Weights[column]
		}
		if total != 10000 {
			t.Fatalf("dungeon 7146 weight column %d total %d, want 10000", column, total)
		}
	}
	pairs, err := cat.WeightPairs(7146, 89, 0)
	if err != nil {
		t.Fatal(err)
	}
	total := uint32(0)
	for _, pair := range pairs {
		total += pair.Weight
	}
	if total != 10000 {
		t.Fatalf("dungeon 7146 selected-column total %d, want 10000", total)
	}

	wantDungeonCounts := map[uint32]int{1002: 2, 1003: 2, 1004: 1}
	wantDungeonRows := map[uint32][2]int{1002: {37, 37}, 1003: {11, 11}, 1004: {12, 13}}
	wantDungeonFirst := map[uint32][2]Candidate{
		1002: {{35037, 0, 151500, 0, 1, 5}, {35037, 0, 151500, 0, 1, 5}},
		1003: {{3033, 0, 100, 0, 100, 100}, {3033, 0, 100, 0, 100, 100}},
		1004: {{10089406, 0, 50000, 0, 1, 10}, {10089406, 0, 50000, 0, 1, 10}},
	}
	wantLevelFirst := map[uint32]Candidate{
		1002: {101000543, 0, 3361500, 0, 1, 5},
		1003: {3033, 0, 100, 0, 100, 100},
		1004: {10088490, 0, 300000, 0, 1, 10},
	}
	for _, npcID := range []uint32{1002, 1003, 1004} {
		normal, extra, err := cat.Pools(npcID, 7146, 89)
		if err != nil {
			t.Fatalf("NPC %d dungeon pool: %v", npcID, err)
		}
		if normal.SelectionCount != wantDungeonCounts[npcID] || extra.SelectionCount != wantDungeonCounts[npcID] {
			t.Fatalf("NPC %d dungeon selection counts normal=%d extra=%d", npcID, normal.SelectionCount, extra.SelectionCount)
		}
		if len(normal.Candidates) != wantDungeonRows[npcID][0] || len(extra.Candidates) != wantDungeonRows[npcID][1] {
			t.Fatalf("NPC %d dungeon candidate rows normal=%d extra=%d", npcID, len(normal.Candidates), len(extra.Candidates))
		}
		if normal.Candidates[0] != wantDungeonFirst[npcID][0] || extra.Candidates[0] != wantDungeonFirst[npcID][1] {
			t.Fatalf("NPC %d dungeon first candidates normal=%+v extra=%+v", npcID, normal.Candidates[0], extra.Candidates[0])
		}

		normal, extra, err = cat.Pools(npcID, 0, 89)
		if err != nil {
			t.Fatalf("NPC %d level-88..90 fallback pool: %v", npcID, err)
		}
		if normal.SelectionCount != wantDungeonCounts[npcID] || extra.SelectionCount != wantDungeonCounts[npcID] {
			t.Fatalf("NPC %d level selection counts normal=%d extra=%d", npcID, normal.SelectionCount, extra.SelectionCount)
		}
		if len(normal.Candidates) == 0 || len(extra.Candidates) == 0 || normal.Candidates[0] != wantLevelFirst[npcID] || extra.Candidates[0] != wantLevelFirst[npcID] {
			t.Fatalf("NPC %d level candidates normal=%+v extra=%+v", npcID, normal.Candidates, extra.Candidates)
		}
	}
}

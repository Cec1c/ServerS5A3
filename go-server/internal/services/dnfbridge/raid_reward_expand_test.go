package dnfbridge

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

// Expected results from the immutable PVF-20261027-005 audit, independently
// decoded before the direct-grant implementation. Zero picks the first entry.
var privateRaidZeroContents = map[uint32]map[uint32]uint32{
	10094733:  {10093974: 2},
	10157783:  {10095702: 1},
	10159819:  {10159143: 5},
	10159820:  {10159143: 5},
	10160380:  {10159143: 2, 10159145: 1, 10159623: 8},
	10160381:  {10159143: 2, 10159623: 8, 109040306: 1},
	10160382:  {10159143: 2, 10159623: 8, 10159697: 1},
	10160384:  {10159143: 2, 10159184: 1, 10159623: 8},
	10161938:  {10159143: 2, 10159623: 8},
	10161939:  {10159143: 2, 10159623: 9},
	10161940:  {10159143: 2, 10159623: 10},
	10161941:  {10159143: 2, 10159623: 20},
	10303046:  {10159145: 1, 10159623: 2},
	10303047:  {10159623: 2, 109040306: 1},
	10303048:  {10159623: 2, 10159697: 1},
	10303049:  {10159184: 1, 10159623: 2},
	10303050:  {10159143: 5, 10159623: 2},
	10304474:  {10304529: 30},
	10304475:  {10304529: 45},
	10304476:  {10304529: 60},
	10304477:  {10304529: 80},
	10304478:  {10304529: 100},
	10304479:  {10093974: 19},
	10304480:  {10093974: 52},
	10304481:  {10093974: 11},
	10304483:  {10093974: 20, 100050518: 1},
	10304484:  {10093974: 20, 10094769: 1},
	10304485:  {10093974: 33, 10304508: 1},
	10304486:  {10093974: 33, 100300212: 1},
	10304487:  {10093974: 54, 10095701: 1},
	10304489:  {10093974: 25, 100050518: 1},
	10304490:  {10093974: 25, 10094769: 1},
	10304491:  {10093974: 15, 10304508: 1},
	10304492:  {10093974: 15, 100300212: 1},
	10304493:  {10093974: 25, 10095701: 1},
	10304671:  {10093974: 25},
	10304817:  {10158124: 1},
	490008035: {10158124: 1},
	490008037: {10304529: 100},
	490008038: {490007849: 3, 490007850: 3},
	490008039: {490007849: 3, 490007850: 3, 490007854: 1},
	490008040: {490007849: 3, 490007850: 3, 490008673: 1},
	490008672: {490007849: 3, 490007850: 10},
	490008818: {400340204: 1, 490007849: 3, 490007850: 3},
}

func TestPrivateRaidExpansionIndependentCopiesAndFailures(t *testing.T) {
	makeCatalog := func(root, child string) *pvfDungeonDropCatalog {
		t.Helper()
		source := mustCurrentBoosterTestCatalog(t).source.(dungeonDropCatalogTestSource)
		source["stackable/cash/random.stk"] = root
		source["stackable/cash/artifact_box.stk"] = child
		catalog, err := newPVFDungeonDropCatalog(source)
		if err != nil {
			t.Fatal(err)
		}
		return catalog
	}
	box := func(rows string) string {
		return "[stackable type]\n`[booster]` 0\n[booster info]\n[etc]\n1 " + rows + "\n[/etc]\n[/booster info]\n"
	}
	now := time.Now()
	c := makeCatalog(box("502 1 2"), box("600 1 3 601 1 4"))
	draws := []int64{0, 0, 1}
	calls := 0
	rewards, err := resolveCurrentRaidRewardContents(c, 500, func(n int64) (int64, error) {
		if calls >= len(draws) {
			t.Fatal("extra draw")
		}
		v := draws[calls]
		calls++
		return v, nil
	}, now)
	if err != nil || calls != 3 {
		t.Fatal(calls, err)
	}
	privateRaidAssertContents(t, rewards, map[uint32]uint32{600: 3, 601: 4})
	// Each malformed or failed path must refuse the whole plan, not give its ID.
	for _, tc := range []struct{ name, root, child string }{
		{"cycle", box("502 1 1"), box("500 1 1")},
		{"missing_leaf", box("502 1 1"), box("999999 1 1")},
		{"paid_wrapper", box("600 1 1") + "[need material]\n601 2\n", box("600 1 1")},
		{"rare_invalid_leaf", box("600 999 1 502 1 1"), box("999999 1 1")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			catalog := makeCatalog(tc.root, tc.child)
			if err := validateCurrentRaidRewardContents(catalog, []uint32{500}, now); err == nil {
				t.Fatal("invalid rare dependency accepted")
			}
			if tc.name != "rare_invalid_leaf" {
				if _, err := resolveCurrentRaidRewardContents(catalog, 500, zeroAntonRoll, now); err == nil {
					t.Fatal("invalid wrapper granted")
				}
			}
		})
	}
	for _, bad := range []func(int64) (int64, error){
		func(int64) (int64, error) { return -1, nil },
		func(n int64) (int64, error) { return n, nil },
		func(int64) (int64, error) { return 0, errors.New("entropy unavailable") },
	} {
		if _, err := resolveCurrentRaidRewardContents(c, 500, bad, now); err == nil {
			t.Fatal("bad RNG accepted")
		}
	}
	overflow := makeCatalog(box("502 1 2"), box("600 1 40000"))
	if _, err := resolveCurrentRaidRewardContents(overflow, 500, zeroAntonRoll, now); err == nil {
		t.Fatal("wire quantity overflow accepted")
	}
}

func TestRealPVFPrivateRaidAllRandomGroupWeightBoundaries(t *testing.T) {
	catalog, _ := actualAntonRewardFixture(t)
	now := time.Now()
	checked := 0
	for id := range privateRaidZeroContents {
		box, err := resolveCurrentBoosterDefinition(catalog, id, currentBoosterRequestRandom)
		if err != nil {
			t.Fatal(err)
		}
		for target, group := range box.Random.Groups {
			var boundary int64
			for _, entry := range group.Entries {
				if entry.Weight <= 0 {
					continue
				}
				for _, edge := range []int64{boundary, boundary + entry.Weight - 1} {
					t.Run(fmt.Sprintf("%d/%d/%d", id, target, edge), func(t *testing.T) {
						// Root group draws happen before its selected child wrappers expand.
						calls := 0
						rewards, err := resolveCurrentRaidRewardContents(catalog, id, func(int64) (int64, error) {
							index := calls
							calls++
							if index == target {
								return edge, nil
							}
							return 0, nil
						}, now)
						if err != nil {
							t.Fatal(err)
						}
						want := map[uint32]uint32{}
						for gi, g := range box.Random.Groups {
							if g.DrawCount != 1 {
								t.Fatal("fixture needs explicit multi-draw oracle")
							}
							picked := g.Entries[0]
							if gi == target {
								picked = entry
							}
							for leaf, count := range privateRaidExpectedZero(uint32(picked.ItemID), uint32(picked.Count)) {
								want[leaf] += count
							}
						}
						privateRaidAssertContents(t, rewards, want)
						checked++
					})
				}
				boundary += entry.Weight
			}
		}
	}
	t.Logf("verified %d weighted boundary outcomes", checked)
}

func privateRaidExpectedZero(id, count uint32) map[uint32]uint32 {
	result := map[uint32]uint32{}
	if expected, ok := privateRaidZeroContents[id]; ok {
		for leaf, amount := range expected {
			result[leaf] = amount * count
		}
	} else {
		result[id] = count
	}
	return result
}

func privateRaidAssertContents(t *testing.T, rewards []currentBoosterReward, want map[uint32]uint32) {
	t.Helper()
	got := map[uint32]uint32{}
	for _, reward := range rewards {
		got[reward.Definition.ItemID] += reward.Count
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("direct contents=%v want=%v", got, want)
	}
}

func TestRealPVFPrivateRaidEveryRandomWrapperDirectContents(t *testing.T) {
	catalog, _ := actualAntonRewardFixture(t)
	now := time.Now()
	roots := []uint32{}
	for id, want := range privateRaidZeroContents {
		roots = append(roots, id)
		rewards, err := resolveCurrentRaidRewardContents(catalog, id, zeroAntonRoll, now)
		if err != nil {
			t.Fatalf("source %d: %v", id, err)
		}
		privateRaidAssertContents(t, rewards, want)
	}
	if err := validateCurrentRaidRewardContents(catalog, roots, now); err != nil {
		t.Fatal(err)
	}
	for _, id := range []uint32{10304508, 490007854} {
		rewards, err := resolveCurrentRaidRewardContents(catalog, id, func(int64) (int64, error) { t.Fatal("player choice was randomly opened"); return 0, nil }, now)
		if err != nil {
			t.Fatal(err)
		}
		privateRaidAssertContents(t, rewards, map[uint32]uint32{id: 1})
	}
}

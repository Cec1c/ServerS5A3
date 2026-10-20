package dnfbridge

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// TestRealPVFSAOWeaponAvatarBoxUsesAvatarRowWidth is the regression for the
// reported 阐释者礼盒 (10007317) defect. The live runtime PVF stores
//
//	[stackable type]
//	`[booster]` 0
//	[booster info]
//	[avatar]
//	1 101600024 100000 1 3 0
//	[/avatar]
//	[/booster info]
//
// i.e. one group DrawCount followed by a five-integer avatar row
// (item, weight, count, avatar category, avatar option). Decoding it with the
// three-wide reward stride turned the DrawCount into item 1 (复活币) and the
// avatar id into the weight, which is exactly the "复活币 x100000" result the
// player screenshotted. This test pins the parsed table, the rolled amount and
// the resolved reward template against the deployed Script.pvf.
func TestRealPVFSAOWeaponAvatarBoxUsesAvatarRowWidth(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to verify the live SAO weapon-avatar boxes")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	for _, sample := range []struct {
		sourceItemID uint32
		rewardItemID uint32
	}{
		{sourceItemID: 10007316, rewardItemID: 101600023},
		{sourceItemID: 10007317, rewardItemID: 101600024},
		{sourceItemID: 10007318, rewardItemID: 112600021},
	} {
		definition, err := resolveCurrentBoosterDefinition(catalog, sample.sourceItemID, currentBoosterRequestRandom)
		if err != nil {
			t.Fatalf("resolve box=%d: %v", sample.sourceItemID, err)
		}
		if definition.Random.Kind != "random" || len(definition.Random.Groups) != 1 {
			t.Fatalf("box=%d random=%+v", sample.sourceItemID, definition.Random)
		}
		group := definition.Random.Groups[0]
		if group.DrawCount != 1 || len(group.Entries) != 1 ||
			group.Entries[0].ItemID != int64(sample.rewardItemID) ||
			group.Entries[0].Weight != 100000 ||
			group.Entries[0].Count != 1 {
			t.Fatalf("box=%d group=%+v want item=%d weight=100000 count=1", sample.sourceItemID, group, sample.rewardItemID)
		}
		amounts, options, err := resolveCurrentBoosterRewardAmounts(
			definition,
			currentBoosterOpenRequest{Kind: currentBoosterRequestRandom},
			func(int64) (int64, error) { return 0, nil },
		)
		if err != nil {
			t.Fatalf("roll box=%d: %v", sample.sourceItemID, err)
		}
		if len(amounts) != 1 || amounts[sample.rewardItemID] != 1 {
			t.Fatalf("box=%d amounts=%v want {%d:1}", sample.sourceItemID, amounts, sample.rewardItemID)
		}
		if _, reviveCoin := amounts[1]; reviveCoin {
			t.Fatalf("box=%d rolled revive coin: %v", sample.sourceItemID, amounts)
		}
		rewards, err := resolveCurrentBoosterRewards(
			catalog,
			amounts,
			options,
			dnfrepo.ItemStack{Extra: map[string]string{"expire_time": "4102416000"}},
			time.Now().UTC(),
		)
		if err != nil {
			t.Fatalf("resolve reward box=%d: %v", sample.sourceItemID, err)
		}
		if len(rewards) != 1 ||
			rewards[0].Definition.ItemID != sample.rewardItemID ||
			rewards[0].Count != 1 ||
			rewards[0].Placement != currentBoosterRewardAvatar {
			t.Fatalf("box=%d rewards=%+v", sample.sourceItemID, rewards)
		}
	}
}

// TestRealPVFBoosterInfoFamiliesStillParse keeps the neighbouring shapes
// honest: the weighted [etc] tables (亚丝娜的篮子 10007393) and the selection
// family must keep resolving after the avatar row width was fixed.
func TestRealPVFBoosterInfoFamiliesStillParse(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to verify the live booster info tables")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	basket, err := resolveCurrentBoosterDefinition(catalog, 10007393, currentBoosterRequestRandom)
	if err != nil {
		t.Fatalf("resolve asuna basket: %v", err)
	}
	if len(basket.Random.Groups) != 1 || basket.Random.Groups[0].DrawCount != 1 || len(basket.Random.Groups[0].Entries) != 19 {
		t.Fatalf("basket groups=%+v", basket.Random.Groups)
	}
	weightsByItem := map[int64][2]int64{}
	for _, entry := range basket.Random.Groups[0].Entries {
		weightsByItem[entry.ItemID] = [2]int64{entry.Weight, entry.Count}
	}
	for itemID, want := range map[int64][2]int64{
		10007317: {4000, 1},
		10007338: {3000, 20},
		10007347: {3000, 1},
	} {
		if got := weightsByItem[itemID]; got != want {
			t.Fatalf("basket item=%d weight/count=%v want %v", itemID, got, want)
		}
	}
	selection, err := resolveCurrentBoosterDefinition(catalog, 10007348, currentBoosterRequestSelection)
	if err != nil {
		t.Fatalf("resolve SAO premium avatar box: %v", err)
	}
	if len(selection.Selection) != 10 || selection.Selection[0].ItemID != 101580164 || selection.Selection[0].CategoryKind != 3 {
		t.Fatalf("selection=%+v", selection.Selection)
	}
}

// TestRealPVFBoosterInfoSweepParsesEveryReachableTable is the fail-closed audit
// behind the fix: every stackable item in equipment/stackable.lst whose runtime
// PVF text declares [booster info] must either resolve into a reward group or
// be rejected through the explicit family gate. Nothing may silently decode
// into a different row layout. It walks the whole item list, so run it on
// demand:
//
//	DNFBRIDGE_REAL_PVF_SMOKE=/opt/dnf90/data/dnf/Script.pvf \
//	DNFBRIDGE_REAL_PVF_BOOSTER_SWEEP=1 go test -run TestRealPVFBoosterInfoSweepParsesEveryReachableTable ./internal/services/dnfbridge
func TestRealPVFBoosterInfoSweepParsesEveryReachableTable(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" || os.Getenv("DNFBRIDGE_REAL_PVF_BOOSTER_SWEEP") == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE and DNFBRIDGE_REAL_PVF_BOOSTER_SWEEP to audit every reachable [booster info] table")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	scanned, resolved, gated, empty := 0, 0, 0, 0
	gatedPaths := make([]string, 0, 128)
	emptyPaths := make([]string, 0, 16)
	failures := make([]string, 0, 16)
	for _, itemID := range catalog.stackableIDs {
		definition, err := catalog.ResolveItem(itemID)
		if err != nil || definition.Kind != dungeonDropItemStackable {
			continue
		}
		_, text, err := readDungeonDropText(catalog.source, "stackable", definition.PVFPath)
		if err != nil || !strings.Contains(text, "[booster info]") {
			continue
		}
		scanned++
		resolution, err := resolveCurrentMagicBox(catalog, catalog.source, int64(itemID))
		switch {
		case err == nil && resolution.Kind == "random" && len(resolution.Groups) > 0:
			resolved++
		case err == nil:
			// Every group was an empty placeholder, so the open is refused
			// without granting anything: fail closed, not a fabrication.
			empty++
			if len(emptyPaths) < 16 {
				emptyPaths = append(emptyPaths, definition.PVFPath)
			}
		case errors.Is(err, errMagicBoxBoosterInfoGated):
			gated++
			if len(gatedPaths) < 128 {
				gatedPaths = append(gatedPaths, definition.PVFPath)
			}
			t.Logf("gated-box item=%d name=%q path=%s", itemID, boosterInfoDocumentName(text), definition.PVFPath)
		default:
			failures = append(failures, definition.PVFPath+" item="+strconv.FormatInt(int64(itemID), 10)+" "+err.Error())
		}
	}
	t.Logf("booster-info sweep: scanned=%d resolved=%d gated=%d empty=%d failures=%d",
		scanned, resolved, gated, empty, len(failures))
	if gated > 0 {
		t.Logf("job/level-gated [charactor] tables rejected (fail closed), first paths=%v", gatedPaths)
	}
	if empty > 0 {
		t.Logf("placeholder-only tables that grant nothing, first paths=%v", emptyPaths)
	}
	if len(failures) > 0 {
		t.Fatalf("unprovable [booster info] tables: %v", failures)
	}
	if scanned == 0 || resolved == 0 {
		t.Fatalf("sweep found nothing to verify: scanned=%d resolved=%d", scanned, resolved)
	}
}

// boosterInfoDocumentName reads the [name] label of one runtime PVF document so
// the sweep can report which boxes the job/level gate rejects.
func boosterInfoDocumentName(text string) string {
	document, err := dnfpvf.Parse("booster-info-sweep", text)
	if err != nil {
		return ""
	}
	values := document.Texts("name")
	if len(values) == 0 {
		return ""
	}
	return strings.TrimSpace(values[0])
}

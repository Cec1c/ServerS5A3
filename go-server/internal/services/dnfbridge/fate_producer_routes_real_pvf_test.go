package dnfbridge

import (
	"os"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

const fateProducerItemID uint32 = 10304529

// This pins the current PVF's eleven Fate producer documents to the same
// production parsers their owning features use. It proves document -> resolver
// routing and the configured grant amount. Source consumption, Credit commit,
// weekly counters and handler ACKs remain covered by the feature-specific
// owner tests listed in the handoff report.
func TestRealPVFFateProducerDocumentsUseTheirProductionResolvers(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to verify the current Fate producer routes")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := archive.Snapshot().Checksum; got != fateIngressPVFSHA256 {
		t.Fatalf("PVF checksum=%s want=%s", got, fateIngressPVFSHA256)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		itemID        uint32
		count         uint32
		stackableType string
	}{
		{10162015, 30, "[booster]"},
		{10162016, 45, "[booster]"},
		{10303189, 60, "[booster]"},
		{10304474, 30, "[upgradable legacy]"},
		{10304475, 45, "[upgradable legacy]"},
		{10304476, 60, "[upgradable legacy]"},
		{10304477, 80, "[upgradable legacy]"},
		{10304478, 100, "[upgradable legacy]"},
		{10304669, 80, "[booster]"},
		{10304674, 100, "[booster]"},
		{490008037, 100, "[booster]"},
	}
	for _, test := range want {
		itemID, wantCount := test.itemID, test.count
		definition, err := catalog.ResolveItem(itemID)
		if err != nil {
			t.Fatalf("resolve producer %d: %v", itemID, err)
		}
		document, err := parseDungeonCardPVFDocument(archive, definition.PVFPath)
		if err != nil {
			t.Fatalf("read producer %d path=%s: %v", itemID, definition.PVFPath, err)
		}
		stackableType := normalizeDungeonDropStackableType(magicBoxDocumentText(document, "stackable type"))
		if stackableType != test.stackableType {
			t.Fatalf("producer %d stackable type=%q want=%q", itemID, stackableType, test.stackableType)
		}
		granted := uint64(0)
		var route string
		var groups []alignedcmd.MagicBoxRewardGroup
		switch stackableType {
		case "[booster]":
			booster, resolveErr := resolveCurrentBoosterDefinition(catalog, itemID, currentBoosterRequestRandom)
			if resolveErr != nil {
				t.Fatalf("booster owner resolver rejected producer %d: %v", itemID, resolveErr)
			}
			if booster.Random.Kind != "random" || len(booster.Random.Groups) == 0 {
				t.Fatalf("booster owner resolver produced no random groups for %d: %+v", itemID, booster.Random)
			}
			route, groups = "resolveCurrentBoosterDefinition", booster.Random.Groups
		case "[upgradable legacy]":
			if !currentLotterySupportedType(stackableType) {
				t.Fatalf("upgradable legacy producer %d is not accepted by the op27 lottery route", itemID)
			}
			if _, _, goldErr := currentLotteryOpeningGold(document); goldErr != nil {
				t.Fatalf("op27 lottery price parser rejected producer %d: %v", itemID, goldErr)
			}
			booster, resolveErr := resolveCurrentBoosterDefinition(catalog, itemID, currentBoosterRequestRandom)
			if resolveErr != nil {
				t.Fatalf("op27 lottery booster resolver rejected producer %d: %v", itemID, resolveErr)
			}
			if booster.Random.Kind != "random" || len(booster.Random.Groups) == 0 {
				t.Fatalf("op27 lottery resolver produced no random groups for %d: %+v", itemID, booster.Random)
			}
			route, groups = "handleCurrentLotteryItem->resolveCurrentBoosterDefinition", booster.Random.Groups
		default:
			t.Fatalf("producer %d has no asserted production route for type=%q", itemID, stackableType)
		}
		for _, group := range groups {
			for _, entry := range group.Entries {
				if entry.ItemID == int64(fateProducerItemID) {
					if entry.Count < 0 {
						t.Fatalf("producer %d has negative Fate count", itemID)
					}
					granted += uint64(entry.Count)
				}
			}
		}
		if granted != uint64(wantCount) {
			t.Fatalf("producer %d route=%s Fate count=%d want=%d", itemID, route, granted, wantCount)
		}
		t.Logf("FATE_PRODUCER id=%d route=%s type=%q count=%d", itemID, route, stackableType, granted)
	}
}

package dnfbridge

import (
	"context"
	"errors"
	"os"
	"testing"

	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFRiftPassPreflightAndTransactionalDailyEntry(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	table, err := worldmap.LoadSource(context.Background(), a, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := worldmap.NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}
	dungeon, found := table.FindDungeon(316)
	if !found {
		t.Fatal("316 missing")
	}
	area, found := table.FindAreaByDungeon(316)
	if !found || len(area.ParticularHellFreePassItems) != 45 || len(area.InfernoFreePassItems) != 24 {
		t.Fatalf("real pass table counts: particular=%d inferno=%d found=%t", len(area.ParticularHellFreePassItems), len(area.InfernoFreePassItems), found)
	}
	repos := dnfrepomemory.NewMemoryGroup()
	ctx := context.Background()
	c := dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "test-account", Level: 90, Stats: map[string]int64{"fatigue": 100}}
	if err := repos.Character.Save(ctx, c); err != nil {
		t.Fatal(err)
	}
	s := &Service{options: options{currentClientVersion: "official-24.9", pvfPath: path, gameUpperBodyCodec: gameUpperBodyCodecPlain}, worldMapTable: table, worldMapResolver: resolver, initialEquipmentArchive: a, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
	session := &gameSession{conn: &bufferConn{}, selectedCharacterID: 19, accountID: "test-account"}
	// Only an ordinary-hell pass is insufficient for the particular-hell entry.
	bag := dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{"0:21": {ItemID: 490010190, Count: 3}}}
	if err := repos.Inventory.Save(ctx, bag); err != nil {
		t.Fatal(err)
	}
	if err := s.validateCurrentRiftEntry(ctx, c, dungeon, session); !errors.Is(err, dnfdungeon.ErrEntryRequiredItemMissing) {
		t.Fatalf("ordinary pass accepted by 316: %v", err)
	}
	bag.Slots["0:21"] = dnfrepo.ItemStack{ItemID: 490005479, Count: 3}
	bag.Slots["0:20"] = dnfrepo.ItemStack{ItemID: 10158964, Count: 30}
	if err := repos.Inventory.Save(ctx, bag); err != nil {
		t.Fatal(err)
	}
	for i := int64(1); i <= 3; i++ {
		c, _, _ = repos.Character.Load(ctx, "19")
		if err := s.validateCurrentRiftEntry(ctx, c, dungeon, session); err != nil {
			t.Fatalf("pass preflight %d: %v", i, err)
		}
		runtime := &runtimeDungeonState{Dungeon: dungeon}
		if err := s.consumeCurrentRiftEntry(session, runtime); err != nil {
			t.Fatalf("pass commit %d: %v", i, err)
		}
		used, err := dnfdungeon.DailyEntryUsed(runtime.Character, 316, s.gameplayNow())
		if err != nil || used != i {
			t.Fatalf("used=%d err=%v", used, err)
		}
		bag, _, _ = repos.Inventory.Load(ctx, "19")
		if bag.Slots["0:21"].Count != 3-i || bag.Slots["0:20"].Count != 30 {
			t.Fatalf("pass scaled/stone consumed: %+v", bag)
		}
	}
	c, _, _ = repos.Character.Load(ctx, "19")
	if err := s.validateCurrentRiftEntry(ctx, c, dungeon, session); !errors.Is(err, dnfdungeon.ErrDailyEntryLimit) {
		t.Fatalf("daily limit bypassed: %v", err)
	}
	t.Logf("real area%d 316 passes=%d; three fixed pass debits, normal stone untouched, daily counter3", area.ID, len(area.ParticularHellFreePassItems))
}

func TestInfernoFreePassDoesNotUseOrdinaryOrParticularTable(t *testing.T) {
	area := worldmap.Area{HellDungeon: worldmap.OptionalInt{Set: true, Value: 1}, HellFreePassItems: []worldmap.TicketItem{{ItemID: 10, Count: 1}}, InfernoFreePassItems: []worldmap.TicketItem{{ItemID: 20, Count: 1}}, ParticularHellFreePassItems: []worldmap.TicketItem{{ItemID: 30, Count: 1}}, ItemConditions: []int64{3330}}
	d := worldmap.Dungeon{Metadata: worldmap.DungeonMetadata{MinimumRequiredLevel: worldmap.OptionalInt{Set: true, Value: 90}, Flags: map[string]bool{"inferno dungeon": true}, IntegerValues: map[string]int64{"hell dungeon": 1, "hell party required item count": 30}}}
	alts, err := currentHellPartyTicketAlternatives(area, d)
	if err != nil || len(alts) != 2 || alts[0][0].ItemID != 20 || alts[1][0].Count != 30 {
		t.Fatalf("inferno alternatives=%+v err=%v", alts, err)
	}
	d.Metadata.Flags["inferno dungeon"] = false
	alts, err = currentHellPartyTicketAlternatives(area, d)
	if err != nil || len(alts) != 2 || alts[0][0].ItemID != 10 {
		t.Fatalf("ordinary alternatives=%+v err=%v", alts, err)
	}
}

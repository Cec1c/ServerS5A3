package dnfbridge

import (
	"context"
	"encoding/binary"
	"os"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFHellPartyGroupEntryChargesAllMembersOnce(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	table, err := worldmap.LoadSource(context.Background(), archive, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := worldmap.NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}
	dungeon, found := table.FindDungeon(310)
	if !found {
		t.Fatal("real dungeon 310 missing")
	}
	area, found := table.FindAreaByDungeon(310)
	if !found {
		t.Fatal("real hell area missing")
	}
	alternatives, err := currentHellPartyTicketAlternatives(area, dungeon)
	if err != nil {
		t.Fatal(err)
	}
	normal := alternatives[len(alternatives)-1][0]
	if normal.Count != 26 {
		t.Fatalf("real 90-level ticket count=%d", normal.Count)
	}
	t.Logf("real area=%d dungeon=%d ticket=%d count=%d alternatives=%d", area.ID, dungeon.ID, normal.ItemID, normal.Count, len(alternatives))
	for _, short := range []bool{false, true} {
		t.Run(map[bool]string{false: "both_paid_and_replay", true: "follower_short_zero_debit"}[short], func(t *testing.T) {
			ctx := context.Background()
			repos := dnfrepomemory.NewMemoryGroup()
			for _, id := range []string{"1", "6"} {
				count := int64(60)
				if short && id == "6" {
					count = normal.Count - 1
				}
				if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: id, AccountID: "account-" + id}); err != nil {
					t.Fatal(err)
				}
				if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: id, Slots: map[string]dnfrepo.ItemStack{"0:121": {ItemID: normal.ItemID, Count: count}}}); err != nil {
					t.Fatal(err)
				}
			}
			s := &Service{options: options{currentClientVersion: "official-24.9", accountID: "account-1", gameUpperBodyCodec: gameUpperBodyCodecPlain}, worldMapTable: table, worldMapResolver: resolver, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
			leader := &gameSession{conn: &bufferConn{}, selectedCharacterID: 1, accountID: "account-1"}
			follower := &gameSession{conn: &bufferConn{}, selectedCharacterID: 6, accountID: "account-6"}
			leadRuntime := &runtimeDungeonState{Dungeon: dungeon, HellRoomSet: true}
			followRuntime := &runtimeDungeonState{Dungeon: dungeon, HellRoomSet: true}
			followers := []*runtimePartyPreparedDungeonEntry{{session: follower, runtime: followRuntime}}
			err := s.consumeCurrentHellPartyGroupEntry(ctx, leader, leadRuntime, followers)
			if short {
				if err == nil || leadRuntime.hellEntryItemsCommitted || followRuntime.hellEntryItemsCommitted {
					t.Fatalf("short accepted: %v", err)
				}
				one, _, _ := repos.Inventory.Load(ctx, "1")
				if one.Slots["0:121"].Count != 60 {
					t.Fatal("leader partially debited")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = s.consumeCurrentHellPartyGroupEntry(ctx, leader, leadRuntime, followers); err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"1", "6"} {
				inv, _, _ := repos.Inventory.Load(ctx, id)
				stack := inv.Slots["0:121"]
				if stack.Count != 60-normal.Count || len(stack.RawEntry) != currentItemListEntryWireSize || binary.LittleEndian.Uint32(stack.RawEntry[6:10]) != uint32(60-normal.Count) {
					t.Fatalf("member %s count/raw mismatch: %+v", id, stack)
				}
			}
		})
	}
}

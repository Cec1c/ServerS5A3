package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"os"
	"reflect"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// A stale/repeated process request must not spend materials or a weekly use
// once the persisted target has no remaining charge capacity. This does not
// implement or assume a completion/claim request.
func TestRealPVFEpicProductionProcessCapacity(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := parseCurrentEpicProductionCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	if catalog.maxChargePoint != 1380000 || catalog.catalysts[10163130].GetPoint != 1000 {
		t.Fatalf("unexpected fixed PVF: max=%d catalyst=%+v", catalog.maxChargePoint, catalog.catalysts[10163130])
	}
	for _, profile := range []string{"official-24.9", "legacy"} {
		for _, tc := range []struct {
			name          string
			charge        int64
			firstSucceeds bool
		}{
			{"full", int64(catalog.maxChargePoint), false},
			{"overfull", int64(catalog.maxChargePoint) + 1, false},
			{"negative", -1, false},
			{"overflow", math.MaxInt64, false},
			{"last_charge_then_repeat", int64(catalog.maxChargePoint) - 1, true},
		} {
			t.Run(profile+"/"+tc.name, func(t *testing.T) {
				ctx := context.Background()
				repos := memory.NewMemoryGroup()
				character := dnfrepo.CharacterRecord{CharacterID: "2", AccountID: "process-capacity", Level: 55, Stats: map[string]int64{
					currentEpicProductionTargetStat:      108000520,
					currentEpicProductionChargeStat:      tc.charge,
					currentEpicProductionWeeklyCountStat: 2,
					currentEpicProductionWeekStartStat:   currentEpicProductionWeekStart(time.Now().UTC()),
				}}
				inventory := dnfrepo.InventoryRecord{CharacterID: "2", Slots: map[string]dnfrepo.ItemStack{
					"0:122": {ItemID: 10163130, Count: 10}, "0:127": {ItemID: 10163135, Count: 10},
				}}
				if err := repos.Character.Save(ctx, character); err != nil {
					t.Fatal(err)
				}
				if err := repos.Inventory.Save(ctx, inventory); err != nil {
					t.Fatal(err)
				}
				conn := &bufferConn{}
				s := &Service{options: options{currentClientVersion: profile, gameUpperHeader: gameUpperHeaderServer16, gameUpperBodyCodec: gameUpperBodyCodecPlain}, initialEquipmentArchive: archive, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
				session := &gameSession{conn: conn, accountID: character.AccountID, selectedCharacterID: 2}
				request := []byte{1, 0, 0, 0, 0, 0, 122, 0, 0, 0}
				binary.LittleEndian.PutUint32(request[2:6], 10163130)
				if tc.firstSucceeds {
					if err := s.handleCurrentEpicProductionProcess(session, request); err != nil {
						t.Fatal(err)
					}
					ack, _ := splitLongHengGameServerUpperPacket(t, conn.write.Bytes())
					if len(ack.Body) != 7 || ack.Body[0] != 1 || binary.LittleEndian.Uint32(ack.Body[1:5]) != catalog.maxChargePoint {
						t.Fatalf("last valid charge ACK=%x", ack.Body)
					}
					saved, _, _ := repos.Character.Load(ctx, "2")
					items, _, _ := repos.Inventory.Load(ctx, "2")
					if saved.Stats[currentEpicProductionChargeStat] != int64(catalog.maxChargePoint) || saved.Stats[currentEpicProductionWeeklyCountStat] != 3 || items.Slots["0:122"].Count != 9 || items.Slots["0:127"].Count != 9 {
						t.Fatalf("last valid charge failed: stats=%v items=%v", saved.Stats, items.Slots)
					}
					conn.write.Reset()
				}
				beforeCharacter, _, _ := repos.Character.Load(ctx, "2")
				beforeInventory, _, _ := repos.Inventory.Load(ctx, "2")
				if err := s.handleCurrentEpicProductionProcess(session, request); err != nil {
					t.Fatal(err)
				}
				ack, rest := splitLongHengGameServerUpperPacket(t, conn.write.Bytes())
				afterCharacter, _, _ := repos.Character.Load(ctx, "2")
				afterInventory, _, _ := repos.Inventory.Load(ctx, "2")
				if !reflect.DeepEqual(beforeCharacter, afterCharacter) || !reflect.DeepEqual(beforeInventory, afterInventory) {
					t.Errorf("rejected charge spent assets or changed persisted state: before=%v after=%v itemsBefore=%v itemsAfter=%v", beforeCharacter.Stats, afterCharacter.Stats, beforeInventory.Slots, afterInventory.Slots)
				}
				if ack.Header.Classification != 1 || !bytes.Equal(ack.Body, []byte{0, currentEpicProductionGenericError}) || len(rest) != 0 {
					t.Errorf("expected failure only: ACK=%x extra=%x", ack.Body, rest)
				}
			})
		}
	}
}

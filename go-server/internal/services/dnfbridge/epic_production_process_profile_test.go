package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// Drive the actual request handler with the native u16-count + 8-byte rows.
// Counts come from the committed character, never a client-provided ACK value.
// Parent integration also verifies the ACK followed by its state notification;
// the notification must not overwrite the newly committed native used count.
func TestEpicProductionProcessACKCommittedCountProfiles(t *testing.T) {
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
	if catalog.weeklyLimit != 7 || catalog.catalysts[10163130].GetPoint != 1000 || catalog.bigChanceMultiply != 2 {
		t.Fatalf("unexpected fixed PVF: limit=%d catalyst=%+v multiplier=%d", catalog.weeklyLimit, catalog.catalysts[10163130], catalog.bigChanceMultiply)
	}
	for _, profile := range []string{"", "90cn", "legacy", "unknown", "official-24.9"} {
		for _, tc := range []struct {
			name          string
			used          int64
			stale, reject bool
		}{
			{"fourth", 3, false, false}, {"last", 6, false, false},
			{"exhausted", 7, false, true}, {"new_week", 7, true, false},
		} {
			t.Run(profile+"/"+tc.name, func(t *testing.T) {
				ctx := context.Background()
				repos := memory.NewMemoryGroup()
				week := currentEpicProductionWeekStart(time.Now().UTC())
				if tc.stale {
					week -= 7 * 24 * 60 * 60
				}
				before := dnfrepo.CharacterRecord{CharacterID: "2", AccountID: "process-ack", Level: 55, Stats: map[string]int64{
					currentEpicProductionTargetStat: 108000520, currentEpicProductionChargeStat: 12345,
					currentEpicProductionWeeklyCountStat: tc.used, currentEpicProductionWeekStartStat: week,
				}}
				if err := repos.Character.Save(ctx, before); err != nil {
					t.Fatal(err)
				}
				inventory := dnfrepo.InventoryRecord{CharacterID: "2", Slots: map[string]dnfrepo.ItemStack{
					"0:122": {ItemID: 10163130, Count: 10}, "0:127": {ItemID: 10163135, Count: 10},
				}}
				if err := repos.Inventory.Save(ctx, inventory); err != nil {
					t.Fatal(err)
				}
				conn := &bufferConn{}
				s := &Service{options: options{currentClientVersion: profile, gameUpperHeader: gameUpperHeaderServer16, gameUpperBodyCodec: gameUpperBodyCodecPlain},
					initialEquipmentArchive: archive, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
				session := &gameSession{conn: conn, accountID: before.AccountID, selectedCharacterID: 2}
				request := []byte{1, 0, 0, 0, 0, 0, 122, 0, 0, 0}
				binary.LittleEndian.PutUint32(request[2:6], 10163130)
				if err := s.handleCurrentEpicProductionProcess(session, request); err != nil {
					t.Fatal(err)
				}
				ack, rest := splitLongHengGameServerUpperPacket(t, conn.write.Bytes())
				saved, found, err := repos.Character.Load(ctx, "2")
				if err != nil || !found {
					t.Fatalf("character found=%t err=%v", found, err)
				}
				afterInventory, found, err := repos.Inventory.Load(ctx, "2")
				if err != nil || !found {
					t.Fatalf("inventory found=%t err=%v", found, err)
				}
				if ack.Header.Classification != 1 {
					t.Fatalf("first packet is not ACK: %+v", ack.Header)
				}
				if profile == "official-24.9" && ack.Header.MsgID != 1417 {
					t.Fatalf("official process ACK id=%d", ack.Header.MsgID)
				}
				if tc.reject {
					if !bytes.Equal(ack.Body, []byte{0, currentEpicProductionWeeklyLimitError}) || len(rest) != 0 || !reflect.DeepEqual(saved.Stats, before.Stats) || !reflect.DeepEqual(afterInventory.Slots, inventory.Slots) {
						t.Fatalf("exhausted mutated state or emitted success: body=%x stats=%v inventory=%v", ack.Body, saved.Stats, afterInventory.Slots)
					}
					return
				}
				wantUsed := tc.used + 1
				if tc.stale {
					wantUsed = 1
				}
				if saved.Stats[currentEpicProductionWeeklyCountStat] != wantUsed {
					t.Fatalf("committed count=%v want=%d", saved.Stats, wantUsed)
				}
				charge := saved.Stats[currentEpicProductionChargeStat]
				gain := charge - before.Stats[currentEpicProductionChargeStat]
				if gain != 1000 && gain != 2000 {
					t.Fatalf("unexpected charge gain=%d", gain)
				}
				var big byte
				if gain == 2000 {
					big = 1
				}
				want := []byte{1, 0, 0, 0, 0, big, 0}
				binary.LittleEndian.PutUint32(want[1:5], uint32(charge))
				if profile == "official-24.9" {
					want[5] = byte(wantUsed)
					want[6] = big
				}
				if !bytes.Equal(ack.Body, want) {
					t.Fatalf("ACK=%x want=%x persisted count=%d big=%d", ack.Body, want, wantUsed, big)
				}
				for _, slot := range []string{"0:122", "0:127"} {
					if afterInventory.Slots[slot].Count != 9 {
						t.Fatalf("material %s=%+v", slot, afterInventory.Slots[slot])
					}
				}
				notices := 0
				for len(rest) > 0 {
					packet, tail := splitLongHengGameServerUpperPacket(t, rest)
					if packet.Header.Classification == 1 {
						t.Fatalf("extra ACK=%x", packet.Body)
					}
					if packet.Header.MsgID == 1354 || packet.Header.MsgID == 1355 {
						notices++
						wantID, wantNoticeCount := uint16(1355), byte(0)
						if profile == "official-24.9" {
							wantID, wantNoticeCount = 1354, byte(wantUsed)
						}
						if packet.Header.MsgID != wantID || len(packet.Body) != 32 || packet.Body[17] != wantNoticeCount || binary.LittleEndian.Uint32(packet.Body[12:16]) != uint32(charge) {
							t.Fatalf("post-ACK notice overwrites committed state: id=%d body=%x used=%d", packet.Header.MsgID, packet.Body, wantUsed)
						}
					}
					rest = tail
				}
				if notices != 1 {
					t.Fatalf("post-ACK notices=%d want one", notices)
				}
				// A forged trailing count is rejected before a second transaction.
				conn.write.Reset()
				if err := s.handleCurrentEpicProductionProcess(session, append(request, 99)); err != nil {
					t.Fatal(err)
				}
				failure, tail := splitLongHengGameServerUpperPacket(t, conn.write.Bytes())
				final, _, _ := repos.Character.Load(ctx, "2")
				finalInventory, _, _ := repos.Inventory.Load(ctx, "2")
				if len(tail) != 0 || !bytes.Equal(failure.Body, []byte{0, currentEpicProductionGenericError}) || !reflect.DeepEqual(final.Stats, saved.Stats) || !reflect.DeepEqual(finalInventory.Slots, afterInventory.Slots) {
					t.Fatal("forged count affected transaction")
				}
			})
		}
	}
}

// Exercise both outcome flags, rollover, and the U8 ceiling through the same
// transaction that persists the authoritative used count. Catalogs are explicit
// synthetic boundary inputs; the handler test above uses the unchanged PVF.
func TestEpicProductionProcessCountTransactionBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name               string
		profile            string
		used               int64
		limit              uint32
		big, stale, reject bool
	}{
		{"ordinary", "official-24.9", 3, 7, false, false, false},
		{"big", "official-24.9", 3, 7, true, false, false},
		{"rollover", "official-24.9", 255, 7, true, true, false},
		{"u8_last", "official-24.9", 254, 255, true, false, false},
		{"u8_overflow", "official-24.9", 255, 300, false, false, true},
		{"legacy_wide_count", "90cn", 255, 300, true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repos := memory.NewMemoryGroup()
			now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
			week := currentEpicProductionWeekStart(now)
			if tc.stale {
				week -= 7 * 24 * 60 * 60
			}
			// Use a target declared by currentEpicProductionTestCatalog; these
			// cases vary weekly-count boundaries, not target validity.
			before := dnfrepo.CharacterRecord{CharacterID: "2", AccountID: "count-boundary", Level: 55, Stats: map[string]int64{
				currentEpicProductionTargetStat: 116010124, currentEpicProductionChargeStat: 12345, currentEpicProductionWeeklyCountStat: tc.used, currentEpicProductionWeekStartStat: week}}
			if err := repos.Character.Save(ctx, before); err != nil {
				t.Fatal(err)
			}
			inventory := dnfrepo.InventoryRecord{CharacterID: "2", Slots: map[string]dnfrepo.ItemStack{"0:122": {ItemID: 10163130, Count: 10}, "0:127": {ItemID: 10163135, Count: 10}}}
			if err := repos.Inventory.Save(ctx, inventory); err != nil {
				t.Fatal(err)
			}
			catalog := currentEpicProductionTestCatalog()
			catalog.weeklyLimit = tc.limit
			if tc.big {
				catalog.bigChanceRate = catalog.bigChanceMaxRate
			}
			s := &Service{options: options{currentClientVersion: tc.profile}, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
			result, err := s.commitCurrentEpicProductionProcess(ctx, &gameSession{accountID: before.AccountID, selectedCharacterID: 2}, catalog, currentEpicProductionProcessRequest{Materials: []currentEpicProductionProcessMaterial{{ItemID: 10163130, Slot: 122}}}, now)
			saved, _, _ := repos.Character.Load(ctx, "2")
			inv, _, _ := repos.Inventory.Load(ctx, "2")
			if tc.reject {
				if !errors.Is(err, errCurrentEpicProductionWeeklyLimit) || !reflect.DeepEqual(saved.Stats, before.Stats) || !reflect.DeepEqual(inv.Slots, inventory.Slots) {
					t.Fatalf("overflow err=%v stats=%v inv=%v", err, saved.Stats, inv.Slots)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			wantCount := tc.used + 1
			if tc.stale {
				wantCount = 1
			}
			if result.WeeklyCount != uint32(wantCount) || saved.Stats[currentEpicProductionWeeklyCountStat] != wantCount || result.BigSuccess != tc.big {
				t.Fatalf("result=%+v stats=%v", result, saved.Stats)
			}
			var flag byte
			if tc.big {
				flag = 1
			}
			want := []byte{0, 0, 0, 0, flag, 0}
			binary.LittleEndian.PutUint32(want[:4], uint32(saved.Stats[currentEpicProductionChargeStat]))
			if tc.profile == "official-24.9" {
				want[4], want[5] = byte(wantCount), flag
			}
			if got := s.buildCurrentEpicProductionProcessSuccessBody(result); !bytes.Equal(got, want) {
				t.Fatalf("transaction ACK=%x want=%x", got, want)
			}
		})
	}
}

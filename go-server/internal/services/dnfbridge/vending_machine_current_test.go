package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"longheng.io/server/internal/modules/dnf/premium"
	doc "longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	archive "longheng.io/server/internal/platform/pvf"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// Exercise the live bridge path using actual PVF outcomes and an isolated UOW.
func TestRealPVFCurrentBlackDiamondVendingAllOutcomes(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires current PVF")
	}
	a, err := archive.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{options: options{gameUpperBodyCodec: gameUpperBodyCodecPlain, currentClientVersion: "official-24.9"}, initialEquipmentArchive: a}
	catalog, err := service.currentPremiumCatalog()
	if err != nil {
		t.Fatal(err)
	}
	contracts := currentPremiumInventoryContracts(catalog)
	for _, machine := range []struct {
		id   uint32
		path string
		coin int64
	}{{1, "etc/pcroom.vm", 7454}, {2, "etc/pcroom3.vm", 10005506}, {3, "etc/pcroom4.vm", 2749933}, {6, "etc/pcroom5.vm", 7454}} {
		text, err := a.ReadText(machine.path)
		if err != nil {
			t.Fatal(err)
		}
		d, err := doc.Parse(machine.path, text)
		if err != nil {
			t.Fatal(err)
		}
		_, rows, ok := parseVendingGroup(d, 1, machine.coin)
		if !ok {
			t.Fatal("missing PVF group")
		}
		offset := 0
		for index, row := range rows {
			draw := offset
			offset += int(row.Weight)
			t.Run(fmt.Sprintf("%d-%d-%d", machine.coin, index, row.ID), func(t *testing.T) {
				repos := memory.NewMemoryGroup()
				ctx := context.Background()
				must := func(err error) {
					t.Helper()
					if err != nil {
						t.Fatal(err)
					}
				}
				started := time.Now().Unix()
				initialExpiry := started + 3600
				must(repos.Account.Save(ctx, repo.AccountRecord{AccountID: "vending-native-fixture", Metadata: map[string]string{premium.MetadataKey(premium.TypeBlackDiamond): strconv.FormatInt(initialExpiry, 10)}}))
				must(repos.Character.Save(ctx, repo.CharacterRecord{CharacterID: "77", AccountID: "vending-native-fixture", Level: 90, Stats: map[string]int64{"gold": 7}}))
				// Some real legacy currencies have a historical PVF deadline.
				// This isolated grant fixture models a valid instance deadline;
				// the separate expiry tests cover absence and expired instances.
				must(repos.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "77", Slots: map[string]repo.ItemStack{"0:121": {ItemID: machine.coin, Count: 2, ExpireAt: time.Unix(initialExpiry, 0)}}}))
				service.repositoryProvider = func() (repo.Group, bool) { return repos, true }
				service.dungeonChoice = func(limit int) (int, error) { return draw, nil }
				conn := &bufferConn{}
				session := &gameSession{conn: conn, selectedCharacterID: 77, accountID: "vending-native-fixture"}
				request := make([]byte, 10)
				binary.LittleEndian.PutUint32(request, machine.id)
				binary.LittleEndian.PutUint32(request[4:], 1)
				binary.LittleEndian.PutUint16(request[8:], 121)
				must(service.handleCurrentVendingMachine(session, request))
				packets := splitAllGameServerUpperPackets(t, conn.write.Bytes())
				if len(packets) < 1 || packets[0].Header.MsgID != 215 || len(packets[0].Body) != 25 || packets[0].Body[0] != 1 {
					t.Fatalf("vending outcome failed: packets=%+v", packets)
				}
				inventory, found, err := repos.Inventory.Load(ctx, "77")
				must(err)
				if !found || inventory.Slots["0:121"].Count != 1 {
					t.Fatal("coin debit not committed")
				}
				var count int64
				for _, stack := range inventory.Slots {
					if stack.ItemID == row.ID {
						count += stack.Count
					}
				}
				if contract, automatic := contracts[row.ID]; automatic {
					if count != 0 {
						t.Fatal("PVF premium contract was not automatically consumed")
					}
					account, found, err := repos.Account.Load(ctx, "vending-native-fixture")
					must(err)
					if !found {
						t.Fatal("premium account disappeared")
					}
					base := started
					if contract.PremiumType == premium.TypeBlackDiamond {
						base = initialExpiry
					}
					want := base + contract.DurationSeconds*row.Count
					got := premium.ExpireAt(account, contract.PremiumType)
					if got < want || got > want+5 {
						t.Fatalf("PVF contract %d expiry=%d want=%d", row.ID, got, want)
					}
					result, err := service.activateCurrentPremiumInventoryContracts(ctx, session)
					must(err)
					if result.Changed() {
						t.Fatal("repeat projection activated the same reward twice")
					}
				} else if count != row.Count {
					t.Fatalf("PVF reward %d count=%d want=%d inventory=%+v", row.ID, count, row.Count, inventory.Slots)
				}
				if dir := os.Getenv("DNF_VENDING_NATIVE_EXPORT"); dir != "" {
					must(os.MkdirAll(dir, 0755))
					must(os.WriteFile(filepath.Join(dir, fmt.Sprintf("machine%d-%d-%d.bin", machine.id, machine.coin, index)), packets[0].Body, 0644))
				}
			})
		}
	}
}

func TestRealPVFVendingRejectsWrongMachineMaterialWithoutAssetChange(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires current PVF")
	}
	a, err := archive.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		machine   uint32
		coin      int64
		drawCount uint32
	}{
		{3, 7454, 1}, {2, 2749933, 1}, {99, 2749933, 1}, {3, 2749933, 2},
	} {
		t.Run(fmt.Sprintf("machine%d-coin%d-draw%d", tc.machine, tc.coin, tc.drawCount), func(t *testing.T) {
			repos := memory.NewMemoryGroup()
			ctx := context.Background()
			if err := repos.Account.Save(ctx, repo.AccountRecord{AccountID: "vending-reject", Metadata: map[string]string{premium.MetadataKey(premium.TypeBlackDiamond): strconv.FormatInt(time.Now().Unix()+3600, 10)}}); err != nil {
				t.Fatal(err)
			}
			if err := repos.Character.Save(ctx, repo.CharacterRecord{CharacterID: "77", AccountID: "vending-reject", Stats: map[string]int64{"gold": 9}}); err != nil {
				t.Fatal(err)
			}
			if err := repos.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "77", Slots: map[string]repo.ItemStack{"0:140": {ItemID: tc.coin, Count: 3}}}); err != nil {
				t.Fatal(err)
			}
			s := &Service{options: options{gameUpperBodyCodec: gameUpperBodyCodecPlain, currentClientVersion: "official-24.9"}, initialEquipmentArchive: a}
			s.repositoryProvider = func() (repo.Group, bool) { return repos, true }
			conn := &bufferConn{}
			session := &gameSession{conn: conn, selectedCharacterID: 77, accountID: "vending-reject"}
			body := make([]byte, 10)
			binary.LittleEndian.PutUint32(body, tc.machine)
			binary.LittleEndian.PutUint32(body[4:], tc.drawCount)
			binary.LittleEndian.PutUint16(body[8:], 140)
			if err := s.handleCurrentVendingMachine(session, body); err != nil {
				t.Fatal(err)
			}
			packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
			if packet.Header.MsgID != 215 || len(packet.Body) < 1 || packet.Body[0] != 0 || len(rest) != 0 {
				t.Fatal("invalid vending request did not fail without item notifications")
			}
			i, found, err := repos.Inventory.Load(ctx, "77")
			if err != nil || !found || len(i.Slots) != 1 || i.Slots["0:140"].ItemID != tc.coin || i.Slots["0:140"].Count != 3 {
				t.Fatal("rejected draw mutated inventory")
			}
		})
	}
}

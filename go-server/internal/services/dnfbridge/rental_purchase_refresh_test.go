package dnfbridge

import (
	"context"
	"encoding/binary"
	"os"
	"testing"

	dnfrental "longheng.io/server/internal/modules/dnf/rental"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRentalPurchaseCurrentClientPublishesCommittedWallets(t *testing.T) {
	for _, profile := range []string{"official-24.9", "legacy"} {
		t.Run(profile, func(t *testing.T) {
			t.Setenv(currentRentalRejectAckEnv, "")
			ctx := context.Background()
			repos := dnfrepomemory.NewMemoryGroup()
			if err := repos.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: "account-1", Metadata: map[string]string{currentRentalPointMetadataKey: "3"}}); err != nil {
				t.Fatal(err)
			}
			if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "account-1", Stats: map[string]int64{"gold": 427377}}); err != nil {
				t.Fatal(err)
			}
			conn := &bufferConn{}
			s := Service{options: options{accountID: "account-1", currentClientVersion: profile}, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
			session := &gameSession{conn: conn, selectedCharacterID: 19}
			if err := s.sendCurrentRentalChargeCommitted(session, currentChargeRentalPointRequest{ChargeType: 1, Count: 1}, currentRentalAssetResult{Points: 3, Gold: 427377}); err != nil {
				t.Fatal(err)
			}
			ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
			quantity := uint32(0)
			if profile == "official-24.9" {
				quantity = 1
			}
			if ack.Header.MsgID != 1000 || ack.Header.Classification != 1 || len(ack.Body) != 13 || ack.Body[0] != 1 || binary.LittleEndian.Uint32(ack.Body[1:5]) != 1 || binary.LittleEndian.Uint32(ack.Body[5:9]) != quantity || binary.LittleEndian.Uint32(ack.Body[9:13]) != 3 {
				t.Fatalf("purchase ACK=%+v %x", ack.Header, ack.Body)
			}
			if profile == "official-24.9" {
				gold, next := splitGameServerUpperPacket(t, rest)
				rest = next
				if gold.Header.MsgID != 14 || gold.Header.Classification != 0 || len(gold.Body) != 3+currentItemListEntryWireSize || gold.Body[0] != 0 || binary.LittleEndian.Uint16(gold.Body[1:3]) != 1 || binary.LittleEndian.Uint16(gold.Body[3:5]) != 0 || binary.LittleEndian.Uint32(gold.Body[5:9]) != 0 || binary.LittleEndian.Uint32(gold.Body[9:13]) != 427377 {
					t.Fatalf("absolute gold row=%+v %x", gold.Header, gold.Body)
				}
			}
			state, rest := splitGameServerUpperPacket(t, rest)
			if state.Header.MsgID != 985 || state.Header.Classification != 0 || len(state.Body) != 8 || binary.LittleEndian.Uint32(state.Body[:4]) != 3 || len(rest) != 0 {
				t.Fatalf("rental state=%+v %x rest=%x", state.Header, state.Body, rest)
			}
			character, _, _ := repos.Character.Load(ctx, "19")
			account, _, _ := repos.Account.Load(ctx, "account-1")
			points, _ := dnfrental.Points(account)
			if character.Stats["gold"] != 427377 || points != 3 {
				t.Fatal("publication charged or credited assets a second time")
			}
		})
	}
}

func TestRealPVFRentalPurchaseCommitsOnceAndRejectsWithoutRefresh(t *testing.T) {
	path := os.Getenv("DNF_RENTAL_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNF_RENTAL_REAL_PVF_SMOKE")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	monsters, err := newPVFDungeonMonsterCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		points   uint32
		gold     int64
		succeeds bool
	}{
		{"purchase", 1, 627377, true},
		{"insufficient_gold", 1, 99999, false},
		{"point_limit", 400, 627377, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repos := dnfrepomemory.NewMemoryGroup()
			account := dnfrepo.AccountRecord{AccountID: "account-1", Metadata: map[string]string{"keep": "metadata"}}
			dnfrental.SetPoints(&account, tc.points)
			for _, err := range []error{
				repos.Account.Save(ctx, account),
				repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "account-1", Level: 90, Job: "0", Stats: map[string]int64{"gold": tc.gold, "grow_type": 2}}),
				repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{}}),
				repos.Equipment.Save(ctx, dnfrepo.EquipmentRecord{CharacterID: "19", Entries: map[string]dnfrepo.EquipmentEntry{}}),
			} {
				if err != nil {
					t.Fatal(err)
				}
			}
			conn := &bufferConn{}
			s := Service{options: options{accountID: "account-1", currentClientVersion: "official-24.9"}, initialEquipmentArchive: archive, dungeonMonsterTable: monsters, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
			body := make([]byte, currentRentalRequestWireSize)
			binary.LittleEndian.PutUint32(body[13:17], 1)
			binary.LittleEndian.PutUint32(body[17:21], 1)
			if err := s.handleCurrentChargeRentalPoint(&gameSession{conn: conn, selectedCharacterID: 19}, body); err != nil {
				t.Fatal(err)
			}
			storedAccount, _, _ := repos.Account.Load(ctx, "account-1")
			storedCharacter, _, _ := repos.Character.Load(ctx, "19")
			points, _ := dnfrental.Points(storedAccount)
			wantPoints, wantGold := tc.points, tc.gold
			if tc.succeeds {
				wantPoints++
				wantGold -= 100000
			}
			if points != wantPoints || storedCharacter.Stats["gold"] != wantGold || storedCharacter.Stats["grow_type"] != 2 || storedAccount.Metadata["keep"] != "metadata" {
				t.Fatalf("wallet/owner changed incorrectly: points=%d char=%+v account=%+v", points, storedCharacter, storedAccount)
			}
			packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
			if packet.Header.MsgID != 1000 || packet.Header.Classification != 1 || packet.Body[0] != byte(map[bool]int{true: 1, false: 0}[tc.succeeds]) {
				t.Fatalf("purchase ACK=%+v %x", packet.Header, packet.Body)
			}
			if !tc.succeeds {
				if len(rest) != 0 {
					t.Fatal("rejected purchase emitted wallet refresh")
				}
				return
			}
			gold, rest := splitGameServerUpperPacket(t, rest)
			state, rest := splitGameServerUpperPacket(t, rest)
			if gold.Header.MsgID != 14 || binary.LittleEndian.Uint32(gold.Body[9:13]) != uint32(wantGold) || state.Header.MsgID != 985 || binary.LittleEndian.Uint32(state.Body[:4]) != wantPoints || len(rest) != 0 {
				t.Fatal("committed purchase did not publish both absolute balances")
			}
		})
	}
}

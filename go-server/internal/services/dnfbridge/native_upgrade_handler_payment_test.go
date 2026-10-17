package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"reflect"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	memory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/town"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// Uses the observed target/slots/NPC trailer but a synthetic name and isolated
// memory character. It is a handler replay, never a historical/native replay.
func TestR4RealPVFJonathanHandlerPaymentReplay(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("matching real PVF required")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	towns, err := town.Load(context.Background(), archive, town.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var townID, areaID int64
	found := false
	for _, v := range towns.Towns() {
		for _, a := range v.Areas {
			if strings.Contains(strings.ToLower(a.MapPath), "centralpark_metrocenter.map") {
				townID, areaID, found = v.ID, a.ID, true
			}
		}
	}
	if !found {
		t.Fatal("Jonathan town area absent")
	}
	const account = "dnf:r4-offline-jonathan"
	for _, tc := range []struct {
		name, flag                   string
		npc                          uint16
		stones, wantStones, wantGold int64
		reject                       bool
	}{
		{"legacy_default_control", "", 476, 5, 5, 100000, false},
		{"candidate_jonathan", "candidate", 476, 5, 4, 100000, false},
		{"candidate_ordinary", "candidate", 1, 5, 5, 15025, false},
		{"candidate_missing_stone", "candidate", 476, 0, 0, 100000, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DNFBRIDGE_NATIVE_UPGRADE_COST", tc.flag)
			ctx := context.Background()
			repos := memory.NewMemoryGroup()
			character := dnfrepo.CharacterRecord{CharacterID: "77", AccountID: account, Level: 90, Stats: map[string]int64{"gold": 100000}}
			target := realForgingWeaponStack(28309, 45)
			binary.LittleEndian.PutUint16(target.RawEntry[:2], 54)
			target.Extra["r4_identity_marker"] = "target-28309"
			bag := dnfrepo.InventoryRecord{CharacterID: "77", Slots: map[string]dnfrepo.ItemStack{"0:54": target, "0:135": {ItemID: 10158964, Count: tc.stones}}}
			sharedKey := dnfrepo.AccountSharedInventorySlotKey(358)
			shared := dnfrepo.AccountInventoryRecord{AccountID: account, Slots: map[string]dnfrepo.ItemStack{sharedKey: {ItemID: 3037, Count: 1000}}}
			if err := repos.Character.Save(ctx, character); err != nil {
				t.Fatal(err)
			}
			if err := repos.Inventory.Save(ctx, bag); err != nil {
				t.Fatal(err)
			}
			if err := repos.AccountInventory.Save(ctx, shared); err != nil {
				t.Fatal(err)
			}
			beforeChar, _, _ := repos.Character.Load(ctx, "77")
			beforeBag, _, _ := repos.Inventory.Load(ctx, "77")
			beforeShared, _, _ := repos.AccountInventory.Load(ctx, account)
			conn := &bufferConn{}
			session := &gameSession{conn: conn, connID: "r4-offline-jonathan", accountID: account, channel: channelcatalog.Channel{ID: 38}, selectedCharacterID: 77, townSceneReadyCharacterID: 77, townPositionSnapshot: currentTownPositionSnapshot{CharacterID: 77, TownID: byte(townID), AreaID: byte(areaID), PositionValid: true}}
			s := &Service{options: options{accountID: account, gameUpperBodyCodec: gameUpperBodyCodecPlain, currentClientVersion: "official-24.9", pvfPath: path}, initialEquipmentArchive: archive, townCatalog: towns, gameSessions: map[uint16]*gameSession{77: session}, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
			policy, err := s.alignedUpgradePolicyResolver()("reinforce", 0)
			if err != nil {
				t.Fatal(err)
			}
			if policy.MaterialItemID != 3037 || policy.MaterialCount <= 0 || policy.SuccessWeight != 100000 {
				t.Fatalf("unexpected pinned +0 policy: %+v", policy)
			}
			body := make([]byte, 16)
			binary.LittleEndian.PutUint16(body[2:], 54)
			binary.LittleEndian.PutUint32(body[4:], 28309)
			binary.LittleEndian.PutUint16(body[8:], 358)
			binary.LittleEndian.PutUint16(body[10:], 65535)
			name := []byte("offline-r4")
			binary.LittleEndian.PutUint32(body[12:], uint32(len(name)))
			body = append(body, name...)
			body = binary.LittleEndian.AppendUint16(body, tc.npc)
			body = binary.LittleEndian.AppendUint16(body, 135)
			handled, err := s.handleAlignedGameCommand(session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketUpgradeItem), body)
			if err != nil || !handled {
				t.Fatalf("handled=%t err=%v", handled, err)
			}
			var ack []byte
			ackIndex := -1
			index := 0
			updates := map[uint16][]byte{}
			walletSeen := false
			raw := conn.write.Bytes()
			for len(raw) > 0 {
				packet, rest := splitGameServerUpperPacket(t, raw)
				raw = rest
				if packet.Header.MsgID == uint16(dnfenum.CmdPacketUpgradeItem) && packet.Header.Classification == 1 {
					if ack != nil {
						t.Fatal("duplicate upgrade ACK")
					}
					ack = append([]byte(nil), packet.Body...)
					ackIndex = index
				}
				if packet.Header.MsgID == 14 && packet.Header.Classification == 0 {
					if ackIndex < 0 {
						t.Fatal("item projection preceded ACK")
					}
					b := packet.Body
					if len(b) < 3 || b[0] != 0 {
						t.Fatalf("bad main update %x", b)
					}
					n := int(binary.LittleEndian.Uint16(b[1:3]))
					if len(b) != 3+n*119 {
						t.Fatalf("bad update width %d rows=%d", len(b), n)
					}
					for i := 0; i < n; i++ {
						row := append([]byte(nil), b[3+i*119:3+(i+1)*119]...)
						slot := binary.LittleEndian.Uint16(row)
						updates[slot] = row
						if slot == 0 && binary.LittleEndian.Uint32(row[2:6]) == 0 {
							walletSeen = true
						}
					}
				}
				index++
			}
			if ack == nil {
				t.Fatal("no op50 ACK")
			}
			gotChar, _, _ := repos.Character.Load(ctx, "77")
			gotBag, _, _ := repos.Inventory.Load(ctx, "77")
			gotShared, _, _ := repos.AccountInventory.Load(ctx, account)
			if tc.reject {
				if !bytes.Equal(ack, []byte{0, 22}) {
					t.Fatalf("rejection ACK=%x want0016", ack)
				}
				if !reflect.DeepEqual(beforeChar, gotChar) || !reflect.DeepEqual(beforeBag, gotBag) || !reflect.DeepEqual(beforeShared, gotShared) {
					t.Fatal("rejected request changed assets")
				}
				if len(updates) != 0 {
					t.Fatal("rejected request projected success rows")
				}
				return
			}
			if len(ack) < 2 || ack[0] != 1 {
				t.Fatalf("success ACK=%x", ack)
			}
			if gotChar.Stats["gold"] != tc.wantGold || gotBag.Slots["0:135"].Count != tc.wantStones || gotShared.Slots[sharedKey].Count != 1000-int64(policy.MaterialCount) {
				t.Fatalf("settlement gold=%d stones=%d ordinary=%d policy=%+v", gotChar.Stats["gold"], gotBag.Slots["0:135"].Count, gotShared.Slots[sharedKey].Count, policy)
			}
			if gotBag.Slots["0:54"].ItemID != 28309 || gotBag.Slots["0:54"].Extra["r4_identity_marker"] != "target-28309" || gotBag.Slots["0:54"].Extra["upgrade_level"] != "1" {
				t.Fatalf("target identity/level changed incorrectly: %+v", gotBag.Slots["0:54"])
			}
			targetRow := updates[54]
			if len(targetRow) != 119 || binary.LittleEndian.Uint32(targetRow[2:6]) != 28309 {
				t.Fatal("target projection identity absent/wrong")
			}
			if tc.flag == "candidate" && tc.npc == 476 {
				stoneRow := updates[135]
				if len(stoneRow) != 119 || binary.LittleEndian.Uint32(stoneRow[2:6]) != 10158964 {
					t.Fatal("conversion material projection absent/wrong")
				}
				if int64(binary.LittleEndian.Uint32(stoneRow[6:10])) != tc.wantStones {
					t.Fatal("conversion material projection has stale quantity")
				}
			}
			normalRow := updates[358]
			if len(normalRow) != 119 || binary.LittleEndian.Uint32(normalRow[2:6]) != 3037 || int64(binary.LittleEndian.Uint32(normalRow[6:10])) != 1000-int64(policy.MaterialCount) {
				t.Fatal("ordinary material projection identity/quantity differs from committed assets")
			}
			if walletSeen != (tc.flag == "candidate" && tc.npc == 1) {
				t.Fatalf("wallet projected=%t for %s", walletSeen, tc.name)
			}
			if walletSeen && int64(binary.LittleEndian.Uint32(updates[0][6:10])) != tc.wantGold {
				t.Fatal("wallet projection has stale balance")
			}
			t.Logf("handler ACK=%x gold=%d stones=%d normal_count=%d rows=%d", ack, gotChar.Stats["gold"], gotBag.Slots["0:135"].Count, gotShared.Slots[sharedKey].Count, len(updates))
		})
	}
}

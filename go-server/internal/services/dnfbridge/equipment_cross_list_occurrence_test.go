package dnfbridge

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Real PVF records, full bridge/owner transaction, and captured server wire.
// Only read-loop scheduling is driven directly: false is game.go's next-packet
// branch, true is its deadline branch. No client/socket replay is claimed.
func TestQuietCandidateRapidDistinctAvatarUnequips(t *testing.T) {
	for _, tc := range []struct {
		name    string
		sameBag bool
		flag    byte
	}{
		{"actual-tail0-distinct-bag", false, 0},
		{"actual-tail0-same-requested-bag", true, 0},
		{"tail1-distinct-bag-outside-login-window", false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _, queue := newEquipmentTailOrderFixture(t)
			now := time.Now()
			queue.now = func() time.Time { return now }
			f.session.op19QuietResolving = false
			f.session.equipLoginWearWindowOpenedAt = time.Now().Add(-time.Minute)
			for i, endpoint := range []struct {
				bag, worn int16
				id        int32
			}{{0, 3, 40734}, {1, 1, 39524}} {
				now = now.Add(100 * time.Millisecond)
				if tc.sameBag {
					endpoint.bag = 0
				}
				if f.session.hasOp19QuietPending() {
					f.service.resolveOp19QuietPending(f.session, false)
				}
				body := loginWearBridgeBody(1, endpoint.bag, 0, 3, endpoint.worn, endpoint.id)
				body[26] = tc.flag
				handled, err := f.service.handleAlignedGameCommand(f.session, 1, 19, body)
				if err != nil || !handled {
					t.Fatalf("op19 %d handled=%v err=%v", i+1, handled, err)
				}
				t.Logf("real handler %d pending=%v occurrence=%d", i+1, f.session.hasOp19QuietPending(), f.session.equipWornSlotClaimOccurrence)
			}
			if f.session.hasOp19QuietPending() {
				f.service.resolveOp19QuietPending(f.session, true)
			}
			equipment, found, err := f.group.Equipment.Load(context.Background(), "19")
			if err != nil || !found {
				t.Fatalf("equipment found=%v err=%v", found, err)
			}
			inventory, found, err := f.group.Inventory.Load(context.Background(), "19")
			if err != nil || !found {
				t.Fatalf("inventory found=%v err=%v", found, err)
			}
			evidence := map[string]any{"equipment": equipment, "inventory": inventory, "wire_hex": hex.EncodeToString(f.buffer.write.Bytes()), "quiet_ms": op19PlayerFlagQuietWindow().Milliseconds(), "scope": "real business handler; driven exact reader resolution policy; no socket or client replay", "requested_same_bag": tc.sameBag, "body26": tc.flag}
			if dir := os.Getenv("DNF90_QUIET_ARTIFACT"); dir != "" {
				raw, e := json.MarshalIndent(evidence, "", "  ")
				if e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(filepath.Join(dir, tc.name+"-snapshot.json"), raw, 0644); e != nil {
					t.Fatal(e)
				}
			}
			if tc.sameBag {
				if len(equipment.Entries) != 1 || equipment.Entries["1"].ItemID != 39524 || inventory.Slots["1:0"].ItemID != 40734 || inventory.Slots["1:1"].ItemID != 0 {
					t.Fatal("same-list automatic burst protection changed")
				}
				return
			}
			if len(equipment.Entries) != 0 || inventory.Slots["1:0"].ItemID != 40734 || inventory.Slots["1:1"].ItemID != 39524 {
				t.Fatalf("two distinct avatar unequips did not both commit: worn=%+v bag0=%d bag1=%d", equipment.Entries, inventory.Slots["1:0"].ItemID, inventory.Slots["1:1"].ItemID)
			}
		})
	}
}

func TestQuietCandidateCrossListSameOrdinalUnequips(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		label := "avatar-then-ordinary"
		if reverse {
			label = "ordinary-then-avatar"
		}
		t.Run(label, func(t *testing.T) {
			f, _, queue := newEquipmentTailOrderFixture(t)
			weaponFixture := newLoginWearBridgeFixture(t)
			weaponRecord, _, err := weaponFixture.group.Equipment.Load(context.Background(), "19")
			if err != nil {
				t.Fatal(err)
			}
			worn, _, err := f.group.Equipment.Load(context.Background(), "19")
			if err != nil {
				t.Fatal(err)
			}
			worn.Entries["12"] = weaponRecord.Entries["12"]
			if err = f.group.Equipment.Save(context.Background(), worn); err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			queue.now = func() time.Time { return now }
			f.session.equipLoginWearWindowOpenedAt = now.Add(-time.Minute)
			f.session.op19QuietResolving = false
			bodies := [][]byte{loginWearBridgeBody(1, 0, 0, 3, 3, 40734), loginWearBridgeBody(0, 0, 0, 3, 12, loginWearBridgeWeaponItemID)}
			if reverse {
				bodies[0], bodies[1] = bodies[1], bodies[0]
			}
			for i, body := range bodies {
				now = now.Add(100 * time.Millisecond)
				handled, e := f.service.handleAlignedGameCommand(f.session, 1, 19, body)
				if e != nil || !handled {
					t.Fatalf("real op19 %d handled=%v err=%v", i+1, handled, e)
				}
				t.Logf("packet %d list=%d bag-slot=0 worn=%d occurrence=%d", i+1, body[0], body[12], f.session.equipWornSlotClaimOccurrence)
			}
			inv, _, err := f.group.Inventory.Load(context.Background(), "19")
			if err != nil {
				t.Fatal(err)
			}
			worn, _, err = f.group.Equipment.Load(context.Background(), "19")
			if err != nil {
				t.Fatal(err)
			}
			if dir := os.Getenv("DNF90_QUIET_ARTIFACT"); dir != "" {
				raw, e := json.MarshalIndent(map[string]any{"inventory": inv, "equipment": worn, "wire_hex": hex.EncodeToString(f.buffer.write.Bytes()), "gameplay_delta_ms": 100, "body26": 0, "scope": "real PVF/handler transactions; controlled gameplay clock; not client evidence"}, "", "  ")
				if e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(filepath.Join(dir, os.Getenv("DNF90_QUIET_LABEL")+"-"+label+".json"), raw, 0644); e != nil {
					t.Fatal(e)
				}
			}
			acks := 0
			positive := map[byte]uint32{}
			stream := f.buffer.write.Bytes()
			for len(stream) > 0 {
				packet, rest := splitLongHengGameServerUpperPacket(t, stream)
				stream = rest
				if packet.Header.MsgID == 19 && packet.Header.Classification == 1 {
					acks++
				}
				if packet.Header.MsgID == 14 && len(packet.Body) >= 9 {
					id := binary.LittleEndian.Uint32(packet.Body[5:9])
					if id == 40734 || id == uint32(loginWearBridgeWeaponItemID) {
						positive[packet.Body[0]] = id
					}
				}
			}
			if acks != 2 || positive[1] != 40734 || positive[0] != uint32(loginWearBridgeWeaponItemID) {
				t.Fatalf("commits lack actual ACK/op14: acks=%d positive=%v", acks, positive)
			}
			if inv.Slots["1:0"].ItemID != 40734 || inv.Slots["0:0"].ItemID != int64(loginWearBridgeWeaponItemID) || worn.Entries["3"].ItemID != 0 || worn.Entries["12"].ItemID != 0 {
				t.Fatalf("distinct inventory namespaces incorrectly coalesced: avatar-bag0=%d main-bag0=%d worn-coat=%d worn-weapon=%d", inv.Slots["1:0"].ItemID, inv.Slots["0:0"].ItemID, worn.Entries["3"].ItemID, worn.Entries["12"].ItemID)
			}
		})
	}
}

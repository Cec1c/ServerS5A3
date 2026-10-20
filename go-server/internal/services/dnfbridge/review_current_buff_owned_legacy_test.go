package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/buffswap"
	repo "longheng.io/server/internal/modules/dnf/repository"
	memory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// Explicit imported-item fixtures, not actual q7a assets or real-PVF claims.
// Shared baseline uses identical optional repository scaffolding/fixture code;
// its state/instance and all bridge production code remain frozen04 bytes.
func reviewOwnedLegacyFixture(list byte) repo.OwnedLegacyItem {
	kind, id, count, slot := "equipment", int64(101040493), int64(1), int16(54)
	if list == 1 {
		kind, id, count, slot = "avatar", 101040494, 0, 5
	}
	if list == 7 {
		kind, id, slot = "pet", 63000, 5
	}
	raw := make([]byte, 119)
	raw[0x5A] = 0xA7
	extra := map[string]string{"raw_entry_hex": hex.EncodeToString(raw), "packed_flag_byte": "4"}
	if list == 1 {
		extra["avatar_socket_data"] = "efff00000000efff00000000"
		extra["avatar_color_data"] = "a1b2c3"
	}
	if list == 7 {
		extra["value_a"] = "7"
	}
	return repo.OwnedLegacyItem{ItemUID: 7000 + int64(list), OwnerScope: "character", OwnerID: "1", CharacterID: "1", ItemKind: kind, Item: repo.LegacyInventoryItem{ListType: list, SlotIndex: slot, ItemTemplateID: id, StackCount: count, InstanceValue: 9, Durability: 37, SealFlag: 1, OptionValue: 6, Marker16: 258, PetSerialOrHandle: 123, Extra: extra}}
}
func reviewOwnedLegacyGroup(t *testing.T, rows []repo.OwnedLegacyItem) repo.Group {
	t.Helper()
	r := memory.NewMemoryGroupWithBuffLegacyItems(rows)
	ctx := context.Background()
	for _, err := range []error{
		r.Character.Save(ctx, repo.CharacterRecord{CharacterID: "1", AccountID: "dnf:10001", Job: "14", Name: "owned-fixture"}),
		r.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "1", Slots: map[string]repo.ItemStack{}}),
		r.Equipment.Save(ctx, repo.EquipmentRecord{CharacterID: "1", Entries: map[string]repo.EquipmentEntry{}}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	return r
}
func reviewOwnedLegacySettings(t *testing.T, r repo.Group, slot byte, uid int64) {
	t.Helper()
	rec, found, err := r.Settings.Load(context.Background(), buffswap.Scope("1"))
	if err != nil || !found {
		t.Fatalf("business registration missing: found=%v err=%v", found, err)
	}
	var raw struct {
		WireProfile string `json:"wire_profile"`
		Slots       map[string]struct {
			SourceKind string `json:"source_kind"`
			LegacyUID  int64  `json:"legacy_item_uid"`
			Instance   string `json:"instance"`
		} `json:"slots"`
	}
	if err := json.Unmarshal([]byte(rec.Values["state"]), &raw); err != nil {
		t.Fatal(err)
	}
	b := raw.Slots[strconv.Itoa(int(slot))]
	if raw.WireProfile != "current1426-1368" || b.SourceKind != "legacy_uid" || b.LegacyUID != uid || b.Instance != "" {
		t.Fatalf("durable true UID/source-kind missing or mixed: %s", rec.Values["state"])
	}
}
func reviewOwnedLegacySnapshot(t *testing.T, conn *bufferConn, skill uint16, slot, list byte, index uint16, bound bool) []byte {
	t.Helper()
	packets := drainUpperPackets(t, conn)
	want := []byte{byte(skill), byte(skill >> 8), 33}
	for i := byte(0); i < 33; i++ {
		row := []byte{0, i, 45, 255, 255}
		if i == slot && bound {
			row = []byte{1, i, list, byte(index), byte(index >> 8)}
		}
		want = append(want, row...)
	}
	if len(packets) < 1 || packets[len(packets)-1].msgID != 1368 || !bytes.Equal(packets[len(packets)-1].body, want) {
		t.Fatalf("actual1426/1368 legacy projection mismatch: %v", packets)
	}
	return want
}
func reviewOwnedLegacyExpectedUserInfo(item repo.LegacyInventoryItem, slot byte) []byte {
	raw := make([]byte, 119)
	raw[0x5A] = 0xA7
	binary.LittleEndian.PutUint16(raw, uint16(item.SlotIndex))
	binary.LittleEndian.PutUint32(raw[2:], uint32(item.ItemTemplateID))
	binary.LittleEndian.PutUint32(raw[6:], 1)
	raw[0xA] = 4
	binary.LittleEndian.PutUint16(raw[0xB:], 37)
	raw[0xD] = 1
	binary.LittleEndian.PutUint32(raw[0xE:], 9)
	raw[0x13] = 6
	binary.LittleEndian.PutUint16(raw[0x14:], 258)
	if item.ListType == 7 {
		binary.LittleEndian.PutUint32(raw[6:], 123)
		binary.LittleEndian.PutUint32(raw[0xE:], 7)
		binary.LittleEndian.PutUint32(raw[0x16:], 0)
		binary.LittleEndian.PutUint32(raw[0x38:], 0)
	}
	out := []byte{38, 0, 1, slot, item.ListType, byte(item.SlotIndex), byte(uint16(item.SlotIndex) >> 8)}
	out = append(out, raw...)
	if item.ListType == 1 {
		// Independent typed-avatar expectation: five 6-byte sockets = 30.
		// The fixture stays 12 bytes; the existing codec pads its other slots.
		// Generic blob readers consume the supplied length, not mandatory30.
		out = append(out, 30, 0, 0, 0)
		socket, _ := hex.DecodeString("efff00000000efff00000000")
		out = append(out, socket...)
		out = append(out, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)
		out = append(out, 3, 0, 0, 0, 0xa1, 0xb2, 0xc3)
	}
	return out
}
func reviewOwnedLegacyBusiness(t *testing.T, list byte) {
	t.Helper()
	t.Setenv(buffSwapReapplyEnv, "on")
	row := reviewOwnedLegacyFixture(list)
	r := reviewOwnedLegacyGroup(t, []repo.OwnedLegacyItem{row})
	slot := byte(12)
	if list == 1 {
		slot = 1
	}
	if list == 7 {
		slot = 26
	}
	ctx := context.Background()
	// Validate identical support independently BEFORE the business command.
	if err := repo.WithinBuffOwnedSnapshot(ctx, r, "1", "dnf:10001", func(tx repo.Group) error {
		rows, err := tx.OwnedLegacyInventory.SelectOwnedBuffItems(ctx, "1", []byte{list})
		if err != nil {
			return err
		}
		if len(rows) != 1 || rows[0].ItemUID != row.ItemUID || !reflect.DeepEqual(rows[0], row) {
			return fmt.Errorf("shared fixture did not expose exact owned UID/metadata: %+v", rows)
		}
		return nil
	}); err != nil {
		t.Fatalf("shared support precondition failed, not businessRED: %v", err)
	}
	invBefore, _, _ := r.Inventory.Load(ctx, "1")
	eqBefore, _, _ := r.Equipment.Load(ctx, "1")
	s, session, conn, logger, path := newSkillSwitchInventoryService(t, skillSwitchInventoryReferenceDrop, r)
	reviewCurrentBuffDispatch(t, s, session, reviewCurrentBuffCommand(38, slot, list, uint16(row.Item.SlotIndex)))
	rec, found, err := r.Settings.Load(ctx, buffswap.Scope("1"))
	if err != nil || !found {
		log := readPeerEndpointTestLog(t, logger, path)
		if !strings.Contains(log, "source item missing") {
			t.Fatalf("unexpected baseline failure, not known domain source-missing: %v %s", err, log)
		}
		t.Fatalf("BUSINESS_RED old domain source item missing; support passed, registration absent: %v", err)
	}
	if rec.Values["state"] == "" {
		t.Fatal("business state empty")
	}
	reviewOwnedLegacySettings(t, r, slot, row.ItemUID)
	snapshot := reviewOwnedLegacySnapshot(t, conn, 38, slot, list, uint16(row.Item.SlotIndex), true)
	for cold := 0; cold < 2; cold++ {
		fresh, cs, cc, _, _ := newSkillSwitchInventoryService(t, skillSwitchInventoryReferenceDrop, r)
		cs.postFinishLoadingPlayerStateSent = true
		if err := fresh.sendCurrentFinishLoadingCharacterState(cs, "owned_legacy_cold"); err != nil {
			t.Fatal(err)
		}
		if got := reviewOwnedLegacySnapshot(t, cc, 38, slot, list, uint16(row.Item.SlotIndex), true); !bytes.Equal(got, snapshot) {
			t.Fatal("cold snapshot bytes changed")
		}
		reader := csharpLegacyUserInfoReader{ctx: ctx, service: fresh, session: cs, characterID: "1"}
		var w packetWriter
		reader.writeNativeBuffSwapUserInfo(&w)
		if want := reviewOwnedLegacyExpectedUserInfo(row.Item, slot); !bytes.Equal(w.bytes(), want) {
			t.Fatalf("full119B/real serial/avatar raw blocks lost: got%x want%x", w.bytes(), want)
		}
	}
	invAfter, _, _ := r.Inventory.Load(ctx, "1")
	eqAfter, _, _ := r.Equipment.Load(ctx, "1")
	if !reflect.DeepEqual(invBefore, invAfter) || !reflect.DeepEqual(eqBefore, eqAfter) {
		t.Fatal("legacy registration copied/changed current assets")
	}
	reviewCurrentBuffDispatch(t, s, session, reviewCurrentBuffCommand(38, slot, 45, 0xffff))
	reviewOwnedLegacySnapshot(t, conn, 38, slot, list, 0, false)
	fresh, cs, cc, _, _ := newSkillSwitchInventoryService(t, skillSwitchInventoryReferenceDrop, r)
	if err := fresh.sendBuffSwapReapply(cs, "owned_clear_cold"); err != nil {
		t.Fatal(err)
	}
	reviewOwnedLegacySnapshot(t, cc, 38, slot, list, 0, false)
}
func TestReviewCurrentBuffLegacy0ActualRegistrationAndCold(t *testing.T) {
	reviewOwnedLegacyBusiness(t, 0)
}
func TestReviewCurrentBuffLegacy1Count0ActualRegistrationAndCold(t *testing.T) {
	reviewOwnedLegacyBusiness(t, 1)
}
func TestReviewCurrentBuffLegacy7ActualRegistrationAndCold(t *testing.T) {
	reviewOwnedLegacyBusiness(t, 7)
}

func TestReviewCurrentBuffLegacyOwnerAndContainerGuards(t *testing.T) {
	for _, name := range []string{"wrong_scope", "wrong_owner", "wrong_character", "zero_uid", "duplicate_uid", "unsupported_kind", "bad_count", "current_container_masks_legacy"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(buffSwapReapplyEnv, "on")
			row := reviewOwnedLegacyFixture(0)
			rows := []repo.OwnedLegacyItem{row}
			switch name {
			case "wrong_scope":
				rows[0].OwnerScope = "account"
			case "wrong_owner":
				rows[0].OwnerID = "2"
			case "wrong_character":
				rows[0].CharacterID = "2"
			case "zero_uid":
				rows[0].ItemUID = 0
			case "duplicate_uid":
				rows = append(rows, row)
			case "unsupported_kind":
				rows[0].ItemKind = "stackable"
			case "bad_count":
				rows[0].Item.StackCount = 2
			case "current_container_masks_legacy":
				rows[0].OwnerID = "2"
			}
			r := reviewOwnedLegacyGroup(t, rows)
			if name == "current_container_masks_legacy" {
				if err := r.Inventory.Save(context.Background(), repo.InventoryRecord{CharacterID: "1", Slots: map[string]repo.ItemStack{"0:55": {ItemID: 999, Count: 1}}}); err != nil {
					t.Fatal(err)
				}
			}
			before, _, _ := r.Inventory.Load(context.Background(), "1")
			s, session, conn, _, _ := newSkillSwitchInventoryService(t, skillSwitchInventoryReferenceDrop, r)
			reviewCurrentBuffDispatch(t, s, session, reviewCurrentBuffCommand(38, 12, 0, 54))
			_, found, err := r.Settings.Load(context.Background(), buffswap.Scope("1"))
			after, _, _ := r.Inventory.Load(context.Background(), "1")
			if err != nil || found || conn.write.Len() != 0 || !reflect.DeepEqual(before, after) {
				t.Fatalf("invalid owner/identity or shadowed legacy registered: found%v err%v bytes%d", found, err, conn.write.Len())
			}
		})
	}
}

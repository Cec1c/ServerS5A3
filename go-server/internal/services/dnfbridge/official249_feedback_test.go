package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"reflect"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestOfficial249LotteryReturnsNativeItemAndCommitsOnce(t *testing.T) {
	_, source := newMagicBoxTestCatalog(t)
	source["stackable/stackable.lst"] += "900 `cash/anton.stk`\n"
	source["stackable/cash/anton.stk"] = "[stackable type] `[upgradable legacy]` 1\n[stack limit] 100\n[need material] 10007367 4\n[int data] 2600014 4167 1\n[/int data]\n"
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, count := range []int64{3, 4} {
		t.Run(string(rune('0'+count)), func(t *testing.T) {
			ctx := context.Background()
			repos := dnfrepomemory.NewMemoryGroup()
			if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "account-1"}); err != nil {
				t.Fatal(err)
			}
			initial := dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{"0:75": {ItemID: 900, Count: 1}, "0:109": {ItemID: 10007367, Count: count}}}
			if err := repos.Inventory.Save(ctx, initial); err != nil {
				t.Fatal(err)
			}
			service := currentBoosterTestService(catalog, repos)
			service.options.currentClientVersion = "official-24.9"
			conn := &bufferConn{}
			session := &gameSession{conn: conn, selectedCharacterID: 19}
			if err := service.handleCurrentLotteryItem(session, []byte{0, 0, 75, 0}); err != nil {
				t.Fatal(err)
			}
			after, _, err := repos.Inventory.Load(ctx, "19")
			if err != nil {
				t.Fatal(err)
			}
			packets := drainUpperPackets(t, conn)
			var result []byte
			for _, p := range packets {
				if p.msgID == 27 {
					result = p.body
					break
				}
			}
			if count < 4 {
				if !bytes.Equal(result, []byte{0, 0x13}) || !reflect.DeepEqual(after.Slots, initial.Slots) {
					t.Fatalf("rejection body=%x changed assets=%v", result, after.Slots)
				}
				return
			}
			if len(result) != 126 || result[0] != 1 || binary.LittleEndian.Uint16(result[1:3]) != 75 || binary.LittleEndian.Uint32(result[5:9]) != 2600014 || binary.LittleEndian.Uint32(result[122:126]) != 0 {
				t.Fatalf("native result len=%d body=%x", len(result), result)
			}
			if _, exists := after.Slots["0:75"]; exists {
				t.Fatal("jar not consumed")
			}
			if _, exists := after.Slots["0:109"]; exists {
				t.Fatal("materials not consumed")
			}
			var rewards int64
			for _, stack := range after.Slots {
				if stack.ItemID == 2600014 {
					rewards += stack.Count
				}
			}
			if rewards != 1 {
				t.Fatalf("rewards=%d", rewards)
			}
			if err := service.handleCurrentLotteryOverflowConfirm(session, []byte{1, 27, 0}); err != nil {
				t.Fatal(err)
			}
			replayed, _, _ := repos.Inventory.Load(ctx, "19")
			if !reflect.DeepEqual(after.Slots, replayed.Slots) {
				t.Fatal("old animation confirmation granted twice")
			}
		})
	}
}

func TestOfficial249PeerProjectionOmitsLegacyRosterCarrier(t *testing.T) {
	service, receiver, actor, repos := peerSubtype0Fixture(t)
	character, found, err := repos.Character.Load(context.Background(), "100")
	if err != nil || !found {
		t.Fatalf("character found=%t err=%v", found, err)
	}
	legacy, ok := service.buildPeerUserInfoSubtype0AppearanceBody(context.Background(), receiver, actor, character, true, 100, character.Name)
	if !ok || len(legacy) < 82 {
		t.Fatal("legacy carrier fixture missing")
	}
	service.options.currentClientVersion = "official-24.9"
	before, _, _ := repos.Equipment.Load(context.Background(), "100")
	if body, ok := service.buildPeerUserInfoSubtype0AppearanceBody(context.Background(), receiver, actor, character, true, 100, character.Name); ok || body != nil {
		t.Fatal("official client received old roster")
	}
	if err := service.sendDungeonPartyPeerActorState(receiver, actor, "official249_feedback"); err != nil {
		t.Fatal(err)
	}
	for _, p := range splitUpperFrames(t, receiver.conn.(*bufferConn).write.Bytes()) {
		if bytes.Equal(p.body, legacy) {
			t.Fatal("malformed roster was sent after native actor")
		}
	}
	after, _, _ := repos.Equipment.Load(context.Background(), "100")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("peer projection changed equipment")
	}
}

func TestOfficial249SavedOptionsUseNativeNoticeIDs(t *testing.T) {
	service, session, conn, repos := testGameOptionFixture(t)
	service.options.currentClientVersion = "official-24.9"
	option := testLengthPrefixedBlob(testGameOptionBlob(512, 0))
	if err := service.handleSaveCharacterOption(session, option); err != nil {
		t.Fatal(err)
	}
	keys := []byte("I01\t2\nI02\t0\nI05\t78\nI06\t57\n")
	if err := repos.Settings.Save(context.Background(), dnfrepo.SettingsRecord{Scope: characterHotkeyConfigScope("19"), Values: map[string]string{"hotkey_config_hex": hex.EncodeToString(keys)}}); err != nil {
		t.Fatal(err)
	}
	if err := service.saveAccountGameOptionValues(session, "account-1", map[string]string{"hotkey_key_type": "1", "hotkey_slots_hex": "0200"}); err != nil {
		t.Fatal(err)
	}
	if err := service.sendCharacterOptionNotice(session, "relogin"); err != nil {
		t.Fatal(err)
	}
	if err := service.sendAccountHotkeyNotice(session, "relogin", "account-1", map[string]string{"hotkey_key_type": "1", "hotkey_slots_hex": "0200"}); err != nil {
		t.Fatal(err)
	}
	packets := drainUpperPackets(t, conn)
	if len(packets) != 2 || packets[0].msgID != 390 || !bytes.Equal(packets[0].body, option) || packets[1].msgID != 454 || !bytes.Equal(packets[1].body, buildAccountHotkeyNoticeBody(1, keys)) {
		t.Fatalf("saved-option native notices=%+v", packets)
	}
}

func TestOfficial249HotkeysRoundTripWithoutMainOptions(t *testing.T) {
	service, session, conn, _ := testGameOptionFixture(t)
	service.options.currentClientVersion = "official-24.9"
	keys := []byte("I01\t2\nI05\t78\nI06\t57\nI07\t80\n")
	if err := service.handleSaveGameOption2(session, testLengthPrefixedBlob(keys)); err != nil {
		t.Fatal(err)
	}
	if err := service.sendAccountGameOptionNotices(session, "relogin"); err != nil {
		t.Fatal(err)
	}
	packets := drainUpperPackets(t, conn)
	if len(packets) != 1 || packets[0].msgID != 454 || !bytes.Equal(packets[0].body, buildAccountHotkeyNoticeBody(0, keys)) {
		t.Fatalf("key-only save did not round-trip: %+v", packets)
	}
	session.selectedCharacterID = 20
	if err := service.sendAccountGameOptionNotices(session, "switch_character"); err != nil {
		t.Fatal(err)
	}
	if got := drainUpperPackets(t, conn); len(got) != 0 {
		t.Fatalf("another character inherited key configuration: %+v", got)
	}
}

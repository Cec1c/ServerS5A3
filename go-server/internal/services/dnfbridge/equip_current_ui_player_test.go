package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"log/slog"
	"os"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestCurrentUIPlayerMarkerScope(t *testing.T) {
	t.Setenv(op19PlayerFlagEnv, "")
	s := &Service{}
	valid := flaggedBody(t, loginWearAnchorLiveSlot17Body(), op19CurrentUIPlayerFlag)
	if !s.op19CurrentUIPlayerRequest(valid) {
		t.Fatal("the current-image manual armor move must be recognized")
	}
	for name, edit := range map[string]func([]byte){
		"old-marker":        func(b []byte) { b[26] = 1 },
		"unmarked":          func(b []byte) { b[26] = 0 },
		"unknown-marker":    func(b []byte) { b[26] = 3 },
		"second-tail":       func(b []byte) { b[27] = 1 },
		"cargo":             func(b []byte) { b[0] = 2 },
		"wallet":            func(b []byte) { binary.LittleEndian.PutUint16(b[1:3], 0) },
		"pet":               func(b []byte) { binary.LittleEndian.PutUint16(b[12:14], 26) },
		"artifact":          func(b []byte) { binary.LittleEndian.PutUint16(b[12:14], 30) },
		"negative-slot":     func(b []byte) { binary.LittleEndian.PutUint16(b[12:14], 65535) },
		"multi-count":       func(b []byte) { binary.LittleEndian.PutUint32(b[7:11], 2) },
		"destination-stack": func(b []byte) { binary.LittleEndian.PutUint32(b[18:22], 1) },
	} {
		body := append([]byte(nil), valid...)
		edit(body)
		if s.op19CurrentUIPlayerRequest(body) {
			t.Fatalf("%s must not receive the new immediate manual interpretation", name)
		}
	}
	t.Setenv(op19PlayerFlagEnv, "off")
	if s.op19PlayerRequestFlagged(valid) || s.op19CurrentUIPlayerRequest(valid) {
		t.Fatal("the existing rollback switch must ignore both markers")
	}
}

// The user's 19:11 armor request is inside the 15-second login window.
// The 19:14 re-equip is inside the historical 4-second mirror window. Run both
// through the actual bridge/owner entry and commit three rapid cycles, without
// manually resolving a quiet queue. Unmarked traffic is covered by old tests.
func TestCurrentUIPlayerQuickArmorCyclesCommitWithoutQuietOrMirror(t *testing.T) {
	setDefaultTier(t)
	t.Setenv(op19PlayerFlagEnv, "")
	t.Setenv(op19PlayerFlagQuietEnv, "")
	t.Setenv(wornSlotClaimRetryTierEnv, "")
	t.Setenv(mirrorReequipEchoSwitchEnv, "")
	t.Setenv(mirrorReequipEchoWindowEnv, "")
	f := newLoginWearAnchorFixture(t, "1", 1, map[int16]int64{16: loginWearAnchorArmorItemID16})
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE for real current UI owner quick-cycle regression")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	f.service.initialEquipmentArchive = archive
	f.service.pvfItemCatalog = catalog
	f.service.options.currentClientVersion = "official-24.9"
	var logs bytes.Buffer
	f.service.logger = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	f.session.equipLoginWearWindowOpenedAt = f.service.gameplayNow().Add(-9 * time.Second)
	defer f.service.stopAvatarAppearanceCoalesce(f.session, "test-complete")
	remove := flaggedBody(t, loginWearBridgeBody(0, 15, 0, currentSocketListEquipment, 16, int32(loginWearAnchorArmorItemID16)), op19CurrentUIPlayerFlag)
	putBack := flaggedBody(t, loginWearBridgeBody(0, 15, int32(loginWearAnchorArmorItemID16), currentSocketListEquipment, 16, 0), op19CurrentUIPlayerFlag)
	for cycle := 0; cycle < 3; cycle++ {
		for _, step := range []struct {
			body []byte
			worn int64
			bag  int
		}{{remove, 0, 1}, {putBack, loginWearAnchorArmorItemID16, 0}} {
			handled, err := f.service.handleAlignedGameCommand(f.session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketMoveItemspace), step.body)
			if err != nil || !handled {
				t.Fatalf("cycle %d handled=%t err=%v", cycle, handled, err)
			}
			if f.session.hasOp19QuietPending() {
				t.Fatal("reliably identified current UI actions must commit immediately")
			}
			if got := f.wornItemID(t, 16); got != step.worn {
				t.Fatalf("cycle %d worn=%d want=%d logs=%s", cycle, got, step.worn, logs.String())
			}
			record, ok, err := f.group.Inventory.Load(context.Background(), "1")
			if err != nil || !ok {
				t.Fatalf("inventory load ok=%t err=%v", ok, err)
			}
			count := 0
			for _, item := range record.Slots {
				if item.ItemID == loginWearAnchorArmorItemID16 {
					count++
				}
			}
			if count != step.bag {
				t.Fatalf("cycle %d bag copies=%d want=%d", cycle, count, step.bag)
			}
		}
	}
}

func TestCurrentUIPlayerQuickAvatarReequipCommitsInsideMirrorWindow(t *testing.T) {
	setDefaultTier(t)
	t.Setenv(op19PlayerFlagEnv, "")
	t.Setenv(op19PlayerFlagQuietEnv, "")
	t.Setenv(wornSlotClaimRetryTierEnv, "")
	t.Setenv(mirrorReequipEchoSwitchEnv, "")
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE for actual avatar placement")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	const avatar int64 = 0x060dd828 // user's 19:13:10 source avatar item, worn slot 2
	f := newLoginWearAnchorFixture(t, "1", 1, nil)
	f.service.initialEquipmentArchive, f.service.pvfItemCatalog = archive, catalog
	f.service.options.currentClientVersion = "official-24.9"
	definition, err := catalog.ResolveItem(uint32(avatar))
	if err != nil {
		t.Fatal(err)
	}
	if !currentItemListStackExtraIsAvatar(map[string]string{"equipment_type": definition.EquipmentType}) {
		t.Fatalf("actual item is not an avatar: %+v", definition)
	}
	raw := make([]byte, currentItemListEntryWireSize)
	binary.LittleEndian.PutUint16(raw, 2)
	binary.LittleEndian.PutUint32(raw[2:], uint32(avatar))
	binary.LittleEndian.PutUint32(raw[0x0e:], uint32(avatar))
	extra := map[string]string{"item_kind": "equipment", "equipment_type": definition.EquipmentType, "pvf_path": definition.PVFPath, "equipment_slot": "2", "equipped_slot": "2", "current_exe_equipment_type": "2", "current_exe_runtime_move": "1", "current_exe_inventory_count": "1"}
	if err := f.group.Equipment.Save(context.Background(), dnfrepo.EquipmentRecord{CharacterID: "1", Entries: map[string]dnfrepo.EquipmentEntry{"2": {SlotIndex: 2, ItemID: avatar, RawEntry: raw, Extra: extra}}}); err != nil {
		t.Fatal(err)
	}
	defer f.service.stopAvatarAppearanceCoalesce(f.session, "test-complete")
	remove := flaggedBody(t, loginWearBridgeBody(1, 121, 0, 3, 2, int32(avatar)), op19CurrentUIPlayerFlag)
	putBack := flaggedBody(t, loginWearBridgeBody(1, 121, int32(avatar), 3, 2, 0), op19CurrentUIPlayerFlag)
	for cycle := 0; cycle < 3; cycle++ {
		for step, body := range [][]byte{remove, putBack} {
			if handled, err := f.service.handleAlignedGameCommand(f.session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketMoveItemspace), body); err != nil || !handled {
				t.Fatalf("cycle=%d step=%d handled=%t err=%v", cycle, step, handled, err)
			}
			if f.session.hasOp19QuietPending() {
				t.Fatal("new native marker queued")
			}
			eq, ok, err := f.group.Equipment.Load(context.Background(), "1")
			if err != nil || !ok {
				t.Fatal(err)
			}
			got := eq.Entries[strconv.Itoa(2)].ItemID
			want := int64(0)
			if step == 1 {
				want = avatar
			}
			if got != want {
				t.Fatalf("cycle=%d step=%d worn=%d want=%d", cycle, step, got, want)
			}
		}
	}
}

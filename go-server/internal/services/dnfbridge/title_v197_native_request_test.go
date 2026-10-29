package dnfbridge

import (
	"bytes"
	"context"
	"encoding/hex"
	"log/slog"
	"os"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// Exact 28-byte requests captured from the isolated V196/V197 producer run.
// The source bag slot is empty; slot13 holds the real PVF title. The original
// client request must remain a login reassert, while V197 is an explicit move.
func TestTitleV197NativeRequestCommitsAndReequips(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires matching real PVF")
	}
	setDefaultTier(t)
	t.Setenv(op19PlayerFlagEnv, "")
	f := newLoginWearAnchorFixture(t, "1", 1, map[int16]int64{13: 400330094})
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	f.service.initialEquipmentArchive, f.service.pvfItemCatalog = archive, catalog
	f.service.options.currentClientVersion = "official-24.9"
	var logs bytes.Buffer
	f.service.logger = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	definition, err := catalog.ResolveItem(400330094)
	if err != nil {
		t.Fatal(err)
	}
	worn, found, err := f.group.Equipment.Load(context.Background(), "1")
	if err != nil || !found {
		t.Fatal(found, err)
	}
	entry := worn.Entries["13"]
	entry.Extra["pvf_path"], entry.Extra["equipment_type"] = definition.PVFPath, definition.EquipmentType
	worn.Entries["13"] = entry
	if err := f.group.Equipment.Save(context.Background(), worn); err != nil {
		t.Fatal(err)
	}
	f.session.equipLoginWearWindowOpenedAt = f.service.gameplayNow().Add(-9 * time.Second)
	defer f.service.stopAvatarAppearanceCoalesce(f.session, "test-complete")
	old, _ := hex.DecodeString("0029000000000001000000030d006e8ddc1700000000ffffffff0000")
	current, _ := hex.DecodeString("0029000000000001000000030d006e8ddc1700000000ffffffff0200")
	send := func(body []byte) {
		t.Helper()
		handled, err := f.service.handleAlignedGameCommand(f.session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketMoveItemspace), body)
		if err != nil || !handled {
			t.Fatalf("handled=%t error=%v", handled, err)
		}
	}
	send(old)
	if f.wornItemID(t, 13) != 400330094 {
		t.Fatal("unmarked login reassert changed equipment")
	}
	// Slot41 is outside the unexpanded fixture's unlocked range. The manual
	// marker must not bypass capacity. Then model the expanded bag used by
	// the native producer fixture, without changing its captured request.
	send(current)
	if f.wornItemID(t, 13) != 400330094 || f.session.hasOp19QuietPending() {
		t.Fatal("manual marker bypassed locked-slot capacity")
	}
	if err := f.group.Settings.Save(context.Background(), dnfrepo.SettingsRecord{
		Scope:  dnfrepo.CharacterContainerStateScope("1"),
		Values: map[string]string{"main_list_param16": "8", "avatar_list_param16": "0", "personal_cargo_list_param16": "8"},
	}); err != nil {
		t.Fatal(err)
	}
	logs.Reset()
	for i := 0; i < 3; i++ {
		send(current)
		if f.session.hasOp19QuietPending() || f.wornItemID(t, 13) != 0 {
			t.Fatalf("native manual request did not unequip immediately: %s", logs.String())
		}
		inventory, ok, err := f.group.Inventory.Load(context.Background(), "1")
		if err != nil || !ok {
			t.Fatal(ok, err)
		}
		copies := 0
		for _, item := range inventory.Slots {
			if item.ItemID == 400330094 {
				copies++
			}
		}
		if copies != 1 {
			t.Fatalf("title copies=%d", copies)
		}
		send(flaggedBody(t, loginWearBridgeBody(0, 41, 400330094, currentSocketListEquipment, 13, 0), op19CurrentUIPlayerFlag))
		if f.wornItemID(t, 13) != 400330094 {
			t.Fatal("title did not re-equip")
		}
	}
}

package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// This exercises the actual bridge/owner/return/re-entry lifecycle with a real
// PVF potion. The room geometry is the existing synthetic return fixture;
// changing its ID to5104 does not claim a native light-altar completion.
func TestRealPVFLukeConsumableDispatchAcrossConfirmedReturnAndReentry(t *testing.T) {
	catalog, _ := actualAntonRewardFixture(t)
	service, session, runtime := newBackToVillageRuntime(t)
	service.options.gameUpperHeader = gameUpperHeaderServer16
	service.pvfItemCatalog = catalog
	runtime.Dungeon.ID = 5104
	repos, _ := service.repositoryGroup()
	ctx := context.Background()
	const itemID int32 = 2660671 // actual hands_remy_shine.stk, [waste]
	const instance int32 = 0x11223344
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "99", Slots: map[string]dnfrepo.ItemStack{
			"0:65": {ItemID: int64(itemID), Count: 4, Extra: map[string]string{"item_kind": "stackable"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	conn := session.conn.(*bufferConn)
	use := func(label string, mismatched uint32, remaining int64, success bool) {
		t.Helper()
		conn.write.Reset()
		before, _, err := repos.Inventory.Load(ctx, "99")
		if err != nil {
			t.Fatal(err)
		}
		body := buildRejectedUseStackableBody(65, 0, instance, itemID)
		binary.LittleEndian.PutUint32(body[7:11], uint32(itemID)+mismatched)
		if err := service.handleGameCommand(session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketUseStackable), body); err != nil {
			t.Fatalf("%s dispatch: %v", label, err)
		}
		packet, rest := splitLongHengGameServerUpperPacket(t, conn.write.Bytes())
		if packet.Header.MsgID != 44 || packet.Header.Classification != 1 {
			t.Fatalf("%s use ACK header=%+v", label, packet.Header)
		}
		if success {
			want := append([]byte{1}, body[:11]...)
			if !bytes.Equal(packet.Body, want) || len(rest) == 0 {
				t.Fatalf("%s success ACK=%x want=%x refresh=%x", label, packet.Body, want, rest)
			}
			refresh, trailing := splitLongHengGameServerUpperPacket(t, rest)
			if refresh.Header.MsgID != 14 || refresh.Header.Classification != 0 || len(trailing) != 0 {
				t.Fatalf("%s changed-slot refresh=%+v trailing=%x", label, refresh.Header, trailing)
			}
		} else {
			want := []byte{0, currentUseStackableRejectionCode, 0}
			want = append(want, body[3:11]...)
			if !bytes.Equal(packet.Body, want) || len(rest) != 0 {
				t.Fatalf("%s reject ACK=%x want=%x trailing=%x", label, packet.Body, want, rest)
			}
		}
		after, found, err := repos.Inventory.Load(ctx, "99")
		if err != nil || !found || after.Slots["0:65"].Count != remaining {
			t.Fatalf("%s persisted inventory=%+v found=%t err=%v", label, after, found, err)
		}
		if !success && !reflect.DeepEqual(before, after) {
			t.Fatalf("%s rejected use mutated inventory", label)
		}
	}
	use("in active altar-labelled fixture", 0, 3, true)
	use("rejected request before return", 1, 3, false)
	if err := runtime.Session.CompleteCurrentRoom(); err != nil {
		t.Fatal(err)
	}
	// This synthetic room has no ordinary result/card settlement. Exercise the
	// confirmed transport boundary, as in the completed-return transport test;
	// the public 132 owner correctly waits for those independent barriers.
	transition, err := service.prepareCurrentDungeonTownTransitionForSession(ctx, session, session.selectedCharacterID)
	if err != nil {
		t.Fatal(err)
	}
	session.dungeon.mu.Lock()
	err = service.sendCurrentDungeonReturnToTownLocked(session, runtime, transition, uint16(dnfenum.CmdPacketBack2Village), "consumable_completed_transport_regression")
	session.dungeon.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := service.commitPendingDungeonReturnForSceneRequest(session, "consumable_transition_test"); err != nil {
		t.Fatal(err)
	}
	if session.dungeon.runtime != nil || runtime.Session.Snapshot().Run.Status != worldmap.DungeonRunCompleted {
		t.Fatal("confirmed completed return did not retire runtime")
	}
	use("town after confirmed return", 0, 2, true)
	bindDungeonSelectorOriginForTest(t, service, session)
	next, _, err := service.prepareDungeonRuntime(ctx, session, dungeoncmd.SelectDungeonRequest{DungeonID: 700, Difficulty: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := freezeCurrentDungeonTownReturnOrigin(session, next); err != nil {
		t.Fatal(err)
	}
	if err := service.commitDungeonRuntime(session, next); err != nil {
		t.Fatal(err)
	}
	if next == runtime || session.dungeon.runtime != next {
		t.Fatal("next dungeon reused retired runtime")
	}
	use("new dungeon after return", 0, 1, true)
	use("rejected request after reentry", 1, 1, false)
	use("next valid use after rejection", 0, 0, true)
}

// Asset flags are scoped evidence only: the real ordinary altar's seven map
// declarations cannot explain a direct persistent block-use-stackable flag.
// Monster/passive-object client scripts and the reported GUI remain unproved.
func TestRealPVFLukeLightAltarMapDeclarationsDoNotBlockStackableUse(t *testing.T) {
	catalog, _ := actualAntonRewardFixture(t)
	source := catalog.source
	text, err := source.ReadText("dungeon/luke_normal/light_altar.dgn")
	if err != nil {
		t.Fatal(err)
	}
	document, err := dnfpvf.Parse("dungeon/luke_normal/light_altar.dgn", text)
	if err != nil {
		t.Fatal(err)
	}
	dungeon := worldmap.ParseDungeon(5104, document.Path, document)
	if len(dungeon.Mazes) != 1 || len(dungeon.Mazes[0].MapSpecifications) != 7 {
		t.Fatalf("actual altar map layout changed: %+v", dungeon.Mazes)
	}
	for _, spec := range dungeon.Mazes[0].MapSpecifications {
		for _, id := range spec.MapIDs {
			path := fmt.Sprintf("map/luke_normal/light_altar/%d.map", id)
			text, err := source.ReadText(path)
			if err != nil {
				t.Fatal(err)
			}
			doc, err := dnfpvf.Parse(path, text)
			if err != nil {
				t.Fatal(err)
			}
			if _, declared := doc.Section("block use stackable item"); declared {
				t.Fatalf("actual altar map %s declares a stackable-use block", path)
			}
			t.Logf("actual altar map %d has no block-use-stackable declaration", id)
		}
	}
}

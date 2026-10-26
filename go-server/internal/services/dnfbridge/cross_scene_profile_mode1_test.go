package dnfbridge

// Regression coverage for the confirmed mode1 cross-scene appearance
// omission in appearance-refresh-path-audit.md (2026-10-24). The tests reuse
// the existing real op19/domain fixtures and wire helpers:
//   - newTownPartyActorHPFixture and newCurrentCrossSceneProfileBatchFixture
//   - loginWearBridgeBody (the current-EXE op19 body layout)
//   - the avatar slots/items used by equip_avatar_appearance_refresh_test.go
//   - requireCurrentCrossSceneProfileBatchMode8 and the mode0 row helpers
//
// The first test pins the required broadcast for one independent mode1 wear.
// The second pins revision invalidation: a batch prepared before the mode1
// refresh must not be able to send its old appearance body afterwards.
//
// The PVF-gated equipmentTailRealUnequip helper is intentionally not used:
// this regression must run in the normal package suite. The same current-EXE
// list17 body and real inventory/equip owner are driven with the existing
// avatar slots/items and an explicit placement-validator callback.

import (
	"context"
	"encoding/binary"
	"strconv"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfinventory "longheng.io/server/internal/modules/dnf/inventory"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// The current EXE carries avatar/fashion wear on the alternate actor-worn
// endpoint. It shares the durable EquipmentRecord with list 3 but stays
// distinct on the wire so the appearance gate always sees a fashion move.
const mode1CrossSceneActorWornAltList byte = 17

// newMode1CrossSceneTwoSceneFixture builds the two-session cross-scene case on
// top of the existing town party fixture. Both characters stay authenticated,
// in the same channel, and in the central party; only the actor's presence row
// moves to another town area so the target is genuinely viewing another scene.
func newMode1CrossSceneTwoSceneFixture(t *testing.T) *townPartyActorHPFixture {
	t.Helper()
	f := newTownPartyActorHPFixture(t, true)
	t.Cleanup(func() { f.service.stopAvatarAppearanceCoalesce(f.actor, "mode1_cross_scene_test_cleanup") })
	f.service.options.currentClientVersion = "official-24.9"
	f.service.options.gameUpperHeader = gameUpperHeaderChannel13
	f.service.options.equipAppearanceRefreshMode1Rollback = true
	f.target.accountID = "dnf:1"
	f.actor.accountID = "dnf:2"
	f.target.connectionTownActorOwnerChannel = byte(f.target.channel.ID)
	f.actor.connectionTownActorOwnerChannel = byte(f.actor.channel.ID)

	actorPresence := f.actorInfo
	actorPresence.AreaID = 0
	f.service.onlinePlayers.EnterArea(&actorPresence)

	targetPresence, targetFound := f.service.onlinePlayers.PlayerForCharacter(f.target.selectedCharacterID)
	actorPresence, actorFound := f.service.onlinePlayers.PlayerForCharacter(f.actor.selectedCharacterID)
	if !targetFound || !actorFound {
		t.Fatalf("scene presence target=%t actor=%t", targetFound, actorFound)
	}
	if targetPresence.TownID != actorPresence.TownID || targetPresence.AreaID == actorPresence.AreaID {
		t.Fatalf("fixture scenes are not distinct: target=(town %d area %d) actor=(town %d area %d)",
			targetPresence.TownID, targetPresence.AreaID, actorPresence.TownID, actorPresence.AreaID)
	}
	if currentTownPresenceChannelID(f.target) != currentTownPresenceChannelID(f.actor) {
		t.Fatal("cross-scene fixture changed the channel relation")
	}

	snapshot, found := f.service.runtimePartyManagerForService().SnapshotByUser(f.target.selectedCharacterID, 0)
	if !found {
		t.Fatal("fixture lost the central party")
	}
	members := make(map[uint16]bool, len(snapshot.Members))
	for _, member := range snapshot.Members {
		members[member.UserID] = true
	}
	if !members[f.target.selectedCharacterID] || !members[f.actor.selectedCharacterID] {
		t.Fatalf("central party members=%v, want target %d and actor %d",
			members, f.target.selectedCharacterID, f.actor.selectedCharacterID)
	}
	f.reset()
	return f
}

// seedMode1CrossSceneAvatar puts the same permanent waist avatar used by the
// existing avatar appearance fixture into the avatar bag. The equipment record
// starts empty so the first real op19 is an unambiguous wear.
func seedMode1CrossSceneAvatar(t *testing.T, service *Service, characterID string) {
	t.Helper()
	ctx := context.Background()
	repositories, ok := service.repositoryGroup()
	if !ok {
		t.Fatal("missing repository group")
	}
	if err := repositories.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: "dnf:2", State: "active"}); err != nil {
		t.Fatalf("save actor account: %v", err)
	}

	raw := make([]byte, currentItemListEntryWireSize)
	binary.LittleEndian.PutUint16(raw[0:2], uint16(avatarAppearanceTestAvatarSlot))
	binary.LittleEndian.PutUint32(raw[2:6], uint32(avatarAppearanceTestItemID))
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: characterID,
		Slots: map[string]dnfrepo.ItemStack{
			inventorySlotKeyAvatar(avatarAppearanceTestAvatarSlot): {
				ItemID:   avatarAppearanceTestItemID,
				Count:    0,
				RawEntry: raw,
				Extra: map[string]string{
					"item_kind":       "avatar",
					"amount_or_count": "0",
					"pvf_path":        "equipment/character/swordman/avatar/waist/waist.equ",
				},
			},
		},
		Warehouse: map[string]dnfrepo.ItemStack{},
	}); err != nil {
		t.Fatalf("save avatar inventory: %v", err)
	}
	if err := repositories.Equipment.Save(ctx, dnfrepo.EquipmentRecord{
		CharacterID: characterID,
		Entries:     map[string]dnfrepo.EquipmentEntry{},
	}); err != nil {
		t.Fatalf("save empty equipment: %v", err)
	}
}

// commitMode1CrossSceneAvatarWear drives the real inventory/equip owner through
// the list 1 -> list 17 fashion endpoint, then sends the committed op19 result
// through the real aligned-response/post-action path.
func commitMode1CrossSceneAvatarWear(t *testing.T, service *Service, actor *gameSession) {
	t.Helper()
	commitMode1CrossSceneAvatarMove(t, service, actor, currentSocketListAvatar, avatarAppearanceTestAvatarSlot, int32(avatarAppearanceTestItemID), mode1CrossSceneActorWornAltList, avatarAppearanceTestWornSlot, 0, avatarAppearanceTestItemID)
}

func commitMode1CrossSceneAvatarMove(t *testing.T, service *Service, actor *gameSession, from byte, fromSlot int16, fromItem int32, to byte, toSlot int16, toItem int32, wantWorn int64) {
	t.Helper()
	ctx := context.Background()
	repositories, ok := service.repositoryGroup()
	if !ok {
		t.Fatal("missing repository group")
	}
	result, err := dnfinventory.NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketMoveItemspace),
		Body:                loginWearBridgeBody(from, fromSlot, fromItem, to, toSlot, toItem),
		SelectedCharacterID: actor.selectedCharacterID,
		Repositories:        repositories,
		EquipmentPlacement: func(context.Context, alignedcmd.EquipmentPlacementRequest) error {
			return nil
		},
	})
	if err != nil {
		t.Fatalf("real list17 op19: %v", err)
	}
	if !result.Handled || !result.ResponseAllowed {
		t.Fatalf("real list17 op19 was not handled: %+v", result)
	}
	if result.EquipmentMoveFact == nil || !result.EquipmentMoveFact.Changed {
		t.Fatalf("real list17 op19 did not commit: %+v", result)
	}
	appearanceRefresh := false
	for _, action := range result.PostActions {
		if action == alignedcmd.PostActionRefreshSelectedActorAppearance {
			appearanceRefresh = true
		}
	}
	if !appearanceRefresh {
		t.Fatalf("list17 op19 did not request actor appearance: %+v", result.PostActions)
	}

	characterID := strconv.Itoa(int(actor.selectedCharacterID))
	equipment, found, err := repositories.Equipment.Load(ctx, characterID)
	if err != nil || !found {
		t.Fatalf("load committed equipment found=%t err=%v", found, err)
	}
	worn, equipped := equipment.Entries[strconv.Itoa(int(avatarAppearanceTestWornSlot))]
	if (wantWorn == 0 && equipped && worn.ItemID != 0) || (wantWorn != 0 && (!equipped || worn.ItemID != wantWorn)) {
		t.Fatalf("committed fashion slot=%+v equipped=%t, want item %d", worn, equipped, wantWorn)
	}

	if err := service.sendAlignedUpperResponses(actor, result); err != nil {
		t.Fatalf("send committed list17 op19: %v", err)
	}
}

// requireMode1CrossSceneActorStreamUnchanged checks that the cross-scene send
// did not change the actor's historical mode1 sequence: op19 ACK followed by
// exactly one class0/op2 mode1 body with equipment create rows.
func requireMode1CrossSceneActorStreamUnchanged(t *testing.T, connection *bufferConn, worn bool) {
	t.Helper()
	ack, rest := splitGameServerUpperPacket(t, connection.write.Bytes())
	if ack.Header.MsgID != uint16(dnfenum.CmdPacketMoveItemspace) || ack.Header.Classification == 0 {
		t.Fatalf("actor stream must start with the class1 op19 ACK: %+v", ack.Header)
	}
	mode1, trailing := splitGameServerUpperPacket(t, rest)
	const codecPrefix = 0 // This fixture explicitly uses the 13-byte upper header.
	if mode1.Header.MsgID != uint16(dnfenum.CmdPacketSetUDPIPPort) || mode1.Header.Classification != 0 {
		t.Fatalf("actor mode1 packet header=%+v", mode1.Header)
	}
	if len(mode1.Body) <= codecPrefix+currentMode1CreateCountOffset {
		t.Fatalf("actor mode1 body truncated: %x", mode1.Body)
	}
	if mode1.Body[codecPrefix] != 1 {
		t.Fatalf("actor appearance mode=%d want 1", mode1.Body[codecPrefix])
	}
	if (mode1.Body[codecPrefix+currentMode1CreateCountOffset] != 0) != worn {
		t.Fatal("actor mode1 equipped create rows differ from committed worn state")
	}
	if len(trailing) != 0 {
		t.Fatalf("cross-scene broadcast changed the actor's packet order: %x", trailing)
	}
}

// requireMode1CrossSceneFullFashion asserts the target received the complete
// mode8 profile and that its mode0 appearance table contains all 14 ordered
// fashion slots, with only the just-worn slot carrying the item.
func requireMode1CrossSceneFullFashion(
	t *testing.T,
	frames []upperFrame,
	actorID uint16,
	owner byte,
	name string,
	wornSlot int,
	itemID uint32,
) []byte {
	t.Helper()
	body := requireCurrentCrossSceneProfileBatchMode8(t, frames, actorID, owner)
	start := currentSceneObjectTailStartForTest(name) + 6
	if len(body) <= start || body[start] != currentActorMode0AppearanceSlotCount {
		t.Fatalf("mode8 appearance table missing: start=%d len=%d body=%x", start, len(body), body)
	}
	appearance := body[start:]
	for slot := 0; slot < currentActorMode0AppearanceSlotCount; slot++ {
		want := currentActorMode0AppearanceEmptyItem
		if slot == wornSlot {
			want = itemID
		}
		if got := currentActorMode0AppearanceGoldenItemID(t, appearance, slot); got != want {
			t.Fatalf("mode8 fashion slot %d item=%d want=%d", slot, got, want)
		}
	}
	return body
}

// TestMode1CrossSceneFashionWearBroadcastsFullMode8Immediately pins the
// required behavior: with mode1 selected and BUFF replay disabled, one list17
// wear must still reach a same-party peer in another scene. No gameplay timer
// is started or run here; the frame has to be produced by the operation itself.
func TestMode1CrossSceneFashionWearBroadcastsFullMode8Immediately(t *testing.T) {
	t.Setenv(buffSwapReapplyEnv, "off")
	f := newMode1CrossSceneTwoSceneFixture(t)
	f.service.stopAvatarAppearanceCoalesce(f.actor, "mode1_cross_scene_test_start")
	seedMode1CrossSceneAvatar(t, f.service, strconv.Itoa(int(f.actor.selectedCharacterID)))
	f.reset()

	commitMode1CrossSceneAvatarWear(t, f.service, f.actor)

	owner := currentConnectionTownActorOwnerContext(f.target)
	requireMode1CrossSceneFullFashion(
		t,
		splitUpperFrames(t, f.targetConn.write.Bytes()),
		f.actor.selectedCharacterID,
		owner,
		"peer",
		int(avatarAppearanceTestWornSlot),
		uint32(avatarAppearanceTestItemID),
	)
	requireMode1CrossSceneActorStreamUnchanged(t, f.actorConn, true)
}

func TestMode1CrossSceneFashionRemoveAndSwapBroadcastImmediately(t *testing.T) {
	for _, swap := range []bool{false, true} {
		name := "remove"
		if swap {
			name = "swap"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv(buffSwapReapplyEnv, "off")
			f := newMode1CrossSceneTwoSceneFixture(t)
			seedMode1CrossSceneAvatar(t, f.service, "17")
			commitMode1CrossSceneAvatarWear(t, f.service, f.actor)
			f.service.stopAvatarAppearanceCoalesce(f.actor, "independent_next_move")
			repos, _ := f.service.repositoryGroup()
			ctx := context.Background()
			want := uint32(currentActorMode0AppearanceEmptyItem)
			f.reset()
			if swap {
				inventory, _, err := repos.Inventory.Load(ctx, "17")
				if err != nil {
					t.Fatal(err)
				}
				raw := make([]byte, currentItemListEntryWireSize)
				binary.LittleEndian.PutUint16(raw[0:2], uint16(avatarAppearanceTestAvatarSlot+1))
				binary.LittleEndian.PutUint32(raw[2:6], uint32(avatarAppearanceTestItemID+1))
				if inventory.Slots == nil {
					inventory.Slots = make(map[string]dnfrepo.ItemStack)
				}
				inventory.Slots[inventorySlotKeyAvatar(avatarAppearanceTestAvatarSlot+1)] = dnfrepo.ItemStack{ItemID: avatarAppearanceTestItemID + 1, RawEntry: raw, Extra: map[string]string{"item_kind": "avatar", "amount_or_count": "0", "pvf_path": "equipment/character/swordman/avatar/waist/waist.equ"}}
				if err := repos.Inventory.Save(ctx, inventory); err != nil {
					t.Fatal(err)
				}
				commitMode1CrossSceneAvatarMove(t, f.service, f.actor, currentSocketListAvatar, avatarAppearanceTestAvatarSlot+1, int32(avatarAppearanceTestItemID+1), mode1CrossSceneActorWornAltList, avatarAppearanceTestWornSlot, int32(avatarAppearanceTestItemID), avatarAppearanceTestItemID+1)
				want = uint32(avatarAppearanceTestItemID + 1)
			} else {
				commitMode1CrossSceneAvatarMove(t, f.service, f.actor, mode1CrossSceneActorWornAltList, avatarAppearanceTestWornSlot, int32(avatarAppearanceTestItemID), currentSocketListAvatar, avatarAppearanceTestAvatarSlot, 0, 0)
			}
			requireMode1CrossSceneFullFashion(t, splitUpperFrames(t, f.targetConn.write.Bytes()), 17, currentConnectionTownActorOwnerContext(f.target), "peer", int(avatarAppearanceTestWornSlot), want)
			requireMode1CrossSceneActorStreamUnchanged(t, f.actorConn, swap)
			inventory, found, err := repos.Inventory.Load(ctx, "17")
			if err != nil || !found {
				t.Fatalf("inventory after move: found=%v err=%v", found, err)
			}
			slot := avatarAppearanceTestAvatarSlot
			if swap {
				slot++
			}
			if got := inventory.Slots[inventorySlotKeyAvatar(slot)].ItemID; got != avatarAppearanceTestItemID {
				t.Fatalf("original avatar was not preserved in bag: %d", got)
			}
		})
	}
}

// TestMode1CrossSceneFashionRefreshInvalidatesPreparedBatch pins the second
// half of the mode1 contract: a profile body prepared before the wear must not
// be sent after the mode1 refresh. The fresh broadcast is deliberately cleared
// before retrying the old batch so only the stale delivery is observed.
func TestMode1CrossSceneFashionRefreshInvalidatesPreparedBatch(t *testing.T) {
	t.Setenv(buffSwapReapplyEnv, "off")
	f := newCurrentCrossSceneProfileBatchFixture(t)
	t.Cleanup(func() { f.service.stopAvatarAppearanceCoalesce(f.actor, "mode1_cross_scene_prepared_batch_cleanup") })
	f.service.options.gameUpperHeader = gameUpperHeaderChannel13
	f.service.options.equipAppearanceRefreshMode1Rollback = true
	f.service.stopAvatarAppearanceCoalesce(f.actor, "mode1_cross_scene_prepared_batch_start")
	seedMode1CrossSceneAvatar(t, f.service, strconv.Itoa(int(f.actor.selectedCharacterID)))
	f.reset()

	owner := currentConnectionTownActorOwnerContext(f.target)
	prepared := newCurrentCrossSceneProfileBatch(f.service, context.Background())
	if prepared == nil {
		t.Fatal("newCurrentCrossSceneProfileBatch returned nil")
	}
	if err := prepared.send(f.target, f.actor, owner); err != nil {
		t.Fatalf("prepare pre-refresh profile batch: %v", err)
	}
	oldBody := requireCurrentCrossSceneProfileBatchMode8(
		t,
		splitUpperFrames(t, f.targetConn.write.Bytes()),
		f.actor.selectedCharacterID,
		owner,
	)
	oldStart := currentSceneObjectTailStartForTest("peer") + 6
	if len(oldBody) <= oldStart {
		t.Fatalf("prepared mode8 body truncated: len=%d start=%d", len(oldBody), oldStart)
	}
	if oldBody[oldStart] != currentActorMode0AppearanceSlotCount {
		t.Fatalf("prepared mode8 appearance count=%d want=%d", oldBody[oldStart], currentActorMode0AppearanceSlotCount)
	}
	if got := currentActorMode0AppearanceGoldenItemID(t, oldBody[oldStart:], int(avatarAppearanceTestWornSlot)); got != currentActorMode0AppearanceEmptyItem {
		t.Fatalf("prepared body already says fashion is worn: slot item=%d", got)
	}
	f.reset()

	commitMode1CrossSceneAvatarWear(t, f.service, f.actor)
	// The fixed mode1 path may also broadcast a fresh body. Drop that fresh
	// delivery; the retry below must be judged only by the prepared body.
	f.reset()
	_ = prepared.send(f.viewer2, f.actor, owner)
	if f.viewer2Conn.write.Len() != 0 {
		t.Fatalf("prepared pre-refresh batch delivered stale appearance: %x", f.viewer2Conn.write.Bytes())
	}

	fresh := newCurrentCrossSceneProfileBatch(f.service, context.Background())
	if fresh == nil {
		t.Fatal("newCurrentCrossSceneProfileBatch returned nil for refreshed body")
	}
	if err := fresh.send(f.viewer2, f.actor, owner); err != nil {
		t.Fatalf("send refreshed profile batch: %v", err)
	}
	requireMode1CrossSceneFullFashion(
		t,
		splitUpperFrames(t, f.viewer2Conn.write.Bytes()),
		f.actor.selectedCharacterID,
		owner,
		"peer",
		int(avatarAppearanceTestWornSlot),
		uint32(avatarAppearanceTestItemID),
	)
}

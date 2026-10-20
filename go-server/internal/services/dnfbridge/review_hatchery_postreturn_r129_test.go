package dnfbridge

import (
	"context"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnftown "longheng.io/server/internal/modules/dnf/town"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func prepareHatcheryPostReturnR129(
	t *testing.T,
) (*Service, *gameSession, *runtimeDungeonState, byte, dnftown.Area) {
	t.Helper()
	const (
		characterID = uint16(99)
		originTown  = byte(38)
		originArea  = byte(1)
	)

	s, session, old, target := actualAntonHatchWarpOwnerFor(t, 222)
	if target != 222 {
		t.Fatalf("actual fixture opened %d, want 222", target)
	}

	archive := s.initialEquipmentArchive
	if archive == nil {
		t.Fatal("real PVF archive missing from actual Anton fixture")
	}
	towns, err := dnftown.Load(context.Background(), archive, dnftown.Options{})
	if err != nil {
		t.Fatalf("load real town catalog: %v", err)
	}
	s.townCatalog = towns
	if _, found := towns.FindArea(int64(originTown), int64(originArea)); !found {
		t.Fatalf("real PVF town %d area %d missing", originTown, originArea)
	}
	destination, found := towns.FindArea(int64(originTown), 0)
	if !found || destination.ID == int64(originArea) || destination.MapPath == "" {
		t.Fatalf("real PVF has no distinct town %d area 0: %+v found=%t", originTown, destination, found)
	}

	repositories, ok := s.repositoryGroup()
	if !ok {
		t.Fatal("repository group unavailable")
	}
	character, found, err := repositories.Character.Load(context.Background(), "99")
	if err != nil || !found {
		t.Fatalf("load character found=%t err=%v", found, err)
	}
	character.Level = 90
	if character.Stats == nil {
		character.Stats = map[string]int64{}
	}
	character.Stats["town_id"] = int64(originTown)
	character.Stats["area_id"] = int64(originArea)
	character.Stats["pos_x"] = 450
	character.Stats["pos_y"] = 234
	character.Stats["direction"] = 6
	character.Stats["area_state"] = 3
	character.Stats["exp"] = 0
	if err := repositories.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Skill.Save(context.Background(), dnfrepo.SkillRecord{
		CharacterID: "99",
		Points:      dnfrepo.SkillPointState{SyncedLevel: character.Level},
	}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := repositories.Inventory.Load(context.Background(), "99"); err != nil {
		t.Fatal(err)
	} else if !found {
		if err := repositories.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{
			CharacterID: "99",
			Slots:       map[string]dnfrepo.ItemStack{},
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Rebind the fixture's synthetic town origin to a real PVF town so both the
	// completed return and the later op36 use the same authoritative location.
	clearCurrentTownSelectorOrigin(session)
	session.townMu.Lock()
	session.townSceneReadyCharacterID = characterID
	session.townPositionSnapshot = currentTownPositionSnapshot{
		CharacterID:   characterID,
		TownID:        originTown,
		AreaID:        originArea,
		PositionX:     450,
		PositionY:     234,
		MovementCode:  6,
		PositionValid: true,
	}
	session.townMu.Unlock()
	old.Character = dnfrepo.CloneCharacter(character)
	old.Character.Stats["town_id"] = int64(originTown)
	old.Character.Stats["area_id"] = int64(originArea)
	old.Character.Stats["pos_x"] = 450
	old.Character.Stats["pos_y"] = 234
	old.Character.Stats["direction"] = 6
	old.Character.Stats["area_state"] = 3
	if _, ok := bindCurrentTownSelectorOrigin(session); !ok {
		t.Fatal("real town selector origin did not bind")
	}
	if err := freezeCurrentDungeonTownReturnOrigin(session, old); err != nil {
		t.Fatalf("freeze real town return origin: %v", err)
	}

	s.onlinePlayers = newOnlinePlayerManager()
	if err := s.handleDungeonSelectUpper(session, encodeCurrentDungeonSelectRequest(dungeoncmd.SelectDungeonRequest{
		DungeonID:  222,
		Difficulty: 2,
	})); err != nil {
		t.Fatal(err)
	}
	hatchery := session.dungeon.runtime
	if hatchery == nil || hatchery == old || hatchery.Dungeon.ID != 222 || hatchery.antonHatcheryTransfer == nil {
		t.Fatalf("actual 218 -> 222 transfer failed: old=%p current=%p", old, hatchery)
	}
	if hatchery.townReturnOrigin.TownID != originTown || hatchery.townReturnOrigin.AreaID != originArea {
		t.Fatalf("transfer changed real town origin: %+v", hatchery.townReturnOrigin)
	}

	reviewCompleteActualHatchery(t, s, session, hatchery)
	if status := hatchery.Session.Snapshot().Run.Status; status != worldmap.DungeonRunCompleted {
		t.Fatalf("actual hatchery completion status=%s", status)
	}
	session.conn.(*bufferConn).write.Reset()
	if err := s.handleDungeonEplpCommand(session, []byte{1, 2}); err != nil {
		t.Fatalf("completed hatchery return: %v", err)
	}

	assertHatcheryReturnDetachedForR129(t, session, hatchery)
	return s, session, hatchery, originTown, destination
}

// This audit starts with the actual Anton 218 -> 222 transfer and the real
// dungeon's boss room. It then consumes the completed-map exit and exercises
// the town-side packets which follow it: op35, legacy scene-ready, op36, and a
// second op35. It does not repeat the op22/op23/op24 return-direction oracle.
func TestReviewHatcheryPostReturnR129Lifecycle(t *testing.T) {
	const (
		characterID = uint16(99)
		originArea  = byte(1)
	)
	s, session, hatchery, originTown, destination := prepareHatcheryPostReturnR129(t)
	presence, found := s.onlinePlayers.PlayerForCharacter(characterID)
	if !found || presence.Session != session || presence.TownID != originTown || presence.AreaID != originArea {
		t.Fatalf("post-return town actor=%+v found=%t", presence, found)
	}

	// The first town op35 must consume nothing from the retired dungeon and
	// update only the selected character's town location.
	session.conn.(*bufferConn).write.Reset()
	if err := s.handleTownSetUserPosition(session, buildTownPositionRequest(470, 245, 4, 91)); err != nil {
		t.Fatalf("op35 after completed return: %v", err)
	}
	if session.conn.(*bufferConn).write.Len() != 0 {
		t.Fatalf("op35 after completed return retransmitted return packets: %x", session.conn.(*bufferConn).write.Bytes())
	}
	assertHatcheryTownStateForR129(t, session, hatchery, originTown, originArea, 470, 245, 4, 91)
	presence, found = s.onlinePlayers.PlayerForCharacter(characterID)
	if !found || presence.Session != session || presence.PositionX != 470 || presence.PositionY != 245 {
		t.Fatalf("op35 did not update post-return actor=%+v found=%t", presence, found)
	}

	// Legacy type1345 is the native scene-ready boundary. It must complete the
	// deferred tail without resurrecting the detached dungeon runtime.
	session.conn.(*bufferConn).write.Reset()
	if err := s.handleLegacyGamePacket(session, buildLegacyTownSceneReadyAckPacket()); err != nil {
		t.Fatalf("post-return scene-ready: %v", err)
	}
	if !session.initialTownLegacySceneReadyAccepted || !session.sceneBootstrapTailSent || session.sceneBootstrapTailDeferred {
		t.Fatalf("post-return scene-ready accepted=%t sent=%t deferred=%t",
			session.initialTownLegacySceneReadyAccepted,
			session.sceneBootstrapTailSent,
			session.sceneBootstrapTailDeferred)
	}
	assertHatcheryReturnDetachedForR129(t, session, hatchery)

	// A normal same-town area transition after scene-ready must move the actor
	// and persisted location while leaving no dungeon owner behind.
	session.conn.(*bufferConn).write.Reset()
	if err := s.handleTownSetUserArea(session, buildTownMoveRequest(originTown, 0, 600, 250, 5)); err != nil {
		t.Fatalf("post-return op36: %v", err)
	}
	if session.conn.(*bufferConn).write.Len() == 0 {
		t.Fatal("post-return op36 emitted no town transition")
	}
	if session.dungeon.runtime != nil {
		t.Fatalf("post-return op36 resurrected runtime=%p", session.dungeon.runtime)
	}
	session.townMu.Lock()
	if session.initialTownRouteCharacterID != 0 || session.initialTownRouteStage != currentInitialTownRouteIdle ||
		session.townSceneReadyCharacterID != characterID {
		t.Fatalf("post-return op36 route char=%d stage=%d ready=%d",
			session.initialTownRouteCharacterID, session.initialTownRouteStage, session.townSceneReadyCharacterID)
	}
	session.townMu.Unlock()
	presence, found = s.onlinePlayers.PlayerForCharacter(characterID)
	if !found || presence.Session != session || presence.TownID != originTown || presence.AreaID != byte(destination.ID) ||
		presence.PositionX != 600 || presence.PositionY != 250 {
		t.Fatalf("post-return op36 actor=%+v found=%t destination=%+v", presence, found, destination)
	}
	session.townMu.Lock()
	if tailOwner := session.completedTownSceneTailOwner; tailOwner != nil {
		t.Logf("inert completed-tail owner remains char=%d conn=%q owner_context=%d",
			tailOwner.characterID, tailOwner.connectionID, tailOwner.ownerContext)
	}
	session.townMu.Unlock()
	repositories, ok := s.repositoryGroup()
	if !ok {
		t.Fatal("repository group unavailable")
	}
	stored, found, err := repositories.Character.Load(context.Background(), "99")
	if err != nil || !found || stored.Stats["town_id"] != int64(originTown) || stored.Stats["area_id"] != destination.ID ||
		stored.Stats["pos_x"] != 600 || stored.Stats["pos_y"] != 250 || stored.Stats["direction"] != 5 {
		t.Fatalf("post-return op36 persisted found=%t err=%v stats=%+v", found, err, stored.Stats)
	}

	// The first op35 in the new area must bind to the new scene, not the old
	// completed return or the original room, and it must not emit dungeon data.
	session.conn.(*bufferConn).write.Reset()
	if err := s.handleTownSetUserPosition(session, buildTownPositionRequest(610, 260, 7, 77)); err != nil {
		t.Fatalf("op35 after post-return op36: %v", err)
	}
	if session.conn.(*bufferConn).write.Len() != 0 {
		t.Fatalf("op35 after post-return op36 wrote dungeon bytes: %x", session.conn.(*bufferConn).write.Bytes())
	}
	assertHatcheryReturnDetachedForR129(t, session, hatchery)
	session.townMu.Lock()
	snapshot := session.townPositionSnapshot
	session.townMu.Unlock()
	if snapshot.CharacterID != characterID || snapshot.TownID != originTown || snapshot.AreaID != byte(destination.ID) ||
		snapshot.PositionX != 610 || snapshot.PositionY != 260 || snapshot.MovementCode != 7 || !snapshot.PositionValid {
		t.Fatalf("post-return op36/op35 snapshot=%+v", snapshot)
	}
	presence, found = s.onlinePlayers.PlayerForCharacter(characterID)
	if !found || presence.Session != session || presence.TownID != originTown || presence.AreaID != byte(destination.ID) ||
		presence.PositionX != 610 || presence.PositionY != 260 {
		t.Fatalf("post-return op36/op35 actor=%+v found=%t", presence, found)
	}
}

func assertHatcheryReturnDetachedForR129(
	t *testing.T,
	session *gameSession,
	completed *runtimeDungeonState,
) {
	t.Helper()
	if session.dungeon.runtime != nil {
		t.Fatalf("completed hatchery still owns session dungeon runtime=%p, want nil", session.dungeon.runtime)
	}
	if completed == nil || completed.Session == nil || !completed.townReturnPending || !completed.townReturnOp24Sent {
		t.Fatalf("completed return receipt missing: %+v", completed)
	}
	if completed.townReturnRequestMsgID != uint16(dnfenum.CmdPacketEplpCommand) {
		t.Fatalf("completed return request=%d want op72", completed.townReturnRequestMsgID)
	}
	if completed.Session.Snapshot().Run.Status != worldmap.DungeonRunCompleted {
		t.Fatalf("completed return status=%s", completed.Session.Snapshot().Run.Status)
	}
	if session.confirmedDungeonReturnStatePending || session.returnTownFinishLoadingAckOnly || session.backToVillageEnterSelectPending {
		t.Fatalf("completed return left session pending=%t ack_only=%t enter_select_pending=%t",
			session.confirmedDungeonReturnStatePending,
			session.returnTownFinishLoadingAckOnly,
			session.backToVillageEnterSelectPending)
	}
}

func assertHatcheryTownStateForR129(
	t *testing.T,
	session *gameSession,
	completed *runtimeDungeonState,
	townID, areaID byte,
	x, y uint16,
	direction byte,
	opaque uint16,
) {
	t.Helper()
	assertHatcheryReturnDetachedForR129(t, session, completed)
	session.townMu.Lock()
	ready := session.townSceneReadyCharacterID
	snapshot := session.townPositionSnapshot
	session.townMu.Unlock()
	if ready != session.selectedCharacterID || snapshot.CharacterID != session.selectedCharacterID ||
		snapshot.TownID != townID || snapshot.AreaID != areaID ||
		snapshot.PositionX != x || snapshot.PositionY != y ||
		snapshot.MovementCode != direction || snapshot.OpaqueScaledU16 != opaque || !snapshot.PositionValid {
		t.Fatalf("post-return town ready=%d snapshot=%+v", ready, snapshot)
	}
}

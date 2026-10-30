package dnfbridge

import (
	"bytes"
	"context"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnftown "longheng.io/server/internal/modules/dnf/town"
)

// This follows the live Luke 5056 result -> town-select return -> stage-rest
// SET_USER_AREA -> legacy op37 chain. The return handler itself arms the
// ACK-only generation; the test does not assign that gate. Town presence is
// installed only through the production SET_USER_AREA handler.
func TestRealPVFLukeStageRestCopresenceAfterConfirmedReturnAreaRegistration(t *testing.T) {
	t.Setenv(currentTownCopresenceResendEnv, cloneAvatarAppearanceOn)
	f, local := prepareR165LukeSpreadSanctumRealResultFixture(t)
	service := f.service
	// The result fixture starts after login. Retain the same channel owner that
	// game bootstrap establishes before the dungeon is entered; SET_USER_AREA
	// restores town ownership from this retained connection context.
	local.connectionTownActorOwnerChannel = byte(local.channel.ID)
	// The reused phase fixture inherits Anton's key prefix. Real Luke waiting
	// admission also checks the server/channel encoded in that key.
	service.raidMu.Lock()
	oldKey := f.state.RaidKey
	newKey := uint32(service.channelAdvertiseID())<<24 | uint32(local.channel.ID)<<16 | oldKey&0xffff
	delete(service.raids, oldKey)
	f.state.RaidKey = newKey
	service.raids[newKey] = f.state
	local.dungeon.runtime.raidEntry.RaidKey = newKey
	service.raidMu.Unlock()
	ctx := context.Background()
	towns, err := dnftown.Load(ctx, service.initialEquipmentArchive, dnftown.Options{})
	if err != nil {
		t.Fatal(err)
	}
	service.townCatalog = towns
	if !service.currentLukeWaitingArea(32, 4) {
		t.Fatal("real PVF did not resolve Luke stage-rest town 32/4")
	}
	service.onlinePlayers = newOnlinePlayerManager()

	localConn := f.conn
	localConn.write.Reset()
	if err := service.handleDungeonEplpCommand(local, []byte{1, 2}); err != nil {
		t.Fatalf("real Luke result return: %v", err)
	}
	if local.dungeon.runtime != nil {
		t.Fatal("real Luke result return retained the dungeon runtime")
	}
	if local.returnTownFinishLoadingAckOnly {
		t.Fatal("completed result route unexpectedly armed the select-return ACK-only generation")
	}
	returnedPresence, found := service.onlinePlayers.PlayerForCharacter(19)
	if !found || returnedPresence.Session != local {
		t.Fatalf("completed return did not publish town presence: %+v found=%t", returnedPresence, found)
	}

	// Model the already-confirmed selector context that precedes the live
	// no-runtime op132 branch. The ACK-only flag itself is set only by the real
	// production handler below.
	character, found, err := f.repos.Character.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load stage-rest character found=%t err=%v", found, err)
	}
	if character.Stats == nil {
		character.Stats = map[string]int64{}
	}
	character.Stats["town_id"] = 32
	character.Stats["area_id"] = 4
	character.Stats["pos_x"] = 580
	character.Stats["pos_y"] = 243
	character.Stats["direction"] = 5
	character.Stats["area_state"] = 3
	if err := f.repos.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	bindDungeonSelectorOriginForTestAt(t, service, local, 32, 4, 580, 243)
	localConn.write.Reset()
	if err := service.sendEnterSelectDungeonState(local, "real_luke_stage_rest_selector_context", false, true); err != nil {
		t.Fatalf("real stage-rest selector context: %v", err)
	}
	if !local.enterSelectDungeonSent || !local.enterSelectDungeonAckSent || !local.enterSelectDungeonContextSent {
		t.Fatal("production selector request did not establish the confirmed op132 return context")
	}
	localConn.write.Reset()
	if err := service.handleDungeonBackToVillage(local, nil); err != nil {
		t.Fatalf("confirmed no-runtime stage-rest return: %v", err)
	}
	if !local.returnTownFinishLoadingAckOnly || !local.confirmedDungeonReturnStatePending {
		t.Fatalf("production return did not arm ACK-only/pending: ack_only=%t pending=%t",
			local.returnTownFinishLoadingAckOnly, local.confirmedDungeonReturnStatePending)
	}
	stillOldArea, found := service.onlinePlayers.PlayerForCharacter(19)
	if !found || stillOldArea.TownID != returnedPresence.TownID || stillOldArea.AreaID != returnedPresence.AreaID ||
		stillOldArea.ChannelID != returnedPresence.ChannelID || stillOldArea.Instance != returnedPresence.Instance {
		t.Fatalf("op132 unexpectedly registered the stage-rest area before SET_USER_AREA: before=%+v after=%+v found=%t",
			returnedPresence, stillOldArea, found)
	}

	peerConn := &bufferConn{}
	peer := &gameSession{
		conn:                            peerConn,
		connID:                          "luke-stage-rest-peer",
		channel:                         local.channel,
		residentChannel:                 local.channel,
		selectedCharacterID:             20,
		accountID:                       "account-20",
		loginPacketSeen:                 true,
		accountFromToken:                true,
		connectionTownActorOwnerChannel: byte(local.channel.ID),
		townActorOwnerChannel:           byte(local.channel.ID),
	}
	service.bindGameSessionCharacter(peer, 20)
	peerCharacter, found, err := f.repos.Character.Load(ctx, "20")
	if err != nil || !found {
		t.Fatalf("load stage-rest peer found=%t err=%v", found, err)
	}
	peerCharacter.Name = "peer"
	peerCharacter.Job = "0"
	peerCharacter.Level = 90
	peerCharacter.Stats = map[string]int64{
		"town_id": 32, "area_id": 4, "pos_x": 620, "pos_y": 210,
		"direction": 5, "area_state": 3,
	}
	if err := f.repos.Character.Save(ctx, peerCharacter); err != nil {
		t.Fatal(err)
	}

	service.raidMu.Lock()
	raidState := service.runtimeRaidByMemberLocked(19)
	if raidState == nil || raidState.Battle == nil {
		service.raidMu.Unlock()
		t.Fatal("real Luke stage-rest raid state is missing")
	}
	raidState.CurrentLayout, raidState.RouteOrRaidType = true, 1
	memberFound := false
	for _, member := range raidState.Members {
		memberFound = memberFound || member.CharID == 20
	}
	if !memberFound {
		raidState.Members = append(raidState.Members, runtimeRaidMemberState{CharID: 20, GroupIndex: 2})
	}
	if raidState.Battle.MemberAccounts == nil {
		raidState.Battle.MemberAccounts = make(map[uint16]string)
	}
	raidState.Battle.MemberAccounts[20] = "account-20"
	if raidState.Battle.MemberGroups == nil {
		raidState.Battle.MemberGroups = make(map[uint16]byte)
	}
	raidState.Battle.MemberGroups[20] = 2
	service.raidMu.Unlock()

	service.markCurrentTownSceneReady(peer)
	peerConn.write.Reset()
	if err := service.handleTownSetUserArea(peer, buildTownMoveRequest(32, 4, 620, 210, 5)); err != nil {
		t.Fatalf("peer SET_USER_AREA: %v", err)
	}
	if _, found := service.onlinePlayers.PlayerForCharacter(20); !found {
		t.Fatalf("peer SET_USER_AREA did not register stage-rest presence: %s", f.logText(t))
	}

	localConn.write.Reset()
	if err := service.handleTownSetUserArea(local, buildTownMoveRequest(32, 4, 580, 243, 5)); err != nil {
		t.Fatalf("local SET_USER_AREA: %v", err)
	}
	if !service.onlinePlayers.PeerInSameArea(19, 20) {
		t.Fatal("real SET_USER_AREA calls did not place both characters in the same stage-rest scope")
	}
	local.townMu.Lock()
	readyCharacterID := local.townSceneReadyCharacterID
	local.townMu.Unlock()
	if readyCharacterID != 19 || currentTownActorOwnerContext(local) == currentSceneObjectContext || sessionHasActiveDungeonRuntime(local) {
		t.Fatalf("helper prerequisites not naturally satisfied: town_ready=%d owner=%d active_runtime=%t",
			readyCharacterID, currentTownActorOwnerContext(local), sessionHasActiveDungeonRuntime(local))
	}
	local.townCopresenceResendMu.Lock()
	resendMarker := local.townCopresenceResendKey
	local.townCopresenceResendMu.Unlock()
	if resendMarker != "" {
		t.Fatalf("real stage-rest area registration retained stale co-presence marker %q", resendMarker)
	}
	if !local.returnTownFinishLoadingAckOnly || local.confirmedDungeonReturnStatePending {
		t.Fatalf("SET_USER_AREA did not leave the natural ACK-only generation: ack_only=%t pending=%t",
			local.returnTownFinishLoadingAckOnly, local.confirmedDungeonReturnStatePending)
	}

	finish := buildLegacyGamePacketForBridgeTest(
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.GameTypeFinishLoading),
		0,
		make([]byte, currentFinishLoadingLegacyRequestBodySize),
	)
	dispatch := func(session *gameSession) []upperFrame {
		t.Helper()
		if err := service.handleGameStreamPacket(session, dnfproto.LatestGameStreamPacket{
			Kind: dnfproto.LatestGameStreamLegacy,
			Data: append([]byte(nil), finish...),
		}); err != nil {
			t.Fatal(err)
		}
		return writeFrames(t, session)
	}

	localConn.write.Reset()
	peerConn.write.Reset()
	localFrames := dispatch(local)
	assertReturnAckAndPeerActor(t, localFrames)
	if got := peerConn.write.Len(); got != 0 {
		t.Fatalf("local loading ACK wrote %d bytes to the peer connection, want 0", got)
	}

	// A duplicate op37 remains idempotent: the ACK is repeatable, but the peer
	// actor projection is not replayed for the same area and peer set.
	duplicate := dispatch(local)
	if len(duplicate) != 1 || duplicate[0].msgID != uint16(dnfenum.CmdPacketFinishLoading) ||
		!bytes.Equal(duplicate[0].body, []byte{1}) {
		t.Fatalf("duplicate ACK-only op37 frames=%v, want one empty success ACK", frameIDs(duplicate))
	}
}

func assertReturnAckAndPeerActor(t *testing.T, frames []upperFrame) {
	t.Helper()
	if len(frames) < 4 || frames[0].msgID != uint16(dnfenum.CmdPacketFinishLoading) ||
		!bytes.Equal(frames[0].body, []byte{1}) {
		t.Fatalf("return op37 frames=%v, want a leading empty success ACK and peer actor", frameIDs(frames))
	}
	modeFrames, areaFrames := 0, 0
	for _, frame := range frames {
		if frame.msgID == uint16(dnfenum.CmdPacketSetUDPIPPort) {
			modeFrames++
		}
		if frame.msgID == currentTownUserAreaNotificationMsgID {
			areaFrames++
		}
		if frame.msgID == uint16(dnfenum.CmdPacketFinishLoading) && len(frame.body) == currentFinishLoadingCharacterStateBodySize {
			t.Fatalf("return ACK-only replay emitted the full op37 character snapshot: %v", frameIDs(frames))
		}
	}
	if modeFrames < 2 || areaFrames == 0 {
		t.Fatalf("return loading ACK did not reinstall the peer actor: frames=%v", frameIDs(frames))
	}
}

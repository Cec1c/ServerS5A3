package dnfbridge

import (
	"context"
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnftown "longheng.io/server/internal/modules/dnf/town"
)

// P05 town-area regression. These cases cross the real SET_USER_AREA handler
// and then cross the real finish-loading command. Assertions are deliberately
// made against emitted wire events rather than internal helper calls:
//   - a same-party old-area peer must receive op23, never op6;
//   - after scene rebuild reports finish-loading, the finishing receiver must
//     still receive an authoritative op153 carrying both party HP rows.
//
// The cross-area cases are expected to be red before a post-load repair includes
// members outside the receiver's current area bucket. They do not claim that a
// final op153 alone proves the client never rendered a transient zero HP frame.
func TestP05TownCrossAreaFinishLoadingPartyHPOrder(t *testing.T) {
	const (
		targetID = uint16(29)
		peerID   = uint16(17)
	)

	t.Run("alone_moves_while_peer_remains", func(t *testing.T) {
		fixture := newTownPartyActorHPFixture(t, true)
		p05CrossAreaPrepareSharedTown(t, fixture)

		// Character 29 leaves the shared town area. Character 17 remains in
		// the old area, so the old peer must retain party identity via op23.
		p05CrossAreaMoveTown(t, fixture.service, fixture.target, 38, 2, 900, 250, 5)
		peerMoveFrames := p05Frames(t, fixture.service, fixture.actor)
		p05CrossAreaRequireFrame(t, peerMoveFrames, currentTownUserAreaNotificationMsgID)
		p05CrossAreaRequireNoLeave(t, peerMoveFrames, targetID)

		finishFrames := p05CrossAreaFinishTownLoading(t, fixture.service, fixture.target)
		p05CrossAreaRequireNoLeave(t, finishFrames, targetID, peerID)
		p05CrossAreaRequirePartyHP(t, finishFrames, targetID, peerID, 100)
	})

	t.Run("pair_moves_to_same_area", func(t *testing.T) {
		fixture := newTownPartyActorHPFixture(t, true)
		p05CrossAreaPrepareSharedTown(t, fixture)

		p05CrossAreaMoveTown(t, fixture.service, fixture.target, 38, 2, 900, 250, 5)
		peerFrames := p05Frames(t, fixture.service, fixture.actor)
		p05CrossAreaRequireFrame(t, peerFrames, currentTownUserAreaNotificationMsgID)
		p05CrossAreaRequireNoLeave(t, peerFrames, targetID)

		pairFrames := p05CrossAreaMoveTown(t, fixture.service, fixture.actor, 38, 2, 870, 248, 6)
		p05CrossAreaRequireNoLeave(t, pairFrames, peerID)
		// The peer's same-area entry also projects into the target connection.
		// Drain it so the target's finish-loading assertion cannot pass on a
		// pre-load op153 emitted before the real second handler.
		_ = p05Frames(t, fixture.service, fixture.target)

		targetFinish := p05CrossAreaFinishTownLoading(t, fixture.service, fixture.target)
		p05CrossAreaRequireNoLeave(t, targetFinish, targetID, peerID)
		p05CrossAreaRequirePartyHP(t, targetFinish, targetID, peerID, 100)

		peerFinish := p05CrossAreaFinishTownLoading(t, fixture.service, fixture.actor)
		p05CrossAreaRequireNoLeave(t, peerFinish, targetID, peerID)
		p05CrossAreaRequirePartyHP(t, peerFinish, targetID, peerID, 100)
	})

	t.Run("staggered_return_first_before_peer_presence", func(t *testing.T) {
		fixture := newTownPartyActorHPFixture(t, true)
		p05CrossAreaPrepareSharedTown(t, fixture)

		p05CrossAreaMoveTown(t, fixture.service, fixture.target, 38, 2, 900, 250, 5)
		p05Frames(t, fixture.service, fixture.actor)
		p05CrossAreaMoveTown(t, fixture.service, fixture.actor, 38, 2, 870, 248, 6)
		p05Frames(t, fixture.service, fixture.target)

		// The first returner finishes while the other member is still in the
		// source area. This is the ordering that distinguishes a same-area-only
		// finish repair from a current-party HUD repair.
		p05CrossAreaMoveTown(t, fixture.service, fixture.target, 38, 0, 900, 250, 5)
		peerFrames := p05Frames(t, fixture.service, fixture.actor)
		p05CrossAreaRequireFrame(t, peerFrames, currentTownUserAreaNotificationMsgID)
		p05CrossAreaRequireNoLeave(t, peerFrames, targetID)

		firstFinish := p05CrossAreaFinishTownLoading(t, fixture.service, fixture.target)
		p05CrossAreaRequireNoLeave(t, firstFinish, targetID, peerID)
		p05CrossAreaRequirePartyHP(t, firstFinish, targetID, peerID, 100)

		p05CrossAreaMoveTown(t, fixture.service, fixture.actor, 38, 0, 870, 248, 6)
		secondFinish := p05CrossAreaFinishTownLoading(t, fixture.service, fixture.actor)
		p05CrossAreaRequireNoLeave(t, secondFinish, targetID, peerID)
		p05CrossAreaRequirePartyHP(t, secondFinish, targetID, peerID, 100)
	})
}

// newTownPartyActorHPFixture stores both characters in 38/1. That area is the
// private Seria room in the fixture table, so it cannot serve as the shared
// departure area. Normalize both sessions to shared 38/0 through the real town
// handler before each scenario. All preparation packets are drained so later
// finish-loading assertions cannot pass on a pre-rebuild op153.
func p05CrossAreaPrepareSharedTown(t *testing.T, fixture *townPartyActorHPFixture) {
	t.Helper()
	startTownPeerTest(t, fixture.service, fixture.target, nil)
	startTownPeerTest(t, fixture.service, fixture.actor, nil)
	// This synthetic catalog models two public areas in one town; crossing to
	// another town would require a separate portal/transport authorization.
	table, err := dnftown.Load(context.Background(), townMoveTestSource{
		"town/town.lst": "38 `new_Elvengard.twn`",
		"town/new_Elvengard.twn": "[area]\n0 `new_Elvengard.map` `[normal]`\n[/area]\n" +
			"[area]\n1 `new_seria_room.map` `[gate]` 450 234\n[/area]\n" +
			"[area]\n2 `new_D_Elvengard_L.map` `[normal]`\n[/area]\n[name]\n`Elvengard`",
	}, dnftown.Options{})
	if err != nil {
		t.Fatal(err)
	}
	fixture.service.townCatalog = table
	p05CrossAreaMoveTown(t, fixture.service, fixture.target, 38, 0, 900, 250, 5)
	_ = p05Frames(t, fixture.service, fixture.actor)
	p05CrossAreaMoveTown(t, fixture.service, fixture.actor, 38, 0, 870, 248, 6)
	_ = p05Frames(t, fixture.service, fixture.target)
}

func p05CrossAreaMoveTown(
	t *testing.T,
	service *Service,
	session *gameSession,
	townID, areaID byte,
	x, y int16,
	direction byte,
) []upperFrame {
	t.Helper()
	if err := service.handleTownSetUserArea(
		session,
		buildTownMoveRequest(townID, areaID, x, y, direction),
	); err != nil {
		t.Fatalf("town area move town=%d area=%d: %v", townID, areaID, err)
	}
	info, found := service.onlinePlayers.PlayerForCharacter(session.selectedCharacterID)
	if !found || info.Session != session || info.TownID != townID || info.AreaID != areaID {
		t.Fatalf("town move did not publish requested area %d/%d: found=%v actual=%+v", townID, areaID, found, info)
	}
	return p05Frames(t, service, session)
}

func p05CrossAreaFinishTownLoading(
	t *testing.T,
	service *Service,
	session *gameSession,
) []upperFrame {
	t.Helper()

	// This is the established readiness state for the ordinary town finish
	// path. SET_USER_AREA starts a fresh generation and clears it, so restore
	// it after the real area handler and before crossing the real second
	// handler. The test does not call helper internals as an assertion.
	session.townMu.Lock()
	session.initialTownRouteCharacterID = session.selectedCharacterID
	session.initialTownRouteStage = currentInitialTownRoutePlayerStateSent
	session.townMu.Unlock()

	if err := service.handleGameCommand(
		session,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.GameTypeFinishLoading),
		make([]byte, 8),
	); err != nil {
		t.Fatalf("finish-loading handler: %v", err)
	}
	frames := p05Frames(t, service, session)
	p05CrossAreaRequireFrame(t, frames, uint16(dnfenum.CmdPacketFinishLoading))
	return frames
}

func p05CrossAreaRequireFrame(t *testing.T, frames []upperFrame, msgID uint16) {
	t.Helper()
	for _, frame := range frames {
		if frame.msgID == msgID {
			return
		}
	}
	t.Fatalf("missing class0 msg %d in wire sequence %v", msgID, townPartyActorHPFrameIDs(frames))
}

func p05CrossAreaRequireNoLeave(t *testing.T, frames []upperFrame, characterIDs ...uint16) {
	t.Helper()
	want := make(map[uint16]bool, len(characterIDs))
	for _, id := range characterIDs {
		want[id] = true
	}
	for _, frame := range frames {
		if frame.msgID != currentTownUserLeaveMsgID || len(frame.body) < 2 {
			continue
		}
		id := binary.LittleEndian.Uint16(frame.body[len(frame.body)-2:])
		if want[id] {
			t.Fatalf("same-party character %d was removed with op6; frames=%v",
				id, townPartyActorHPFrameIDs(frames))
		}
	}
}

func p05CrossAreaRequirePartyHP(
	t *testing.T,
	frames []upperFrame,
	targetID, peerID uint16,
	want byte,
) {
	t.Helper()
	found := false
	for _, frame := range frames {
		if frame.msgID != 0x0099 {
			continue
		}
		rows := parseRealtimeInfoRows(t, frame.body)
		targetHP, targetFound := p05CrossAreaHPFor(rows, targetID)
		peerHP, peerFound := p05CrossAreaHPFor(rows, peerID)
		if !targetFound || !peerFound {
			continue
		}
		if targetHP != want || peerHP != want {
			t.Fatalf("post-load party HP target=%d peer=%d, want both %d; rows=%+v",
				targetHP, peerHP, want, rows)
		}
		found = true
	}
	if !found {
		t.Fatalf("finish-loading emitted no authoritative op153 containing both party rows %d/%d; frames=%v",
			targetID, peerID, townPartyActorHPFrameIDs(frames))
	}
}

func p05CrossAreaHPFor(rows []realtimeInfoRow, id uint16) (byte, bool) {
	for _, row := range rows {
		if row.uid == id {
			return row.hp, true
		}
	}
	return 0, false
}

func TestP05TownCrossAreaPostTransitionPartyHP(t *testing.T) {
	fixture := newTownPartyActorHPFixture(t, true)
	p05CrossAreaPrepareSharedTown(t, fixture)
	p05CrossAreaMoveTown(t, fixture.service, fixture.target, 38, 2, 900, 250, 5)
	_ = p05Frames(t, fixture.service, fixture.actor)
	if err := fixture.service.sendCurrentTownPostTransitionPlayerState(fixture.target, "p05_cross_area_post_transition"); err != nil {
		t.Fatal(err)
	}
	frames := p05Frames(t, fixture.service, fixture.target)
	p05CrossAreaRequirePartyHP(t, frames, 29, 17, 100)
	p05CrossAreaRequireNoLeave(t, frames, 29, 17)
	for _, frame := range frames {
		if frame.msgID == 9 || frame.msgID == 11 {
			t.Fatalf("cross-area HP repair redeclared party or restarted peer link: %v", townPartyActorHPFrameIDs(frames))
		}
	}
}

// A stale session cache is deliberately retained after central membership is
// removed. Finishing a scene must not resurrect that retired party in the HUD.
func TestP05TownCrossAreaFinishAfterPeerLeaves(t *testing.T) {
	fixture := newTownPartyActorHPFixture(t, true)
	p05CrossAreaPrepareSharedTown(t, fixture)
	p05CrossAreaMoveTown(t, fixture.service, fixture.target, 38, 2, 900, 250, 5)
	_ = p05Frames(t, fixture.service, fixture.actor)
	fixture.service.runtimePartyManagerForService().LeaveCurrent(17)
	frames := p05CrossAreaFinishTownLoading(t, fixture.service, fixture.target)
	for _, frame := range frames {
		if frame.msgID == 153 || frame.msgID == 9 || frame.msgID == 11 {
			t.Fatalf("scene finish revived retired party: %v", townPartyActorHPFrameIDs(frames))
		}
	}
}

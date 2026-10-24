package dnfbridge

// Minimal two-character town/party fixture extracted from the private HP test
// solely because the accepted cross-scene profile tests reuse it. The HP
// production helper and all HP-specific test cases are intentionally excluded
// from this split.

import (
	"context"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

type townPartyActorHPFixture struct {
	service    *Service
	target     *gameSession
	actor      *gameSession
	targetConn *bufferConn
	actorConn  *bufferConn
	actorInfo  onlinePlayerInfo
}

func newTownPartyActorHPFixture(t *testing.T, managed bool) *townPartyActorHPFixture {
	t.Helper()
	service, target, repositories := newTownMoveTest(t)
	service.options.currentClientVersion = "official-24.9"
	service.options.gameUpperBodyCodec = gameUpperBodyCodecPlain
	service.onlinePlayers = newOnlinePlayerManager()
	service.bindGameSessionCharacter(target, target.selectedCharacterID)

	if err := repositories.Character.Save(context.Background(), dnfrepo.CharacterRecord{
		CharacterID: "17",
		AccountID:   "dnf:2",
		Name:        "peer",
		Job:         "1",
		Level:       2,
		Stats: map[string]int64{
			"town_id": 38, "area_id": 1, "pos_x": 400, "pos_y": 200,
			"direction": 5, "area_state": 3,
		},
	}); err != nil {
		t.Fatalf("save peer character: %v", err)
	}

	actorConn := &bufferConn{}
	actor := &gameSession{
		conn:                            actorConn,
		connID:                          "town-party-actor-hp-peer",
		channel:                         target.channel,
		residentChannel:                 target.residentChannel,
		selectedCharacterID:             17,
		townSceneReadyCharacterID:       17,
		townActorOwnerChannel:           byte(target.channel.ID),
		connectionTownActorOwnerChannel: byte(target.channel.ID),
	}
	service.bindGameSessionCharacter(actor, 17)
	service.onlinePlayers.EnterArea(&onlinePlayerInfo{
		CharacterID: 29,
		AccountID:   "dnf:1",
		Name:        "target",
		Job:         1,
		Level:       1,
		TownID:      38,
		AreaID:      1,
		PositionX:   450,
		PositionY:   234,
		Direction:   5,
		AreaState:   3,
		Session:     target,
	})
	service.onlinePlayers.EnterArea(&onlinePlayerInfo{
		CharacterID: 17,
		AccountID:   "dnf:2",
		Name:        "peer",
		Job:         1,
		Level:       2,
		TownID:      38,
		AreaID:      1,
		PositionX:   400,
		PositionY:   200,
		Direction:   5,
		AreaState:   3,
		Session:     actor,
	})

	if managed {
		ok, partyID, reason, members := service.forceManagedRuntimeParty(29, 17)
		if !ok {
			t.Fatalf("force managed town party: party=%d members=%d reason=%s", partyID, members, reason)
		}
	} else {
		state := townPartyActorHPPartyState()
		storeRuntimePartyState(target, state)
		storeRuntimePartyState(actor, state)
	}

	targetConn := target.conn.(*bufferConn)
	targetConn.write.Reset()
	actorConn.write.Reset()
	actorInfo, found := service.onlinePlayers.PlayerForCharacter(17)
	if !found {
		t.Fatal("town party fixture lost the actor presence row")
	}
	return &townPartyActorHPFixture{
		service:    service,
		target:     target,
		actor:      actor,
		targetConn: targetConn,
		actorConn:  actorConn,
		actorInfo:  actorInfo,
	}
}

func townPartyActorHPPartyState() alignedcmd.PartyState {
	return alignedcmd.PartyState{
		PartyID:    1,
		UserID:     29,
		UserState:  1,
		IsLeader:   true,
		MaxMembers: 4,
		Members: []alignedcmd.PartyMemberState{
			{UserID: 29, UserState: 1, HPPercent: 100, MPPercent: 100},
			{UserID: 17, UserState: 1, HPPercent: 100, MPPercent: 100},
		},
	}
}

func (fixture *townPartyActorHPFixture) reset() {
	fixture.targetConn.write.Reset()
	fixture.actorConn.write.Reset()
}

func townPartyActorHPFrameIDs(frames []upperFrame) []uint16 {
	ids := make([]uint16, 0, len(frames))
	for _, frame := range frames {
		ids = append(ids, frame.msgID)
	}
	return ids
}

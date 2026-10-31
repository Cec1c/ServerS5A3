package dnfbridge

import (
	"bytes"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

// A replaced/auxiliary socket can lose its local party projection while the
// central manager still contains both members. Crossing town areas must send
// that peer an area update rather than the actor/party removal packet.
func TestTownPartyAreaChangeKeepsManagedPeerWhenSessionProjectionWasLost(t *testing.T) {
	for _, tc := range []struct {
		name, profile, switchValue string
		wantID                     uint16
	}{
		{"official default", "official-24.9", "", currentTownUserAreaNotificationMsgID},
		{"official enabled", "official-24.9", "on", currentTownUserAreaNotificationMsgID},
		{"official rollback", "official-24.9", "off", currentTownUserLeaveMsgID},
		{"legacy default", "90cn-decode-bypass-v1", "", currentTownUserLeaveMsgID},
		{"legacy enabled", "90cn-decode-bypass-v1", "on", currentTownUserAreaNotificationMsgID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(townCopresencePartyAuthoritativeEnv, tc.switchValue)
			h := newPartyForceHarness(t)
			h.service.onlinePlayers = newOnlinePlayerManager()
			h.service.options.currentClientVersion = tc.profile
			if ok, _, reason, _ := h.service.forceManagedRuntimeParty(1001, 1002); !ok {
				t.Fatal(reason)
			}
			state := h.service.runtimePartyStateForSession(h.leader)
			storeRuntimePartyState(h.member, alignedcmd.PartyState{})
			outside := &gameSession{conn: &bufferConn{}, selectedCharacterID: 4000, channel: h.member.channel}
			startTownPeerTest(t, h.service, h.leader, &onlinePlayerInfo{
				CharacterID: 1001, TownID: 31, AreaID: 3, PositionX: 120, PositionY: 240,
				Direction: 1, AreaState: 1, Session: h.leader,
			})
			startTownPeerTest(t, h.service, h.member, &onlinePlayerInfo{
				CharacterID: 1002, TownID: 31, AreaID: 2, Session: h.member,
			})
			startTownPeerTest(t, h.service, outside, &onlinePlayerInfo{
				CharacterID: 4000, TownID: 31, AreaID: 2, Session: outside,
			})
			h.memberConn.write.Reset()
			outside.conn.(*bufferConn).write.Reset()
			mover, leaderPresent := h.service.onlinePlayers.PlayerForCharacter(1001)
			member, memberPresent := h.service.onlinePlayers.PlayerForCharacter(1002)
			outsiderPresence, outsidePresent := h.service.onlinePlayers.PlayerForCharacter(4000)
			if !leaderPresent || !memberPresent || !outsidePresent {
				t.Fatalf("town presence missing: leader=%t member=%t outsider=%t",
					leaderPresent, memberPresent, outsidePresent)
			}
			h.service.broadcastTownPlayerAreaChange(&mover, []onlinePlayerInfo{member, outsiderPresence}, state)
			drainTownPeerTest(t, h.service, h.member)
			drainTownPeerTest(t, h.service, outside)
			packet, trailing := splitGameServerUpperPacketWithHeader(t, h.memberConn.write.Bytes(), dnfproto.GameServerUpperHeaderSize16)
			if len(trailing) != 0 || packet.Header.MsgID != tc.wantID || packet.Header.Classification != 0 {
				t.Fatalf("peer area-change packet = %+v trailing=%x; want exactly class0 op%d", packet, trailing, tc.wantID)
			}
			want := buildTownUserLeaveBody(1001)
			if tc.wantID == currentTownUserAreaNotificationMsgID {
				want = buildCurrentTownUserAreaNotificationBody(currentSceneActorObjectKey(1001), 31, 3, 120, 240, 1, 1)
				if restored := runtimePartyStateSnapshot(h.member); restored.PartyID != state.PartyID || len(runtimePartyMembers(restored)) != 2 {
					t.Fatalf("managed peer projection was not restored: %+v", restored)
				}
			}
			if !bytes.Equal(packet.Body, want) {
				t.Fatalf("peer body %x want %x", packet.Body, want)
			}
			outsider, trailing := splitGameServerUpperPacketWithHeader(t, outside.conn.(*bufferConn).write.Bytes(), dnfproto.GameServerUpperHeaderSize16)
			if len(trailing) != 0 || outsider.Header.MsgID != currentTownUserLeaveMsgID || !bytes.Equal(outsider.Body, buildTownUserLeaveBody(1001)) {
				t.Fatalf("unrelated old-area actor must still be removed: %+v trailing=%x", outsider, trailing)
			}
		})
	}
}

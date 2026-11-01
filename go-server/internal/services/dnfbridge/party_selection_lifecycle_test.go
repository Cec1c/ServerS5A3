package dnfbridge

import (
	"bytes"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/party"
)

func TestPartySelectionExitRemovesMemberAndNotifiesSurvivor(t *testing.T) {
	for _, mode := range []string{"character", "channel"} {
		for _, drift := range []bool{false, true} {
			t.Run(mode+map[bool]string{false: "_current", true: "_generation_drift"}[drift], func(t *testing.T) {
				h := newPartyLeaveHarness(t)
				m := h.service.runtimePartyManagerForService()
				if drift {
					if result := m.Leave(1002, h.member.characterGeneration); !result.OK {
						t.Fatal(result.Reason)
					}
					if result := m.Join(h.partyID, party.RuntimePartyMember{UserID: 1002, SessionGeneration: h.member.characterGeneration + 1}); !result.OK {
						t.Fatal(result.Reason)
					}
					storeRuntimePartyState(h.member, alignedcmd.PartyState{})
				}
				h.leaderConn.write.Reset()
				h.memberConn.write.Reset()
				if mode == "character" {
					if err := h.service.handleUpperReturnSelectCharacter(h.member, nil); err != nil {
						t.Fatal(err)
					}
				} else if err := h.service.handleCurrentChannelExit(h.member, []byte{1}, "test_channel_selection"); err != nil {
					t.Fatal(err)
				}
				if current, found := m.SnapshotByUser(1002, 0); found {
					t.Fatalf("selection left member in party: %+v", current)
				}
				assertPartyProjectionEmpty(t, "selecting member", h.member)
				current, found := m.SnapshotByUser(1001, 0)
				if !found || len(current.Members) != 1 || current.Leader != 1001 {
					t.Fatalf("surviving party=%+v", current)
				}
				notices, rosters := 0, 0
				for _, frame := range splitUpperFrames(t, h.leaderConn.write.Bytes()) {
					if frame.msgID == currentAreaChatNotifyOpcode {
						notices++
					}
					if frame.msgID == 153 {
						rosters++
					}
				}
				if notices != 1 || rosters == 0 {
					t.Fatalf("survivor notifications=%d rosters=%d", notices, rosters)
				}
				before := append([]byte(nil), h.leaderConn.write.Bytes()...)
				h.service.detachRuntimePartySession(h.member, "test_late_socket_close")
				if !bytes.Equal(before, h.leaderConn.write.Bytes()) {
					t.Fatal("late socket close repeated departure notification")
				}
			})
		}
	}
}

func TestPartySelectionExitFromRetiredSocketKeepsReplacementParty(t *testing.T) {
	h := newPartyLeaveHarness(t)
	replacement := &gameSession{conn: &bufferConn{}, channel: h.member.channel, selectedCharacterID: 1002}
	h.service.bindGameSessionCharacter(replacement, 1002)
	h.leaderConn.write.Reset()
	h.service.detachRuntimePartySession(h.member, "return_select_character")
	if _, found := h.service.runtimePartyManagerForService().SnapshotByUser(1002, replacement.characterGeneration); !found {
		t.Fatal("retired socket removed the replacement member")
	}
	if h.leaderConn.write.Len() != 0 {
		t.Fatal("retired socket notified peers of a false departure")
	}
}

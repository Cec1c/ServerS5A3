package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/raid"
)

func TestCurrentRaidDirectoryViewerGetsActualRosterWithoutJoining(t *testing.T) {
	for _, selector := range []byte{1, 2} {
		s, leader, viewer, lc, vc := prepareAccountFriendTest(t)
		leader.channel = channelcatalog.Channel{ID: 23, Type: dnfenum.RaidChannelType}
		viewer.channel = leader.channel
		if err := s.handleCurrentCreateRaid(leader, 664, currentRaidRequestFixture(0, "visible raid", 0, 0, 0)); err != nil {
			t.Fatal(err)
		}
		state, ok := s.runtimeRaidForMember(leader.selectedCharacterID)
		if !ok {
			t.Fatal("missing authoritative raid")
		}
		lc.write.Reset()
		vc.write.Reset()
		req := []byte{selector, 0, 0, 0, 0}
		binary.LittleEndian.PutUint32(req[1:], state.RaidKey)
		if err := s.handleOnlineRequestRaidInfo(viewer, req); err != nil {
			t.Fatal(err)
		}
		raw := consumeCurrentRaidProfiles(t, vc.write.Bytes(), state)
		ack, rest := splitGameServerUpperPacket(t, raw)
		want, err := raid.BuildCurrentInfoReplyBody(currentRuntimeRaidMetadata(state), selector)
		if err != nil || len(rest) != 0 || ack.Header.Classification != 1 || ack.Header.MsgID != 1359 || !bytes.Equal(ack.Body, want) {
			t.Fatal("viewer must receive ACK1359, without own-raid assignment NOTI591")
		}
		after, _ := s.runtimeRaidForMember(leader.selectedCharacterID)
		if _, joined := s.runtimeRaidForMember(viewer.selectedCharacterID); joined || len(s.raids) != 1 || !reflect.DeepEqual(after, state) || lc.write.Len() != 0 {
			t.Fatal("viewing changed membership, created an object or broadcast to leader")
		}
	}
}

func TestCurrentRaidDirectoryViewRejectsWrongAuthorityAndNamespace(t *testing.T) {
	for _, kind := range []string{"unauthed", "wrong_account", "retired", "ordinary", "luke", "server", "channel", "legacy_object", "legacy_profile", "unknown", "short", "trailing", "mode"} {
		t.Run(kind, func(t *testing.T) {
			s, leader, viewer, _, vc := prepareAccountFriendTest(t)
			leader.channel = channelcatalog.Channel{ID: 23, Type: dnfenum.RaidChannelType}
			viewer.channel = leader.channel
			if err := s.handleCurrentCreateRaid(leader, 664, currentRaidRequestFixture(0, "raid", 0, 0, 0)); err != nil {
				t.Fatal(err)
			}
			state, _ := s.runtimeRaidForMember(leader.selectedCharacterID)
			req := []byte{1, 0, 0, 0, 0}
			binary.LittleEndian.PutUint32(req[1:], state.RaidKey)
			switch kind {
			case "unauthed":
				viewer.accountFromToken = false
			case "wrong_account":
				viewer.accountID = "foreign"
			case "retired":
				s.unbindGameSession(viewer)
			case "ordinary":
				viewer.channel.Type = 11
			case "luke":
				viewer.channel.Type = dnfenum.HiddenRaidType
			case "channel":
				viewer.channel.ID = 24
			case "server":
				s.raids[state.RaidKey].RaidKey ^= 1 << 24
				s.raids[state.RaidKey^1<<24] = s.raids[state.RaidKey]
				delete(s.raids, state.RaidKey)
				binary.LittleEndian.PutUint32(req[1:], state.RaidKey^1<<24)
			case "legacy_object":
				s.raids[state.RaidKey].CurrentLayout = false
			case "legacy_profile":
				s.options.currentClientVersion = "legacy"
			case "unknown":
				binary.LittleEndian.PutUint32(req[1:], state.RaidKey+1)
			case "short":
				req = req[:4]
			case "trailing":
				req = append(req, 0)
			case "mode":
				req[0] = 3
			}
			vc.write.Reset()
			if err := s.handleOnlineRequestRaidInfo(viewer, req); err != nil {
				t.Fatal(err)
			}
			if vc.write.Len() != 0 {
				t.Fatal("unauthorized view received metadata")
			}
			if _, joined := s.runtimeRaidForMember(viewer.selectedCharacterID); joined {
				t.Fatal("view granted membership")
			}
		})
	}
}

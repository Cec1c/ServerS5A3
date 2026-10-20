package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	proto "longheng.io/server/internal/modules/dnf/protocol"
	"longheng.io/server/internal/modules/dnf/raid"
)

func currentRaidRequestFixture(route byte, name string, flags ...byte) []byte {
	var writer packetWriter
	writer.writeByte(route)
	writer.writeRawDstr(rosterNameBytes(name))
	for _, flag := range flags {
		writer.writeByte(flag)
	}
	return writer.bytes()
}

func prepareCurrentRaidTest(t *testing.T) (*Service, *gameSession, *bufferConn) {
	t.Helper()
	s, a, _, ac, _ := prepareAccountFriendTest(t)
	a.channel = channelcatalog.Channel{ID: 23, Type: dnfenum.RaidChannelType}
	ac.write.Reset()
	return s, a, ac
}

func TestCurrentRaidActualDispatchCreatesQueriesAndModifiesVerifiedLayout(t *testing.T) {
	for _, flags := range [][3]byte{{0, 0, 0}, {1, 0, 0}} {
		s, a, ac := prepareCurrentRaidTest(t)
		body := currentRaidRequestFixture(0, "测试攻坚", flags[:]...)
		if err := s.handleGameCommand(a, 1, 664, body); err != nil {
			t.Fatal(err)
		}
		metadata, rest := splitGameServerUpperPacket(t, ac.write.Bytes())
		ack, rest := splitGameServerUpperPacket(t, rest)
		state, found := s.runtimeRaidForMember(a.selectedCharacterID)
		if !found || !state.CurrentLayout || state.CurrentUIFlags != flags || ack.Header.MsgID != 664 || !bytes.Equal(ack.Body, raid.BuildCreateRaidResultBody(state.RaidKey)) {
			t.Fatal("current create was rejected or controls collapsed", state, ack)
		}
		rest = consumeCurrentRaidProfiles(t, rest, state)
		roster, rest := splitGameServerUpperPacket(t, rest)
		wantMetadata, err := buildCurrentRuntimeRaidMetadata(state, 0)
		wantRoster, rosterErr := raid.BuildCurrentMemberRefreshMode3Body(state.RaidKey, currentRuntimeRaidMembers(state))
		if err != nil || rosterErr != nil || len(rest) != 0 || metadata.Header.Classification != 0 || metadata.Header.MsgID != 591 || ack.Header.Classification != 1 || roster.Header.Classification != 0 || roster.Header.MsgID != 591 || !bytes.Equal(metadata.Body, wantMetadata) || !bytes.Equal(roster.Body, wantRoster) {
			t.Fatal("current owner must precede ACK before native waiting UI opens")
		}
		if dir := os.Getenv("DNF90_CURRENT_RAID_BRIDGE_NATIVE_DIR"); dir != "" {
			if err := os.WriteFile(filepath.Join(dir, "raid-bridge-mode0-"+string('0'+flags[0])+string('0'+flags[1])+".bin"), metadata.Body, 0600); err != nil {
				t.Fatal(err)
			}
		}
		ac.write.Reset()
		query := []byte{1, 0, 0, 0, 0}
		binary.LittleEndian.PutUint32(query[1:], state.RaidKey)
		frame, err := proto.BuildChannelPacket(uint16(dnfenum.CmdPacketRequestRaidInfo), query, 1, 1)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.handleGameUpper(a, frame); err != nil {
			t.Fatal(err)
		}
		raw := consumeCurrentRaidProfiles(t, ac.write.Bytes(), state)
		ack, rest = splitGameServerUpperPacket(t, raw)
		roster, rest = splitGameServerUpperPacket(t, rest)
		wantInfo, _ := raid.BuildCurrentInfoReplyBody(currentRuntimeRaidMetadata(state), 2)
		if len(rest) != 0 || ack.Header.Classification != 1 || ack.Header.MsgID != 1359 || !bytes.Equal(ack.Body, wantInfo) || roster.Header.Classification != 0 || roster.Header.MsgID != 591 || !bytes.Equal(roster.Body, wantRoster) || len(s.raids) != 1 {
			t.Fatal("actual upper query did not update existing current object")
		}
		ac.write.Reset()
		if err := s.handleOnlineModifyRaidInfo(a, 670, currentRaidRequestFixture(0, "改名", flags[0], flags[1])); err != nil {
			t.Fatal(err)
		}
		state, _ = s.runtimeRaidForMember(a.selectedCharacterID)
		ack, rest = splitGameServerUpperPacket(t, ac.write.Bytes())
		metadata, rest = splitGameServerUpperPacket(t, rest)
		rest = consumeCurrentRaidProfiles(t, rest, state)
		_, rest = splitGameServerUpperPacket(t, rest)
		wantMetadata, _ = buildCurrentRuntimeRaidMetadata(state, 2)
		if state.CurrentUIFlags != flags || !bytes.Equal(state.NameBytes, rosterNameBytes("改名")) || !bytes.Equal(ack.Body, []byte{1}) || !bytes.Equal(metadata.Body, wantMetadata) || len(rest) != 0 {
			t.Fatal("current modify lost flags or metadata update")
		}
	}
}

func TestCurrentRaidRejectsMalformedUnsupportedAndUnauthenticatedOwners(t *testing.T) {
	for _, kind := range []string{"legacy", "third", "bad_flag", "alternate_phase", "wrong_route", "no_auth", "wrong_account", "ordinary_channel", "blank", "control", "stale", "channel_overflow", "server_overflow", "negative_server"} {
		t.Run(kind, func(t *testing.T) {
			s, a, ac := prepareCurrentRaidTest(t)
			body := currentRaidRequestFixture(0, "raid", 0, 0, 0)
			switch kind {
			case "legacy":
				body = body[:len(body)-2]
			case "third":
				body[len(body)-1] = 1
			case "bad_flag":
				body[len(body)-3] = 2
			case "alternate_phase":
				body[len(body)-2] = 1
			case "wrong_route":
				body[0] = 1
			case "no_auth":
				a.accountFromToken = false
			case "wrong_account":
				a.accountID = "other"
			case "ordinary_channel":
				a.channel.Type = 11
			case "channel_overflow":
				a.channel.ID = 279
			case "server_overflow":
				s.options.channelAdvertiseID = 256
			case "negative_server":
				s.options.channelAdvertiseID = -1
				s.options.channelServerID = 256
			case "blank":
				body = currentRaidRequestFixture(0, " ", 0, 0, 0)
			case "control":
				body = currentRaidRequestFixture(0, "a\x00b", 0, 0, 0)
			case "stale":
				id := a.selectedCharacterID
				s.unbindGameSession(a)
				s.bindGameSessionCharacter(&gameSession{conn: &bufferConn{}}, id)
				a.selectedCharacterID = id
			}
			if err := s.handleOnlineCreateRaid(a, 664, body); err != nil {
				t.Fatal(err)
			}
			ack, rest := splitGameServerUpperPacket(t, ac.write.Bytes())
			if len(s.raids) != 0 || len(rest) != 0 || ack.Body[0] != 0 {
				t.Fatal("invalid owner mutated current raid")
			}
		})
	}
}

func TestCurrentRaidKeyMatchesNativeServerAndChannelAdmission(t *testing.T) {
	for _, owner := range [][2]int{{0, 23}, {1, 23}, {7, 32}, {255, 255}} {
		s, a, _ := prepareCurrentRaidTest(t)
		s.options.channelAdvertiseID = owner[0]
		a.channel.ID = owner[1]
		a.channel.ServerID = 99 // The advertised ID, not catalog source, is native.
		state, err := s.createCurrentRuntimeRaid(a, raid.CurrentInfoRequest{NameBytes: []byte("raid")})
		if err != nil {
			t.Fatal(err)
		}
		if state.RaidKey>>24 != uint32(owner[0]) || (state.RaidKey>>16)&255 != uint32(owner[1]) || uint16(state.RaidKey) != a.selectedCharacterID {
			t.Fatalf("native owner fields disagree: %#x", state.RaidKey)
		}
		s.raidMu.Lock()
		second := s.nextRuntimeRaidKeyLocked(a, a.selectedCharacterID)
		s.raidMu.Unlock()
		if second == state.RaidKey || second>>16 != state.RaidKey>>16 {
			t.Fatalf("collision allocation lost native owner fields: %#x", second)
		}
		for _, started := range []bool{false, true} {
			state.Started = started
			body, err := buildCurrentRuntimeRaidMetadata(state, 2)
			if err != nil {
				t.Fatal(err)
			}
			// Outer key/mode, object key, DSTR length/name, route, state.
			offset := 17 + len(state.NameBytes)
			want := byte(0)
			if started {
				want = 2
			}
			if body[offset] != want {
				t.Fatalf("native recruiting/battle state=%d want=%d", body[offset], want)
			}
		}
	}
}

func TestCurrentRaidRepeatedCreationDoesNotReplaceStartedOwner(t *testing.T) {
	s, a, ac := prepareCurrentRaidTest(t)
	body := currentRaidRequestFixture(0, "raid", 0, 0, 0)
	if err := s.handleOnlineCreateRaid(a, 664, body); err != nil {
		t.Fatal(err)
	}
	state, _ := s.runtimeRaidForMember(a.selectedCharacterID)
	s.raids[state.RaidKey].Started = true
	ac.write.Reset()
	if err := s.handleOnlineCreateRaid(a, 664, body); err != nil {
		t.Fatal(err)
	}
	ack, rest := splitGameServerUpperPacket(t, ac.write.Bytes())
	if len(s.raids) != 1 || !s.raids[state.RaidKey].Started || len(rest) != 0 || ack.Body[0] != 0 {
		t.Fatal("replayed creation replaced running raid")
	}
}

package dnfbridge

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"longheng.io/server/internal/modules/dnf/characterfriend"
)

func exportGuildSocialPacket(t *testing.T, name string, body []byte) {
	t.Helper()
	if dir := os.Getenv("DNF_GUILD_SOCIAL_NATIVE_DIR"); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), body, 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGuildSocialNativeFriendZeroGroup(t *testing.T) {
	s, source, _, conn, _ := prepareOfficialFriendTest(t)
	s.options.channelAdvertiseID = 1
	for _, opcode := range []uint16{290, 291} {
		conn.write.Reset()
		// The old native sender substitutes directory 1 for character group 0.
		// The existing character then receives the same 0x15 as a missing name.
		wrongGroup := officialFriendRequest(opcode, 0xffff, 1, "来来来")
		if dir := os.Getenv("DNF_GUILD_SOCIAL_NATIVE_DIR"); dir != "" {
			native, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("friend-%d-group0-before.bin", opcode)))
			if err != nil || !bytes.Equal(native, wrongGroup) {
				t.Fatalf("old native request differs: %x err=%v", native, err)
			}
			wrongGroup = native
		}
		if err := s.handleCurrentOfficialCharacterFriendMutation(source, opcode, wrongGroup); err != nil {
			t.Fatal(err)
		}
		rejected, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
		if !bytes.Equal(rejected.Body, []byte{0, 0x15}) || len(rest) != 0 {
			t.Fatalf("old group must reject without list projection: %x", conn.write.Bytes())
		}
		reposBefore, _ := s.repositoryGroup()
		beforeRows, err := characterfriend.NewOwner(reposBefore).List(context.Background(), "dnf:1", "1")
		if err != nil || (opcode == 290 && len(beforeRows) != 0) || (opcode == 291 && (len(beforeRows) != 1 || beforeRows[0].CharacterID != "5")) {
			t.Fatalf("rejected request changed friends: rows=%+v err=%v", beforeRows, err)
		}
		conn.write.Reset()
		body := officialFriendRequest(opcode, 0xffff, 0, "来来来")
		if dir := os.Getenv("DNF_GUILD_SOCIAL_NATIVE_DIR"); dir != "" {
			native, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("friend-%d-group0.bin", opcode)))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(native, body) {
				t.Fatalf("native request differs: %x", native)
			}
			body = native
		}
		if err := s.handleCurrentOfficialCharacterFriendMutation(source, opcode, body); err != nil {
			t.Fatal(err)
		}
		ack, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
		if !bytes.Equal(ack.Body, []byte{1}) {
			t.Fatalf("native group0 friend mutation failed: %x", ack.Body)
		}
		repos, _ := s.repositoryGroup()
		rows, err := characterfriend.NewOwner(repos).List(context.Background(), "dnf:1", "1")
		if err != nil || (opcode == 290 && (len(rows) != 1 || rows[0].CharacterID != "5")) || (opcode == 291 && len(rows) != 0) {
			t.Fatalf("friend mutation not persisted: rows=%+v err=%v", rows, err)
		}
	}
}

func TestGuildSocialNativeChannelReplies(t *testing.T) {
	for _, kind := range []string{"online", "offline", "missing", "foreign"} {
		t.Run(kind, func(t *testing.T) {
			s, a, b, ac, _ := prepareUserChannelTest(t)
			s.options.channelAdvertiseID = 1
			name, group := "来来来", byte(0)
			if kind == "offline" {
				s.unbindGameSession(b)
			}
			if kind == "missing" {
				name = "不存在"
			}
			if kind == "foreign" {
				group = 1
			}
			request := officialFriendRequest(291, 0, group, name)
			if _, err := s.handleCurrentUserChannel(a, 804, 1, request); err != nil {
				t.Fatal(err)
			}
			ack, rest := splitGameServerUpperPacket(t, ac.write.Bytes())
			if len(rest) != 0 {
				t.Fatal("extra packets")
			}
			exportGuildSocialPacket(t, kind+"-804.bin", ack.Body)
		})
	}
}

func TestGuildSocialWhisperUsesCharacterGroupInsteadOfChannel(t *testing.T) {
	for _, group := range []byte{0, 7, 249} {
		for _, messageType := range []byte{0x24, 0x28} {
			t.Run(fmt.Sprintf("group%d/type%d", group, messageType), func(t *testing.T) {
				s, source, target, sourceConn, targetConn := newCurrentSocialPeerTest(t)
				s.options.currentClientVersion = "official-24.9"
				s.options.channelAdvertiseID = 1
				target.channel.ID = 11
				repos, _ := s.repositoryGroup()
				for _, session := range []*gameSession{source, target} {
					c, _, _ := repos.Character.Load(context.Background(), strconv.Itoa(int(session.selectedCharacterID)))
					if c.Stats == nil {
						c.Stats = map[string]int64{}
					}
					c.Stats["server_group_id"] = int64(group)
					if err := repos.Character.Save(context.Background(), c); err != nil {
						t.Fatal(err)
					}
				}
				var w packetWriter
				w.writeByte(messageType)
				w.writeUint16(0)
				w.writeUint32(0)
				w.writeRawDstr([]byte("hello"))
				w.writeRawDstr(rosterNameBytes("来来来"))
				w.writeByte(group)
				request := w.bytes()
				if dir := os.Getenv("DNF_GUILD_SOCIAL_NATIVE_DIR"); dir != "" && messageType == 0x24 {
					path := filepath.Join(dir, fmt.Sprintf("whisper-group%d-17.bin", group))
					body, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(body, request) {
						t.Fatalf("native producer request differs: %x", body)
					}
					request = body
				}
				if err := s.handleCurrentPeerChat(source, 17, request); err != nil {
					t.Fatal(err)
				}
				if targetConn.write.Len() == 0 {
					t.Fatal("native character-group whisper rejected")
				}
				incoming, rest := splitGameServerUpperPacket(t, targetConn.write.Bytes())
				if len(rest) != 0 || !bytes.Equal(incoming.Body, buildCurrentOfficialWhisperBody(group, "pouut", "pouut", []byte("hello"))) {
					t.Fatalf("sender identity uses channel instead of group: %x", incoming.Body)
				}
				ack, rest := splitGameServerUpperPacket(t, sourceConn.write.Bytes())
				if !bytes.Equal(ack.Body, []byte{1}) {
					t.Fatal("whisper ACK failed")
				}
				echo, rest := splitGameServerUpperPacket(t, rest)
				if len(rest) != 0 || !bytes.Equal(echo.Body, buildCurrentOfficialWhisperBody(group, "pouut", "来来来", []byte("hello"))) {
					t.Fatal("echo identity differs")
				}
				exportGuildSocialPacket(t, fmt.Sprintf("group%d-type%d-65.bin", group, messageType), incoming.Body)
			})
		}
	}
}

func TestGuildSocialWhisperRejectsForeignAndMalformedGroups(t *testing.T) {
	for _, tc := range []struct {
		name           string
		source, target int64
		trailer        byte
	}{
		{"directory_is_not_group", 0, 0, 1}, {"channel_is_not_group", 0, 0, 42},
		{"foreign_same_name", 0, 1, 1}, {"negative_source", -1, 0, 0},
		{"overflow_source", 256, 0, 0}, {"negative_target", 0, -1, 0},
		{"sentinel", 250, 250, 250},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, a, b, ac, bc := newCurrentSocialPeerTest(t)
			s.options.currentClientVersion = "official-24.9"
			repos, _ := s.repositoryGroup()
			for i, p := range []*gameSession{a, b} {
				c, _, _ := repos.Character.Load(context.Background(), strconv.Itoa(int(p.selectedCharacterID)))
				c.Stats = map[string]int64{"server_group_id": []int64{tc.source, tc.target}[i]}
				if err := repos.Character.Save(context.Background(), c); err != nil {
					t.Fatal(err)
				}
			}
			body := currentNativeWhisperFixture(0)
			body[len(body)-1] = tc.trailer
			if err := s.handleCurrentPeerChat(a, 17, body); err != nil {
				t.Fatal(err)
			}
			ack, rest := splitGameServerUpperPacket(t, ac.write.Bytes())
			if !bytes.Equal(ack.Body, []byte{0, 1}) || len(rest) != 0 || bc.write.Len() != 0 {
				t.Fatal("invalid group delivered")
			}
		})
	}
}

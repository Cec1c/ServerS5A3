package dnfbridge

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
)

func mainWhisperRequest(uid uint16, group byte) []byte {
	var w packetWriter
	w.writeByte(1)
	w.writeUint16(uid)
	w.writeUint32(0)
	w.writeRawDstr([]byte("hello"))
	w.writeRawDstr(rosterNameBytes("来来来"))
	w.writeByte(group)
	return w.bytes()
}

func TestCurrentMainWhisperNativeMenuSenderAndNamedLookup(t *testing.T) {
	for _, group := range []byte{0, 7, 249} {
		for _, uid := range []uint16{5, 65535} {
			t.Run(fmt.Sprintf("group%d/uid%d", group, uid), func(t *testing.T) {
				s, source, target, src, dst := newCurrentSocialPeerTest(t)
				s.options.currentClientVersion = "official-24.9"
				target.channel = channelcatalog.Channel{ID: 11}
				s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 5, TownID: 2, AreaID: 2, Session: target})
				observerConn := &bufferConn{}
				observer := &gameSession{conn: observerConn, channel: source.channel, accountID: "dnf:3", selectedCharacterID: 9}
				s.bindGameSessionCharacter(observer, 9)
				s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 9, TownID: 1, AreaID: 1, Session: observer})
				repos, _ := s.repositoryGroup()
				for _, p := range []*gameSession{source, target} {
					c, _, _ := repos.Character.Load(context.Background(), strconv.Itoa(int(p.selectedCharacterID)))
					c.Stats = map[string]int64{"server_group_id": int64(group)}
					if err := repos.Character.Save(context.Background(), c); err != nil {
						t.Fatal(err)
					}
				}
				body := mainWhisperRequest(uid, group)
				if dir := os.Getenv("DNF_GUILD_SOCIAL_NATIVE_DIR"); dir != "" {
					native, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("main-whisper-group%d-uid%d.bin", group, uid)))
					if err != nil || !bytes.Equal(native, body) {
						t.Fatalf("native writer differs: %x err=%v", native, err)
					}
					body = native
				}
				if err := s.handleCurrentPeerChat(source, 17, body); err != nil {
					t.Fatal(err)
				}
				if dst.write.Len() == 0 {
					t.Fatal("native main-input type1 was rejected")
				}
				ack, rest := splitGameServerUpperPacket(t, src.write.Bytes())
				if ack.Header.MsgID != 17 || ack.Header.Classification != 1 || !bytes.Equal(ack.Body, []byte{1}) || len(rest) != 0 {
					t.Fatalf("main whisper ACK differs: %+v", ack)
				}
				notice, rest := splitGameServerUpperPacket(t, dst.write.Bytes())
				var w packetWriter
				w.writeBytes([]byte{1, 0})
				w.writeRawDstr(rosterNameBytes("pouut"))
				w.writeByte(group)
				w.writeRawDstr([]byte("hello"))
				w.writeRawDstr(nil)
				if notice.Header.MsgID != 65 || notice.Header.Classification != 0 || !bytes.Equal(notice.Body, w.bytes()) || len(rest) != 0 || observerConn.write.Len() != 0 {
					t.Fatalf("main whisper notification or recipient scope differs: %+v", notice)
				}
				exportGuildSocialPacket(t, fmt.Sprintf("main-whisper-group%d-uid%d-noti65.bin", group, uid), notice.Body)
			})
		}
	}
}

func TestCurrentMainWhisperRetainsIdentityAndShapeBoundaries(t *testing.T) {
	for _, kind := range []string{"wrong_group", "unknown", "wrong_name", "wrong_uid", "no_trailer", "extra_byte", "nul", "too_long", "offline", "stale", "legacy"} {
		t.Run(kind, func(t *testing.T) {
			s, source, target, _, dst := newCurrentSocialPeerTest(t)
			s.options.currentClientVersion = "official-24.9"
			body := mainWhisperRequest(5, 0)
			switch kind {
			case "wrong_group":
				body[len(body)-1] = 1
			case "unknown":
				body[3] = 1
			case "wrong_name":
				body[len(body)-2] = 'x'
			case "wrong_uid":
				body[1] = 9
			case "no_trailer":
				body = body[:len(body)-1]
			case "extra_byte":
				body = append(body, 0)
			case "nul":
				body[11] = 0
			case "too_long":
				body = append(body[:11], append(bytes.Repeat([]byte("x"), 256), body[16:]...)...)
				body[7], body[8] = 0, 1
			case "offline":
				s.unbindGameSession(target)
			case "stale":
				s.promoteResidentGameSession(&gameSession{conn: &bufferConn{}, accountID: source.accountID,
					channel: source.channel, selectedCharacterID: source.selectedCharacterID, characterGeneration: 99}, source.selectedCharacterID)
			case "legacy":
				s.options.currentClientVersion = ""
			}
			if err := s.handleCurrentPeerChat(source, 17, body); err != nil {
				t.Fatal(err)
			}
			if dst.write.Len() != 0 {
				t.Fatal("invalid or unverified main whisper delivered")
			}
		})
	}
}

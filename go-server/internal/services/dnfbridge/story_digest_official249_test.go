package dnfbridge

import (
	"context"
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// Native 43F9DF0 registers 1377/1455210 and 1443/1FC0020.
// 14542A9 builds a bodyless 1443 when playback is accepted.
func TestOfficial249StoryDigestFirstPlaybackPersistAndRelog(t *testing.T) {
	for _, transport := range []string{"upper", "legacy"} {
		t.Run(transport, func(t *testing.T) {
			viewed := uint32(0)
			f := newStoryDigestAccountFixture(t, "77", 35, 0, &viewed, false)
			f.service.options.currentClientVersion = "official-24.9"
			f.session.selectedCharacterID = 77
			load := func() dnfrepo.CharacterRecord {
				r, found, err := f.repositories.Character.Load(context.Background(), "77")
				if err != nil || !found {
					t.Fatal("character missing", err)
				}
				return r
			}
			checkNotice := func(want uint32) {
				f.session.conn.(*bufferConn).write.Reset()
				if err := f.service.sendCurrentStoryDigestLastLevel(f.session, load(), true, "test_relogin"); err != nil {
					t.Fatal(err)
				}
				packet, rest := splitGameServerUpperPacket(t, f.session.conn.(*bufferConn).write.Bytes())
				if packet.Header.Classification != 0 || packet.Header.MsgID != 1377 || len(rest) != 0 || len(packet.Body) != 4 || binary.LittleEndian.Uint32(packet.Body) != want {
					t.Fatalf("native story notice=%+v body=%x want=%d", packet.Header, packet.Body, want)
				}
			}
			checkNotice(0) // Never mark the first playback as already viewed.
			f.session.conn.(*bufferConn).write.Reset()
			for i := 0; i < 2; i++ {
				var err error
				if transport == "upper" {
					frame, buildErr := dnfproto.BuildChannelPacket(1443, nil, 0, dnfproto.DefaultChannelClassification)
					if buildErr != nil {
						t.Fatal(buildErr)
					}
					err = f.service.handleGameUpper(f.session, frame)
				} else {
					err = f.service.handleGameCommand(f.session, byte(dnfenum.GameCmdCommand), 1443, nil)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if f.session.conn.(*bufferConn).write.Len() != 0 {
				t.Fatal("playback update fabricated a reply")
			}
			if load().Stats[dnfrepo.CharacterStoryDigestLastLevelStatKey] != 35 {
				t.Fatal("viewed character progress not persisted")
			}
			account, found, err := f.repositories.Account.Load(context.Background(), defaultAccountPrefix+"1")
			if err != nil || !found || dnfrepo.AccountStoryDigestViewedLevel(account) != 35 {
				t.Fatal("account viewed progress not persisted", err)
			}
			f.session = newStoryDigestTestSession()
			f.session.selectedCharacterID = 77
			checkNotice(35)
		})
	}
}

func TestStoryDigestOpcodeProfileKeepsLegacyClient(t *testing.T) {
	for _, profile := range []string{"", "90cn", "90cn-decode-bypass-v1"} {
		s := &Service{options: options{currentClientVersion: profile}}
		if s.storyDigestLastLevelMsgID() != 1378 {
			t.Fatal("legacy profile opcode changed")
		}
	}
}

package dnfbridge

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestChatSenderServerGroupKeepsZeroAndRejectsNativeSentinels(t *testing.T) {
	for _, group := range []int64{-1, 0, 7, 42, 249, 250, 255, 256} {
		t.Run(fmt.Sprint(group), func(t *testing.T) {
			got, ok := currentChatCharacterServerGroup(dnfrepo.CharacterRecord{Stats: map[string]int64{"server_group_id": group}})
			valid := group >= 0 && group < 250
			if ok != valid || (valid && int64(got) != group) {
				t.Fatalf("group=%d got=%d valid=%v", group, got, ok)
			}
		})
	}
}

func TestChatSenderServerGroupNormalAndItemLinkUseSenderNotChannelOrRecipient(t *testing.T) {
	for _, linked := range []bool{false, true} {
		for _, group := range []int64{0, 7, 249, -1, 250} {
			t.Run(fmt.Sprintf("linked_%v_group_%d", linked, group), func(t *testing.T) {
				s, source, _, sourceConn, peerConn := newCurrentSocialPeerTest(t)
				repos, _ := s.repositoryGroup()
				for id, value := range map[string]int64{"1": group, "5": 19} {
					c, found, err := repos.Character.Load(context.Background(), id)
					if err != nil || !found {
						t.Fatal("missing fixture", err)
					}
					c.Stats = map[string]int64{"server_group_id": value}
					if err := repos.Character.Save(context.Background(), c); err != nil {
						t.Fatal(err)
					}
				}
				var err error
				if linked {
					body := buildCurrentHyperlinkBodyForTest(3, 9999, 0, []byte("hello"), [][]byte{bytes.Repeat([]byte{1}, 148)})
					err = s.handleCurrentItemHyperlinkMessage(source, 424, body)
				} else {
					err = s.handleCurrentAreaChat(source, 17, currentPeerChatRequest{MessageType: 3, Message: []byte("hello")})
				}
				if err != nil {
					t.Fatal(err)
				}
				ack, rest := splitCurrentGameServerUpperPacketAuto(t, sourceConn.write.Bytes())
				if group < 0 || group >= 250 {
					if !bytes.Equal(ack.Body, []byte{0, 1}) || len(rest) != 0 || peerConn.write.Len() != 0 {
						t.Fatal("invalid group emitted chat")
					}
					return
				}
				if !bytes.Equal(ack.Body, []byte{1}) {
					t.Fatalf("ACK=%x", ack.Body)
				}
				self, rest := splitCurrentGameServerUpperPacketAuto(t, rest)
				peer, trailing := splitCurrentGameServerUpperPacketAuto(t, peerConn.write.Bytes())
				if len(rest) != 0 || len(trailing) != 0 || !bytes.Equal(self.Body, peer.Body) {
					t.Fatal("sender and recipient differ")
				}
				if self.Body[0] != 3 || self.Body[1] != 1 || self.Body[2] != 0 || self.Body[3] != byte(group) {
					t.Fatalf("native sender header=%x; expected source group %d instead of channel42 or recipient19", self.Body[:4], group)
				}
				if dir := os.Getenv("DNFBRIDGE_CHAT_GROUP_EXPORT"); dir != "" {
					if err := os.MkdirAll(dir, 0755); err != nil {
						t.Fatal(err)
					}
					file := filepath.Join(dir, fmt.Sprintf("chat-linked-%v-group-%d.bin", linked, group))
					if err := os.WriteFile(file, self.Body, 0600); err != nil {
						t.Fatal(err)
					}
				}
			})
		}
	}
}

package dnfbridge

import (
	"net"
	"testing"
)

// Documentation-range addresses stand in for distinct external peers. This
// checks address-independent projection and staggered loading, not WAN packet
// loss, real NAT traversal, or rendering in the game client.
func TestPeerFinalProjectionAlsoResendsForDistinctExternalAddresses(t *testing.T) {
	for _, leaderFirst := range []bool{false, true} {
		name := "member_first"
		if leaderFirst {
			name = "leader_first"
		}
		t.Run(name, func(t *testing.T) {
			f := newLeaderExitFixture(t)
			leaderConn := &tcpAddrBufferConn{addr: &net.TCPAddr{IP: net.IPv4(198, 51, 100, 11), Port: 41001}}
			memberConn := &tcpAddrBufferConn{addr: &net.TCPAddr{IP: net.IPv4(203, 0, 113, 22), Port: 52002}}
			f.leader.conn, f.member.conn = leaderConn, memberConn
			for _, peer := range []*gameSession{f.leader, f.member} {
				peer.setFinishLoadingGateState(false)
				peer.setFinishLoadingGateCompletion(false)
				peer.setFinishLoadingGatePostFinish(false)
			}
			first, late := f.member, f.leader
			if leaderFirst {
				first, late = f.leader, f.member
			}
			markPeerSceneStableForTest(first)
			f.service.finishDungeonPartyPeerActorProjection(first, "external_first")
			if leaderConn.write.Len() != 0 || memberConn.write.Len() != 0 {
				t.Fatal("final projection crossed an incomplete loading tail")
			}
			markPeerSceneStableForTest(late)
			f.service.finishDungeonPartyPeerActorProjection(late, "external_late")
			for _, pair := range []struct {
				conn  *tcpAddrBufferConn
				actor uint16
			}{{leaderConn, f.member.selectedCharacterID}, {memberConn, f.leader.selectedCharacterID}} {
				frames := splitUpperFrames(t, pair.conn.write.Bytes())
				created, bound, roster := r160UpperPeerActorCounts(frames, pair.actor)
				if created == 0 || bound == 0 || roster == 0 {
					t.Fatalf("external peer model incomplete: mode0=%d mode1=%d op9=%d", created, bound, roster)
				}
				for _, frame := range frames {
					if frame.msgID == 11 {
						t.Fatal("model resend reissued UDP endpoint handshake")
					}
				}
				pair.conn.write.Reset()
			}
			f.service.finishDungeonPartyPeerActorProjection(late, "external_repeat")
			f.service.finishDungeonPartyPeerActorProjection(first, "external_repeat_first")
			if leaderConn.write.Len() != 0 || memberConn.write.Len() != 0 {
				t.Fatal("successful external model resend was not idempotent")
			}
		})
	}
}

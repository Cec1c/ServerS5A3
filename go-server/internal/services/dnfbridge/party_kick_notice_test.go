package dnfbridge

import (
	"bytes"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
)

func TestPartyKickSendsNativeWalkoutBeforeClearingKickedMember(t *testing.T) {
	h := newPartyLeaveHarness(t)
	h.service.options.currentClientVersion = "official-24.9"
	h.leaderConn.write.Reset()
	h.memberConn.write.Reset()
	if err := h.service.handleOnlineWalkoutPartyMember(h.leader, uint16(dnfenum.CmdPacketWalkoutPartyMember), []byte{1}); err != nil {
		t.Fatal(err)
	}
	notice, rest := splitGameServerUpperPacket(t, h.memberConn.write.Bytes())
	if notice.Header.Classification != 0 || notice.Header.MsgID != 10 || !bytes.Equal(notice.Body, []byte{1, 0}) {
		t.Fatalf("kicked member needs class0/op10 slot=1 mode=0 before removal; got class=%d op=%d body=%x", notice.Header.Classification, notice.Header.MsgID, notice.Body)
	}
	removal, _ := splitGameServerUpperPacket(t, rest)
	if removal.Header.MsgID != 9 || len(removal.Body) != 15 || removal.Body[9] != 2 || !bytes.Equal(removal.Body[10:], make([]byte, 5)) {
		t.Fatal("native kick notice must be followed by a leave update, not a disband record")
	}
	ack, rest := splitGameServerUpperPacket(t, h.leaderConn.write.Bytes())
	if ack.Header.Classification != 1 || ack.Header.MsgID != 14 || !bytes.Equal(ack.Body, []byte{1}) {
		t.Fatal("leader's kick command ACK changed")
	}
	peerNotice, _ := splitGameServerUpperPacket(t, rest)
	if peerNotice.Header.Classification != 0 || peerNotice.Header.MsgID != 10 || !bytes.Equal(peerNotice.Body, []byte{1, 0}) {
		t.Fatal("remaining member did not receive the native walkout notification")
	}
	if _, present := h.service.runtimePartyManagerForService().SnapshotByUser(1002, 0); present {
		t.Fatal("kicked member retained authoritative membership")
	}
}

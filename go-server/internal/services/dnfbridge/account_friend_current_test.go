package dnfbridge

import (
	"bytes"
	"context"
	"longheng.io/server/internal/modules/dnf/accountfriend"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	proto "longheng.io/server/internal/modules/dnf/protocol"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"strings"
	"testing"
)

func prepareAccountFriendTest(t *testing.T) (*Service, *gameSession, *gameSession, *bufferConn, *bufferConn) {
	t.Helper()
	s, a, b, ac, bc := prepareOfficialFriendTest(t)
	g, _ := s.repositoryGroup()
	for id, name := range map[string]string{"dnf:1": "Alpha", "dnf:2": "Beta"} {
		record, _, _ := g.Account.Load(nil, id)
		record.RepresentAccountName = name
		if err := g.Account.Save(nil, record); err != nil {
			t.Fatal(err)
		}
	}
	if err := g.Character.Save(nil, repo.CharacterRecord{CharacterID: "8", AccountID: "dnf:2", Name: "小号", Job: "0", Level: 85, Slot: 1}); err != nil {
		t.Fatal(err)
	}
	a.loginPacketSeen = true
	a.accountFromToken = true
	b.loginPacketSeen = true
	b.accountFromToken = true
	return s, a, b, ac, bc
}

func TestAccountFriendLegacyAndUpperDispatchUseCurrentIDs(t *testing.T) {
	s, a, b, ac, bc := prepareAccountFriendTest(t)
	if err := s.handleGameCommand(a, 1, 1436, accountFriendRequestFixture(1436, "Beta", "申请", 0)); err != nil {
		t.Fatal(err)
	}
	ack, _ := splitGameServerUpperPacket(t, ac.write.Bytes())
	if ack.Header.MsgID != 1436 || ack.Body[0] != 1 {
		t.Fatal("legacy current request not dispatched")
	}
	ac.write.Reset()
	bc.write.Reset()
	frame, err := proto.BuildChannelPacket(1437, accountFriendRequestFixture(1437, "Alpha", "", 0), 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.handleGameUpper(b, frame); err != nil {
		t.Fatal(err)
	}
	ack, _ = splitGameServerUpperPacket(t, bc.write.Bytes())
	if ack.Header.MsgID != 1437 || ack.Body[0] != 1 {
		t.Fatal("upper acceptance consumed by historical represent-name owner")
	}
}
func callAccountFriend(t *testing.T, s *Service, a *gameSession, op uint16, target, text string, action byte) {
	t.Helper()
	handled, err := s.handleCurrentAccountFriendCommand(a, op, 1, accountFriendRequestFixture(op, target, text, action))
	if !handled || err != nil {
		t.Fatalf("op%d handled=%v err=%v", op, handled, err)
	}
}
func accountFriendPacketIDs(t *testing.T, c *bufferConn) []uint16 {
	t.Helper()
	raw := c.write.Bytes()
	var ids []uint16
	for len(raw) > 0 {
		packet, rest := splitGameServerUpperPacket(t, raw)
		ids = append(ids, packet.Header.MsgID)
		raw = rest
	}
	return ids
}

func TestAccountFriendRequestAcceptProjectsAllCharactersAndDeletesBothSides(t *testing.T) {
	s, a, b, ac, bc := prepareAccountFriendTest(t)
	g, _ := s.repositoryGroup()
	spectatorConn := &bufferConn{}
	spectator := &gameSession{conn: spectatorConn, accountID: "dnf:3", selectedCharacterID: 9, channel: channelcatalog.Channel{ID: 42}, loginPacketSeen: true, accountFromToken: true}
	s.bindGameSessionCharacter(spectator, 9)
	callAccountFriend(t, s, a, 1436, "Beta", "想加好友", 0)
	ack, rest := splitGameServerUpperPacket(t, ac.write.Bytes())
	want, _ := buildCurrentAccountFriendSuccessTail(1436, currentAccountFriendRequest{Target: "Beta"})
	if ack.Header.MsgID != 1436 || !bytes.Equal(ack.Body, append([]byte{1}, want...)) {
		t.Fatal("request ACK")
	}
	empty, rest := splitGameServerUpperPacket(t, rest)
	pending, rest := splitGameServerUpperPacket(t, rest)
	if empty.Header.MsgID != 1371 || !bytes.Equal(empty.Body, []byte{0}) || pending.Header.MsgID != 1372 || pending.Body[0] != 1 || pending.Body[1] != 1 || len(rest) != 0 {
		t.Fatal("sender invitation projection")
	}
	peerEmpty, rest := splitGameServerUpperPacket(t, bc.write.Bytes())
	peerPending, rest := splitGameServerUpperPacket(t, rest)
	if peerEmpty.Header.MsgID != 1371 || peerPending.Header.MsgID != 1372 || peerPending.Body[0] != 1 || peerPending.Body[1] != 0 || len(rest) != 0 {
		t.Fatal("recipient invitation projection")
	}
	if spectatorConn.write.Len() != 0 {
		t.Fatal("private invitation leaked")
	}
	ac.write.Reset()
	bc.write.Reset()
	callAccountFriend(t, s, b, 1437, "Alpha", "", 0)
	rows, pendingRows, err := s.currentAccountFriendProjection(context.Background(), "dnf:1")
	// D33 feedback 10: an adventure-group friend list is a presence projection.
	// Only the online character of dnf:2 may appear; the offline 小号 must not
	// be enumerated.
	if err != nil || len(rows) != 1 || len(pendingRows) != 0 {
		t.Fatal("account roster", rows, pendingRows, err)
	}
	if rows[0].CharacterName != "来来来" || rows[0].Channel != 42 {
		t.Fatal("actual online identities", rows)
	}
	accepted, err := accountfriend.NewOwner(g).List(nil, "dnf:2")
	if err != nil || len(accepted) != 1 || accepted[0].Relation.State != accountfriend.Friend {
		t.Fatal("not persisted after accept", accepted, err)
	}
	ac.write.Reset()
	bc.write.Reset()
	note := strings.Repeat("字", 40)
	callAccountFriend(t, s, a, 1440, "小号", note, 0)
	ack, _ = splitGameServerUpperPacket(t, ac.write.Bytes())
	r, err := decodeCurrentAccountFriendRequest(1440, ack.Body[1:])
	if err != nil || r.Text != note {
		t.Fatal("remark truncated", r, err)
	}
	reverse, _, err := s.currentAccountFriendProjection(context.Background(), "dnf:2")
	if err != nil || len(reverse) != 1 || reverse[0].Remark != "" {
		t.Fatal("private note leaked to peer", reverse, err)
	}
	ac.write.Reset()
	bc.write.Reset()
	callAccountFriend(t, s, a, 1438, "小号", "", 0)
	ids := accountFriendPacketIDs(t, ac)
	if len(ids) != 4 || ids[0] != 1438 || ids[1] != 1373 || ids[2] != 1371 || ids[3] != 1372 {
		t.Fatal("delete missing character removals", ids)
	}
	ids = accountFriendPacketIDs(t, bc)
	if len(ids) != 3 || ids[0] != 1373 || ids[1] != 1371 || ids[2] != 1372 {
		t.Fatal("reciprocal delete missing", ids)
	}
	for _, id := range []string{"dnf:1", "dnf:2"} {
		entries, err := accountfriend.NewOwner(g).List(nil, id)
		if err != nil || len(entries) != 0 {
			t.Fatal("delete not durable", entries, err)
		}
	}
	if spectatorConn.write.Len() != 0 {
		t.Fatal("private relation leaked")
	}
}

func TestAccountFriendPresenceUsesSessionDirectoryAndRemovesRenamedRow(t *testing.T) {
	s, a, b, ac, bc := prepareAccountFriendTest(t)
	callAccountFriend(t, s, a, 1436, "Beta", "申请", 0)
	callAccountFriend(t, s, b, 1437, "Alpha", "", 0)
	ac.write.Reset()
	bc.write.Reset()
	s.unbindGameSession(b)
	rows, _, err := s.currentAccountFriendProjection(context.Background(), "dnf:1")
	if err != nil || len(rows) != 1 || rows[0].CharacterName != "来来来" || rows[0].Channel != 0 {
		t.Fatal("offline directory stale", rows, err)
	}
	ids := accountFriendPacketIDs(t, ac)
	if len(ids) != 2 || ids[0] != 1371 || ids[1] != 1372 {
		t.Fatal("friend did not receive offline update", ids)
	}
	ac.write.Reset()
	g, _ := s.repositoryGroup()
	// Rename the character the offline row actually projects (the persisted
	// accept-time character); the stale 1371 node must be replaced by 1373+1371.
	character, _, _ := g.Character.Load(nil, "5")
	character.Name = "改名"
	if err := g.Character.Save(nil, character); err != nil {
		t.Fatal(err)
	}
	if err := s.sendCurrentAccountFriendLists(a, "rename"); err != nil {
		t.Fatal(err)
	}
	ids = accountFriendPacketIDs(t, ac)
	if len(ids) != 3 || ids[0] != 1373 || ids[1] != 1371 || ids[2] != 1372 {
		t.Fatal("stale renamed node left in native manager", ids)
	}
}

func TestAccountFriendUnauthenticatedWrongClassAndStaleGenerationDoNotWrite(t *testing.T) {
	for _, kind := range []string{"unauthenticated", "wrong_class", "stale"} {
		t.Run(kind, func(t *testing.T) {
			s, a, _, ac, bc := prepareAccountFriendTest(t)
			classification := byte(1)
			switch kind {
			case "unauthenticated":
				a.accountFromToken = false
			case "wrong_class":
				classification = 0
			case "stale":
				s.unbindGameSession(a)
				replacement := &gameSession{conn: &bufferConn{}, accountID: a.accountID, selectedCharacterID: 1}
				s.bindGameSessionCharacter(replacement, 1)
			}
			handled, err := s.handleCurrentAccountFriendCommand(a, 1436, classification, accountFriendRequestFixture(1436, "Beta", "申请", 0))
			if !handled || err != nil {
				t.Fatal(handled, err)
			}
			g, _ := s.repositoryGroup()
			rows, err := accountfriend.NewOwner(g).List(nil, "dnf:1")
			if err != nil || len(rows) != 0 || bc.write.Len() != 0 {
				t.Fatal("rejected request mutated/leaked", rows, err)
			}
			if classification == 0 && ac.write.Len() != 0 {
				t.Fatal("class0 interpreted as friend command")
			}
		})
	}
	s, a, _, _, _ := prepareAccountFriendTest(t)
	s.options.currentClientVersion = "legacy"
	handled, err := s.handleCurrentAccountFriendCommand(a, 1436, 1, accountFriendRequestFixture(1436, "Beta", "申请", 0))
	if handled || err != nil {
		t.Fatal("legacy protocol intercepted", handled, err)
	}
}

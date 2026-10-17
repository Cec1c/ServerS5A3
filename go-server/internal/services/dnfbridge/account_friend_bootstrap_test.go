package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/accountfriend"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	proto "longheng.io/server/internal/modules/dnf/protocol"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func TestAccountFriendNormalTownBootstrapRestoresNonemptyDurableLists(t *testing.T) {
	s, session, repositories := newTownMoveTest(t)
	s.options.currentClientVersion = "official-24.9"
	s.adventureGroupTable = loadAdventureGroupTestTables(t)
	session.accountID, session.accountFromToken, session.loginPacketSeen = "dnf:1", true, true
	session.selectedCharacterID = 0
	ctx := context.Background()
	c, _, _ := repositories.Character.Load(ctx, "29")
	c.Name, c.Job, c.Level = "owner", "0", 90
	c.Stats["grow_type"] = 33
	c.Stats[currentDungeonTutorialCompletedKey] = currentDungeonTutorialCompleteFlag
	if err := repositories.Character.Save(ctx, c); err != nil {
		t.Fatal(err)
	}
	for id, name := range map[string]string{"dnf:1": "Owner", "dnf:2": "Requester", "dnf:3": "Accepted"} {
		if err := repositories.Account.Save(ctx, repo.AccountRecord{AccountID: id, RepresentAccountName: name, Metadata: map[string]string{"keep": "yes"}}); err != nil {
			t.Fatal(err)
		}
	}
	for id, account := range map[string]string{"31": "dnf:2", "32": "dnf:3"} {
		peer := c
		peer.CharacterID, peer.AccountID, peer.Name, peer.Slot = id, account, "peer"+id, 0
		if err := repositories.Character.Save(ctx, peer); err != nil {
			t.Fatal(err)
		}
	}
	owner := accountfriend.NewOwner(repositories)
	if err := owner.Request(ctx, accountfriend.Command{AccountID: "dnf:2", CharacterID: "31", TargetAccountID: "dnf:1", TargetName: "Owner", Text: "offline request"}); err != nil {
		t.Fatal(err)
	}
	if err := owner.Request(ctx, accountfriend.Command{AccountID: "dnf:1", CharacterID: "29", TargetAccountID: "dnf:3", TargetName: "Accepted", Text: "accepted request"}); err != nil {
		t.Fatal(err)
	}
	if err := owner.Accept(ctx, accountfriend.Command{AccountID: "dnf:3", CharacterID: "32", TargetAccountID: "dnf:1", TargetName: "Owner"}); err != nil {
		t.Fatal(err)
	}
	before, _, _ := repositories.Account.Load(ctx, "dnf:1")
	rows, pending, err := s.currentAccountFriendProjection(ctx, "dnf:1")
	if err != nil || len(rows) != 1 || rows[0].CharacterName != "peer32" || rows[0].Channel != 0 || len(pending) != 1 || pending[0].AdventureName != "Requester" || pending[0].Kind != 0 {
		t.Fatal("fixture must contain real durable nonempty lists", rows, pending, err)
	}
	wantInfo, _ := buildCurrentAccountFriendInfoBody(rows)
	wantPending, _ := buildCurrentAccountFriendPendingBody(pending)
	roster, _ := proto.BuildChannelPacket(uint16(dnfenum.UpperMsgCharacterRoster), nil, 0, proto.DefaultChannelClassification)
	if err := s.handleGameUpper(session, roster); err != nil {
		t.Fatal(err)
	}
	conn := session.conn.(*bufferConn)
	for stream := conn.write.Bytes(); len(stream) > 0; {
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, stream)
		stream = rest
		if packet.Header.MsgID == 1371 || packet.Header.MsgID == 1372 {
			t.Fatal("friends leaked before selection")
		}
	}
	var selected [2]byte
	binary.LittleEndian.PutUint16(selected[:], 29)
	selection, _ := proto.BuildChannelPacket(uint16(dnfenum.UpperMsgSelectCharacter), selected[:], 0, proto.DefaultChannelClassification)
	if err := s.handleGameUpper(session, selection); err != nil {
		t.Fatal(err)
	}
	conn.write.Reset()
	if err := s.handleDungeonTutorialFlag(session, []byte{currentDungeonTutorialFinalPrefix, 36, 0, 0, 0, currentDungeonTutorialFinalCommit}); err != nil {
		t.Fatal(err)
	}
	barrier, _ := proto.BuildChannelPacket(uint16(dnfenum.UpperMsgCheckUserConnection), nil, 0, proto.DefaultChannelClassification)
	if err := s.handleGameUpper(session, barrier); err != nil {
		t.Fatal(err)
	}
	infoAt, pendingAt, transitionAt := -1, -1, -1
	for index, stream := 0, conn.write.Bytes(); len(stream) > 0; index++ {
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, stream)
		stream = rest
		switch packet.Header.MsgID {
		case 1371:
			infoAt = index
			if !bytes.Equal(packet.Body, wantInfo) {
				t.Fatalf("wrong accepted rows %x", packet.Body)
			}
		case 1372:
			pendingAt = index
			if !bytes.Equal(packet.Body, wantPending) {
				t.Fatalf("wrong offline request %x", packet.Body)
			}
		case currentSceneTransitionMsgID:
			transitionAt = index
		}
	}
	if infoAt < 0 || pendingAt <= infoAt || transitionAt <= pendingAt {
		t.Fatalf("normal CMD2/4 path missing friends before town: info=%d pending=%d transition=%d", infoAt, pendingAt, transitionAt)
	}
	after, _, _ := repositories.Account.Load(ctx, "dnf:1")
	if before.Metadata["account_friend_v1:dnf:2"] != after.Metadata["account_friend_v1:dnf:2"] || before.Metadata["account_friend_v1:dnf:3"] != after.Metadata["account_friend_v1:dnf:3"] {
		t.Fatal("bootstrap rewrote friend persistence")
	}
	conn.write.Reset()
	if err := s.sendCurrentAccountFriendLists(session, "later_finish_loading_replacement"); err != nil {
		t.Fatal(err)
	}
	request, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if request.Header.MsgID != 1372 || !bytes.Equal(request.Body, wantPending) || len(rest) != 0 {
		t.Fatal("unchanged bootstrap replayed online rows or lost pending requests")
	}
}

func TestAccountFriendBootstrapRejectsWrongOwnerExpiredAndFailedSend(t *testing.T) {
	for _, kind := range []string{"wrong_owner", "expired", "failed_send"} {
		t.Run(kind, func(t *testing.T) {
			s, a, b, ac, _ := prepareAccountFriendTest(t)
			callAccountFriend(t, s, a, 1436, "Beta", "pending", 0)
			ac.write.Reset()
			switch kind {
			case "wrong_owner":
				a.accountID = b.accountID
			case "expired":
				s.unbindGameSession(a)
				ac.write.Reset()
			case "failed_send":
				a.conn = &expeditionRosterFailConn{}
			}
			err := s.sendCurrentAccountFriendLists(a, "bootstrap_boundary")
			if kind == "failed_send" {
				if err == nil {
					t.Fatal("send failure hidden")
				}
				if a.currentAccountFriendNames != nil && len(a.currentAccountFriendNames) > 0 {
					t.Fatal("failed send committed cache")
				}
			} else if err != nil || ac.write.Len() != 0 {
				t.Fatal("invalid session received another account lists", err)
			}
		})
	}
}

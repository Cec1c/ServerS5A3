package dnfbridge

import (
	"context"
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/characterfriend"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	proto "longheng.io/server/internal/modules/dnf/protocol"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
)

func TestCharacterFriendReselectionRestoresBeforeTownWithoutOp37(t *testing.T) {
	s, session, repositories := newTownMoveTest(t)
	s.options.currentClientVersion = "official-24.9"
	s.adventureGroupTable = loadAdventureGroupTestTables(t)
	// Initial marriage projection also reads the durable guest-invitation
	// index for single characters. Supply its rule dependency in this isolated
	// scene fixture instead of opening an unrelated default Script.pvf path.
	s.wedding.rules = &wedding.Rules{MinimumLevel: 30, QuestIDs: []int64{20737, 20738}}
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
	for _, id := range []string{"dnf:1", "dnf:2"} {
		if err := repositories.Account.Save(ctx, repo.AccountRecord{AccountID: id, RepresentAccountName: id, Metadata: map[string]string{"keep": "yes"}}); err != nil {
			t.Fatal(err)
		}
	}
	peer := c
	peer.CharacterID, peer.AccountID, peer.Name, peer.Slot = "31", "dnf:2", "friend", 0
	if err := repositories.Character.Save(ctx, peer); err != nil {
		t.Fatal(err)
	}
	if err := characterfriend.NewOwner(repositories).Add(ctx, "dnf:1", "29", "31"); err != nil {
		t.Fatal(err)
	}
	conn := session.conn.(*bufferConn)
	for cycle := 0; cycle < 2; cycle++ {
		if cycle > 0 {
			if err := s.handleUpperReturnSelectCharacter(session, nil); err != nil {
				t.Fatal(err)
			}
		}
		roster, _ := proto.BuildChannelPacket(uint16(dnfenum.UpperMsgCharacterRoster), nil, 0, proto.DefaultChannelClassification)
		if err := s.handleGameUpper(session, roster); err != nil {
			t.Fatal(err)
		}
		var selected [2]byte
		conn.write.Reset()
		binary.LittleEndian.PutUint16(selected[:], 29)
		selection, _ := proto.BuildChannelPacket(uint16(dnfenum.UpperMsgSelectCharacter), selected[:], 0, proto.DefaultChannelClassification)
		if err := s.handleGameUpper(session, selection); err != nil {
			t.Fatal(err)
		}
		if err := s.handleDungeonTutorialFlag(session, []byte{currentDungeonTutorialFinalPrefix, 36, 0, 0, 0, currentDungeonTutorialFinalCommit}); err != nil {
			t.Fatal(err)
		}
		barrier, _ := proto.BuildChannelPacket(uint16(dnfenum.UpperMsgCheckUserConnection), nil, 0, proto.DefaultChannelClassification)
		if err := s.handleGameUpper(session, barrier); err != nil {
			t.Fatal(err)
		}
		friendAt, transitionAt := -1, -1
		for index, stream := 0, conn.write.Bytes(); len(stream) > 0; index++ {
			packet, rest := splitCurrentGameServerUpperPacketAuto(t, stream)
			stream = rest
			if packet.Header.MsgID == currentCharacterFriendListOpcode {
				friendAt = index
				if binary.LittleEndian.Uint32(packet.Body[4:]) != 1 {
					t.Fatal("restored friend list is empty")
				}
			}
			if packet.Header.MsgID == currentSceneTransitionMsgID {
				transitionAt = index
			}
		}
		if friendAt < 0 || transitionAt <= friendAt {
			t.Fatalf("cycle %d: friends=%d town=%d, no client op37 was sent", cycle, friendAt, transitionAt)
		}
	}
}

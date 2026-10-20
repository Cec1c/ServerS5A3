package dnfbridge

import (
	"bytes"
	"context"
	"strconv"
	"testing"

	proto "longheng.io/server/internal/modules/dnf/protocol"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func prepareUserChannelTest(t *testing.T) (*Service, *gameSession, *gameSession, *bufferConn, *bufferConn) {
	t.Helper()
	s, a, b, ac, bc := prepareAccountFriendTest(t)
	s.options.channelInfoFile = tempChannelInfoFile(t)
	s.options.channelServerID = 1
	s.options.channelAdvertiseID = 0
	catalog := testChannelCatalog(t)
	s.catalog = catalog
	channel, ok := catalog.Channel(11)
	if !ok {
		t.Fatal("test directory lacks channel 11")
	}
	b.channel = channel
	return s, a, b, ac, bc
}

func TestCurrentUserChannelRejectsRawServerGroupAliasing(t *testing.T) {
	for _, owner := range []string{"source", "target"} {
		for _, value := range []int64{-1, 250, 255, 256, 65536} {
			t.Run(owner+"/"+strconv.FormatInt(value, 10), func(t *testing.T) {
				s, a, b, ac, _ := prepareUserChannelTest(t)
				g, _ := s.repositoryGroup()
				id := a.selectedCharacterID
				if owner == "target" {
					id = b.selectedCharacterID
				}
				row, _, _ := g.Character.Load(context.Background(), strconv.Itoa(int(id)))
				if row.Stats == nil {
					row.Stats = map[string]int64{}
				}
				row.Stats["server_group_id"] = value
				if err := g.Character.Save(context.Background(), row); err != nil {
					t.Fatal(err)
				}
				handled, err := s.handleCurrentUserChannel(a, 804, 1, officialFriendRequest(291, 0, 0, "来来来"))
				if !handled || err != nil {
					t.Fatalf("handled=%v err=%v", handled, err)
				}
				ack, _ := splitGameServerUpperPacket(t, ac.write.Bytes())
				if !bytes.Equal(ack.Body, []byte{0, 0}) {
					t.Fatalf("raw server aliased: %x", ack.Body)
				}
			})
		}
	}
}

type userChannelSourceSubstitution struct{ repo.CharacterRepository }

func (r userChannelSourceSubstitution) Load(ctx context.Context, id string) (repo.CharacterRecord, bool, error) {
	row, found, err := r.CharacterRepository.Load(ctx, id)
	if id == "1" {
		row.CharacterID = "8"
	}
	return row, found, err
}

func TestCurrentUserChannelRejectsSubstitutedSourceCharacter(t *testing.T) {
	s, a, _, ac, _ := prepareUserChannelTest(t)
	g, _ := s.repositoryGroup()
	g.Character = userChannelSourceSubstitution{g.Character}
	s.repositoryProvider = func() (repo.Group, bool) { return g, true }
	handled, err := s.handleCurrentUserChannel(a, 804, 1, officialFriendRequest(291, 0, 0, "来来来"))
	if !handled || err != nil {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	ack, _ := splitGameServerUpperPacket(t, ac.write.Bytes())
	if !bytes.Equal(ack.Body, []byte{0, 0}) {
		t.Fatalf("source identity substituted: %x", ack.Body)
	}
}

func TestCurrentUserChannelUsesRealDirectoryAndPreservesOwners(t *testing.T) {
	s, a, b, ac, bc := prepareUserChannelTest(t)
	g, _ := s.repositoryGroup()
	beforeA, _, _ := g.Account.Load(context.Background(), "dnf:1")
	beforeB, _, _ := g.Account.Load(context.Background(), "dnf:2")
	request := officialFriendRequest(291, 0, 0, "来来来")
	for _, transport := range []string{"legacy", "upper"} {
		ac.write.Reset()
		if transport == "legacy" {
			if err := s.handleGameCommand(a, 1, 804, request); err != nil {
				t.Fatal(err)
			}
		} else {
			frame, err := proto.BuildChannelPacket(804, request, 1, 1)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.handleGameUpper(a, frame); err != nil {
				t.Fatal(err)
			}
		}
		ack, rest := splitGameServerUpperPacket(t, ac.write.Bytes())
		var w packetWriter
		w.writeByte(1)
		w.writeByte(0)
		w.writeRawDstr(rosterNameBytes("来来来"))
		w.writeByte(0) // advertised server, source ETC server is 1.
		w.writeByte(11)
		if ack.Header.MsgID != 804 || ack.Header.Classification != 1 || len(rest) != 0 || !bytes.Equal(ack.Body, w.bytes()) {
			t.Fatalf("%s ack=%+v rest=%x", transport, ack, rest)
		}
	}
	if bc.write.Len() != 0 || b.channel.ID != 11 {
		t.Fatal("query modified target presentation or channel")
	}
	afterA, _, _ := g.Account.Load(context.Background(), "dnf:1")
	afterB, _, _ := g.Account.Load(context.Background(), "dnf:2")
	if beforeA.RepresentAccountName != afterA.RepresentAccountName || beforeB.RepresentAccountName != afterB.RepresentAccountName || afterA.Metadata["keep"] != "yes" || afterB.Metadata["keep"] != "yes" {
		t.Fatal("query changed account owner")
	}
}

func TestCurrentUserChannelRejectsUnverifiedAndUnrepresentablePositions(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Service, *gameSession, *gameSession)
		group  byte
		target string
		code   byte
	}{
		{"offline", func(s *Service, a, b *gameSession) { s.unbindGameSession(b) }, 0, "来来来", 100},
		{"unauthenticated_source", func(s *Service, a, b *gameSession) { a.accountFromToken = false }, 0, "来来来", 0},
		{"unauthenticated_target", func(s *Service, a, b *gameSession) { b.accountFromToken = false }, 0, "来来来", 100},
		{"target_owner_mismatch", func(s *Service, a, b *gameSession) { b.accountID = "dnf:1" }, 0, "来来来", 100},
		{"source_owner_mismatch", func(s *Service, a, b *gameSession) { a.accountID = "dnf:2" }, 0, "来来来", 0},
		{"foreign_name_alias", nil, 1, "来来来", 0},
		{"missing_character", nil, 0, "不存在", 0},
		{"above_byte_channel", func(s *Service, a, b *gameSession) { b.channel.ID = 256 }, 0, "来来来", 0},
		{"unadvertised_channel", func(s *Service, a, b *gameSession) { b.channel.Port++ }, 0, "来来来", 0},
		{"no_catalog", func(s *Service, a, b *gameSession) { s.catalog = nil }, 0, "来来来", 0},
		{"server_sentinel", func(s *Service, a, b *gameSession) { s.options.channelAdvertiseID = 250 }, 0, "来来来", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, a, b, ac, bc := prepareUserChannelTest(t)
			if tc.change != nil {
				tc.change(s, a, b)
			}
			handled, err := s.handleCurrentUserChannel(a, 804, 1, officialFriendRequest(291, 0, tc.group, tc.target))
			if !handled || err != nil {
				t.Fatalf("handled=%v err=%v", handled, err)
			}
			ack, rest := splitGameServerUpperPacket(t, ac.write.Bytes())
			if !bytes.Equal(ack.Body, []byte{0, tc.code}) || len(rest) != 0 || bc.write.Len() != 0 {
				t.Fatalf("unexpected failure=%x rest=%x peer=%x", ack.Body, rest, bc.write.Bytes())
			}
		})
	}
}

func TestCurrentUserChannelMalformedAndClassGuards(t *testing.T) {
	request := officialFriendRequest(291, 0, 0, "来来来")
	for _, bad := range [][]byte{nil, request[:len(request)-1], append(append([]byte(nil), request...), 0), officialFriendRequest(291, 0, 250, "来来来")} {
		s, a, _, ac, _ := prepareUserChannelTest(t)
		handled, err := s.handleCurrentUserChannel(a, 804, 1, bad)
		if !handled || err != nil {
			t.Fatalf("handled=%v err=%v", handled, err)
		}
		ack, _ := splitGameServerUpperPacket(t, ac.write.Bytes())
		if !bytes.Equal(ack.Body, []byte{0, 0}) {
			t.Fatalf("bad request ack=%x", ack.Body)
		}
	}
	s, a, _, ac, _ := prepareUserChannelTest(t)
	for _, class := range []byte{0, 2} {
		handled, err := s.handleCurrentUserChannel(a, 804, class, request)
		if !handled || err != nil || ac.write.Len() != 0 {
			t.Fatal("wrong class replied")
		}
	}
	s.options.currentClientVersion = "legacy"
	if handled, err := s.handleCurrentUserChannel(a, 804, 1, request); handled || err != nil {
		t.Fatal("historical client intercepted")
	}
}

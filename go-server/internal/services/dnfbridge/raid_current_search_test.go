package dnfbridge

import (
	"encoding/binary"
	"fmt"
	"testing"

	proto "longheng.io/server/internal/modules/dnf/protocol"
	"longheng.io/server/internal/modules/dnf/raid"
)

func currentSearchBody(offset uint32, column byte, reverse, own, exclude bool, name string) []byte {
	var w packetWriter
	w.writeByte(0)
	w.writeUint32(offset)
	w.writeByte(column)
	for _, v := range []bool{reverse, own, exclude} {
		if v {
			w.writeByte(1)
		} else {
			w.writeByte(0)
		}
	}
	w.writeRawDstr(rosterNameBytes(name))
	return w.bytes()
}

func prepareCurrentSearchTest(t *testing.T) (*Service, *gameSession, *bufferConn) {
	s, a, ac := prepareCurrentRaidTest(t)
	base, err := s.createCurrentRuntimeRaid(a, raid.CurrentInfoRequest{NameBytes: []byte("raid")})
	if err != nil {
		t.Fatal(err)
	}
	s.raids = make(map[uint32]*runtimeRaidState)
	for i := 0; i < 38; i++ {
		state := cloneRuntimeRaidState(base)
		state.RaidKey = 0x01170001 + uint32(i)
		state.NameBytes = rosterNameBytes(fmt.Sprintf("第%02d攻坚", i))
		for n := 1; n <= i%3; n++ {
			member := state.Members[0]
			member.CharID += uint16(n)
			state.Members = append(state.Members, member)
		}
		switch i {
		case 34:
			state.Started = true
		case 35:
			state.RaidKey = 0x01180001
		case 36:
			state.RaidKey = 0x01200001
			state.RouteOrRaidType = 1
		case 37:
			state.CurrentLayout = false
		}
		s.raids[state.RaidKey] = &state
	}
	ac.write.Reset()
	return s, a, ac
}

func TestCurrentRaidSearchActualDispatchFiltersPagesAndClearsWith753(t *testing.T) {
	for _, c := range []struct {
		name                  string
		offset                uint32
		column                byte
		reverse, own, exclude bool
		filter                string
		total, count, first   uint32
	}{
		{"default", 0, 5, false, false, true, "", 35, 33, 0x01170001},
		{"last-own-page", 33, 3, false, true, true, "", 34, 1, 0x01170022},
		{"out-of-range", 0xffffffff, 5, false, true, true, "", 34, 0, 0},
		{"include-started", 0, 3, false, true, false, "", 35, 33, 0x01170001},
		{"chinese-name", 0, 5, false, false, true, "第12", 1, 1, 0x0117000d},
		{"descending-members", 0, 5, true, true, true, "", 34, 33, 0x01170003},
		{"other-channel-name", 0, 5, false, false, true, "第35", 1, 1, 0x01180001},
		{"only-channel-name", 0, 5, false, true, true, "第35", 0, 0, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, a, ac := prepareCurrentSearchTest(t)
			body := currentSearchBody(c.offset, c.column, c.reverse, c.own, c.exclude, c.filter)
			frame, err := proto.BuildChannelPacket(831, body, 1, 1)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.handleGameUpper(a, frame); err != nil {
				t.Fatal(err)
			}
			packet, rest := splitGameServerUpperPacket(t, ac.write.Bytes())
			if packet.Header.MsgID != 753 || len(rest) != 0 || len(packet.Body) < 12 {
				t.Fatal("search missed actual native completion opcode", packet, len(rest))
			}
			p := packet.Body
			offset := c.offset
			if offset > c.total {
				offset = c.total
			}
			if binary.LittleEndian.Uint32(p) != c.total || binary.LittleEndian.Uint32(p[4:]) != offset || binary.LittleEndian.Uint32(p[8:]) != c.count {
				t.Fatalf("search page: %x", p[:12])
			}
			if c.count > 0 && binary.LittleEndian.Uint32(p[12:]) != c.first {
				t.Fatalf("wrong filter/order first=%#x", binary.LittleEndian.Uint32(p[12:]))
			}
			if c.count == 0 && len(p) != 12 {
				t.Fatal("empty page has row payload")
			}
			if len(s.raids) != 38 || !s.raids[0x01170023].Started {
				t.Fatal("read-only search mutated raid owner")
			}
		})
	}
}

func TestCurrentRaidSearchCatalogUses590AndRetainsStarted(t *testing.T) {
	s, a, ac := prepareCurrentSearchTest(t)
	if err := s.handleGameCommand(a, 1, 831, []byte{1}); err != nil {
		t.Fatal(err)
	}
	packet, rest := splitGameServerUpperPacket(t, ac.write.Bytes())
	if packet.Header.MsgID != 590 || binary.LittleEndian.Uint32(packet.Body) != 35 || len(rest) != 0 {
		t.Fatal("mode1 is not same-channel catalog", packet)
	}
	ac.write.Reset()
	s.options.currentClientVersion = "legacy"
	if handled, err := s.handleOnlineRaidCommand(a, 831, []byte{1}); handled || err != nil || ac.write.Len() != 0 {
		t.Fatal("current search took over legacy owner")
	}
}

func TestCurrentRaidSearchRejectsUnauthorizedStaleAndMalformed(t *testing.T) {
	for _, kind := range []string{"unauth", "wrong-account", "stale", "mode", "truncated", "control", "bad-encoding", "ordinary-channel"} {
		t.Run(kind, func(t *testing.T) {
			s, a, ac := prepareCurrentSearchTest(t)
			body := currentSearchBody(0, 5, false, false, true, "")
			switch kind {
			case "unauth":
				a.accountFromToken = false
			case "wrong-account":
				a.accountID = "other"
			case "stale":
				id := a.selectedCharacterID
				s.unbindGameSession(a)
				s.bindGameSessionCharacter(&gameSession{conn: &bufferConn{}}, id)
				a.selectedCharacterID = id
			case "mode":
				body = []byte{2}
			case "truncated":
				body = body[:12]
			case "control":
				body = currentSearchBody(0, 5, false, false, true, "a\x00b")
			case "bad-encoding":
				body = append(body, 0xff)
				binary.LittleEndian.PutUint32(body[9:], 1)
			case "ordinary-channel":
				a.channel.Type = 11
			}
			if err := s.handleCurrentRaidSearch(a, 831, body); err != nil {
				t.Fatal(err)
			}
			packet, rest := splitGameServerUpperPacket(t, ac.write.Bytes())
			if packet.Header.MsgID != 831 || packet.Body[0] != 0 || len(rest) != 0 || len(s.raids) != 38 {
				t.Fatal("invalid search leaked directory or mutated owner", packet)
			}
		})
	}
}

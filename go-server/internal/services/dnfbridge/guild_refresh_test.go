package dnfbridge

import (
	"bytes"
	"context"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/guild"
	"longheng.io/server/internal/modules/dnf/protocol"
)

func TestGuildRefreshNativeOpenWindowSequence(t *testing.T) {
	s, p, repos := guildContractFixture(t)
	before, _, _ := repos.Settings.Load(context.Background(), guild.RegistryScope)
	for _, upper := range []bool{false, true} {
		p.conn.(*bufferConn).write.Reset()
		for i, op := range []uint16{339, 352, 1300, 1301} {
			wire, _ := protocol.BuildChannelPacket(op, nil, uint16(40+i), 1)
			if upper {
				if err := s.handleGameUpper(p, wire); err != nil {
					t.Fatal(err)
				}
			} else {
				packets, rest, skipped, err := protocol.SplitLatestGameStream(wire, 65535)
				if err != nil || len(packets) != 1 || len(rest) != 0 || skipped != 0 {
					t.Fatal("bad stream")
				}
				if err := s.handleGameStreamPacket(p, packets[0]); err != nil {
					t.Fatal(err)
				}
			}
		}
		reg, _ := guild.Load(context.Background(), repos.Settings)
		if !bytes.Equal(guildReply(t, p, 70), buildCurrentGuildManagementBody(reg.Guilds[1], 3, s.gameplayNow(), 1)) {
			t.Fatal("guild window has stale model")
		}
		for _, op := range []uint16{352, 1300, 1301} {
			if guildReply(t, p, op)[0] != 1 {
				t.Fatal("query failed", op)
			}
		}
		var ops []uint16
		for wire := p.conn.(*bufferConn).write.Bytes(); len(wire) > 0; {
			packet, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
			wire = rest
			ops = append(ops, packet.Header.MsgID)
		}
		if !reflect.DeepEqual(ops, []uint16{70, 352, 1300, 1301}) {
			t.Fatal("refresh emitted unrelated membership or invented ACK", ops)
		}
	}
	after, _, _ := repos.Settings.Load(context.Background(), guild.RegistryScope)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("queries wrote guild state")
	}
}

func TestGuildRefreshRejectsForeignOrUnreadySession(t *testing.T) {
	for _, mode := range []string{"account", "scene", "body", "class"} {
		t.Run(mode, func(t *testing.T) {
			s, p, _ := guildContractFixture(t)
			body := []byte(nil)
			class := byte(1)
			switch mode {
			case "account":
				p.accountID = "foreign"
			case "scene":
				p.townSceneReadyCharacterID = 0
			case "body":
				body = []byte{1}
			case "class":
				class = 0
			}
			wire, _ := protocol.BuildChannelPacket(339, body, 40, 1)
			wire[0] = class
			packets, _, _, err := protocol.SplitLatestGameStream(wire, 65535)
			if err != nil || len(packets) != 1 {
				t.Fatal(err)
			}
			if err := s.handleGameStreamPacket(p, packets[0]); err != nil {
				t.Fatal(err)
			}
			if p.conn.(*bufferConn).write.Len() != 0 {
				t.Fatal("invalid query exposed guild state")
			}
		})
	}
}

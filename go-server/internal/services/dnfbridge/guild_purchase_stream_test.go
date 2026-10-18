package dnfbridge

import (
	"context"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/guild"
	"longheng.io/server/internal/modules/dnf/protocol"
)

func TestGuildPurchaseStreamSequenceIncludesLegacyTraffic(t *testing.T) {
	s, peers, repos := guildPurchaseFixture(t)
	p := peers[0]
	sendGuildPurchase(t, s, p, 12, 5)
	// A full connection sequence cycle can consist almost entirely of legacy
	// commands. Ignoring those headers would mistake the next purchase for a retry.
	for _, seq := range []uint16{20000, 40000, 60000} {
		wire, _ := protocol.BuildChannelPacket(747, nil, seq, 1)
		packets, rest, skipped, err := protocol.SplitLatestGameStream(wire, 65535)
		if err != nil || len(packets) != 1 || len(rest) != 0 || skipped != 0 || packets[0].Kind != protocol.LatestGameStreamLegacy {
			t.Fatalf("history stream: %+v %v", packets, err)
		}
		if err := s.handleGameStreamPacket(p, packets[0]); err != nil {
			t.Fatal(err)
		}
	}
	sendGuildPurchase(t, s, p, 12, 5)
	sendGuildPurchase(t, s, p, 12, 5)
	// The upper route must share the same identity when replaying this header.
	wire, _ := protocol.BuildChannelPacket(761, []byte{12, 0, 0, 0, 0, 0, 0, 0}, 5, 1)
	if err := s.handleGameUpper(p, wire); err != nil {
		t.Fatal(err)
	}
	reg, err := guild.Load(context.Background(), repos.Settings)
	if err != nil || reg.Guilds[1].Mileage != 20000 || reg.Guilds[1].Cargo.Capacity != 24 || len(reg.CargoPurchases) != 2 {
		t.Fatalf("sequence cycle/retry: %+v %v", reg, err)
	}
}

func TestGuildPurchaseStreamRejectsWrongClass(t *testing.T) {
	for _, mode := range []string{"class"} {
		t.Run(mode, func(t *testing.T) {
			s, peers, repos := guildPurchaseFixture(t)
			before, _, _ := repos.Settings.Load(context.Background(), guild.RegistryScope)
			classification := byte(0)
			wire, _ := protocol.BuildChannelPacket(761, []byte{11, 0, 0, 0, 0, 0, 0, 0}, 10, classification)
			wire[0] = classification // The builder defaults zero to command class 1.
			packets, rest, skipped, err := protocol.SplitLatestGameStream(wire, 65535)
			if err != nil || len(packets) != 1 || len(rest) != 0 || skipped != 0 {
				t.Fatal("invalid stream fixture")
			}
			err = s.handleGameStreamPacket(peers[0], packets[0])
			if err != nil {
				t.Fatal(err)
			}
			after, _, _ := repos.Settings.Load(context.Background(), guild.RegistryScope)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("invalid request changed guild")
			}
			for wire := peers[0].conn.(*bufferConn).write.Bytes(); len(wire) > 0; {
				packet, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
				wire = rest
				if packet.Header.MsgID == 761 {
					t.Fatal("invalid request reached purchase handler")
				}
			}
		})
	}
}

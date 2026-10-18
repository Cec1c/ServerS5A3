package dnfbridge

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/guild"
	"longheng.io/server/internal/modules/dnf/protocol"
)

// V110 1196AE0 also calls 2978080: opening the window refreshes the
// online/all-member roster between 352 and the two hongbao queries.
func TestGuildRefreshNativeRosterOpenSequence(t *testing.T) {
	for _, upper := range []bool{false, true} {
		for _, roster := range []uint16{67, 140} {
			t.Run(fmt.Sprintf("upper_%v_roster_%d", upper, roster), func(t *testing.T) {
				s, p, repos := guildContractFixture(t)
				before, _, err := repos.Settings.Load(context.Background(), guild.RegistryScope)
				if err != nil {
					t.Fatal(err)
				}
				p.conn.(*bufferConn).write.Reset()
				for i, op := range []uint16{339, 352, roster, 1300, 1301} {
					var body []byte
					if op == 140 {
						body = []byte{0}
					}
					wire, err := protocol.BuildChannelPacket(op, body, uint16(80+i), 1)
					if err != nil {
						t.Fatal(err)
					}
					if upper {
						err = s.handleGameUpper(p, wire)
					} else {
						packets, rest, skipped, splitErr := protocol.SplitLatestGameStream(wire, 65535)
						if splitErr != nil || len(packets) != 1 || len(rest) != 0 || skipped != 0 {
							t.Fatal("invalid native request fixture", splitErr)
						}
						err = s.handleGameStreamPacket(p, packets[0])
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				var ops []uint16
				for wire := p.conn.(*bufferConn).write.Bytes(); len(wire) > 0; {
					packet, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
					wire = rest
					ops = append(ops, packet.Header.MsgID)
				}
				if !reflect.DeepEqual(ops, []uint16{70, 352, 70, roster, 1300, 1301}) {
					t.Fatalf("read-only window refresh replayed membership notifications: %v", ops)
				}
				for _, op := range []uint16{352, roster, 1300, 1301} {
					if guildReply(t, p, op)[0] != 1 {
						t.Fatalf("query %d failed", op)
					}
				}
				after, _, err := repos.Settings.Load(context.Background(), guild.RegistryScope)
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Fatal("window refresh mutated persisted guild state", err)
				}
			})
		}
	}
}

func TestGuildRefreshRosterKeepsBenefitProjection(t *testing.T) {
	s, p, repos := guildContractFixture(t)
	ctx := context.Background()
	reg, err := guild.Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	reg.Guilds[1].Benefits = map[byte]guild.Benefit{5: {Type: 5, Value: 169}}
	if err := guild.Save(ctx, repos.Settings, reg, s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	p.conn.(*bufferConn).write.Reset()
	guildDispatch(t, s, p, 140, []byte{0})
	if body := guildReply(t, p, 487); len(body) != 17 || body[0] != 1 || body[1] != 169 {
		t.Fatalf("roster did not restore the current benefit: %x", body)
	}
	for wire := p.conn.(*bufferConn).write.Bytes(); len(wire) > 0; {
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
		wire = rest
		if packet.Header.MsgID == 127 || packet.Header.MsgID == 308 {
			t.Fatal("benefit refresh replayed membership", packet.Header.MsgID)
		}
	}
}

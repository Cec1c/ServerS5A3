package dnfbridge

import (
	"fmt"
	"testing"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
)

// An actually loaded energy scene retains the successful selector receipt
// from its own entry. The older fixtures started with all three flags false.
func TestRealPVFHatcheryTransferRenewsConsumedEnergySelector(t *testing.T) {
	for _, target := range []uint32{222, 223, 224} {
		t.Run(fmt.Sprint(target), func(t *testing.T) {
			s, p, old, _ := actualAntonHatchWarpOwnerFor(t, target)
			p.enterSelectDungeonSent = true
			p.enterSelectDungeonAckSent = true
			p.enterSelectDungeonContextSent = true
			origin := old.townReturnOrigin
			conn := p.conn.(*bufferConn)
			conn.write.Reset()
			if err := s.handleDungeonSelectUpper(p, encodeCurrentDungeonSelectRequest(dungeoncmd.SelectDungeonRequest{DungeonID: target, Difficulty: 2})); err != nil {
				t.Fatal(err)
			}
			r := p.dungeon.runtime
			if r == old || r == nil || r.Dungeon.ID != int64(target) || r.antonHatcheryTransfer == nil || r.townReturnOrigin != origin {
				t.Fatal("transfer did not preserve target ownership and return origin")
			}
			stream := conn.write.Bytes()
			var order []uint16
			for len(stream) != 0 {
				packet, rest := splitGameServerUpperPacket(t, stream)
				stream = rest
				if packet.Header.Classification == 0 && (packet.Header.MsgID == 27 || packet.Header.MsgID == 28 || packet.Header.MsgID == 29) {
					order = append(order, packet.Header.MsgID)
				}
			}
			if len(order) != 3 || order[0] != 27 || order[1] != 28 || order[2] != 29 {
				t.Fatalf("loaded energy selector was reused across hatchery scene: %v", order)
			}
			if !p.enterSelectDungeonSent || !p.enterSelectDungeonAckSent || !p.enterSelectDungeonContextSent {
				t.Fatal("renewal discarded request ownership or did not commit fresh context")
			}
		})
	}
}

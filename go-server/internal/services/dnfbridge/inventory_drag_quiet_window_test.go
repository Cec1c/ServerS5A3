package dnfbridge

import (
	"context"
	"fmt"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
)

// A same-bag drag cannot be an automatic worn-slot reassertion. Exercise the
// actual transaction and ACK, without resolving a pending request by hand.
func TestInventoryDragDoesNotWaitForWearQuietWindow(t *testing.T) {
	for _, marker := range []byte{0, 1, 2} {
		t.Run(fmt.Sprint(marker), func(t *testing.T) {
			t.Setenv(op19PlayerFlagEnv, "")
			t.Setenv(op19PlayerFlagQuietEnv, "400")
			t.Setenv(itemSlotFamilyGuardEnv, "enforce")
			s, session, conn, logger, _, repos := newItemSlotFamilyGuardFixture(t)
			t.Cleanup(func() { _ = logger.Close() })
			s.packetLog = nil
			for _, move := range [][2]int16{{12, 13}, {13, 14}, {14, 12}} {
				body := itemSlotFamilyGuardMoveBody(0, move[0], 0, move[1])
				body[26] = marker
				conn.write.Reset()
				handled, err := s.handleAlignedGameCommand(session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketMoveItemspace), body)
				if err != nil || !handled {
					t.Fatalf("move %v handled=%t err=%v", move, handled, err)
				}
				if session.hasOp19QuietPending() {
					t.Fatalf("ordinary bag move %v was delayed by the wear quiet window", move)
				}
				if conn.write.Len() == 0 {
					t.Fatal("move did not answer immediately")
				}
				packet, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
				if packet.Header.MsgID != 19 || len(packet.Body) != 11 || packet.Body[0] != 1 {
					t.Fatalf("move ACK = %+v", packet)
				}
				record, found, err := repos.Inventory.Load(context.Background(), itemSlotFamilyGuardTestCharacterID)
				if err != nil || !found {
					t.Fatalf("reload found=%t err=%v", found, err)
				}
				if record.Slots[fmt.Sprintf("0:%d", move[1])].ItemID != 101040127 || record.Slots[fmt.Sprintf("0:%d", move[0])].ItemID != 0 {
					t.Fatalf("move %v did not persist before returning: %+v", move, record.Slots)
				}
			}
		})
	}
}

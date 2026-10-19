package packageitem

import (
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
)

func TestSeriaLuckProjectionOnlyFollowsSuccessfulSeriaOpens(t *testing.T) {
	for _, opcode := range []dnfenum.CmdPacket{dnfenum.CmdPacketUseRandomboxItem, dnfenum.CmdPacketUseRandomboxItemExpand} {
		for _, success := range []bool{false, true} {
			for _, itemID := range []int64{currentSeriaLuckItemID, 8001} {
				got := ownerAppliedMagicBoxResult(uint16(opcode), "测试幸运值显示", MagicBoxResult{
					Success: success, BoxItemID: itemID, SeriaLuckBefore: 7, SeriaLuckAfter: 1,
				})
				if want := success && itemID == currentSeriaLuckItemID; (got.SeriaLuck != nil) != want {
					t.Fatalf("opcode=%d success=%t item=%d projection=%+v", opcode, success, itemID, got.SeriaLuck)
				}
				if got.SeriaLuck != nil && (got.SeriaLuck.Before != 7 || got.SeriaLuck.After != 1) {
					t.Fatalf("丢失已提交的幸运值：%+v", got.SeriaLuck)
				}
			}
		}
	}
}

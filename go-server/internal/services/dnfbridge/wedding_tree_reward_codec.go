package dnfbridge

import (
	"encoding/binary"
	"longheng.io/server/internal/modules/dnf/wedding"
)

func buildCurrentWeddingTreeHarvestReward(reward wedding.TreeReward) []byte {
	b := []byte{1, 0}
	b = binary.LittleEndian.AppendUint32(b, uint32(reward.ItemID))
	return binary.LittleEndian.AppendUint32(b, uint32(reward.Count))
}

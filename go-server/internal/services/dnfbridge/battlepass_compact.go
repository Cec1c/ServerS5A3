package dnfbridge

import (
	"encoding/binary"
	"errors"
	bp "longheng.io/server/internal/modules/dnf/battlepass"
)

var errBattlePassCatalogMismatch = errors.New("客户端与服务端战令PVF配置不一致")

// BPA6仅360字节：96字节数值头、32字节配置摘要、26项ID/状态/进度、20字节领取位及4字节保留。
func encodeBattlePassCompactSnapshot(c *bp.Catalog, v bp.Snapshot) []byte {
	b := make([]byte, 360)
	copy(b, "BPA6")
	put := func(off int, n uint32) { binary.LittleEndian.PutUint32(b[off:off+4], n) }
	put(4, v.Policy.ID)
	put(8, bp.DayNumber(v.Policy.Start))
	put(12, v.State.Day)
	put(16, v.State.Week)
	put(20, v.State.Level)
	put(24, v.State.XP)
	put(28, c.Experience[v.State.Level])
	binary.LittleEndian.PutUint64(b[32:40], uint64(v.Policy.End.Unix()))
	put(40, 1)
	if v.State.Premium {
		put(40, 3)
	}
	put(44, 26)
	put(48, 80)
	put(52, uint32(len(v.MailIDs)))
	put(56, uint32(c.Tier[v.State.Level]))
	put(60, bp.PremiumPrice)
	binary.LittleEndian.PutUint64(b[64:72], uint64(max(v.CeraBalance, 0)))
	binary.LittleEndian.PutUint64(b[72:80], uint64(max(v.TokenCeraBalance, 0)))
	binary.LittleEndian.PutUint64(b[80:88], uint64(max(v.HappyTokenCeraBalance, 0)))
	digest := c.WireDigest()
	copy(b[96:128], digest[:])
	for i, m := range v.Missions {
		p := b[128+i*8 : 136+i*8]
		binary.LittleEndian.PutUint16(p, uint16(m.ID))
		binary.LittleEndian.PutUint16(p[2:], m.Flags)
		binary.LittleEndian.PutUint32(p[4:], m.Progress)
	}
	for track := 0; track < 2; track++ {
		copy(b[336+track*10:346+track*10], v.State.RewardClaimed[track][:])
	}
	return b
}

package battlepass

import (
	"crypto/sha256"
	"encoding/binary"
	"sort"
	"unicode/utf16"
)

// WireDigest核对客户端本地PVF解析结果；不改变玩家持久化的ConfigFingerprint。
// BPC1按任务ID排序，固定整数宽度与UTF-16LE文字，独立于共享字符串偏移和空白。
func (c *Catalog) WireDigest() [32]byte {
	h := sha256.New()
	h.Write([]byte("BPC1"))
	put := func(v uint32) { var p [4]byte; binary.LittleEndian.PutUint32(p[:], v); h.Write(p[:]) }
	ids := make([]uint32, 0, len(c.Missions))
	for id := range c.Missions {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	put(uint32(len(ids)))
	for _, id := range ids {
		m := c.Missions[id]
		put(m.ID)
		put(m.Target)
		put(m.XP)
		put(m.Kind)
		put(uint32(m.Param))
		title := utf16.Encode([]rune(m.Text))
		put(uint32(len(title)))
		for _, unit := range title {
			var p [2]byte
			binary.LittleEndian.PutUint16(p[:], unit)
			h.Write(p[:])
		}
	}
	put(uint32(len(c.Daily)))
	for _, row := range c.Daily {
		for _, id := range row {
			put(id)
		}
	}
	put(uint32(len(c.Weekly)))
	for _, row := range c.Weekly {
		for _, id := range row {
			put(id)
		}
	}
	put(uint32(len(c.Season)))
	for _, id := range c.Season {
		put(id)
	}
	for lv := 1; lv <= 80; lv++ {
		put(c.Experience[lv])
	}
	for lv := 1; lv <= 80; lv++ {
		put(uint32(c.Tier[lv]))
	}
	for track := 0; track < 2; track++ {
		for lv := 1; lv <= 80; lv++ {
			row := c.Rewards[track][lv]
			put(uint32(len(row)))
			for _, reward := range row {
				put(reward.ItemID)
				put(reward.Count)
			}
		}
	}
	put(c.PremiumRatio)
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

package packageitem

import (
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
)

func TestMagicBoxResultRowsKeepRewardSealAcrossAllWindows(t *testing.T) {
	for _, sealed := range []bool{false, true} {
		item := alignedcmd.MagicBoxRewardItem{ItemID: 700, Kind: "equipment", Durability: 50, Seal: sealed}
		reward := magicBoxResultReward(item, 1, 9)
		result := MagicBoxResult{ClientType: 4, OpenCount: 1, BoxSlotIndex: 75, MaterialSlotIndex: -1, Rewards: []MagicBoxGrantedReward{reward}, DisplayRewards: []MagicBoxGrantedReward{reward}, DoubleRewards: []MagicBoxGrantedReward{reward}}
		want := byte(0)
		if sealed {
			want = 1
		}
		for _, window := range []struct {
			name    string
			data    []byte
			offsets []int
		}{
			{"单开", buildMagicBoxSingleAck(result), []int{9}},
			{"普通连开", buildMagicBoxExpandAck(result), []int{14}},
			{"赛丽亚普通与双倍", buildMagicBoxSeriaBatchAck(result), []int{11, 11 + currentMagicBoxEntrySize + 4}},
		} {
			t.Run(window.name+string(rune('0'+want)), func(t *testing.T) {
				for _, offset := range window.offsets {
					if offset+currentMagicBoxEntrySize > len(window.data) {
						t.Fatal("开盒结果行布局错误")
					}
					row := window.data[offset : offset+currentMagicBoxEntrySize]
					if row[0x0D] != want || binary.LittleEndian.Uint32(row[2:6]) != 700 || binary.LittleEndian.Uint32(row[6:10]) != currentMagicBoxTopQualitySeed || binary.LittleEndian.Uint16(row[0x0B:0x0D]) != 50 {
						t.Fatalf("开盒结果行封装/身份/耐久错误：seal=%d want=%d row=%x", row[0x0D], want, row)
					}
				}
			})
		}
	}
}

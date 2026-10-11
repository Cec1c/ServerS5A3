package dnfbridge

import (
	"encoding/hex"
	"strings"
	"testing"
)

// vendorPVPMissionComboVisibleHex 是厂商参考服务端 `game-000059` 会话里
// op411 的 `hex=` 原文（`packet_log.txt` 按 512 字节截断 ⇒ 16 字节包头 +
// 496 字节包体）。它是本文件的黄金基准：我们的编码器必须逐字节复现它。
const vendorPVPMissionComboVisibleHex = "019b01a3020000986867bfaa0000000001520000006100000000000000" +
	"62000000000000006300000000000000640000000000000065000000000000006600000000000000" +
	"01000000000000000200000000000000030000000000000004000000000000000500000000000000" +
	"06000000000000000700000000000000080000000000000009000000000000000a00000000000000" +
	"0b000000000000000c000000000000000d000000000000000e000000000000000f00000000000000" +
	"10000000000000001100000000000000120000000000000013000000000000001400000000000000" +
	"15000000000000001600000000000000170000000000000018000000000000001900000000000000" +
	"1a000000000000001b000000000000001c000000000000001d000000000000001e00000000000000" +
	"1f000000000000002000000000000000210000000000000022000000000000002300000000000000" +
	"24000000000000002600000000000000270000000000000028000000000000003200000000000000" +
	"33000000000000003400000000000000350000000000000036000000000000003700000000000000" +
	"380000000000000039000000000000003a000000000000003b000000000000004700000000000000" +
	"480000000000000049000000000000004a000000000000004b000000000000004c00000000000000" +
	"4d0000"

// TestCurrentPVPMissionComboBodyMatchesVendorPacketPrefix 钉住编码器与厂商字节：
// 用已证实的 61 条 id 编码出来的包体，必须与厂商可见包体的前 len(body) 字节完全一致
// （唯一允许的差异是 count 字段：厂商 82 vs 我们 61，因为厂商包体在日志里被截断）。
func TestCurrentPVPMissionComboBodyMatchesVendorPacketPrefix(t *testing.T) {
	raw, err := hex.DecodeString(vendorPVPMissionComboVisibleHex)
	if err != nil {
		t.Fatalf("vendor hex decode: %v", err)
	}
	if len(raw) < 16 {
		t.Fatalf("vendor packet too short: %d", len(raw))
	}
	vendorBody := raw[16:]

	body := buildCurrentPVPMissionComboBody(currentPVPMissionComboIDs, nil)
	if len(body) == 0 {
		t.Fatal("empty op411 body")
	}
	if len(body) > len(vendorBody) {
		t.Fatalf("body %d longer than vendor visible body %d", len(body), len(vendorBody))
	}

	// 头 5 字节：flag + count(u32)。flag 必须一致；count 允许不同（表尾被日志截断）。
	if body[0] != currentPVPMissionComboFlag || body[0] != vendorBody[0] {
		t.Fatalf("flag byte ours=0x%02X vendor=0x%02X", body[0], vendorBody[0])
	}
	vendorCount := uint32(vendorBody[1]) | uint32(vendorBody[2])<<8 | uint32(vendorBody[3])<<16 | uint32(vendorBody[4])<<24
	if vendorCount != 82 {
		t.Fatalf("vendor count=%d, expected the documented 82", vendorCount)
	}
	if len(currentPVPMissionComboIDs) != 61 {
		t.Fatalf("documented visible rows=61, got %d", len(currentPVPMissionComboIDs))
	}
	// 厂商可见 496 字节 = 5 头 + 61 整行 + 第 62 行前 3 字节（id=77）。据此确认
	// "可见行数=61"这一事实本身，避免表尾推断再次跑偏。
	tailID := uint32(vendorBody[5+61*8]) | uint32(vendorBody[5+61*8+1])<<8 | uint32(vendorBody[5+61*8+2])<<16
	if tailID != 77 {
		t.Fatalf("vendor row 62 partial id=%d, expected 77", tailID)
	}

	// 逐字节比较除 count 之外的每一个字节。
	compared := 0
	for i := 0; i < len(body); i++ {
		if i >= 1 && i <= 4 {
			continue
		}
		if body[i] != vendorBody[i] {
			t.Fatalf("byte %d differs: ours=0x%02X vendor=0x%02X", i, body[i], vendorBody[i])
		}
		compared++
	}
	if compared < len(body)-4 {
		t.Fatalf("compared %d bytes, expected at least %d", compared, len(body)-4)
	}
}

// TestCurrentPVPMissionComboLayoutRoundTrip 钉住布局本身：u8 flag + u32 count +
// count*(u32 id,u32 value)，并检查表尾完整。
func TestCurrentPVPMissionComboLayoutRoundTrip(t *testing.T) {
	body := buildCurrentPVPMissionComboBody([]uint32{97, 1, 76}, map[uint32]uint32{1: 5})
	// u8 flag + u32 count(=3) + (97,0) + (1,5) + (76,0)，全部小端。
	want := "01" + "03000000" + "61000000" + "00000000" + "01000000" + "05000000" + "4c000000" + "00000000"
	got := hex.EncodeToString(body)
	if !strings.EqualFold(got, want) {
		t.Fatalf("layout mismatch:\n got=%s\nwant=%s", got, want)
	}
}

// TestCurrentPVPMissionComboIDTableIsSelfConsistent 防止后续编辑把 count 与行数
// 弄不一致（客户端会按 count 读行）。
func TestCurrentPVPMissionComboIDTableIsSelfConsistent(t *testing.T) {
	body := buildCurrentPVPMissionComboBody(currentPVPMissionComboIDs, nil)
	count := uint32(body[1]) | uint32(body[2])<<8 | uint32(body[3])<<16 | uint32(body[4])<<24
	if int(count) != len(currentPVPMissionComboIDs) {
		t.Fatalf("count field %d != rows %d", count, len(currentPVPMissionComboIDs))
	}
	if len(body) != 5+8*len(currentPVPMissionComboIDs) {
		t.Fatalf("body len %d != 5+8*%d", len(body), len(currentPVPMissionComboIDs))
	}
	seen := map[uint32]bool{}
	for _, id := range currentPVPMissionComboIDs {
		if id == 0 {
			t.Fatal("mission id 0 is invalid")
		}
		if seen[id] {
			t.Fatalf("duplicate mission id %d", id)
		}
		seen[id] = true
	}
}

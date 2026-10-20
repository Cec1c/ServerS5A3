package skillswitch

import (
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

// 本文件是 **2026-09-28 实机更正**（`_dist/buff-pairs-measurement.md` §4）的钉子：
//
//	真实 op1428 上行**包体** = 4 字节 `u8 Slot | u8 Mode | u16 Value LE`；
//	改动前当成包体的 6 字节 = **2 字节导线前缀（客户端 u32 序号域高 16 位）+ 4 字节真包体**。
//
// 反证口径：v4 侧与 v6 侧**两边都有表**，且 v6 侧逐字节等于改动前的现场样本；
// 把 ParseRecordIn 的布局分支接反（v4 走 6 字节读、v6 走 4 字节读）两侧立刻同时红灯。

// liveBody4 是实机报文缓冲里客户端**自己发的** 4 字节包体
// （`_dsh_tmp/buff-pairs-evidence/scan3.out.txt` 的 `HIT 0x3319F92A` 等）。
var liveBody4 = []string{
	"10031000", // slot 16 mode 3 value 16
	"11031100", // slot 17 mode 3 value 17
	"12031200", // slot 18 mode 3 value 18
	"0f030f00", // slot 15 mode 3 value 15
	"0e030e00", // slot 14 mode 3 value 14
	"0c030c00", // slot 12 mode 3 value 12
	"0d030d00", // slot 13 mode 3 value 13
	"14031400", // slot 20 mode 3 value 20
	"13031300", // slot 19 mode 3 value 19
	"15031500", // slot 21 mode 3 value 21
	"16031600", // slot 22 mode 3 value 22
	"19031900", // slot 25 mode 3 value 25
	"17031700", // slot 23 mode 3 value 23
}

// storedRecordRows 是库内 `character:6:skill_switch_inventory` 的 18 条落库行
// （`_dist/buff-pairs-measurement.md` §4 的表；`_dist/buff-skill-switch-diagnosis.md` §4.6）。
//
//	旧 6 字节行          新 4 字节包体   Slot Mode Value
var storedRecordRows = []struct {
	six  string
	four string
	slot byte
	mode byte
	want uint16
}{
	{"00000c030c00", "0c030c00", 12, 3, 12},
	{"00000e030e00", "0e030e00", 14, 3, 14},
	{"00000f030f00", "0f030f00", 15, 3, 15},
	{"000010031000", "10031000", 16, 3, 16},
	{"000012031200", "12031200", 18, 3, 18},
	{"260003012f00", "03012f00", 3, 1, 47},
	{"26000c003600", "0c003600", 12, 0, 54},
	{"26000e003e00", "0e003e00", 14, 0, 62},
	{"26000f003d00", "0f003d00", 15, 0, 61},
	{"260010003900", "10003900", 16, 0, 57},
	{"260011003b00", "11003b00", 17, 0, 59},
	{"260012003f00", "12003f00", 18, 0, 63},
	{"260013003a00", "13003a00", 19, 0, 58},
	{"260014004000", "14004000", 20, 0, 64},
	{"260015003c00", "15003c00", 21, 0, 60},
	{"260016003800", "16003800", 22, 0, 56},
	{"260017003700", "17003700", 23, 0, 55},
	// 哨兵：4 字节读法下 0xFFFF 落在 u16 Value 上（这才是自洽的哨兵位）。
	{"2600212cffff", "212cffff", 0x21, 0x2c, 0xFFFF},
}

func decodeHex(t *testing.T, value string) []byte {
	t.Helper()
	raw, err := hex.DecodeString(value)
	if err != nil {
		t.Fatalf("hex %q: %v", value, err)
	}
	return raw
}

// 1) v4（默认档）：实机 4 字节包体逐条解析，字段与字节形都必须一致。
func TestParseRecordV4LiveBodies(t *testing.T) {
	if RecordBodySize != 4 || RecordWirePrefixSize != 2 || RecordWireSize != 6 {
		t.Fatalf("layout constants = %d/%d/%d, want 4/2/6",
			RecordBodySize, RecordWirePrefixSize, RecordWireSize)
	}
	for _, body := range liveBody4 {
		raw := decodeHex(t, body)
		record, err := ParseRecord(raw)
		if err != nil {
			t.Fatalf("ParseRecord(%s) error = %v", body, err)
		}
		if record.Layout != LayoutV4 {
			t.Fatalf("body %s layout = %q, want %q", body, record.Layout, LayoutV4)
		}
		if record.WirePrefix != 0 {
			t.Fatalf("body %s wire prefix = 0x%04x, want 0 (a 4-byte body has no prefix)",
				body, record.WirePrefix)
		}
		// 实机样本全是 ModeConfirm：Slot == Value，且 Slot 落在现场值域里。
		if record.Mode != ModeConfirm {
			t.Fatalf("body %s mode = 0x%02x, want ModeConfirm", body, record.Mode)
		}
		if uint16(record.Slot) != record.Value {
			t.Fatalf("body %s slot=%d value=%d, want ModeConfirm invariant slot == value",
				body, record.Slot, record.Value)
		}
		if record.Hex() != body {
			t.Fatalf("body %s round trip = %s (v4 Bytes() must be the 4-byte body)", body, record.Hex())
		}
		if got := len(record.Bytes()); got != RecordBodySize {
			t.Fatalf("body %s Bytes() len = %d, want %d", body, got, RecordBodySize)
		}
	}
	// 哨兵（实机 4 字节形 `21 2c ff ff`）。
	sentinel, err := ParseRecord(decodeHex(t, "212cffff"))
	if err != nil {
		t.Fatal(err)
	}
	if sentinel.Slot != 0x21 || sentinel.Mode != ModeClear || sentinel.Value != ValueNone || !sentinel.Sentinel() {
		t.Fatalf("sentinel = %+v, want slot=0x21 mode=0x2c value=0xffff Sentinel()=true", sentinel)
	}
}

// 2) dual-read：现行分帧交来的 6 字节切片（2 非包体字节 + 4 包体）在 **v4 档**下
// 必须读成同一条记录，且那 2 个字节只进 WirePrefix —— 不进任何字段。
func TestParseRecordV4DualReadsWireSlice(t *testing.T) {
	for _, row := range storedRecordRows {
		six := decodeHex(t, row.six)
		wire, err := ParseRecord(six) // v4（默认）
		if err != nil {
			t.Fatalf("ParseRecord(v4, %s) error = %v", row.six, err)
		}
		if wire.Layout != LayoutV4 {
			t.Fatalf("row %s layout = %q, want %q", row.six, wire.Layout, LayoutV4)
		}
		if wire.WirePrefix != uint16(six[0])|uint16(six[1])<<8 {
			t.Fatalf("row %s wire prefix = 0x%04x, want 0x%04x", row.six, wire.WirePrefix,
				uint16(six[0])|uint16(six[1])<<8)
		}
		if wire.Hex() != row.four {
			t.Fatalf("row %s v4 view = %s, want %s (strip the 2 header bytes)", row.six, wire.Hex(), row.four)
		}
		if wire.Slot != row.slot || wire.Mode != row.mode || wire.Value != row.want {
			t.Fatalf("row %s fields = slot=%d mode=0x%02x value=%d, want %d/0x%02x/%d",
				row.six, wire.Slot, wire.Mode, wire.Value, row.slot, row.mode, row.want)
		}
		// 同一组字节按规范 4 字节包体再读一次，字段必须完全相同。
		canonical, err := ParseRecord(decodeHex(t, row.four))
		if err != nil {
			t.Fatalf("ParseRecord(%s) error = %v", row.four, err)
		}
		if !wire.SameFields(canonical) {
			t.Fatalf("row %s: dual-read %+v != canonical %+v", row.six, wire, canonical)
		}
	}
}

// 3) 旧 6 字节行 → 新 4 字节包体：逐条钉住那条**固定映射**（去掉前 2 字节），
// 并证明字段没有变（即"重新解读"没有引入任何语义变化）。
func TestRecordLegacySixByteRowMapsToFourByteBody(t *testing.T) {
	for _, row := range storedRecordRows {
		legacy, err := ParseRecordIn(LayoutV6, decodeHex(t, row.six))
		if err != nil {
			t.Fatalf("ParseRecordIn(v6, %s) error = %v", row.six, err)
		}
		if legacy.Layout != LayoutV6 {
			t.Fatalf("row %s layout = %q, want %q", row.six, legacy.Layout, LayoutV6)
		}
		fixed, err := ParseRecordIn(LayoutV4, decodeHex(t, row.six))
		if err != nil {
			t.Fatalf("ParseRecordIn(v4, %s) error = %v", row.six, err)
		}
		if !legacy.SameFields(fixed) {
			t.Fatalf("row %s: v6 %+v and v4 %+v must be the same record", row.six, legacy, fixed)
		}
		if legacy.Hex() != row.six {
			t.Fatalf("row %s v6 round trip = %s, want byte-for-byte the stored row", row.six, legacy.Hex())
		}
		if fixed.Hex() != row.four {
			t.Fatalf("row %s v4 view = %s, want %s", row.six, fixed.Hex(), row.four)
		}
		if got := hex.EncodeToString(decodeHex(t, row.six)[2:]); got != row.four {
			t.Fatalf("row %s: literal strip = %s, want %s", row.six, got, row.four)
		}
	}
}

// 4) v6 = 一键回退 = 改动前**逐字节**：现场 16 条样本（store_test.go 的 6 字节体）
// 解析出的字段、Hex() 与 Bytes() 都必须与改动前完全一致。
func TestParseRecordV6ReproducesTodayByteForByte(t *testing.T) {
	bodies := append(append([]string(nil), currentClientBodies...), liveFourteenThirtyBodies...)
	bodies = append(bodies, livePairedBodies...)
	bodies = append(bodies, liveSentinelBody, liveUnreadBody)
	for _, body := range bodies {
		legacy, err := ParseRecordIn(LayoutV6, decodeHex(t, body))
		if err != nil {
			t.Fatalf("ParseRecordIn(v6, %s) error = %v", body, err)
		}
		if legacy.Hex() != body {
			t.Fatalf("v6 must reproduce today byte-for-byte: %s -> %s", body, legacy.Hex())
		}
		if got := legacy.Bytes(); hex.EncodeToString(got) != body || len(got) != RecordWireSize {
			t.Fatalf("v6 Bytes(%s) = %x (len %d), want the 6-byte wire row", body, got, len(got))
		}
		// 布局是唯一区别：字段必须与 v4 读法一致。
		fixed, err := ParseRecordIn(LayoutV4, decodeHex(t, body))
		if err != nil {
			t.Fatalf("ParseRecordIn(v4, %s) error = %v", body, err)
		}
		if !legacy.SameFields(fixed) {
			t.Fatalf("body %s: v6 %+v vs v4 %+v", body, legacy, fixed)
		}
	}
}

// 5) 严格性：只认已知长度，其余一律 ErrRecordShape（两种档位各自的允许集合不同）。
func TestParseRecordLayoutStrictness(t *testing.T) {
	for _, length := range []int{0, 1, 2, 3, 5, 7, 8, 9, 96, 97, 98} {
		body := make([]byte, length)
		if length > 0 {
			body[0] = byte(length)
		}
		if _, err := ParseRecordIn(LayoutV4, body); !errors.Is(err, ErrRecordShape) {
			t.Fatalf("v4 len=%d error = %v, want ErrRecordShape", length, err)
		}
	}
	// v4 允许 4 与 6。
	for _, length := range []int{RecordBodySize, RecordWireSize} {
		if _, err := ParseRecordIn(LayoutV4, make([]byte, length)); err != nil {
			t.Fatalf("v4 len=%d error = %v, want nil", length, err)
		}
	}
	// v6 只允许 6：4 字节包体在回退档下必须**具名拒绝**（不是静默当成别的版本）。
	if _, err := ParseRecordIn(LayoutV6, make([]byte, RecordBodySize)); !errors.Is(err, ErrRecordShape) {
		t.Fatalf("v6 len=4 error = %v, want ErrRecordShape", err)
	}
	if _, err := ParseRecordIn(LayoutV6, make([]byte, RecordWireSize)); err != nil {
		t.Fatalf("v6 len=6 error = %v, want nil", err)
	}
	// 报错要带上布局与两个允许长度，便于线上判读。
	_, err := ParseRecordIn(LayoutV4, make([]byte, 7))
	if err == nil || !strings.Contains(err.Error(), "layout=v4") || !strings.Contains(err.Error(), "want=4_or_6") {
		t.Fatalf("v4 shape error = %v, want layout=v4 + want=4_or_6", err)
	}
}

// 6) 档位归一化：默认与未知取值都必须落到**修复档 v4**；只有显式 v6 才回退。
func TestNormalizeLayout(t *testing.T) {
	for _, value := range []string{"", "v4", "V4", "  v4  ", "fix", "on", "typo", "5"} {
		if got := NormalizeLayout(value); got != LayoutV4 {
			t.Fatalf("NormalizeLayout(%q) = %q, want %q", value, got, LayoutV4)
		}
	}
	for _, value := range []string{"v6", "V6", "  v6  ", "legacy", "today", "old", "6"} {
		if got := NormalizeLayout(value); got != LayoutV6 {
			t.Fatalf("NormalizeLayout(%q) = %q, want %q", value, got, LayoutV6)
		}
	}
	if LayoutV4.String() != "v4" || LayoutV6.String() != "v6" {
		t.Fatalf("Layout.String() = %q/%q", LayoutV4.String(), LayoutV6.String())
	}
}

// 7) InLayout / SameFields / Bytes 的语义边界。
func TestRecordInLayoutSwitchesByteFormOnly(t *testing.T) {
	record, err := ParseRecord(decodeHex(t, "0c030c00"))
	if err != nil {
		t.Fatal(err)
	}
	legacy := record.InLayout(LayoutV6)
	if legacy.Hex() != "00000c030c00" {
		t.Fatalf("InLayout(v6).Hex() = %s, want 00000c030c00 (zero prefix)", legacy.Hex())
	}
	if !legacy.SameFields(record) {
		t.Fatalf("InLayout must not touch fields: %+v vs %+v", legacy, record)
	}
	if record.Hex() != "0c030c00" {
		t.Fatalf("original record mutated: %s", record.Hex())
	}
	// 前缀不参与 SameFields：v6 的 `2600...` 与 v4 的 4 字节包体是同一组字段。
	a, _ := ParseRecordIn(LayoutV6, decodeHex(t, "26000c003600"))
	b, _ := ParseRecord(decodeHex(t, "0c003600"))
	if !a.SameFields(b) {
		t.Fatalf("wire prefix must not affect SameFields: %+v vs %+v", a, b)
	}
}

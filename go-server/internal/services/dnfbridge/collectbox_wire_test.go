package dnfbridge

import (
	"encoding/binary"
	"encoding/hex"
	"testing"
	"time"
)

// 本文件把「收集箱」的线协议**逐字节**钉在厂商 9.16 参考环境的实测包上。
//
// 夹具来源（`D:\DNF90-test\server\runtime\logs\packet_log.txt`，
// 2026-09-25T10:19:36.2400554Z，同一条 select ack 之后连续 6 条）：
//
//	SEND msg_id=1027 class0 hex=…0100000000000000  (box 1 缓存)
//	SEND msg_id=1027 class0 hex=…0200000000000000  (box 2)
//	SEND msg_id=1027 class0 hex=…0300000000000000  (box 3)
//	SEND msg_id=1027 class0 hex=…0400000000000000  (box 4)
//	SEND msg_id=1027 class0 hex=…0500a7ec42040000  (box 5 缓存)
//	SEND msg_id=1021 class1 hex=…0105              (选中应答 = 箱 5)
//	SEND msg_id=1027 class0 hex=…0501a7ec42040000  (选中箱刷新)
//
// 同一批事件行还自带字段名：`game-collectbox-state-send box_index=N item_count=0
// body_len=8`，以及
// `game-collectbox-initialization-sent box_count=5 selected_box_index=5
// wire_contract=class0_op1027_cache_then_class1_op1021_selection_ack_then_selected_refresh`。
var vendorCollectboxCaptureTime = time.Date(2026, 9, 25, 10, 19, 36, 240055400, time.UTC)

// vendorCollectboxBoxes 用真档的 [base expiration date] 造出厂商当时那 5 个箱子
// （箱 1..4 已过期 ⇒ u32 = 0；箱 5 = 2028-12-31 06:00:00 东八区 ⇒ 71494823）。
func vendorCollectboxBoxes(t *testing.T) []currentCollectboxBox {
	t.Helper()
	dates := []string{
		"2014-12-30 06:00:00",
		"2015-09-01 06:00:00",
		"2017-01-05 06:00:00",
		"2017-12-31 06:00:00",
		"2028-12-31 06:00:00",
	}
	titles := []string{"SAO收集箱", "生肖之灵收集箱", "三国志收集箱", "猎龙者收集箱", "兽人守护神"}
	boxes := make([]currentCollectboxBox, 0, len(dates))
	for idx, raw := range dates {
		expiration, ok := parseCurrentCollectboxDate(raw)
		if !ok {
			t.Fatalf("fixture date %q must parse", raw)
		}
		boxes = append(boxes, currentCollectboxBox{
			Index:             int32(idx + 1),
			Title:             titles[idx],
			BaseExpiration:    expiration,
			HasBaseExpiration: true,
		})
	}
	return boxes
}

func TestCurrentCollectboxStateBodyMatchesVendorCapture(t *testing.T) {
	boxes := vendorCollectboxBoxes(t)
	wantCache := []string{
		"0100000000000000",
		"0200000000000000",
		"0300000000000000",
		"0400000000000000",
		"0500a7ec42040000",
	}
	for idx, box := range boxes {
		body := buildCurrentCollectboxStateBody(box, false, nil, vendorCollectboxCaptureTime)
		if got := hex.EncodeToString(body); got != wantCache[idx] {
			t.Fatalf("box %d cache body=%s want %s (vendor capture)", box.Index, got, wantCache[idx])
		}
	}
	// 选中箱刷新：只有 [1] 由 0 变 1，其余逐字节相同。
	selected := buildCurrentCollectboxStateBody(boxes[4], true, nil, vendorCollectboxCaptureTime)
	if got := hex.EncodeToString(selected); got != "0501a7ec42040000" {
		t.Fatalf("selected box body=%s want 0501a7ec42040000 (vendor capture)", got)
	}
	if remaining := currentCollectboxRemainingSeconds(boxes[4], vendorCollectboxCaptureTime); remaining != 71494823 {
		t.Fatalf("box5 remaining=%d want 71494823 (vendor u32 0x0442eca7)", remaining)
	}
	for _, box := range boxes[:4] {
		if remaining := currentCollectboxRemainingSeconds(box, vendorCollectboxCaptureTime); remaining != 0 {
			t.Fatalf("expired box %d remaining=%d want 0", box.Index, remaining)
		}
	}
}

func TestCurrentCollectboxSelectAckBodyMatchesVendorCapture(t *testing.T) {
	if got := hex.EncodeToString(buildCurrentCollectboxSelectAckBody(5)); got != "0105" {
		t.Fatalf("select ack body=%s want 0105 (vendor capture)", got)
	}
	if index, ok := parseCurrentCollectboxSelectRequest([]byte{0x01}); !ok || index != 1 {
		t.Fatalf("request body 01 decoded=%d ok=%v want 1/true (香港现场实测 body_len=1 body=01)", index, ok)
	}
	if _, ok := parseCurrentCollectboxSelectRequest(nil); ok {
		t.Fatal("empty request must not decode")
	}
}

// TestCurrentCollectboxStateBodyCarriesOnlyPVFSlotItemIDs 钉住"下发的条目 id 与
// PVF 对应"：不在 [Item Position Info] 里的 id、非正数、重复 id 一律不下发。
func TestCurrentCollectboxStateBodyCarriesOnlyPVFSlotItemIDs(t *testing.T) {
	box := currentCollectboxBox{
		Index:             5,
		BaseExpiration:    time.Date(2028, 12, 30, 22, 0, 0, 0, time.UTC),
		HasBaseExpiration: true,
		SlotRows:          3,
		SlotsPerRow:       3,
		Slots: []currentCollectboxSlot{
			{ItemID: 490007143, PositionCode: 3},
			{ItemID: 490007144, PositionCode: 3},
			{ItemID: 490007145, PositionCode: 3},
		},
	}
	body := buildCurrentCollectboxStateBody(
		box,
		true,
		[]int32{490007144, 490007143, 490007144, 0, -1, 490700761},
		vendorCollectboxCaptureTime,
	)
	if len(body) != currentCollectboxStateFixedBodySize+8 {
		t.Fatalf("body_len=%d want 16 (2 PVF ids)", len(body))
	}
	if body[0] != 5 || body[1] != 1 || body[7] != 2 {
		t.Fatalf("body head=%x want 05 01 … 02", body[:8])
	}
	// 490007144 = 0x1D34EA68 → 小端 68ea341d；490007143 → 67ea341d。
	if got := hex.EncodeToString(body[8:12]); got != "68ea341d" {
		t.Fatalf("first item id bytes=%s want 68ea341d (490007144 LE, 调用方顺序)", got)
	}
	if got := hex.EncodeToString(body[12:16]); got != "67ea341d" {
		t.Fatalf("second item id bytes=%s want 67ea341d (490007143 LE)", got)
	}
	if binary.LittleEndian.Uint32(body[8:12]) != 490007144 || binary.LittleEndian.Uint32(body[12:16]) != 490007143 {
		t.Fatalf("item ids=%d,%d want 490007144,490007143",
			binary.LittleEndian.Uint32(body[8:12]), binary.LittleEndian.Uint32(body[12:16]))
	}
}

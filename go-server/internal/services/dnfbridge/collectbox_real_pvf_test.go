package dnfbridge

import (
	"encoding/binary"
	"encoding/hex"
	"os"
	"testing"
	"time"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 真档回归：用运行期 `runtime/data/dnf/Script.pvf` 钉住「收集箱」的目录与下发形状。
//
// 运行：
//
//	DNFBRIDGE_REAL_PVF_SMOKE=<repo>\runtime\data\dnf\Script.pvf \
//	  go test -count=1 ./internal/services/dnfbridge -run RealCollectbox
//
// 事实来源（2026-09-25 本机真档 + 厂商 9.16 参考环境 packet_log）：
//   - `localization/collectbox/collectboxinfo.etc` 共 5 段 [Collect Box Info]，
//     Index 1..5；箱 1 = SAO 收集箱、箱 2 = 生肖之灵、箱 3 = 三国志、
//     箱 4 = 猎龙者、箱 5 = **兽人守护神**（2018 春节礼包那套）；
//   - 箱 5 的 [Item Position Info] = `3 3` + 9 个 id（490007143..490007151）；
//   - 箱 5 的 [base expiration date] = 2028-12-31 06:00:00（东八区）；
//   - 厂商在 2026-09-25T10:19:36.2400554Z 下发的箱 5 body 是
//     `0500a7ec42040000`（选中刷新 `0501a7ec42040000`），箱 1..4 是
//     `0N00000000000000` —— 本用例要求用真档目录复现**同一批字节**。
func TestRealScriptPVFCollectboxCatalogExpiresAllBoxesIn2028(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to verify the real collectbox catalog")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatalf("load real Script.pvf: %v", err)
	}
	catalog, err := loadCurrentCollectboxCatalog(archive)
	if err != nil {
		t.Fatalf("load collectbox catalog from real PVF: %v", err)
	}
	if len(catalog.Boxes) != 5 {
		t.Fatalf("boxes=%d want 5", len(catalog.Boxes))
	}
	for idx, box := range catalog.Boxes {
		if box.Index != int32(idx+1) {
			t.Fatalf("box[%d] index=%d want %d", idx, box.Index, idx+1)
		}
		if box.Title == "" || box.BuffFormat == "" || box.ExpiryFormat == "" {
			t.Fatalf("box %d missing [UI Explain 1/2/3] title=%q buff=%q expiry=%q",
				box.Index, box.Title, box.BuffFormat, box.ExpiryFormat)
		}
		// 占位串只能存在于客户端 ui/collectbox/collectbox.xui；真档目录里不许出现。
		if box.Title == "test string1" || box.BuffFormat == "test string2" || box.ExpiryFormat == "test string3" {
			t.Fatalf("box %d carries the client placeholder text", box.Index)
		}
		want := time.Date(2028, 12, 30, 22, 0, 0, 0, time.UTC)
		if !box.HasBaseExpiration || !box.HasMaxExpiration || !box.BaseExpiration.Equal(want) || !box.MaxExpiration.Equal(want) {
			t.Fatalf("box %d base/max expiration not extended: %+v", box.Index, box)
		}
	}
	box5, ok := catalog.Box(5)
	if !ok {
		t.Fatal("box 5 missing")
	}
	if box5.Title != "兽人守护神" {
		t.Fatalf("box 5 title=%q want 兽人守护神", box5.Title)
	}
	if box4, ok := catalog.Box(4); !ok || box4.Title != "猎龙者收集箱" {
		t.Fatalf("box 4 title=%q want 猎龙者收集箱", box4.Title)
	}
	wantSlots := []int32{490007148, 490007147, 490007146, 490007150, 490007149, 490007151, 490007144, 490007145, 490007143}
	if box5.SlotCount() != 9 || len(box5.Slots) != 9 {
		t.Fatalf("box 5 rows=%d perRow=%d slots=%d want 3/3/9", box5.SlotRows, box5.SlotsPerRow, len(box5.Slots))
	}
	for idx, want := range wantSlots {
		if box5.Slots[idx].ItemID != want {
			t.Fatalf("box 5 slot[%d]=%d want %d (真实 [Item Position Info] 顺序)", idx, box5.Slots[idx].ItemID, want)
		}
	}
	if want := time.Date(2028, 12, 30, 22, 0, 0, 0, time.UTC); !box5.BaseExpiration.Equal(want) {
		t.Fatalf("box 5 base expiration=%s want %s", box5.BaseExpiration.UTC(), want)
	}

	// 原生格式不变；用户确认五箱均延长到与厂商箱5相同的日期。
	wantCache := []string{
		"0100a7ec42040000",
		"0200a7ec42040000",
		"0300a7ec42040000",
		"0400a7ec42040000",
		"0500a7ec42040000",
	}
	for _, box := range catalog.Boxes {
		body := buildCurrentCollectboxStateBody(box, false, nil, vendorCollectboxCaptureTime)
		got := hex.EncodeToString(body)
		if got != wantCache[box.Index-1] {
			t.Fatalf("box %d body=%s want %s (vendor capture)", box.Index, got, wantCache[box.Index-1])
		}
	}
	selected := buildCurrentCollectboxStateBody(box5, true, nil, vendorCollectboxCaptureTime)
	if got := hex.EncodeToString(selected); got != "0501a7ec42040000" {
		t.Fatalf("box 5 selected body=%s want 0501a7ec42040000 (vendor capture)", got)
	}
	if got := hex.EncodeToString(buildCurrentCollectboxSelectAckBody(5)); got != "0105" {
		t.Fatalf("select ack body=%s want 0105 (vendor capture)", got)
	}

	// 条目 id 一律来自本箱 PVF 槛位：真档那 9 个 id 原样进 body，非 PVF id 被丢弃。
	withItems := buildCurrentCollectboxStateBody(box5, true, append(append([]int32(nil), wantSlots...), 490700761), vendorCollectboxCaptureTime)
	if len(withItems) != currentCollectboxStateFixedBodySize+4*len(wantSlots) {
		t.Fatalf("body_len=%d want %d", len(withItems), currentCollectboxStateFixedBodySize+4*len(wantSlots))
	}
	if withItems[7] != byte(len(wantSlots)) {
		t.Fatalf("count=%d want %d", withItems[7], len(wantSlots))
	}
	for idx, want := range wantSlots {
		if got := int32(binary.LittleEndian.Uint32(withItems[8+idx*4:])); got != want {
			t.Fatalf("body item[%d]=%d want %d", idx, got, want)
		}
	}
}

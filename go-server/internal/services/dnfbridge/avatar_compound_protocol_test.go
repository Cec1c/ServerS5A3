package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// currentAvatarCompoundTestBody 构造 22 字节的 op99 请求体。
func currentAvatarCompoundTestBody(
	consumeSlot int16,
	slot1 int16,
	itemID1 uint32,
	slot2 int16,
	itemID2 uint32,
	requestedItemID uint32,
	abilityNo uint16,
) []byte {
	body := make([]byte, currentAvatarCompoundRequestSize)
	binary.LittleEndian.PutUint16(body[0:2], uint16(consumeSlot))
	binary.LittleEndian.PutUint16(body[2:4], uint16(slot1))
	binary.LittleEndian.PutUint32(body[4:8], itemID1)
	binary.LittleEndian.PutUint16(body[8:10], uint16(slot2))
	binary.LittleEndian.PutUint32(body[10:14], itemID2)
	binary.LittleEndian.PutUint32(body[14:18], requestedItemID)
	binary.LittleEndian.PutUint16(body[18:20], abilityNo)
	return body
}

func TestParseCurrentAvatarCompoundRequestLiveShape(t *testing.T) {
	body := currentAvatarCompoundTestBody(9, 5, 41001, 6, 41002, 41003, 4)
	request, err := parseCurrentAvatarCompoundRequest(body)
	if err != nil {
		t.Fatalf("parse error = %v", err)
	}
	if request.ConsumeSlot != 9 || request.Slot1 != 5 || request.Slot2 != 6 ||
		request.ItemID1 != 41001 || request.ItemID2 != 41002 ||
		request.RequestedItemID != 41003 || request.AbilityNo != 4 {
		t.Fatalf("request = %+v", request)
	}
}

func TestParseCurrentAvatarCompoundRequestAcceptsLegacyTrailer(t *testing.T) {
	body := currentAvatarCompoundTestBody(9, 5, 41001, 6, 41002, 41003, 4)
	legacy := append(append([]byte(nil), body...), 0, 0, 0, 0)
	normalized := stripLegacyAvatarCompoundTransportTrailer(legacy)
	if !bytes.Equal(normalized, body) {
		t.Fatalf("normalized = %x want %x", normalized, body)
	}
	request, err := parseCurrentAvatarCompoundRequest(normalized)
	if err != nil || request.RequestedItemID != 41003 {
		t.Fatalf("request = %+v err = %v", request, err)
	}
	// 非 22/26 字节的包体必须原样交给解析器拒绝，不能被悄悄截断。
	for _, size := range []int{0, 21, 23, 25, 27} {
		raw := make([]byte, size)
		if len(stripLegacyAvatarCompoundTransportTrailer(raw)) != size {
			t.Fatalf("size=%d was normalized", size)
		}
	}
}

func TestParseCurrentAvatarCompoundRequestRejectsInvalidBody(t *testing.T) {
	cases := map[string][]byte{
		"short":         currentAvatarCompoundTestBody(9, 5, 41001, 6, 41002, 41003, 4)[:21],
		"same slot":     currentAvatarCompoundTestBody(9, 5, 41001, 5, 41002, 41003, 4),
		"negative slot": currentAvatarCompoundTestBody(9, -1, 41001, 6, 41002, 41003, 4),
		"no requested":  currentAvatarCompoundTestBody(9, 5, 41001, 6, 41002, 0, 4),
		"ability high":  currentAvatarCompoundTestBody(9, 5, 41001, 6, 41002, 41003, 0x1ff),
	}
	for name, body := range cases {
		if _, err := parseCurrentAvatarCompoundRequest(body); err == nil {
			t.Fatalf("%s body accepted: %x", name, body)
		}
	}
}

// 成功回包形状与同族既有实现（InventoryHandler.Package.cs:1973-1998）逐字节对齐：
// 这里用固定字面量做期望，任何形状漂移都会直接失败。
func TestBuildCurrentAvatarCompoundSuccessBodyPinsSiblingShape(t *testing.T) {
	request, err := parseCurrentAvatarCompoundRequest(currentAvatarCompoundTestBody(9, 5, 41001, 6, 41002, 41003, 4))
	if err != nil {
		t.Fatal(err)
	}
	rows := []currentAvatarCompoundResultRow{{Slot: 5, ItemID: 41003, AbilityNo: 4}}
	got := buildCurrentAvatarCompoundSuccessBody(request, rows)

	socketBlock := append([]byte{0x1e, 0x00, 0x00, 0x00}, make([]byte, currentAvatarSocketBytes)...)
	socketBlock = append(socketBlock, 0x04, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00)
	placeholder := append([]byte{0xff, 0xff, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}, socketBlock...)
	want := []byte{
		0x01, 0x03,
		0x01, 0x05, 0x00, 0x01, 0x00, 0x00, 0x00,
		0x01, 0x06, 0x00, 0x01, 0x00, 0x00, 0x00,
		0x00, 0x09, 0x00, 0x01, 0x00, 0x00, 0x00,
		0x05, 0x00, 0x2b, 0xa0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x04, 0x00,
	}
	want = append(want, socketBlock...)
	want = append(want, placeholder...)
	if !bytes.Equal(got, want) {
		t.Fatalf("body = %x\nwant   = %x", got, want)
	}
}

func TestBuildCurrentAvatarCompoundFailureBodyShape(t *testing.T) {
	if got := buildCurrentAvatarCompoundFailureBody(); !bytes.Equal(got, []byte{0x00, currentAvatarCompoundFailureCode, 0x00}) {
		t.Fatalf("failure body = %x", got)
	}
}

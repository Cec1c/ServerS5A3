package dnfbridge

import (
	"bytes"
	"encoding/hex"
	"testing"
)

// 现场形状（当前 EXE 0x25fae16 的写序，见 forging_codec.go）：
// list 0、slot 9、item 101011368（0x06054fa8）、材料槽 121、两字节名字 0xCED0、confirmation 0。
const currentForgingWireSample = "000900a84f0506790002000000ced000"

func TestCurrentForgingRequestWireContract(t *testing.T) {
	body, err := hex.DecodeString(currentForgingWireSample)
	if err != nil {
		t.Fatal(err)
	}
	request, err := decodeCurrentForgingRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	if request.List != 0 || request.Slot != 9 || request.ItemID != 101011368 ||
		request.MaterialSlot != 121 || !bytes.Equal(request.Name, []byte{0xce, 0xd0}) || request.Confirmation != 0 {
		t.Fatalf("decoded = %+v", request)
	}
}

func TestCurrentForgingAckWireContract(t *testing.T) {
	body, _ := hex.DecodeString(currentForgingWireSample)
	request, err := decodeCurrentForgingRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	// 材料槽 121、剩余 100、旧 2、成功 → 新 3、main slot 9。
	want, _ := hex.DecodeString("01790064000000020003000900")
	if got := buildCurrentForgingAck(request, 100, 2, 3, true); !bytes.Equal(got, want) {
		t.Fatalf("ack = %x, want %x", got, want)
	}
	// 穿戴目标（list 3 slot 0）+ 掷点失败：剩余 0、等级保持 2、outcome 1。
	request.List, request.Slot = 3, 0
	want, _ = hex.DecodeString("01790000000000020102030000")
	if got := buildCurrentForgingAck(request, 0, 2, 2, false); !bytes.Equal(got, want) {
		t.Fatalf("failed roll ack = %x, want %x", got, want)
	}
}

func TestCurrentForgingRequestRejectsMalformedFrames(t *testing.T) {
	body, _ := hex.DecodeString(currentForgingWireSample)
	for length := 0; length < len(body); length++ {
		if _, err := decodeCurrentForgingRequest(body[:length]); err == nil {
			t.Fatalf("accepted truncated body of %d bytes", length)
		}
	}
	mutations := []struct {
		name   string
		mutate func([]byte)
	}{
		{"name length past end", func(b []byte) { b[9], b[10], b[11], b[12] = 0xff, 0xff, 0xff, 0xff }},
		{"unsupported list", func(b []byte) { b[0] = 2 }},
		{"material equals target on main", func(b []byte) { b[7] = 9 }},
		{"item template zero", func(b []byte) { b[3], b[4], b[5], b[6] = 0, 0, 0, 0 }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			bad := append([]byte(nil), body...)
			mutation.mutate(bad)
			if _, err := decodeCurrentForgingRequest(bad); err == nil {
				t.Fatalf("accepted invalid body %x", bad)
			}
		})
	}
	// confirmation 的取值域没有证据：任何字节都必须解析通过（不按 0..2 猜测限制）。
	for _, value := range []byte{0, 1, 2, 3, 255} {
		probe := append([]byte(nil), body...)
		probe[len(probe)-1] = value
		if _, err := decodeCurrentForgingRequest(probe); err != nil {
			t.Fatalf("confirmation %d rejected: %v", value, err)
		}
	}
}

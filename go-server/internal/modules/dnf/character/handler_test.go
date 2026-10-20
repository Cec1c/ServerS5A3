package character

import (
	"context"
	"encoding/binary"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestDecodeCreateCharacterRequest(t *testing.T) {
	got, err := DecodeCreateCharacterRequest([]byte{15, 3, 0, 0, 0, 'a', 'b', 'c', 0})
	if err != nil {
		t.Fatalf("DecodeCreateCharacterRequest error = %v", err)
	}
	if got.Job != 15 || got.Name != "abc" {
		t.Fatalf("got = %+v", got)
	}
}

// TestDecodeDeleteCharacterRequest 钉死真实 wire 形态
// [slot:u16 LE][nameLen:u32 LE @2][name @6]（厂商 CharacterSelectHandler.cs:935-937）。
func TestDecodeDeleteCharacterRequest(t *testing.T) {
	got, err := DecodeDeleteCharacterRequest([]byte{2, 0, 3, 0, 0, 0, 'a', 'b', 'c'})
	if err != nil {
		t.Fatalf("DecodeDeleteCharacterRequest error = %v", err)
	}
	if got.Slot != 2 || got.Name != "abc" {
		t.Fatalf("got = %+v", got)
	}
}

// deleteCharacterLiveBody 是 2026-09-28 现场抓到的 17 字节删除请求体：
// packet_log.txt:61050 / dnf90.out:9441，hex = 02 00 0b000000 "test78_copy"。
func deleteCharacterLiveBody() []byte {
	return append([]byte{0x02, 0x00, 0x0b, 0x00, 0x00, 0x00}, []byte("test78_copy")...)
}

// TestDecodeDeleteCharacterRequestPinsLiveWireBody 钉死现场 17 字节体：
// slot=2 / name=test78_copy（与 DB 行 character_id=15 slot=2 name=test78_copy 对合）。
func TestDecodeDeleteCharacterRequestPinsLiveWireBody(t *testing.T) {
	body := deleteCharacterLiveBody()
	if len(body) != 17 {
		t.Fatalf("live fixture is %d bytes, want the recorded 17", len(body))
	}
	got, err := DecodeDeleteCharacterRequest(body)
	if err != nil {
		t.Fatalf("live delete body must decode, error = %v", err)
	}
	if got.Slot != 2 || got.Name != "test78_copy" {
		t.Fatalf("got = %+v, want slot=2 name=test78_copy", got)
	}
}

// TestDecodeDeleteCharacterRequestCannotMisread2816 反证旧偏移 bug 不会复发。
// 旧实现读 body[1:5] = 00 0b 00 00 = 2816，现场日志逐字报
// "name length 2816 invalid for body 17"；修好后这个错读必须既不发生也不报错。
func TestDecodeDeleteCharacterRequestCannotMisread2816(t *testing.T) {
	body := deleteCharacterLiveBody()
	if legacy := int(int32(binary.LittleEndian.Uint32(body[1:5]))); legacy != 2816 {
		t.Fatalf("fixture drift: legacy mis-read = %d, want the recorded 2816", legacy)
	}
	if _, err := DecodeDeleteCharacterRequest(body); err != nil {
		t.Fatalf("decode error = %v, must not reproduce the 2816 rejection", err)
	}
	// 旧形态的 8 字节固定（slot:u8 + nameLen@1）现在必须被拒：那是另一种布局，
	// 不能静默解析成一个看似合理的请求。
	if _, err := DecodeDeleteCharacterRequest([]byte{2, 3, 0, 0, 0, 'a', 'b', 'c'}); err == nil {
		t.Fatal("legacy [slot:u8][nameLen@1] layout must no longer decode")
	}
}

// TestDecodeDeleteCharacterRequestKeepsStrictness 保留原有严格性：
// 短包、非法 nameLen（<=0 / 超过厂商 30 上限 / 越过包尾）都必须报同风格的错。
func TestDecodeDeleteCharacterRequestKeepsStrictness(t *testing.T) {
	cases := []struct {
		name string
		body []byte
	}{
		{"short", []byte{2, 0, 3, 0, 0}},
		{"zero length", []byte{2, 0, 0, 0, 0, 0}},
		{"negative length", []byte{2, 0, 0xff, 0xff, 0xff, 0xff}},
		{"over vendor cap 30", append([]byte{2, 0, 31, 0, 0, 0}, make([]byte, 31)...)},
		{"length past body end", []byte{2, 0, 9, 0, 0, 0, 'a', 'b', 'c'}},
	}
	for _, testCase := range cases {
		_, err := DecodeDeleteCharacterRequest(testCase.body)
		if err == nil {
			t.Fatalf("%s: body %x must be rejected", testCase.name, testCase.body)
		}
		if !strings.Contains(err.Error(), "invalid for body") && !strings.Contains(err.Error(), "body too short") {
			t.Fatalf("%s: unexpected error style: %v", testCase.name, err)
		}
	}
	if _, err := DecodeDeleteCharacterRequest(append([]byte{2, 0, 30, 0, 0, 0}, make([]byte, 30)...)); err != nil {
		t.Fatalf("nameLen=30 is the vendor-accepted boundary, got error %v", err)
	}
}

func TestHandlerBlocksCharacterFallbackAck(t *testing.T) {
	got, err := NewHandler().Handle(context.Background(), alignedcmd.Request{
		Opcode: uint16(dnfenum.CmdPacketReturnSelectCharacter),
	})
	if err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	if !got.Handled || got.ResponseAllowed {
		t.Fatalf("result = %+v, want handled without response", got)
	}
}

func TestHandlerUsesOwnerPreflightAndStillBlocksCharacterAck(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "77",
		AccountID:   "acc",
		Slot:        2,
		Name:        "hero",
		Job:         "15",
		Level:       86,
	}); err != nil {
		t.Fatalf("Character.Save error = %v", err)
	}

	got, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode:       uint16(dnfenum.CmdPacketCheckDoubleCharacterName),
		AccountID:    "acc",
		Repositories: repos,
		Body:         []byte{4, 0, 0, 0, 'h', 'e', 'r', 'o'},
	})
	if err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	if !got.Handled || got.ResponseAllowed {
		t.Fatalf("result = %+v, want handled without response", got)
	}
	for _, want := range []string{"character owner verified", "roster=1", `requested=(slotOrChar=0 slot=0 job=0 name="hero")`, "nameTaken=true", "owner=77"} {
		if !strings.Contains(got.Reason, want) {
			t.Fatalf("reason %q missing %q", got.Reason, want)
		}
	}
}

func TestCreateCommandRecordsCharacterOwnerGap(t *testing.T) {
	cmd := NewCreateCommand(alignedcmd.Request{
		AccountID:           " acc ",
		SelectedCharacterID: 0,
	}, CreateCharacterRequest{Job: 15, Name: "abc"})
	summary := cmd.String()
	for _, want := range []string{`account="acc"`, "job=15", `name="abc"`, "character owner", "roster op2"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary %q missing %q", summary, want)
		}
	}
}

package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"

	dnfguild "longheng.io/server/internal/modules/dnf/guild"
)

func TestGuildInvitationWireContract(t *testing.T) {
	g := &dnfguild.Guild{ID: 0x12345678, Name: "公会", MasterName: "会长", Promotion: "介绍", Level: 7, MemberLimit: 1000, Tags: []byte{1, 4}, Members: map[string]dnfguild.Member{"1": {}}}
	body, err := buildCurrentGuildInvitationBody(g, "邀请者")
	if err != nil {
		t.Fatal(err)
	}
	offset := 0
	readString := func(want string) {
		t.Helper()
		got, next, ok := parseCurrentGuildDSTR(body, offset)
		if !ok || got != want {
			t.Fatalf("offset=%d string=%q want=%q", offset, got, want)
		}
		offset = next
	}
	readString("公会")
	readString("邀请者")
	readString("介绍")
	if binary.LittleEndian.Uint32(body[offset:]) != 7 || binary.LittleEndian.Uint16(body[offset+4:]) != 1 || !bytes.Equal(body[offset+6:offset+9], []byte{2, 1, 4}) || binary.LittleEndian.Uint32(body[offset+9:]) != g.ID {
		t.Fatalf("numeric fields=%x", body[offset:])
	}
	offset += 13
	readString("会长")
	if len(body)-offset != 4 || binary.LittleEndian.Uint32(body[offset:]) != 1000 {
		t.Fatal("final DWORD mismatch")
	}
	for _, code := range []byte{0, 1, 39, 104} {
		got := buildCurrentGuildInvitationResult(code, "目标")
		want := []byte{code}
		if code < 2 {
			want = append(want, guildWireString("目标")...)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("result%d=%x", code, got)
		}
	}
}

func TestGuildInvitationRejectsMalformedWire(t *testing.T) {
	valid := append([]byte{1}, guildWireString("角色2")...)
	for _, b := range [][]byte{nil, {1}, {250, 1, 0, 0, 0, 'a'}, append(append([]byte{}, valid...), 0), {1, 1, 0, 0, 0, 0x81}, {1, 1, 0, 0, 0, 0}, append([]byte{1}, guildWireString(" x")...), append([]byte{1}, guildWireString(strings.Repeat("x", rosterNameMaxBytes+1))...)} {
		if _, _, ok := parseCurrentGuildInvitation(b); ok {
			t.Fatalf("accepted malformed=%x", b)
		}
	}
	for _, region := range []byte{0, 1, 249} {
		b := append([]byte{region}, guildWireString("角色2")...)
		got, name, ok := parseCurrentGuildInvitation(b)
		if !ok || got != region || name != "角色2" {
			t.Fatal("valid actor route rejected")
		}
	}
}

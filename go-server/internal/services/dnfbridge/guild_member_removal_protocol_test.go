package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"

	dnfguild "longheng.io/server/internal/modules/dnf/guild"
)

func TestGuildSecedeRequestWireShapes(t *testing.T) {
	for _, region := range []byte{0, 1, 249} {
		for _, name := range []string{"", "角色2", " spaced "} {
			body := append([]byte{region}, guildWireString(name)...)
			r, ok := parseCurrentGuildSecedeRequest(body)
			if !ok || r.Region != region || r.Name != name {
				t.Fatalf("request=%x parsed=%+v valid=%v", body, r, ok)
			}
		}
	}
}

func TestGuildSecedeRequestRejectsMalformedWire(t *testing.T) {
	for _, body := range [][]byte{
		nil, {1}, {1, 0, 0, 0}, {250, 0, 0, 0, 0}, {255, 0, 0, 0, 0},
		{1, 1, 0, 0, 0}, {1, 0, 0, 0, 0, 0}, {1, 255, 255, 255, 255},
		{1, 1, 0, 0, 0, 0x81}, {1, 1, 0, 0, 0, 0},
		append([]byte{1}, guildWireString(strings.Repeat("a", rosterNameMaxBytes+1))...),
	} {
		if r, ok := parseCurrentGuildSecedeRequest(body); ok || r != (currentGuildSecedeRequest{}) {
			t.Fatalf("malformed body=%x accepted as %+v", body, r)
		}
	}
}

func TestGuildSecedeSuccessNativeReadOrder(t *testing.T) {
	for _, voluntary := range []bool{true, false} {
		result := dnfguild.MemberRemoval{GuildID: 7, GuildName: "测试公会", Member: dnfguild.Member{Name: "成员甲"}, Voluntary: voluntary}
		body := buildCurrentGuildSecedeSuccessBody(result)
		action := byte(2)
		if voluntary {
			action = 1
		}
		// Decode independently as the native callback: byte, DSTR, byte, DSTR.
		if len(body) < 10 || body[0] != action {
			t.Fatalf("success action/body=%x", body)
		}
		guildLength := int(binary.LittleEndian.Uint32(body[1:5]))
		if guildLength > len(body)-10 {
			t.Fatal("guild DSTR overruns success body")
		}
		nameOffset := 5 + guildLength
		if body[nameOffset] != 0 {
			t.Fatal("missing unused byte between DSTRs")
		}
		characterLength := int(binary.LittleEndian.Uint32(body[nameOffset+1 : nameOffset+5]))
		if nameOffset+5+characterLength != len(body) {
			t.Fatal("character DSTR length or trailing bytes mismatch")
		}
		if !bytes.Equal(body[5:nameOffset], rosterNameBytes("测试公会")) || !bytes.Equal(body[nameOffset+5:], rosterNameBytes("成员甲")) {
			t.Fatalf("native formatter would swap guild/character names: %x", body)
		}
	}
}

package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"reflect"
	"strings"
	"testing"

	dnfguild "longheng.io/server/internal/modules/dnf/guild"
)

func guildSearchFilterBody(tags uint32, page byte) []byte {
	b := []byte{2, 0, 0, 0, 0, page}
	binary.LittleEndian.PutUint32(b[1:], tags)
	return b
}

// Decode the actual v105 class-1 callback layout independently of the writer.
func readGuildSearchReply(t *testing.T, body []byte) (uint32, byte, []uint32) {
	t.Helper()
	if len(body) < 7 || body[0] != 1 {
		t.Fatalf("invalid search success: %x", body)
	}
	total, page, count := binary.LittleEndian.Uint32(body[1:]), body[5], int(body[6])
	body = body[7:]
	ids := make([]uint32, 0, count)
	for range count {
		if len(body) < 12 {
			t.Fatalf("truncated rich row: %x", body)
		}
		ids = append(ids, binary.LittleEndian.Uint32(body))
		body = body[12:] // id/u8 join/u32 tags/u16 members/u8 level
		for range 3 {
			if len(body) < 4 || uint64(binary.LittleEndian.Uint32(body)) > uint64(len(body)-4) {
				t.Fatalf("truncated row text: %x", body)
			}
			body = body[4+int(binary.LittleEndian.Uint32(body)):]
		}
	}
	if len(body) != 0 {
		t.Fatalf("trailing search reply: %x", body)
	}
	return total, page, ids
}

func TestGuildSearchNativeTagMaskAndPage(t *testing.T) {
	s, p, repo := guildFoundationFixture(t)
	r := dnfguild.NewRegistry()
	for i := uint32(1); i <= 65; i++ {
		tag := byte(0)
		if i > 35 {
			tag = 1
		}
		master := fmt.Sprint(1000 + i)
		r.Guilds[i] = &dnfguild.Guild{ID: i, Name: fmt.Sprintf("公会%d", i), MasterName: fmt.Sprintf("会长%d", i), MasterCharacterID: master, Level: 1, Tags: []byte{tag}, Members: map[string]dnfguild.Member{master: {CharacterID: master, AccountID: "owner" + master, Grade: 1}}}
	}
	if err := dnfguild.Save(context.Background(), repo.Settings, r, s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	before, _, err := repo.Settings.Load(context.Background(), dnfguild.RegistryScope)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		body  []byte
		total uint32
		page  byte
		ids   []uint32
	}{
		{"tag0 page1", guildSearchFilterBody(1, 1), 35, 1, []uint32{31, 32, 33, 34, 35}},
		{"tag1 page0", guildSearchFilterBody(2, 0), 30, 0, guildSearchIDs(36, 65)},
		{"no filter page2", guildSearchFilterBody(0, 2), 65, 2, []uint32{61, 62, 63, 64, 65}},
		{"last byte is page255", guildSearchFilterBody(0, 255), 65, 255, []uint32{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p[2].conn.(*bufferConn).write.Reset()
			guildDispatch(t, s, p[2], 1450, tc.body)
			total, page, ids := readGuildSearchReply(t, guildReply(t, p[2], 1450))
			if total != tc.total || page != tc.page || !reflect.DeepEqual(ids, tc.ids) {
				t.Fatalf("total/page/ids = %d/%d/%v, want %d/%d/%v", total, page, ids, tc.total, tc.page, tc.ids)
			}
		})
	}
	after, _, err := repo.Settings.Load(context.Background(), dnfguild.RegistryScope)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatal("public search mutated guild state")
	}
}

func guildSearchIDs(first, last uint32) []uint32 {
	ids := []uint32{}
	for i := first; i <= last; i++ {
		ids = append(ids, i)
	}
	return ids
}

func TestGuildSearchNativeNameModesAndSettingsReadback(t *testing.T) {
	s, p, repo := guildTextFixture(t)
	ctx := context.Background()
	r, err := dnfguild.Load(ctx, repo.Settings)
	if err != nil {
		t.Fatal(err)
	}
	r.Guilds[1].Name = "Alpha公会"
	r.Guilds[2].MasterName = "Alpha会长"
	if err := dnfguild.Save(ctx, repo.Settings, r, s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	for mode, want := range map[byte]uint32{0: 1, 1: 2} {
		p[2].conn.(*bufferConn).write.Reset()
		guildDispatch(t, s, p[2], 1450, append([]byte{mode}, guildWireString("alpha")...))
		total, page, ids := readGuildSearchReply(t, guildReply(t, p[2], 1450))
		if total != 1 || page != 0 || !reflect.DeepEqual(ids, []uint32{want}) {
			t.Fatalf("mode%d: %d/%d/%v", mode, total, page, ids)
		}
	}
	// Public search consumes committed tags even with web promotion disabled.
	guildDispatch(t, s, p[0], 740, guildSettingsBody(0x20020, "欢迎加入", 0, 0x45, 1))
	p[2].conn.(*bufferConn).write.Reset()
	guildDispatch(t, s, p[2], 1450, guildSearchFilterBody(0x44, 0))
	response := guildReply(t, p[2], 1450)
	total, _, ids := readGuildSearchReply(t, response)
	if total != 1 || !reflect.DeepEqual(ids, []uint32{1}) || !bytes.Contains(response, rosterNameBytes("欢迎加入")) {
		t.Fatalf("committed settings missing: %x", response)
	}
}

func TestGuildSearchMalformedRequestsDoNotMutate(t *testing.T) {
	for name, body := range map[string][]byte{
		"missing": nil, "short filter": {2, 0, 0, 0, 0},
		"trailing filter": {2, 0, 0, 0, 0, 0, 0},
		"unknown mode":    append([]byte{3}, guildWireString("公会")...),
		"unknown tag":     guildSearchFilterBody(128, 0), "four tags": guildSearchFilterBody(15, 0),
		"huge tags":      guildSearchFilterBody(0xffffffff, 0),
		"truncated dstr": {0, 2, 0, 0, 0, 'a'}, "huge dstr": {1, 255, 255, 255, 255},
		"trailing dstr":  append(append([]byte{0}, guildWireString("公会")...), 0),
		"broken gb18030": {0, 3, 0, 0, 0, 0x81, 0x30, 0x81},
		"nul":            append([]byte{0}, guildWireString("公\x00会")...),
		"control":        append([]byte{1}, guildWireString("a\nb")...),
		"long query":     append([]byte{0}, guildWireString(strings.Repeat("中", 15))...),
	} {
		t.Run(name, func(t *testing.T) {
			s, p, repo := guildTextFixture(t)
			before, _, _ := repo.Settings.Load(context.Background(), dnfguild.RegistryScope)
			guildDispatch(t, s, p[2], 1450, body)
			if got := guildReply(t, p[2], 1450); !bytes.Equal(got, []byte{0, currentGuildGenericError}) {
				t.Fatalf("unexpected ACK: %x", got)
			}
			after, _, _ := repo.Settings.Load(context.Background(), dnfguild.RegistryScope)
			if !reflect.DeepEqual(before, after) || p[0].conn.(*bufferConn).write.Len() != 0 || p[1].conn.(*bufferConn).write.Len() != 0 {
				t.Fatal("search rejection mutated or broadcast")
			}
		})
	}
}

func TestGuildSearchRetainsSessionAndClassGuards(t *testing.T) {
	for _, badClass := range []bool{false, true} {
		s, p, _ := guildTextFixture(t)
		class := byte(1)
		if badClass {
			class = 0
		} else {
			p[2].accountID = "wrong-owner"
		}
		handled, err := currentGameplayModules.DispatchUpper(s, p[2], 1450, class, guildSearchFilterBody(0, 0))
		if !handled || err != nil {
			t.Fatalf("handled=%v err=%v", handled, err)
		}
		for wire := p[2].conn.(*bufferConn).write.Bytes(); len(wire) != 0; {
			packet, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
			wire = rest
			if packet.Header.MsgID == 1450 && len(packet.Body) > 0 && packet.Body[0] == 1 {
				t.Fatal("invalid session/class received public search success")
			}
		}
	}
}

func TestGuildSearchAccountIndexNativeCallbackLayout(t *testing.T) {
	s, p, _ := guildTextFixture(t)
	guildDispatch(t, s, p[0], 1451, nil)
	// class1 success, one rich row: id1/join1/tags0/members2/level1.
	want := []byte{1, 1, 1, 0, 0, 0, 1, 0, 0, 0, 0, 2, 0, 1}
	for _, text := range []string{"测试公会", "角色1", ""} {
		want = append(want, guildWireString(text)...)
	}
	// One mapping: guild1/social group0/current account's character1.
	want = append(want, 1, 1, 0, 0, 0, 0)
	want = append(want, guildWireString("角色1")...)
	if got := guildReply(t, p[0], 1451); !bytes.Equal(got, want) {
		t.Fatalf("native account reader layout got=%x want=%x", got, want)
	}
}

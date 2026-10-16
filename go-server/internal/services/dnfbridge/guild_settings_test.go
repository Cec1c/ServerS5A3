package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"reflect"
	"testing"
	"time"

	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func guildSettingsBody(mask uint32, promotion string, recruitment byte, tags uint32, mode byte) []byte {
	var w packetWriter
	w.writeUint32(mask)
	writeClientNameDstr(&w, promotion)
	w.writeByte(recruitment)
	w.writeUint32(tags)
	w.writeByte(mode)
	return w.bytes()
}

func TestGuildSettingsAdmissionPolicyAndPublicReadback(t *testing.T) {
	s, p, repos := guildTextFixture(t)
	ctx := context.Background()
	r, _ := dnfguild.Load(ctx, repos.Settings)
	delete(r.Guilds, 2)
	if err := dnfguild.Save(ctx, repos.Settings, r, s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	guildDispatch(t, s, p[0], 740, guildSettingsBody(0x20022, "宣传", 1, 0x45, 0))
	guildDispatch(t, s, p[2], 348, guildWireString("测试公会"))
	r, _ = dnfguild.Load(ctx, repos.Settings)
	if _, _, joined := r.GuildForCharacter("3"); joined || len(r.Guilds[1].Applications) != 1 {
		t.Fatal("approval mode directly admitted applicant")
	}
	guildDispatch(t, s, p[2], 745, guildWireString("测试公会"))
	if !bytes.Equal(guildReply(t, p[2], 745), append([]byte{1}, buildCurrentGuildSearchResultBody(r.Guilds[1])...)) {
		t.Fatal("public detail omitted current settings")
	}
	// Only join mode selected: stale form values for promotion and tags must not
	// revert the previous committed changes.
	guildDispatch(t, s, p[0], 740, guildSettingsBody(2, "", 0, 0, 1))
	guildDispatch(t, s, p[2], 348, guildWireString("测试公会"))
	r, _ = dnfguild.Load(ctx, repos.Settings)
	g, _, joined := r.GuildForCharacter("3")
	if !joined || len(g.Applications) != 0 || g.Promotion != "宣传" || !g.WebPromotion || !bytes.Equal(g.Tags, []byte{0, 2, 6}) {
		t.Fatal("immediate mode or masked update failed")
	}
}

func TestGuildSettingsRejectsMalformedAndUnauthorizedBatches(t *testing.T) {
	valid := guildSettingsBody(0x20022, "介绍", 1, 5, 0)
	for _, tc := range []struct {
		name   string
		body   []byte
		member bool
		code   byte
	}{
		{"short", valid[:len(valid)-1], false, 108}, {"trailing", append(append([]byte{}, valid...), 0), false, 108},
		{"huge_string", []byte{0x22, 0, 2, 0, 255, 255, 255, 255, 0, 0, 0, 0, 0, 0}, false, 108},
		{"bad_boolean", guildSettingsBody(0x20, "介绍", 2, 0, 0), false, 108},
		{"zero_mask", guildSettingsBody(0, "介绍", 0, 0, 0), false, 7},
		{"unknown_mask", guildSettingsBody(0x20023, "介绍", 0, 0, 0), false, 7},
		{"invalid_selected_mode", guildSettingsBody(2, "介绍", 0, 0, 2), false, 7},
		{"invalid_tags", guildSettingsBody(0x20022, "介绍", 0, 15, 0), false, 7},
		{"ordinary_member", valid, true, 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, p, repos := guildTextFixture(t)
			ctx := context.Background()
			before, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
			actor := p[0]
			if tc.member {
				actor = p[1]
			}
			guildDispatch(t, s, actor, 740, tc.body)
			if got := guildReply(t, actor, 740); !bytes.Equal(got, []byte{0, tc.code}) {
				t.Fatalf("ack=%x", got)
			}
			after, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("bad batch partly committed")
			}
			for _, peer := range p {
				if peer != actor && peer.conn.(*bufferConn).write.Len() != 0 {
					t.Fatal("rejection broadcast")
				}
			}
		})
	}
}

func TestGuildSettingsCustomPermissionsAndStaleOwnership(t *testing.T) {
	for _, mode := range []string{"allowed_partial", "denied_combined", "stale_member", "wrong_account", "class_zero"} {
		t.Run(mode, func(t *testing.T) {
			s, p, repos := guildTextFixture(t)
			ctx := context.Background()
			r, _ := dnfguild.Load(ctx, repos.Settings)
			r.Guilds[1].GradePermissions = map[byte]uint32{4: 2}
			mask := uint32(2)
			want := []byte{1}
			switch mode {
			case "denied_combined":
				mask = 0x22
				want = []byte{0, 7}
			case "stale_member":
				m := r.Guilds[1].Members["2"]
				m.AccountID = "old"
				r.Guilds[1].Members["2"] = m
				want = []byte{0, 34}
			case "wrong_account":
				p[1].accountID = "old"
				want = []byte{0, 34}
			}
			if err := dnfguild.Save(ctx, repos.Settings, r, s.gameplayNow()); err != nil {
				t.Fatal(err)
			}
			before, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
			if mode == "class_zero" {
				_, err := currentGameplayModules.DispatchUpper(s, p[1], 740, 0, guildSettingsBody(mask, "stale", 0, 127, 0))
				if err != nil {
					t.Fatal(err)
				}
			} else {
				guildDispatch(t, s, p[1], 740, guildSettingsBody(mask, "stale", 0, 127, 0))
				if got := guildReply(t, p[1], 740); !bytes.Equal(got, want) {
					t.Fatalf("ack=%x want=%x", got, want)
				}
			}
			after, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
			if mode != "allowed_partial" && !reflect.DeepEqual(before, after) {
				t.Fatal("unauthorized change")
			}
		})
	}
}

func TestGuildSettingsSaveFailureAndConcurrentEdits(t *testing.T) {
	s, p, repos := guildTextFixture(t)
	ctx := context.Background()
	before, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
	broken := repos
	broken.Guild = guildTextFailUoW{repos.Guild}
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return broken, true }
	guildDispatch(t, s, p[0], 740, guildSettingsBody(0x20022, "rollback", 1, 5, 0))
	if b := guildReply(t, p[0], 740); len(b) != 2 || b[0] != 0 {
		t.Fatalf("ACK=%x", b)
	}
	after, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
	if !reflect.DeepEqual(before, after) || p[1].conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("save failure changed data or refreshed")
	}
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	for _, peer := range p {
		peer.conn.(*bufferConn).write.Reset()
		peer.guild.memberListRequested = true
	}
	done := make(chan error, 2)
	go func() { done <- s.handleCurrentGuildSettings(p[0], guildSettingsBody(0x20022, "settings", 1, 5, 0)) }()
	go func() { done <- s.handleCurrentGuildMemberMemo(p[1], guildWireString("memo")) }()
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("settings/memo deadlocked")
		}
	}
	r, _ := dnfguild.Load(ctx, repos.Settings)
	if r.Guilds[1].Promotion != "settings" || r.Guilds[1].Members["2"].Memo != "memo" {
		t.Fatal("concurrent edit lost")
	}
	if !bytes.Equal(guildReply(t, p[0], 740), []byte{1}) || !bytes.Equal(guildReply(t, p[1], 224), []byte{1}) {
		t.Fatal("missing concurrent ACK")
	}
}

func TestGuildSettingsWireLengthAndUnusedMode(t *testing.T) {
	for _, value := range []string{"", "中文", "😀"} {
		t.Run(fmt.Sprint(len(value)), func(t *testing.T) {
			body := guildSettingsBody(0x20000, value, 0, 3, 2)
			n := binary.LittleEndian.Uint32(body[4:8])
			if int(n)+14 != len(body) {
				t.Fatal("wrong wire length")
			}
			change, ok := parseCurrentGuildSettings(body)
			if !ok || change.Promotion != value || change.JoinMode != 2 {
				t.Fatal("unselected mode lost")
			}
			s, p, _ := guildTextFixture(t)
			guildDispatch(t, s, p[0], 740, body)
			if !bytes.Equal(guildReply(t, p[0], 740), []byte{1}) {
				t.Fatal("unselected fallback mode rejected")
			}
		})
	}
}

func TestGuildSettingsNativeCompositeUpdate(t *testing.T) {
	s, p, repos := guildTextFixture(t)
	promotion := "  共同冒险\n欢迎加入  "
	guildDispatch(t, s, p[0], 740, guildSettingsBody(0x20022, promotion, 1, 5, 0))
	if !bytes.Equal(guildReply(t, p[0], 740), []byte{1}) {
		t.Fatal("missing native settings acknowledgement")
	}
	first, _ := splitCurrentGameServerUpperPacketAuto(t, p[0].conn.(*bufferConn).write.Bytes())
	if first.Header.MsgID != 740 || !bytes.Contains(guildReply(t, p[1], 70), rosterNameBytes("共同冒险")) || p[2].conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("wrong settings refresh order or recipients")
	}
	r, err := dnfguild.Load(context.Background(), repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	g := r.Guilds[1]
	if g.Promotion != promotion || !g.WebPromotion || g.JoinMode != 0 || !bytes.Equal(g.Tags, []byte{0, 2}) {
		t.Fatalf("composite settings lost: %+v", g)
	}
}

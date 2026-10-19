package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func guildTextFixture(t *testing.T) (*Service, []*gameSession, dnfrepo.Group) {
	t.Helper()
	s, peers, repos := guildFoundationFixture(t)
	r := dnfguild.NewRegistry()
	ctx := context.Background()
	master, _, _ := repos.Character.Load(ctx, "1")
	g, err := r.Create(master, "测试公会", s.gameplayNow())
	if err != nil {
		t.Fatal(err)
	}
	member, _, _ := repos.Character.Load(ctx, "2")
	if _, err := r.JoinDirect(g.ID, member, s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	other, _, _ := repos.Character.Load(ctx, "3")
	if _, err := r.Create(other, "其他公会", s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	if err := dnfguild.Save(ctx, repos.Settings, r, s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	return s, peers, repos
}

func TestGuildTextRejectsMalformedWireWithoutMutation(t *testing.T) {
	for _, op := range []uint16{154, 224} {
		limit := dnfguild.MemberMemoMaxBytes
		if op == 154 {
			limit = dnfguild.NoticeMaxBytes
		}
		for name, body := range map[string][]byte{
			"missing": nil, "short": {1, 0, 0}, "missing_text": {1, 0, 0, 0}, "huge": {255, 255, 255, 255},
			"trailing": append(guildWireString("abc"), 0), "nul": guildWireString("ab\x00c"),
			"control": guildWireString("a\tb"), "over_bytes": guildWireString(strings.Repeat("a", limit+1)),
			"broken_gbk": {1, 0, 0, 0, 0x81}, "broken_gb18030": {3, 0, 0, 0, 0x81, 0x30, 0x81},
		} {
			t.Run(fmt.Sprintf("%d/%s", op, name), func(t *testing.T) {
				s, p, repos := guildTextFixture(t)
				before, _, _ := repos.Settings.Load(context.Background(), dnfguild.RegistryScope)
				guildDispatch(t, s, p[0], op, body)
				if got := guildReply(t, p[0], op); !bytes.Equal(got, []byte{0, 108}) {
					t.Fatalf("rejection=%x", got)
				}
				after, _, _ := repos.Settings.Load(context.Background(), dnfguild.RegistryScope)
				if !reflect.DeepEqual(before, after) || p[1].conn.(*bufferConn).write.Len() != 0 {
					t.Fatal("rejected request mutated or broadcast")
				}
			})
		}
	}
}

func TestGuildTextNativeBoundariesClearAndRelog(t *testing.T) {
	s, p, repos := guildTextFixture(t)
	for _, notice := range []string{strings.Repeat("会", 127) + "a", ""} {
		guildDispatch(t, s, p[0], 154, guildWireString(notice))
		if !bytes.Equal(guildReply(t, p[0], 154), []byte{1}) {
			t.Fatal("valid notice rejected")
		}
	}
	for _, memo := range []string{strings.Repeat("会", 10), strings.Repeat("😀", 5), "  同行  "} {
		guildDispatch(t, s, p[1], 224, guildWireString(memo))
		if !bytes.Equal(guildReply(t, p[1], 224), []byte{1}) {
			t.Fatal("valid memo rejected")
		}
	}
	for _, memo := range []string{"", "　 ", strings.Repeat("会", 11), "a\nb"} {
		guildDispatch(t, s, p[1], 224, guildWireString(memo))
		if !bytes.Equal(guildReply(t, p[1], 224), []byte{0, 108}) {
			t.Fatal("invalid memo accepted")
		}
	}
	fresh := &Service{options: s.options, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }, gameSessions: map[uint16]*gameSession{}}
	peer := &gameSession{conn: &bufferConn{}, accountID: "a2", selectedCharacterID: 2, characterGeneration: 99, townSceneReadyCharacterID: 2}
	fresh.gameSessions[2] = peer
	guildDispatch(t, fresh, peer, 140, nil)
	if !bytes.Contains(guildReply(t, peer, 140), rosterNameBytes("  同行  ")) {
		t.Fatal("memo missing after reconnect")
	}
	r, err := dnfguild.Load(context.Background(), repos.Settings)
	if err != nil || r.Guilds[1].Notice != "" {
		t.Fatalf("notice clear did not persist: %v", err)
	}
}

func TestGuildTextPermissionFailuresAndClass(t *testing.T) {
	for _, mode := range []string{"ordinary", "deputy", "custom_allow", "custom_deny", "stale_member", "no_member", "wrong_account", "class_zero"} {
		t.Run(mode, func(t *testing.T) {
			s, p, repos := guildTextFixture(t)
			ctx := context.Background()
			r, _ := dnfguild.Load(ctx, repos.Settings)
			g := r.Guilds[1]
			m := g.Members["2"]
			want := []byte{0, 36}
			switch mode {
			case "deputy":
				m.Grade = 2
				want = []byte{1}
			case "custom_allow":
				g.GradePermissions = map[byte]uint32{m.Grade: 1 << 4}
				want = []byte{1}
			case "custom_deny":
				m.Grade = 2
				g.GradePermissions = map[byte]uint32{2: 0}
			case "stale_member":
				m.AccountID = "former-owner"
				want = []byte{0, 34}
			case "no_member":
				want = []byte{0, 34}
			case "wrong_account":
				p[1].accountID = "other-owner"
				want = []byte{0, 34}
			}
			g.Members["2"] = m
			if mode == "no_member" {
				delete(g.Members, "2")
			}
			if err := dnfguild.Save(ctx, repos.Settings, r, s.gameplayNow()); err != nil {
				t.Fatal(err)
			}
			before, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
			if mode == "class_zero" {
				handled, err := currentGameplayModules.DispatchUpper(s, p[1], 154, 0, guildWireString("notice"))
				if !handled || err != nil {
					t.Fatalf("handled=%v err=%v", handled, err)
				}
			} else {
				guildDispatch(t, s, p[1], 154, guildWireString("notice"))
				if got := guildReply(t, p[1], 154); !bytes.Equal(got, want) {
					t.Fatalf("ack=%x want=%x", got, want)
				}
			}
			after, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
			if !bytes.Equal(want, []byte{1}) && !reflect.DeepEqual(before, after) {
				t.Fatal("denied request persisted")
			}
		})
	}
}

type guildTextFailSettings struct{ dnfrepo.SettingsRepository }

func (g guildTextFailSettings) Save(context.Context, dnfrepo.SettingsRecord) error {
	return errors.New("injected text save failure")
}

type guildTextFailUoW struct{ base dnfrepo.GuildUnitOfWork }

func (g guildTextFailUoW) WithinGuild(ctx context.Context, owners []dnfrepo.GuildOwner, apply func(dnfrepo.Group) error) error {
	return g.base.WithinGuild(ctx, owners, func(tx dnfrepo.Group) error { tx.Settings = guildTextFailSettings{tx.Settings}; return apply(tx) })
}

func TestGuildTextSaveFailureRollsBackAndDoesNotRefresh(t *testing.T) {
	for _, op := range []uint16{154, 224} {
		t.Run(fmt.Sprint(op), func(t *testing.T) {
			s, p, repos := guildTextFixture(t)
			ctx := context.Background()
			before, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
			broken := repos
			broken.Guild = guildTextFailUoW{repos.Guild}
			s.repositoryProvider = func() (dnfrepo.Group, bool) { return broken, true }
			guildDispatch(t, s, p[0], op, guildWireString("rollback"))
			if got := guildReply(t, p[0], op); len(got) != 2 || got[0] != 0 {
				t.Fatalf("ack=%x", got)
			}
			after, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
			if !reflect.DeepEqual(before, after) || p[1].conn.(*bufferConn).write.Len() != 0 {
				t.Fatal("save failure changed state or refreshed peers")
			}
		})
	}
}

func TestGuildTextConcurrentEditsAndRefreshes(t *testing.T) {
	s, p, repos := guildTextFixture(t)
	for _, peer := range p {
		peer.guild.memberListRequested = true
	}
	done := make(chan error, 2)
	go func() { done <- s.handleCurrentGuildNotice(p[0], guildWireString("notice")) }()
	go func() { done <- s.handleCurrentGuildMemberMemo(p[1], guildWireString("memo")) }()
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("simultaneous edits deadlocked")
		}
	}
	r, _ := dnfguild.Load(context.Background(), repos.Settings)
	if r.Guilds[1].Notice != "notice" || r.Guilds[1].Members["2"].Memo != "memo" {
		t.Fatal("lost committed text")
	}
	if !bytes.Equal(guildReply(t, p[0], 154), []byte{1}) || !bytes.Equal(guildReply(t, p[1], 224), []byte{1}) {
		t.Fatal("missing concurrent acknowledgements")
	}
	if p[2].conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("cross-guild refresh")
	}
}

func TestGuildTextRefreshRevalidatesSessionAndGuild(t *testing.T) {
	for _, mode := range []string{"replacement", "removed", "other_guild", "stale_member", "repository_owner", "closed_roster"} {
		t.Run(mode, func(t *testing.T) {
			s, p, repos := guildTextFixture(t)
			ctx := context.Background()
			source, _ := s.boundGameSessionCharacterSnapshot(p[0])
			peer, _ := s.boundGameSessionCharacterSnapshot(p[1])
			r, _ := dnfguild.Load(ctx, repos.Settings)
			g := r.Guilds[1]
			m := g.Members["2"]
			switch mode {
			case "replacement":
				s.promoteResidentGameSession(&gameSession{conn: &bufferConn{}, accountID: "a2", selectedCharacterID: 2, characterGeneration: 999}, 2)
			case "removed":
				delete(g.Members, "2")
			case "other_guild":
				delete(g.Members, "2")
				r.Guilds[2].Members["2"] = m
			case "stale_member":
				m.AccountID = "former-owner"
				g.Members["2"] = m
			case "repository_owner":
				c, _, _ := repos.Character.Load(ctx, "2")
				c.AccountID = "other"
				if err := repos.Character.Save(ctx, c); err != nil {
					t.Fatal(err)
				}
			}
			if err := dnfguild.Save(ctx, repos.Settings, r, s.gameplayNow()); err != nil {
				t.Fatal(err)
			}
			_ = s.refreshCurrentGuildTextPeer(ctx, 1, peer, source, mode != "closed_roster")
			if p[1].conn.(*bufferConn).write.Len() != 0 {
				t.Fatal("stale or unsubscribed peer got refresh")
			}
		})
	}
}

func TestGuildTextDSTRLengthIsBytes(t *testing.T) {
	input := guildWireString("中文")
	if binary.LittleEndian.Uint32(input) != 4 {
		t.Fatal("test fixture is not native GB18030")
	}
	text, ok := parseCurrentGuildText(input, 4)
	if !ok || text != "中文" {
		t.Fatal("valid native string rejected")
	}
	if _, ok := parseCurrentGuildText(input, 3); ok {
		t.Fatal("byte cap ignored")
	}
}

func TestGuildTextNativeNoticeAndSelfMemo(t *testing.T) {
	s, p, repos := guildTextFixture(t)
	notice := "  欢迎冒险\n每晚组队  "
	guildDispatch(t, s, p[0], 154, guildWireString(notice))
	if !bytes.Equal(guildReply(t, p[0], 154), []byte{1}) {
		t.Fatal("notice did not receive native success ACK")
	}
	first, _ := splitCurrentGameServerUpperPacketAuto(t, p[0].conn.(*bufferConn).write.Bytes())
	if first.Header.MsgID != 154 {
		t.Fatal("notice refresh preceded its ACK")
	}
	if !bytes.Contains(guildReply(t, p[1], 70), rosterNameBytes("欢迎冒险")) || p[2].conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("notice was not refreshed only to the same guild")
	}
	for _, peer := range p {
		peer.conn.(*bufferConn).write.Reset()
		peer.guild.memberListRequested = true
	}
	memo := "一起冒险"
	guildDispatch(t, s, p[1], 224, guildWireString(memo))
	if !bytes.Equal(guildReply(t, p[1], 224), []byte{1}) || !bytes.Contains(guildReply(t, p[0], 140), rosterNameBytes(memo)) {
		t.Fatal("memo ACK or roster refresh missing")
	}
	r, err := dnfguild.Load(context.Background(), repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	g, _, _ := r.GuildForCharacter("1")
	if g.Notice != notice || g.Members["2"].Memo != memo || g.Members["1"].Memo != "" || p[2].conn.(*bufferConn).write.Len() != 0 {
		t.Fatalf("wrong persistent text or recipient: notice=%q members=%+v", g.Notice, g.Members)
	}
}

package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"reflect"
	"testing"

	guild "longheng.io/server/internal/modules/dnf/guild"
	repo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func guildAppearanceFixture(t *testing.T) (*Service, []*gameSession, repo.Group) {
	t.Helper()
	s, peers, repos := guildTextFixture(t)
	source := cargoQuerySource()
	source["etc/(r)guild.etc"] = "[guild mark]\n5 2000"
	source["etc/guild/insigniashapelist.etc"] = "[insignia mark]\n`a.ani` `b.ani`\n[/insignia mark]\n[insignia frame]\n`c.ani` `d.ani` `e.ani`\n[/insignia frame]"
	ui := "<ROOT><CONTROL>"
	for i := 1; i <= 15; i++ {
		ui += fmt.Sprintf("<IControl ID=\"STR:%d\"/>", i)
	}
	source["ui/guild/guildmark_cn.xui"] = ui + "</CONTROL></ROOT>"
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	s.pvfItemCatalog = catalog
	r, err := guild.Load(context.Background(), repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	r.Guilds[1].Mileage = 4000
	if err := guild.Save(context.Background(), repos.Settings, r, s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	return s, peers, repos
}

func auraRequest(mark, frame uint32) []byte {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint32(b, mark)
	binary.LittleEndian.PutUint32(b[4:], frame)
	return b
}

func TestGuildAppearanceMarkSaveAndSameValueRetry(t *testing.T) {
	s, peers, repos := guildAppearanceFixture(t)
	for _, mark := range []byte{14, 14, 4} {
		guildDispatch(t, s, peers[0], 787, []byte{mark})
		if got := guildReply(t, peers[0], 787); !bytes.Equal(got, []byte{1}) {
			t.Fatalf("mark ACK=%x", got)
		}
	}
	r, err := guild.Load(context.Background(), repos.Settings)
	if err != nil || r.Guilds[1].Mark != 4 || r.Guilds[1].Mileage != 2000 {
		t.Fatalf("saved state=%+v err=%v", r.Guilds[1], err)
	}
	for _, p := range peers[:2] {
		guildReply(t, p, 70)
	}
	if peers[2].conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("cross-guild refresh")
	}
}

func TestGuildAppearanceAuraSaveClearAndRelog(t *testing.T) {
	s, peers, repos := guildAppearanceFixture(t)
	for _, pair := range [][2]uint32{{1, 2}, {^uint32(0), ^uint32(0)}} {
		guildDispatch(t, s, peers[0], 832, auraRequest(pair[0], pair[1]))
		r, err := guild.Load(context.Background(), repos.Settings)
		if err != nil || r.Guilds[1].AuraMark != pair[0] || r.Guilds[1].AuraFrame != pair[1] {
			t.Fatalf("saved aura=%+v err=%v", r.Guilds[1], err)
		}
		for _, p := range peers[:2] {
			b := guildReply(t, p, 70)
			if len(b) < 17 || binary.LittleEndian.Uint32(b[len(b)-17:]) != pair[0] || binary.LittleEndian.Uint32(b[len(b)-13:]) != pair[1] {
				t.Fatal("NOTI70 appearance fields differ from saved pair")
			}
		}
	}
	if peers[2].conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("cross-guild refresh")
	}
}

func TestGuildAppearanceRejectedWritesAreAtomic(t *testing.T) {
	for _, mode := range []string{"mark_short", "mark_trailing", "mark_range", "insufficient", "member", "stale_owner", "stale_master", "aura_short", "aura_range", "partial_clear", "save_failure"} {
		t.Run(mode, func(t *testing.T) {
			s, p, repos := guildAppearanceFixture(t)
			ctx := context.Background()
			r, _ := guild.Load(ctx, repos.Settings)
			op, body, actor := uint16(787), []byte{5}, p[0]
			want := byte(34)
			switch mode {
			case "mark_short":
				body = nil
			case "mark_trailing":
				body = []byte{5, 0}
			case "mark_range":
				body = []byte{15}
			case "insufficient":
				r.Guilds[1].Mileage = 1999
				want = 22
			case "member":
				actor = p[1]
				want = 36
			case "stale_owner":
				m := r.Guilds[1].Members["1"]
				m.AccountID = "old"
				r.Guilds[1].Members["1"] = m
			case "stale_master":
				r.Guilds[1].MasterCharacterID = "2"
				m := r.Guilds[1].Members["1"]
				m.Grade = 4
				r.Guilds[1].Members["1"] = m
				m = r.Guilds[1].Members["2"]
				m.Grade = 1
				r.Guilds[1].Members["2"] = m
				want = 36
			case "aura_short":
				op = 832
				body = []byte{0}
			case "aura_range":
				op = 832
				body = auraRequest(2, 2)
			case "partial_clear":
				op = 832
				body = auraRequest(0, ^uint32(0))
			case "save_failure":
				broken := repos
				broken.Guild = guildTextFailUoW{repos.Guild}
				s.repositoryProvider = func() (repo.Group, bool) { return broken, true }
			}
			if err := guild.Save(ctx, repos.Settings, r, s.gameplayNow()); err != nil {
				t.Fatal(err)
			}
			before, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
			guildDispatch(t, s, actor, op, body)
			if op == 787 && !bytes.Equal(guildReply(t, actor, op), []byte{0, want}) {
				t.Fatal("wrong rejection")
			}
			after, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("failed edit mutated state")
			}
		})
	}
}

func TestGuildAppearanceRealPVF(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires matching Script.pvf")
	}
	source, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := guild.LoadAppearanceCatalog(source)
	if err != nil || c.Marks != 15 || c.FreeMarks != 5 || c.MarkPrice != 2000 || c.AuraMarks != 25 || c.AuraFrames != 25 {
		t.Fatalf("real appearance=%+v err=%v", c, err)
	}
}

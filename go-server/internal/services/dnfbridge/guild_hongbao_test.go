package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"reflect"
	"strings"
	"testing"

	guild "longheng.io/server/internal/modules/dnf/guild"
)

func TestGuildHongbaoQueryNativeListsAndNoMutation(t *testing.T) {
	s, p, repos := guildTextFixture(t)
	ctx := context.Background()
	r, err := guild.Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	g := r.Guilds[1]
	g.Mileage = 77777
	a, b := g.Members["1"], g.Members["2"]
	a.ContributionPoints, a.Mileage = 12, 999
	b.ContributionPoints, b.Mileage = 37, 123
	g.Members["1"], g.Members["2"] = a, b
	if err = guild.Save(ctx, repos.Settings, r, s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	before, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
	for range 2 {
		for _, actor := range p[:2] {
			guildDispatch(t, s, actor, 1300, nil)
			list := guildReply(t, actor, 1300)
			if len(list) != 15 || list[0] != 1 || binary.LittleEndian.Uint16(list[1:]) != 2 || !bytes.Equal(list[3:], make([]byte, 12)) {
				t.Fatalf("list=%x", list)
			}
			guildDispatch(t, s, actor, 1301, nil)
			points := guildReply(t, actor, 1301)
			if len(points) != 85 || points[0] != 1 || binary.LittleEndian.Uint32(points[1:]) != 2 {
				t.Fatalf("points=%x", points)
			}
			for i, m := range []guild.Member{a, b} {
				row := points[5+40*i : 5+40*(i+1)]
				if binary.LittleEndian.Uint32(row) != uint32(i+1) || row[4] != 90 || !bytes.Equal(bytes.TrimRight(row[5:35], "\x00"), rosterNameBytes(m.Name)) || binary.LittleEndian.Uint32(row[35:]) != m.ContributionPoints || row[39] != 0 {
					t.Fatalf("row=%x", row)
				}
			}
		}
	}
	after, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("view query awarded or rewrote guild data")
	}
	if p[2].conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("query leaked to another guild")
	}
	guildDispatch(t, s, p[2], 1301, nil)
	if b := guildReply(t, p[2], 1301); len(b) != 45 || binary.LittleEndian.Uint32(b[5:]) != 3 {
		t.Fatal("other guild got wrong members")
	}
}

func TestGuildHongbaoNativeBufferAndNameBoundaries(t *testing.T) {
	members := make([]guild.Member, 1000)
	for i := range members {
		members[i] = guild.Member{CharacterID: fmt.Sprint(i + 1), Name: strings.Repeat("会", 14) + "a", Level: 90, ContributionPoints: uint32(i)}
	}
	body, err := buildCurrentGuildHongbaoPointsBody(members)
	if err != nil || len(body) != 40004 || binary.LittleEndian.Uint32(body) != 1000 {
		t.Fatalf("max roster len=%d err=%v", len(body), err)
	}
	if body[4+34] != 0 || body[4+39] != 0 {
		t.Fatal("native name terminator/flags corrupt")
	}
	if _, err := buildCurrentGuildHongbaoPointsBody(append(members, members[0])); err == nil {
		t.Fatal("native stack bound not enforced")
	}
	for _, bad := range []guild.Member{{CharacterID: "0", Name: "a"}, {CharacterID: "not numeric", Name: "a"}, {CharacterID: "1", Name: "a", Level: 256}, {CharacterID: "1", Name: strings.Repeat("会", 15)}} {
		if _, err := buildCurrentGuildHongbaoPointsBody([]guild.Member{bad}); err == nil {
			t.Fatal("unrepresentable row accepted")
		}
	}
}

func TestGuildHongbaoFailuresHaveNativeErrorTail(t *testing.T) {
	for _, op := range []uint16{1300, 1301} {
		for _, mode := range []string{"trailing", "no_guild", "stale_member", "wrong_account", "class_zero"} {
			t.Run(fmt.Sprintf("%d/%s", op, mode), func(t *testing.T) {
				s, p, repos := guildTextFixture(t)
				ctx := context.Background()
				r, _ := guild.Load(ctx, repos.Settings)
				actor := p[1]
				var body []byte
				switch mode {
				case "trailing":
					body = []byte{0}
				case "no_guild":
					delete(r.Guilds[1].Members, "2")
				case "stale_member":
					m := r.Guilds[1].Members["2"]
					m.AccountID = "old"
					r.Guilds[1].Members["2"] = m
				case "wrong_account":
					actor.accountID = "wrong"
				}
				if err := guild.Save(ctx, repos.Settings, r, s.gameplayNow()); err != nil {
					t.Fatal(err)
				}
				before, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
				if mode == "class_zero" {
					handled, err := currentGameplayModules.DispatchUpper(s, actor, op, 0, body)
					if !handled || err != nil {
						t.Fatalf("route=%t err=%v", handled, err)
					}
				} else {
					guildDispatch(t, s, actor, op, body)
					if got := guildReply(t, actor, op); !bytes.Equal(got, []byte{0, 34, 34, 0, 0, 0}) {
						t.Fatalf("short/wrong failure=%x", got)
					}
				}
				after, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
				if !reflect.DeepEqual(before, after) {
					t.Fatal("rejected query mutated state")
				}
			})
		}
	}
}

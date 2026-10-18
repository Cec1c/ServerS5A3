package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"reflect"
	"testing"

	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func guildApplicationFixture(t *testing.T) (*Service, []*gameSession, dnfrepo.Group) {
	t.Helper()
	s, peers, repos := guildTextFixture(t)
	ctx := context.Background()
	r, err := dnfguild.Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	delete(r.Guilds, 2)
	r.Guilds[1].JoinMode = 0
	applicant, _, _ := repos.Character.Load(ctx, "3")
	if err := r.Apply(1, applicant, "申请", s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	if err := dnfguild.Save(ctx, repos.Settings, r, s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	return s, peers, repos
}

func TestGuildApplicationListDeniedClearsNativeCache(t *testing.T) {
	for _, mode := range []string{"ordinary_member", "not_member", "stale_member", "wrong_account", "unselected"} {
		t.Run(mode, func(t *testing.T) {
			s, peers, repos := guildApplicationFixture(t)
			ctx := context.Background()
			actor := peers[1]
			switch mode {
			case "not_member":
				actor = peers[2]
			case "wrong_account":
				actor.accountID = "wrong"
			case "unselected":
				actor.selectedCharacterID = 0
			case "stale_member":
				r, _ := dnfguild.Load(ctx, repos.Settings)
				m := r.Guilds[1].Members["2"]
				m.Grade, m.AccountID = 2, "old"
				r.Guilds[1].Members["2"] = m
				if err := dnfguild.Save(ctx, repos.Settings, r, s.gameplayNow()); err != nil {
					t.Fatal(err)
				}
			}
			before, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
			guildDispatch(t, s, actor, 352, nil)
			// Matched class1 callback clears its cached applications only on 36.
			if got := guildReply(t, actor, 352); !bytes.Equal(got, []byte{0, 36}) {
				t.Fatalf("native cache remains stale: %x", got)
			}
			after, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("list rejection changed persistent state")
			}
		})
	}
}

func TestGuildApplicationDecisionFailurePreservesPersistentState(t *testing.T) {
	for _, op := range []uint16{350, 351} {
		for _, mode := range []string{"save_failure", "ordinary_member", "wrong_region", "stale_applicant", "full", "malformed"} {
			if op == 351 && mode == "full" {
				continue
			} // denial requires no capacity
			t.Run(fmt.Sprintf("%d/%s", op, mode), func(t *testing.T) {
				s, peers, repos := guildApplicationFixture(t)
				ctx := context.Background()
				actor, body := peers[0], []byte{3, 0, 0, 0, 0}
				if op == 351 {
					body = append(body, 0, 0, 0)
				}
				r, _ := dnfguild.Load(ctx, repos.Settings)
				switch mode {
				case "save_failure":
					broken := repos
					broken.Guild = guildTextFailUoW{repos.Guild}
					s.repositoryProvider = func() (dnfrepo.Group, bool) { return broken, true }
				case "ordinary_member":
					actor = peers[1]
				case "wrong_region":
					body[4] = 1
				case "malformed":
					body = body[:len(body)-1]
				case "stale_applicant":
					a := r.Guilds[1].Applications["3"]
					a.AccountID = "old"
					r.Guilds[1].Applications["3"] = a
				case "full":
					r.Guilds[1].MemberLimit = len(r.Guilds[1].Members)
				}
				if err := dnfguild.Save(ctx, repos.Settings, r, s.gameplayNow()); err != nil {
					t.Fatal(err)
				}
				before, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
				guildDispatch(t, s, actor, op, body)
				got := guildReply(t, actor, op)
				if len(got) < 2 || got[0] != 0 {
					t.Fatalf("failure ACK=%x", got)
				}
				if op == 350 {
					echo := make([]byte, 5)
					if len(body) == 5 {
						copy(echo, body)
					}
					if !bytes.Equal(got[2:], echo) {
						t.Fatalf("approval failure identity=%x", got)
					}
				}
				after, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
				if !reflect.DeepEqual(before, after) {
					t.Fatal("failed decision persisted partial changes")
				}
				if peers[2].conn.(*bufferConn).write.Len() != 0 {
					t.Fatal("failed decision refreshed applicant")
				}
				if mode != "malformed" {
					// Native row buttons remove rows before either decision returns.
					list := guildReply(t, actor, 352)
					if mode == "ordinary_member" {
						if !bytes.Equal(list, []byte{0, 36}) {
							t.Fatalf("unauthorized list=%x", list)
						}
					} else {
						want := uint32(1)
						if mode == "stale_applicant" {
							want = 0
						}
						if len(list) < 5 || list[0] != 1 || binary.LittleEndian.Uint32(list[1:]) != want {
							t.Fatalf("restored list=%x", list)
						}
					}
				}
			})
		}
	}
}

func TestGuildApplicationDenialRefreshesAuthoritativeList(t *testing.T) {
	s, peers, repos := guildApplicationFixture(t)
	body := []byte{3, 0, 0, 0, 0, 0, 0, 0}
	guildDispatch(t, s, peers[0], 351, body)
	if !bytes.Equal(guildReply(t, peers[0], 351), append([]byte{1}, body...)) {
		t.Fatal("denial ACK changed")
	}
	list := guildReply(t, peers[0], 352)
	if len(list) != 5 || list[0] != 1 || binary.LittleEndian.Uint32(list[1:]) != 0 {
		t.Fatalf("list=%x", list)
	}
	r, err := dnfguild.Load(context.Background(), repos.Settings)
	if err != nil || len(r.Guilds[1].Members) != 2 || len(r.Guilds[1].Applications) != 0 {
		t.Fatalf("denial state=%+v %v", r, err)
	}
}

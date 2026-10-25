package dnfbridge

import (
	"bytes"
	"context"
	"reflect"
	"testing"

	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func guildMemberGradeFixture(t *testing.T) (*Service, []*gameSession, dnfrepo.Group) {
	t.Helper()
	s, peers, repos := guildTextFixture(t)
	ctx := context.Background()
	r, _ := dnfguild.Load(ctx, repos.Settings)
	for _, id := range []string{"1", "2", "3"} {
		c, _, _ := repos.Character.Load(ctx, id)
		c.Stats["server_group_id"] = 1
		if err := repos.Character.Save(ctx, c); err != nil {
			t.Fatal(err)
		}
		g, m, _ := r.GuildForCharacter(id)
		m.ServerGroup = 1
		g.Members[id] = m
	}
	if err := dnfguild.Save(ctx, repos.Settings, r, s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	return s, peers, repos
}

func guildMemberGradeBody(region byte, name string, grade byte) []byte {
	body := append([]byte{region}, guildWireString(name)...)
	return append(body, grade)
}

func TestGuildMemberGradeNativeFlow(t *testing.T) {
	s, peers, repos := guildMemberGradeFixture(t)
	ctx := context.Background()
	beforeCharacter, _, _ := repos.Character.Load(ctx, "2")
	for _, grade := range []byte{2, 3, 4} {
		for _, peer := range peers {
			peer.conn.(*bufferConn).write.Reset()
		}
		guildDispatch(t, s, peers[0], 126, guildMemberGradeBody(1, "角色2", grade))
		// Native class1 consumes a leading notification byte, then region,
		// name and grade. A request echo alone is one byte too short.
		want := append([]byte{1, 0}, guildMemberGradeBody(1, "角色2", grade)...)
		if got := guildReply(t, peers[0], 126); !bytes.Equal(got, want) {
			t.Fatalf("ACK=%x want=%x", got, want)
		}
		if got := guildReply(t, peers[1], 127); !bytes.Equal(got, []byte{grade}) {
			t.Fatalf("target self rank=%x", got)
		}
		r, err := dnfguild.Load(ctx, repos.Settings)
		if err != nil || r.Guilds[1].Members["2"].Grade != grade || r.Guilds[1].MasterCharacterID != "1" {
			t.Fatalf("rank did not persist: %v", err)
		}
	}
	afterCharacter, _, _ := repos.Character.Load(ctx, "2")
	if !reflect.DeepEqual(beforeCharacter, afterCharacter) || peers[2].conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("rank edit changed character assets or crossed guilds")
	}
}

func TestGuildMemberGradeRejectsInvalidIdentityAndTarget(t *testing.T) {
	for _, mode := range []string{"short", "trailing", "encoding", "mismatched_region_zero", "role_region_zero_member_mismatch", "member_region_zero_role_mismatch", "region_250", "region_255", "region_mismatch", "unknown_name", "self", "master", "foreign", "ordinary", "revoked", "stale_target", "stale_name", "stale_region", "wrong_account", "target_master_grade", "invalid_grade", "save_failure", "class_zero"} {
		t.Run(mode, func(t *testing.T) {
			s, peers, repos := guildMemberGradeFixture(t)
			ctx := context.Background()
			actor, body, want := peers[0], guildMemberGradeBody(1, "角色2", 2), byte(3)
			r, _ := dnfguild.Load(ctx, repos.Settings)
			switch mode {
			case "short":
				body = body[:5]
			case "trailing":
				body = append(body, 0)
			case "encoding":
				body = []byte{1, 1, 0, 0, 0, 0x81, 2}
			case "mismatched_region_zero":
				body[0] = 0
			case "role_region_zero_member_mismatch":
				body[0] = 0
				c, _, _ := repos.Character.Load(ctx, "2")
				c.Stats["server_group_id"] = 0
				if err := repos.Character.Save(ctx, c); err != nil {
					t.Fatal(err)
				}
			case "member_region_zero_role_mismatch":
				body[0] = 0
				m := r.Guilds[1].Members["2"]
				m.ServerGroup = 0
				r.Guilds[1].Members["2"] = m
			case "region_250":
				body[0] = 250
			case "region_255":
				body[0] = 255
			case "region_mismatch":
				body[0] = 2
			case "unknown_name":
				body = guildMemberGradeBody(1, "不存在", 2)
			case "self":
				body, want = guildMemberGradeBody(1, "角色1", 2), 85
			case "master":
				actor, body, want = peers[1], guildMemberGradeBody(1, "角色1", 2), 87
				r.Guilds[1].GradePermissions = map[byte]uint32{4: 1 << 11}
			case "foreign":
				body = guildMemberGradeBody(1, "角色3", 2)
			case "ordinary":
				actor, want = peers[1], 83
			case "revoked":
				r.Guilds[1].GradePermissions = map[byte]uint32{1: 0}
				want = 83
			case "stale_target":
				m := r.Guilds[1].Members["2"]
				m.AccountID = "old"
				r.Guilds[1].Members["2"] = m
			case "stale_name", "stale_region":
				m := r.Guilds[1].Members["2"]
				if mode == "stale_name" {
					m.Name = "旧名"
				} else {
					m.ServerGroup = 2
				}
				r.Guilds[1].Members["2"] = m
			case "wrong_account":
				actor.accountID, want = "wrong", 3
			case "target_master_grade":
				body[len(body)-1], want = 1, 88
			case "invalid_grade":
				body[len(body)-1], want = 5, 88
			case "save_failure":
				broken := repos
				broken.Guild = guildTextFailUoW{repos.Guild}
				s.repositoryProvider = func() (dnfrepo.Group, bool) { return broken, true }
			}
			if err := dnfguild.Save(ctx, repos.Settings, r, s.gameplayNow()); err != nil {
				t.Fatal(err)
			}
			before, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
			if mode == "class_zero" {
				if _, err := currentGameplayModules.DispatchUpper(s, actor, 126, 0, body); err != nil {
					t.Fatal(err)
				}
			} else {
				guildDispatch(t, s, actor, 126, body)
				if got := guildReply(t, actor, 126); !bytes.Equal(got, []byte{0, want}) {
					t.Fatalf("ACK=%x want=%d", got, want)
				}
			}
			after, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("rejected grade changed registry")
			}
			for _, peer := range peers {
				if peer != actor && peer.conn.(*bufferConn).write.Len() != 0 {
					t.Fatal("rejected grade refreshed peers")
				}
			}
		})
	}
}

func TestGuildMemberGradeGrantRevokeAndOfflineTarget(t *testing.T) {
	s, peers, repos := guildMemberGradeFixture(t)
	ctx := context.Background()
	r, _ := dnfguild.Load(ctx, repos.Settings)
	third := r.Guilds[2].Members["3"]
	delete(r.Guilds, 2)
	third.Grade = 4
	r.Guilds[1].Members["3"] = third
	if err := dnfguild.Save(ctx, repos.Settings, r, s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	// Grade editing uses the configurable permission, even for a regular member.
	guildDispatch(t, s, peers[0], 748, guildGradePermissionsBody(4, 1<<11))
	guildDispatch(t, s, peers[1], 126, guildMemberGradeBody(1, "角色3", 3))
	if got := guildReply(t, peers[1], 126); !bytes.Equal(got, append([]byte{1, 0}, guildMemberGradeBody(1, "角色3", 3)...)) {
		t.Fatalf("granted reply=%x", got)
	}
	guildDispatch(t, s, peers[0], 748, guildGradePermissionsBody(4, 0))
	peers[1].conn.(*bufferConn).write.Reset()
	guildDispatch(t, s, peers[1], 126, guildMemberGradeBody(1, "角色3", 2))
	if !bytes.Equal(guildReply(t, peers[1], 126), []byte{0, 83}) {
		t.Fatal("revoked bit retained")
	}
	s.mu.Lock()
	delete(s.gameSessions, 3)
	s.mu.Unlock()
	peers[2].conn.(*bufferConn).write.Reset()
	guildDispatch(t, s, peers[0], 126, guildMemberGradeBody(1, "角色3", 2))
	r, _ = dnfguild.Load(ctx, repos.Settings)
	if r.Guilds[1].Members["3"].Grade != 2 || peers[2].conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("offline member mutation or routing failed")
	}
	before, _ := dnfguild.Load(ctx, repos.Settings)
	guildDispatch(t, s, peers[0], 126, guildMemberGradeBody(1, "角色3", 2))
	after, _ := dnfguild.Load(ctx, repos.Settings)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("same grade replay changed guild")
	}
}

type guildMemberGradeHookUoW struct {
	base dnfrepo.GuildUnitOfWork
	hook func(dnfrepo.Group) error
}

func (g guildMemberGradeHookUoW) WithinGuild(ctx context.Context, owners []dnfrepo.GuildOwner, apply func(dnfrepo.Group) error) error {
	return g.base.WithinGuild(ctx, owners, func(tx dnfrepo.Group) error {
		if err := g.hook(tx); err != nil {
			return err
		}
		return apply(tx)
	})
}

func TestGuildMemberGradeRevalidatesLockedTarget(t *testing.T) {
	for _, mode := range []string{"name", "region", "account", "generation"} {
		t.Run(mode, func(t *testing.T) {
			s, peers, repos := guildMemberGradeFixture(t)
			ctx := context.Background()
			wrapped := repos
			wrapped.Guild = guildMemberGradeHookUoW{base: repos.Guild, hook: func(tx dnfrepo.Group) error {
				if mode == "generation" {
					s.mu.Lock()
					peers[0].characterGeneration++
					s.mu.Unlock()
					return nil
				}
				c, _, err := tx.Character.Load(ctx, "2")
				if err != nil {
					return err
				}
				switch mode {
				case "name":
					c.Name = "改名"
				case "region":
					c.Stats["server_group_id"] = 2
				case "account":
					c.AccountID = "other"
				}
				return tx.Character.Save(ctx, c)
			}}
			s.repositoryProvider = func() (dnfrepo.Group, bool) { return wrapped, true }
			before, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
			guildDispatch(t, s, peers[0], 126, guildMemberGradeBody(1, "角色2", 2))
			if mode == "generation" {
				if peers[0].conn.(*bufferConn).write.Len() != 0 {
					t.Fatal("old generation received ACK")
				}
			} else if !bytes.Equal(guildReply(t, peers[0], 126), []byte{0, 3}) {
				t.Fatal("changed target accepted")
			}
			after, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
			if !reflect.DeepEqual(before, after) || peers[1].conn.(*bufferConn).write.Len() != 0 {
				t.Fatal("stale mutation persisted or refreshed")
			}
		})
	}
}

type guildMemberGradeLookupCharacter struct {
	dnfrepo.CharacterRepository
	stage            string
	switchGeneration func()
}

func (c guildMemberGradeLookupCharacter) FindIDByName(ctx context.Context, name string) (string, bool, error) {
	if c.stage == "find" {
		c.switchGeneration()
		return "", false, nil
	}
	return c.CharacterRepository.FindIDByName(ctx, name)
}

func (c guildMemberGradeLookupCharacter) Load(ctx context.Context, id string) (dnfrepo.CharacterRecord, bool, error) {
	if c.stage == "load" && id == "2" {
		c.switchGeneration()
		return dnfrepo.CharacterRecord{}, false, nil
	}
	return c.CharacterRepository.Load(ctx, id)
}

func TestGuildMemberGradeLookupFailureDoesNotReplyToNewGeneration(t *testing.T) {
	for _, stage := range []string{"find", "load"} {
		t.Run(stage, func(t *testing.T) {
			s, peers, repos := guildMemberGradeFixture(t)
			wrapped := repos
			wrapped.Character = guildMemberGradeLookupCharacter{repos.Character, stage, func() {
				s.mu.Lock()
				peers[0].characterGeneration++
				s.mu.Unlock()
			}}
			s.repositoryProvider = func() (dnfrepo.Group, bool) { return wrapped, true }
			ctx := context.Background()
			before, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
			guildDispatch(t, s, peers[0], 126, guildMemberGradeBody(1, "角色2", 2))
			for _, peer := range peers {
				if peer.conn.(*bufferConn).write.Len() != 0 {
					t.Fatal("lookup failure replied after generation changed")
				}
			}
			after, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("failed lookup mutated guild")
			}
		})
	}
}

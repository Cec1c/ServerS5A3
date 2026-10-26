package guild

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestGuildMemberRemovalOwnedSelfAndReplay(t *testing.T) {
	reg, g, master, member := textRegistry(t)
	g.GradePermissions = map[byte]uint32{4: 0}
	g.Experience, g.Mileage = 99, 123
	before := g.Members[member.CharacterID]
	now := g.UpdatedAt.Add(time.Hour)
	removed, err := reg.RemoveOrdinaryMember(member, member, now)
	if err != nil || !removed.Voluntary || removed.GuildID != g.ID || removed.GuildName != g.Name || !reflect.DeepEqual(removed.Member, before) {
		t.Fatalf("removal=%+v error=%v", removed, err)
	}
	if len(g.Members) != 1 || g.Members[master.CharacterID].Grade != 1 || g.MasterCharacterID != master.CharacterID || g.Experience != 99 || g.Mileage != 123 || !g.UpdatedAt.Equal(now) {
		t.Fatal("self-exit changed the master or guild assets")
	}
	baseline, _ := json.Marshal(reg)
	if _, err := reg.RemoveOrdinaryMember(member, member, now.Add(time.Hour)); !errors.Is(err, ErrNotMember) {
		t.Fatalf("repeated removal=%v", err)
	}
	after, _ := json.Marshal(reg)
	if string(baseline) != string(after) {
		t.Fatal("repeated removal changed registry")
	}
}

func TestGuildMemberRemovalPermissionAndRank(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		actorGrade, targetGrade byte
		custom                  bool
		mask                    uint32
		want                    error
	}{
		{"master_to_deputy", 1, 2, false, 0, nil},
		{"master_to_member", 1, 4, false, 0, nil},
		{"master_revoked", 1, 4, true, 0, ErrPermissionDenied},
		{"default_deputy", 2, 4, false, 0, nil},
		{"deputy_to_equal", 2, 2, false, 0, ErrPermissionDenied},
		{"excellent_granted", 3, 4, true, 1 << 10, nil},
		{"excellent_revoked", 3, 4, true, 0, ErrPermissionDenied},
		{"excellent_to_deputy", 3, 2, true, GradePermissionMask, ErrPermissionDenied},
		{"ordinary_to_equal", 4, 4, true, GradePermissionMask, ErrPermissionDenied},
		{"ordinary_to_excellent", 4, 3, true, GradePermissionMask, ErrPermissionDenied},
		{"rank_bit_not_kick_bit", 3, 4, true, 1 << 11, ErrPermissionDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg, g, master, member := textRegistry(t)
			third := r.CharacterRecord{CharacterID: "3", AccountID: "a3", Name: "三号"}
			if _, err := reg.JoinDirect(g.ID, third, g.UpdatedAt); err != nil {
				t.Fatal(err)
			}
			actor := master
			if tc.actorGrade != 1 {
				actor = r.GuildOwner{CharacterID: "3", AccountID: "a3"}
				m := g.Members["3"]
				m.Grade = tc.actorGrade
				g.Members["3"] = m
			}
			m := g.Members[member.CharacterID]
			m.Grade = tc.targetGrade
			g.Members[member.CharacterID] = m
			if tc.custom {
				g.GradePermissions = map[byte]uint32{tc.actorGrade: tc.mask}
			}
			before, _ := json.Marshal(reg)
			removed, err := reg.RemoveOrdinaryMember(actor, member, g.UpdatedAt.Add(time.Hour))
			if !errors.Is(err, tc.want) {
				t.Fatalf("error=%v want=%v", err, tc.want)
			}
			if err != nil {
				after, _ := json.Marshal(reg)
				if string(before) != string(after) || removed != (MemberRemoval{}) {
					t.Fatal("denied kick changed state or returned a removal")
				}
			} else if removed.Voluntary || removed.Member.CharacterID != member.CharacterID || len(g.Members) != 2 {
				t.Fatalf("kick result=%+v", removed)
			}
		})
	}
}

func TestGuildMemberRemovalRejectsStaleOwnerAndMaster(t *testing.T) {
	for _, mode := range []string{"actor_account", "target_account", "nonmember", "foreign_guild", "master_self", "master_target", "sole_master", "invalid_target_grade", "invalid_actor_grade", "forged_master_grade"} {
		t.Run(mode, func(t *testing.T) {
			reg, g, master, member := textRegistry(t)
			actor, target, want := master, member, ErrNotMember
			switch mode {
			case "actor_account":
				actor.AccountID = "old"
			case "target_account":
				target.AccountID = "old"
			case "nonmember":
				target.CharacterID = "999"
			case "foreign_guild":
				c := r.CharacterRecord{CharacterID: "3", AccountID: "a3", Name: "三号"}
				if _, err := reg.Create(c, "别的公会", g.UpdatedAt); err != nil {
					t.Fatal(err)
				}
				actor = r.GuildOwner{CharacterID: c.CharacterID, AccountID: c.AccountID}
			case "master_self", "sole_master":
				target, want = master, ErrMemberRemovalMaster
				if mode == "sole_master" {
					delete(g.Members, member.CharacterID)
				}
			case "master_target":
				actor, target, want = member, master, ErrMemberRemovalMaster
			case "invalid_target_grade":
				m := g.Members[member.CharacterID]
				m.Grade = 5
				g.Members[member.CharacterID], want = m, ErrInvalidRegistry
			case "invalid_actor_grade", "forged_master_grade":
				c := r.CharacterRecord{CharacterID: "3", AccountID: "a3", Name: "三号"}
				if _, err := reg.JoinDirect(g.ID, c, g.UpdatedAt); err != nil {
					t.Fatal(err)
				}
				actor = r.GuildOwner{CharacterID: "3", AccountID: "a3"}
				m := g.Members["3"]
				m.Grade = 5
				if mode == "forged_master_grade" {
					m.Grade = 1
				}
				g.Members["3"], want = m, ErrInvalidRegistry
			}
			before, _ := json.Marshal(reg)
			if _, err := reg.RemoveOrdinaryMember(actor, target, g.UpdatedAt.Add(time.Hour)); !errors.Is(err, want) {
				t.Fatalf("error=%v want=%v", err, want)
			}
			after, _ := json.Marshal(reg)
			if string(before) != string(after) {
				t.Fatal("invalid identity changed registry")
			}
		})
	}
}

func TestGuildMemberRemovalUoWRollbackAndConcurrentLeaveKick(t *testing.T) {
	ctx, repos := context.Background(), guildTestGroup(t, 2)
	reg, g, master, member := textRegistry(t)
	now := g.UpdatedAt.Add(time.Hour)
	if err := Save(ctx, repos.Settings, reg, g.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	beforeSettings, _, _ := repos.Settings.Load(ctx, RegistryScope)
	beforeCharacter, _, _ := repos.Character.Load(ctx, member.CharacterID)
	remove := func(actor r.GuildOwner, uow r.GuildUnitOfWork) error {
		owners := []r.GuildOwner{actor}
		if actor != member {
			owners = append(owners, member)
		}
		return Update(ctx, uow, owners, now, func(_ r.Group, current *Registry) error {
			_, err := current.RemoveOrdinaryMember(actor, member, now)
			return err
		})
	}
	boom := errors.New("failure after removal save")
	if err := remove(master, lateFailGuildTx{base: repos.Guild, fail: boom}); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	afterFailure, _, _ := repos.Settings.Load(ctx, RegistryScope)
	if !reflect.DeepEqual(beforeSettings, afterFailure) {
		t.Fatal("failed UoW persisted removal")
	}
	var wg sync.WaitGroup
	errorsByRequest := make([]error, 2)
	wg.Go(func() { errorsByRequest[0] = remove(master, repos.Guild) })
	wg.Go(func() { errorsByRequest[1] = remove(member, repos.Guild) })
	wg.Wait()
	wins := 0
	for _, err := range errorsByRequest {
		if err == nil {
			wins++
		} else if !errors.Is(err, ErrNotMember) {
			t.Fatal(err)
		}
	}
	if wins != 1 {
		t.Fatalf("committed removals=%d", wins)
	}
	loaded, err := Load(ctx, repos.Settings)
	if err != nil || len(loaded.Guilds[g.ID].Members) != 1 {
		t.Fatal("concurrent requests lost the master or kept the target")
	}
	afterCharacter, _, _ := repos.Character.Load(ctx, member.CharacterID)
	if !reflect.DeepEqual(beforeCharacter, afterCharacter) {
		t.Fatal("membership stage changed character assets")
	}
}

func TestGuildMemberRemovalUoWRechecksCharacterOwnership(t *testing.T) {
	ctx, repos := context.Background(), guildTestGroup(t, 2)
	reg, g, master, member := textRegistry(t)
	if err := Save(ctx, repos.Settings, reg, g.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	member.AccountID = "old-account"
	err := Update(ctx, repos.Guild, []r.GuildOwner{master, member}, g.UpdatedAt, func(_ r.Group, current *Registry) error {
		_, err := current.RemoveOrdinaryMember(master, member, g.UpdatedAt)
		return err
	})
	if !errors.Is(err, r.ErrGuildTransaction) {
		t.Fatalf("stale owner acquired guild transaction: %v", err)
	}
	loaded, err := Load(ctx, repos.Settings)
	if err != nil || len(loaded.Guilds[g.ID].Members) != 2 {
		t.Fatal("stale owner removal persisted")
	}
}

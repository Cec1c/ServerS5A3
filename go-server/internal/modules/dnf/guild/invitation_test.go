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

func TestGuildInvitationEligibilityAndPermissions(t *testing.T) {
	for _, mode := range []string{"master", "granted", "revoked", "deputy", "wrong_owner", "wrong_guild", "already_member", "full", "missing_identity", "invalid_region"} {
		t.Run(mode, func(t *testing.T) {
			reg, g, master, member := textRegistry(t)
			actor := master
			target := r.CharacterRecord{CharacterID: "3", AccountID: "a3", Name: "新会员"}
			id := g.ID
			var want error
			switch mode {
			case "granted":
				actor = member
				g.GradePermissions = map[byte]uint32{4: InvitePermission}
			case "revoked":
				g.GradePermissions = map[byte]uint32{1: 0}
				want = ErrPermissionDenied
			case "deputy":
				actor = member
				m := g.Members["2"]
				m.Grade = 2
				g.Members["2"] = m
				want = ErrPermissionDenied
			case "wrong_owner":
				actor.AccountID = "other"
				want = ErrNotMember
			case "wrong_guild":
				id++
				want = ErrNotMember
			case "already_member":
				target.CharacterID = "2"
				want = ErrAlreadyMember
			case "full":
				g.MemberLimit = len(g.Members)
				want = ErrMemberLimit
			case "missing_identity":
				target.AccountID = ""
				want = ErrCharacterNotFound
			case "invalid_region":
				target.Stats = map[string]int64{"server_group_id": 250}
				want = ErrCharacterNotFound
			}
			before, _ := json.Marshal(reg)
			_, err := reg.CheckInvitation(id, actor, target)
			if !errors.Is(err, want) {
				t.Fatalf("err=%v want=%v", err, want)
			}
			after, _ := json.Marshal(reg)
			if string(before) != string(after) {
				t.Fatal("advisory check changed registry")
			}
		})
	}
}

func TestGuildInvitationAtomicJoinAndRollback(t *testing.T) {
	ctx, repos := context.Background(), guildTestGroup(t, 3)
	reg, g, master, _ := textRegistry(t)
	target, _, _ := repos.Character.Load(ctx, "3")
	if err := reg.Apply(g.ID, target, "申请", g.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	if err := Save(ctx, repos.Settings, reg, g.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	before, _, _ := repos.Settings.Load(ctx, RegistryScope)
	now := g.UpdatedAt.Add(time.Minute)
	join := func(uow r.GuildUnitOfWork) error {
		return Update(ctx, uow, []r.GuildOwner{master, {CharacterID: target.CharacterID, AccountID: target.AccountID}}, now, func(tx r.Group, current *Registry) error {
			c, _, err := tx.Character.Load(ctx, target.CharacterID)
			if err != nil {
				return err
			}
			_, err = current.AcceptInvitation(g.ID, master, c, now)
			return err
		})
	}
	boom := errors.New("late save failure")
	if err := join(lateFailGuildTx{base: repos.Guild, fail: boom}); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	after, _, _ := repos.Settings.Load(ctx, RegistryScope)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed join changed membership/application")
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Go(func() { errs[i] = join(repos.Guild) })
	}
	wg.Wait()
	if (errs[0] == nil) == (errs[1] == nil) || (errs[0] != nil && !errors.Is(errs[0], ErrAlreadyMember)) || (errs[1] != nil && !errors.Is(errs[1], ErrAlreadyMember)) {
		t.Fatalf("concurrent joins=%v", errs)
	}
	loaded, err := Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	m := loaded.Guilds[g.ID].Members["3"]
	if m.Grade != DefaultMemberGrade || !m.JoinedAt.Equal(now) || len(loaded.Guilds[g.ID].Applications) != 0 || len(loaded.Guilds[g.ID].Members) != 3 {
		t.Fatal("join did not atomically remove applications")
	}
	afterCharacter, _, _ := repos.Character.Load(ctx, "3")
	if !reflect.DeepEqual(target, afterCharacter) {
		t.Fatal("join changed character assets")
	}
}

package guild

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/itemexpiration"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestGuildLifecycleDeparturePersistsAndRestrictsAllAdmissionPaths(t *testing.T) {
	for _, hour := range []int{5, 6, 23} {
		reg, g, master, owner := textRegistry(t)
		now := time.Date(2026, 10, 25, hour, 59, 59, 0, time.FixedZone("CST", 8*3600))
		removed, err := reg.DepartMember(owner, owner, now)
		if err != nil || !removed.Voluntary {
			t.Fatalf("departure=%+v err=%v", removed, err)
		}
		ctx, repos := context.Background(), guildTestGroup(t, 2)
		if err := Save(ctx, repos.Settings, reg, now); err != nil {
			t.Fatal(err)
		}
		reg, err = Load(ctx, repos.Settings)
		if err != nil {
			t.Fatal(err)
		}
		deadline := itemexpiration.NextDailyDeleteAt(now)
		if d := reg.Departures[owner.CharacterID]; d.Owner != owner || !d.Until.Equal(deadline) {
			t.Fatalf("departure=%+v", d)
		}
		character := r.CharacterRecord{CharacterID: owner.CharacterID, AccountID: owner.AccountID, Name: removed.Member.Name}
		before := deadline.Add(-time.Nanosecond)
		if _, err = reg.Create(character, "新公会", before); !errors.Is(err, ErrDepartureCooldown) {
			t.Fatal("create", err)
		}
		if err = reg.Apply(g.ID, character, "申请", before); !errors.Is(err, ErrDepartureCooldown) {
			t.Fatal("apply", err)
		}
		if _, err = reg.JoinDirect(g.ID, character, before); !errors.Is(err, ErrDepartureCooldown) {
			t.Fatal("join", err)
		}
		if _, err = reg.AcceptInvitation(g.ID, master, character, before); !errors.Is(err, ErrDepartureCooldown) {
			t.Fatal("invitation", err)
		}
		g = reg.Guilds[g.ID]
		g.Applications[owner.CharacterID] = Application{CharacterID: owner.CharacterID, AccountID: owner.AccountID, Name: character.Name}
		if _, err = reg.DecideApplication(g.ID, master.CharacterID, owner.CharacterID, true, before); !errors.Is(err, ErrDepartureCooldown) {
			t.Fatal("approve", err)
		}
		// Other characters on the account remain eligible.
		other := character
		other.CharacterID, other.Name = "99", "同账号角色"
		if _, err = reg.JoinDirect(g.ID, other, before); err != nil {
			t.Fatal("account-wide restriction", err)
		}
		if _, err = reg.JoinDirect(g.ID, character, deadline); err != nil {
			t.Fatal("exact 06:00 boundary", err)
		}
	}
}

func TestGuildLifecycleDisbandAuthorizationAndNoIDReuse(t *testing.T) {
	reg, g, master, member := textRegistry(t)
	now := g.UpdatedAt.Add(time.Hour)
	before, _ := json.Marshal(reg)
	if _, err := reg.Disband(member, now); !errors.Is(err, ErrPermissionDenied) {
		t.Fatal(err)
	}
	if _, err := reg.Disband(master, now); !errors.Is(err, ErrDisbandHasMembers) {
		t.Fatal(err)
	}
	if _, err := reg.DepartMember(master, master, now); !errors.Is(err, ErrDisbandHasMembers) {
		t.Fatal(err)
	}
	after, _ := json.Marshal(reg)
	if string(before) != string(after) {
		t.Fatal("rejected disband changed state")
	}
	if _, err := reg.DepartMember(master, member, now); err != nil {
		t.Fatal(err)
	}
	if _, found := reg.Departures[member.CharacterID]; found {
		t.Fatal("kick created voluntary cooldown")
	}
	oldNext := reg.NextID
	result, err := reg.DepartMember(master, master, now)
	if err != nil || result.GuildID != g.ID || !result.Voluntary || len(reg.Guilds) != 0 || reg.NextID != oldNext {
		t.Fatalf("disband=%+v err=%v", result, err)
	}
	before, _ = json.Marshal(reg)
	if _, err := reg.Disband(master, now.Add(time.Minute)); !errors.Is(err, ErrNotMember) {
		t.Fatal(err)
	}
	after, _ = json.Marshal(reg)
	if string(before) != string(after) {
		t.Fatal("replay changed deadline or IDs")
	}
	created, err := reg.Create(r.CharacterRecord{CharacterID: member.CharacterID, AccountID: member.AccountID, Name: "会员"}, g.Name, now)
	if err != nil || created.ID != oldNext {
		t.Fatalf("new guild=%+v err=%v", created, err)
	}
}

func TestGuildLifecycleUoWRollbackIncludesCooldown(t *testing.T) {
	ctx, repos := context.Background(), guildTestGroup(t, 2)
	reg, g, _, owner := textRegistry(t)
	if err := Save(ctx, repos.Settings, reg, g.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	before, _, _ := repos.Settings.Load(ctx, RegistryScope)
	boom := errors.New("fail after save")
	err := Update(ctx, lateFailGuildTx{base: repos.Guild, fail: boom}, []r.GuildOwner{owner}, g.UpdatedAt.Add(time.Hour), func(_ r.Group, current *Registry) error {
		_, err := current.DepartMember(owner, owner, g.UpdatedAt.Add(time.Hour))
		return err
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	after, _, _ := repos.Settings.Load(ctx, RegistryScope)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed transaction persisted removal or cooldown")
	}
}

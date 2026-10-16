package guild

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestGuildCargoQueryRevalidatesAndReturnsDetachedRead(t *testing.T) {
	ctx := context.Background()
	repos := guildTestGroup(t, 1)
	req := guildRequest(1, 0, "查询公会", 72)
	req.Founder = r.GuildOwner{}
	created, err := CreateSolo(ctx, repos.Guild, req, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	reg, _ := Load(ctx, repos.Settings)
	g := reg.Guilds[created.Guild.ID]
	g.Cargo = &Cargo{Capacity: 3, Slots: map[uint16]r.ItemStack{1: {ItemID: 100, Count: 2, Extra: map[string]string{"marker": "before"}}}}
	if err = Save(ctx, repos.Settings, reg, time.Now()); err != nil {
		t.Fatal(err)
	}
	before, _, _ := repos.Settings.Load(ctx, r.GuildRegistryScope)
	query := CargoQuery{Owner: req.Creator, Revalidate: func() bool { return true }}
	view, err := ReadCargo(ctx, repos.Guild, query)
	if err != nil || view.GuildID != g.ID || view.Cargo.Slots[1].Count != 2 {
		t.Fatal(view, err)
	}
	view.Cargo.Slots[1].Extra["marker"] = "changed"
	delete(view.Cargo.Slots, 1)
	after, _, _ := repos.Settings.Load(ctx, r.GuildRegistryScope)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("query mutated durable registry")
	}
	calls := 0
	query.Revalidate = func() bool { calls++; return calls == 1 }
	if got, err := ReadCargo(ctx, repos.Guild, query); !errors.Is(err, ErrPermissionDenied) || got.Cargo != nil {
		t.Fatal("stale view returned", err)
	}
	query.Revalidate = func() bool { return true }
	for _, mode := range []string{"permission", "capacity", "member_account", "wrong_account"} {
		t.Run(mode, func(t *testing.T) {
			record, _ := Load(ctx, repos.Settings)
			guild := record.Guilds[g.ID]
			q := query
			switch mode {
			case "permission":
				guild.GradePermissions = map[byte]uint32{1: 0}
			case "capacity":
				guild.Cargo = nil
			case "member_account":
				m := guild.Members[req.Creator.CharacterID]
				m.AccountID = "different"
				guild.Members[m.CharacterID] = m
			case "wrong_account":
				q.Owner.AccountID = "different"
			}
			if err := Save(ctx, repos.Settings, record, time.Now()); err != nil {
				t.Fatal(err)
			}
			if got, err := ReadCargo(ctx, repos.Guild, q); err == nil || got.Cargo != nil {
				t.Fatal("unauthorized view returned")
			}
			if err := repos.Settings.Save(ctx, before); err != nil {
				t.Fatal(err)
			}
		})
	}
}

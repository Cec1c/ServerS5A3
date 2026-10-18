package guild

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestGuildCargoPersistenceRejectsInvalidSlotsWithoutDefaultCapacity(t *testing.T) {
	ctx := context.Background()
	repos := guildTestGroup(t, 1)
	req := guildRequest(1, 0, "仓库公会", 71)
	req.Founder = r.GuildOwner{}
	created, err := CreateSolo(ctx, repos.Guild, req, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	reg, err := Load(ctx, repos.Settings)
	if err != nil || reg.Guilds[created.Guild.ID].Cargo != nil {
		t.Fatalf("invented capacity: %v", err)
	}
	for _, cargo := range []*Cargo{
		{Capacity: math.MaxUint16 + 2},
		{Capacity: 3, Slots: map[uint16]r.ItemStack{3: {ItemID: 1, Count: 1}}},
		{Capacity: 3, Slots: map[uint16]r.ItemStack{1: {ItemID: 1, Count: 0}}},
		{Capacity: 3, Slots: map[uint16]r.ItemStack{1: {ItemID: 0, Count: 1}}},
	} {
		reg.Guilds[created.Guild.ID].Cargo = cargo
		if err = Save(ctx, repos.Settings, reg, time.Now()); !errors.Is(err, ErrInvalidRegistry) {
			t.Fatalf("invalid cargo saved: %+v %v", cargo, err)
		}
		loaded, err := Load(ctx, repos.Settings)
		if err != nil || loaded.Guilds[created.Guild.ID].Cargo != nil {
			t.Fatalf("invalid save changed state: %v", err)
		}
	}
}

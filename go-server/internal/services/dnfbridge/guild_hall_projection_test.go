package dnfbridge

import (
	"bytes"
	"context"
	"testing"

	"longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestGuildHallProjectionPreservesNativePackets(t *testing.T) {
	s, _, repos, _ := guildMerchantFixture(t)
	ctx := context.Background()
	reg, err := guild.Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	g := reg.Guilds[1]
	g.Essence = guild.EssenceBalance{AP: 123, GP: 456}
	g.Guardians = &guild.GuardianState{EverAcquired: true, ActiveID: 2, Owned: []guild.Guardian{{ID: 1, Level: 10, Skills: [5]uint8{1, 2, 3, 4, 5}, APProgress: 18}, {ID: 2, Level: 21, GPProgress: 604}}}
	g.Merchants = &guild.MerchantState{Levels: [2]uint8{3, 4}}
	g.Placements = &guild.PlacementState{NextID: 2, Rows: []guild.Placement{{ID: 1, ItemID: 1, ObjectID: 2, CreatorID: 29, Area: 1, X: 10, Y: 20, Maintenance: 7, Cells: []guild.PlacementCell{{X: 10, Y: 20}}, CreatedAt: s.gameplayNow(), ReadyAt: s.gameplayNow()}}}
	if err := guild.Save(ctx, repos.Settings, reg, s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	view, err := guild.ReadHallView(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	member := g.Members["29"]
	projection, found := view.Projection(r.GuildOwner{CharacterID: "29", AccountID: member.AccountID})
	if !found {
		t.Fatal("missing member")
	}
	detached := currentGuildHallDisplay(projection)
	for _, test := range []struct {
		name  string
		build func(*guild.Guild) ([]byte, error)
	}{{"1297", buildCurrentGuildGuardianSnapshot}, {"191", buildCurrentGuildPlacementSnapshot}} {
		t.Run(test.name, func(t *testing.T) {
			want, e1 := test.build(g)
			got, e2 := test.build(detached)
			if e1 != nil || e2 != nil || !bytes.Equal(want, got) {
				t.Fatal("packet changed", e1, e2)
			}
		})
	}
	if detached.Members != nil || detached.Cargo != nil || detached.Dungeon != nil || detached.MileageHistory != nil {
		t.Fatal("hall projection included unrelated history")
	}
}

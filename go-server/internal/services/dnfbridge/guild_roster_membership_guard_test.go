package dnfbridge

import (
	"context"
	"testing"
	"time"

	dnfguild "longheng.io/server/internal/modules/dnf/guild"
)

// A roster sweep may wait on the viewer transport after its original snapshot.
// Neither a departing viewer nor an actor that left/rejoined may reuse that
// snapshot to authorize a lightweight profile frame.
func TestGuildRosterPrimeRejectsMembershipChangedAfterSnapshot(t *testing.T) {
	for _, id := range []string{"17", "29"} {
		for _, rejoin := range []bool{false, true} {
			t.Run(id+map[bool]string{false: "/leave", true: "/rejoin"}[rejoin], func(t *testing.T) {
				f, repos := guildMemberProfileFixture(t)
				ctx := context.Background()
				a, err := f.service.loadCurrentGuildMemberProfileAccess(ctx, f.target, currentGuildMemberProfileRequest{channel: 11, character: 17, directory: 1})
				if err != nil {
					t.Fatal(err)
				}
				reg, err := dnfguild.Load(ctx, repos.Settings)
				if err != nil {
					t.Fatal(err)
				}
				g, _, ok := reg.GuildForCharacter(id)
				if !ok {
					t.Fatalf("fixture member %s missing", id)
				}
				member := g.Members[id]
				if id == "29" && !rejoin {
					replacement := g.Members["17"]
					replacement.Grade = dnfguild.DefaultMasterGrade
					g.Members["17"] = replacement
					g.MasterCharacterID, g.MasterName = "17", replacement.Name
				}
				delete(g.Members, id)
				if rejoin {
					member.JoinedAt = member.JoinedAt.Add(time.Second)
					g.Members[id] = member
				}
				if err := dnfguild.Save(ctx, repos.Settings, reg, f.service.gameplayNow()); err != nil {
					t.Fatal(err)
				}
				if err := f.service.validateCurrentGuildRosterPrimeActorAccess(ctx, a); err == nil {
					t.Fatal("stale roster membership authorized a profile frame")
				}
			})
		}
	}
}

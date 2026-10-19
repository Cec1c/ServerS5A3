package dnfbridge

import (
	"context"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	dnfpvp "longheng.io/server/internal/modules/dnf/pvp"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestPVPTotalTeamSelectionWaitsForSceneActor(t *testing.T) {
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	const account = "pvp-team-selector"
	if err := repos.Account.Save(ctx, repo.AccountRecord{AccountID: account}); err != nil {
		t.Fatal(err)
	}
	team := dnfpvp.TotalTeam{Name: "12", Characters: [3]string{"3", "16", "13"}}
	for slot, id := range team.Characters {
		if err := repos.Character.Save(ctx, repo.CharacterRecord{CharacterID: id, AccountID: account, Slot: slot}); err != nil {
			t.Fatal(err)
		}
	}
	if err := dnfpvp.SaveTotalTeam(ctx, repos.Account, account, team, time.Now()); err != nil {
		t.Fatal(err)
	}
	s := &Service{repositoryProvider: func() (repo.Group, bool) { return repos, true }}
	p := &gameSession{conn: &bufferConn{}, accountID: account, channel: channelcatalog.Channel{ID: 75, Type: 8, Group: "pvp"}}
	// The saved-team packet dereferences the scene actor at 1D589DF/1D589E6.
	// A populated selector roster alone does not supply that actor.
	if err := s.sendCurrentPVPTotalTeamSelection(p); err != nil {
		t.Fatal(err)
	}
	if packets := totalTestPackets(t, p); len(packets) != 0 {
		t.Fatalf("actor-dependent team state sent in character selector: %v", packets)
	}
	stored, err := dnfpvp.ReadTotalTeam(ctx, repos, account)
	if err != nil || stored.Team != team {
		t.Fatalf("deferral lost persisted team: %+v, %v", stored.Team, err)
	}
}

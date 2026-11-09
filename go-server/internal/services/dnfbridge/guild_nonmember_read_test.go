package dnfbridge

import (
	"context"
	"errors"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

type nonmemberCharacterReadGuard struct {
	r.CharacterRepository
	loads int
}

func (g *nonmemberCharacterReadGuard) Load(context.Context, string) (r.CharacterRecord, bool, error) {
	g.loads++
	return r.CharacterRecord{}, false, errors.New("character read must be skipped for a nonmember")
}

func TestCurrentGuildNonMemberSkipsCharacterReads(t *testing.T) {
	group := memory.NewMemoryGroup()
	guard := &nonmemberCharacterReadGuard{CharacterRepository: group.Character}
	group.Character = guard
	service := &Service{repositoryProvider: func() (r.Group, bool) { return group, true }}
	session := &gameSession{accountID: "account-1", selectedCharacterID: 99}
	g, err := service.currentGuildForSelectedCharacter(session)
	if err != nil || g != nil {
		t.Fatalf("nonmember display=%v err=%v", g, err)
	}
	scene, err := service.currentSceneGuild(context.Background(), 99)
	if err != nil || scene != (currentSceneGuildProjection{}) {
		t.Fatalf("nonmember scene=%v err=%v", scene, err)
	}
	if guard.loads != 0 {
		t.Fatalf("nonmember character loads=%d", guard.loads)
	}
}

func TestCurrentGuildMemberStillValidatesAuthoritativeIdentity(t *testing.T) {
	ctx := context.Background()
	group := memory.NewMemoryGroup()
	reg := guild.NewRegistry()
	claimed := r.CharacterRecord{CharacterID: "99", AccountID: "old-owner", Name: "Member", Level: 90, Stats: map[string]int64{"server_group_id": 1}}
	if _, err := reg.Create(claimed, "Guild", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := guild.Save(ctx, group.Settings, reg, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	actual := claimed
	actual.AccountID = "new-owner"
	if err := group.Character.Save(ctx, actual); err != nil {
		t.Fatal(err)
	}
	service := &Service{repositoryProvider: func() (r.Group, bool) { return group, true }}
	stale := &gameSession{accountID: "old-owner", selectedCharacterID: 99}
	service.gameSessions = map[uint16]*gameSession{99: stale}
	if g, err := service.currentGuildForSelectedCharacter(stale); err == nil || g != nil {
		t.Fatalf("stale session inherited guild: %v err=%v", g, err)
	}
	current := &gameSession{accountID: "new-owner", selectedCharacterID: 99}
	service.gameSessions[99] = current
	if g, err := service.currentGuildForSelectedCharacter(current); err != nil || g != nil {
		t.Fatalf("replacement character inherited old membership: %v err=%v", g, err)
	}
	if g, err := service.currentSceneGuild(ctx, 99); err != nil || g != (currentSceneGuildProjection{}) {
		t.Fatalf("replacement character inherited scene guild: %v err=%v", g, err)
	}
	if err := group.Character.Save(ctx, claimed); err != nil {
		t.Fatal(err)
	}
	service.gameSessions[99] = stale
	if g, err := service.currentGuildForSelectedCharacter(stale); err != nil || g == nil || g.ID != 1 {
		t.Fatalf("matching authoritative owner lost guild: %v err=%v", g, err)
	}
	if g, err := service.currentSceneGuild(ctx, 99); err != nil || g.id != 1 {
		t.Fatalf("matching authoritative owner lost scene guild: %v err=%v", g, err)
	}
}

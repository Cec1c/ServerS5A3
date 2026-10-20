package dnfbridge

import (
	"bytes"
	"context"
	"longheng.io/server/internal/modules/dnf/buffswap"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
)

func TestNativeBuffSwapUnregisteredActorProjectsEmptyWithoutPersisting(t *testing.T) {
	t.Setenv(buffSwapReapplyEnv, "on")
	r := newSkillSwitchInventoryRepos(t)
	ctx := context.Background()
	if err := buffswap.Apply(ctx, r, "1", "dnf:10001", buffswap.Command{Skill: 38, Slot: 33, List: 44, Index: 0xffff}); err != nil {
		t.Fatal(err)
	}
	if err := r.Character.Save(ctx, repo.CharacterRecord{CharacterID: "2", AccountID: "dnf:10001"}); err != nil {
		t.Fatal(err)
	}
	s, session, conn, _, _ := newSkillSwitchInventoryService(t, skillSwitchInventoryShapeGuardTier, r)
	if err := s.sendBuffSwapReapply(session, "registered_actor"); err != nil {
		t.Fatal(err)
	}
	previous := drainUpperPackets(t, conn)
	if len(previous) != 1 {
		t.Fatalf("registered actor packets=%v", previous)
	}
	session.selectedCharacterID = 2
	if err := s.sendBuffSwapReapply(session, "new_actor"); err != nil {
		t.Fatal(err)
	}
	got := drainUpperPackets(t, conn)
	if len(got) != 1 || got[0].msgID != 1368 || !bytes.Equal(got[0].body, nativeBuffSwapSnapshotBody(buffswap.State{WireProfile: buffswap.CurrentWireProfile})) {
		t.Fatalf("unregistered actor must replace prior registration with empty projection: packets=%v", got)
	}
	if _, found, err := r.Settings.Load(ctx, buffswap.Scope("2")); err != nil || found {
		t.Fatalf("empty projection wrote registration: found=%v err=%v", found, err)
	}
	old, found, err := buffswap.Load(ctx, r, "1", "dnf:10001")
	if err != nil || !found || old.Skill != 38 {
		t.Fatalf("previous character altered: %+v found=%v err=%v", old, found, err)
	}
}

func TestNativeBuffSwapProjectionRejectsUnknownOwnerAndCorruptState(t *testing.T) {
	t.Setenv(buffSwapReapplyEnv, "on")
	for _, kind := range []string{"missing_character", "wrong_owner", "corrupt_state"} {
		t.Run(kind, func(t *testing.T) {
			r := newSkillSwitchInventoryRepos(t)
			s, session, conn, _, _ := newSkillSwitchInventoryService(t, skillSwitchInventoryShapeGuardTier, r)
			switch kind {
			case "missing_character":
				session.selectedCharacterID = 999
			case "wrong_owner":
				session.accountID = "other-account"
			case "corrupt_state":
				if err := r.Settings.Save(context.Background(), repo.SettingsRecord{Scope: buffswap.Scope("1"), Values: map[string]string{"state": "broken"}}); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.sendBuffSwapReapply(session, kind); err != nil {
				t.Fatal(err)
			}
			if conn.write.Len() != 0 {
				t.Fatal("failed Load must not erase runtime state")
			}
		})
	}
}

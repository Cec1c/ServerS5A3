package dnfbridge

import (
	"context"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

type guildProfileValidationReads struct {
	dnfrepo.CharacterRepository
	full, narrow, identity int
}

func (r *guildProfileValidationReads) Load(ctx context.Context, id string) (dnfrepo.CharacterRecord, bool, error) {
	r.full++
	return r.CharacterRepository.Load(ctx, id)
}

// Preserve the narrow interface used by the MySQL store and scene guild badge.
func (r *guildProfileValidationReads) LoadCharacterIdentity(ctx context.Context, id string) (dnfrepo.CharacterIdentity, bool, error) {
	r.identity++
	return dnfrepo.LoadCharacterIdentity(ctx, r.CharacterRepository, id)
}

func (r *guildProfileValidationReads) LoadCharacterMirrorStats(ctx context.Context, id string, keys ...string) (dnfrepo.CharacterMirrorStats, bool, error) {
	r.narrow++
	return dnfrepo.LoadCharacterMirrorStats(ctx, r.CharacterRepository, id, keys...)
}

func TestGuildProfileValidationUsesNarrowFreshOwnershipReads(t *testing.T) {
	f, repos := guildMemberProfileFixture(t)
	r := &guildProfileValidationReads{CharacterRepository: repos.Character}
	repos.Character = r
	f.service.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	ctx := context.Background()
	a, err := f.service.loadCurrentGuildMemberProfileAccess(ctx, f.target, currentGuildMemberProfileRequest{channel: 11, character: 17, directory: 1})
	if err != nil {
		t.Fatal(err)
	}
	if r.full != 0 || r.narrow != 2 {
		t.Fatalf("access check read full=%d narrow=%d, want full=0 narrow=2", r.full, r.narrow)
	}
	if delivered, _, err := f.service.sendCurrentGuildMemberProfileWithPolicyDelivered(ctx, a, true); err != nil || !delivered {
		t.Fatalf("guild cache projection delivered=%t err=%v", delivered, err)
	}
	if r.full != 1 || r.narrow != 6 || r.identity != 1 {
		t.Fatalf("guild projection reads full=%d narrow=%d identity=%d, want 1/6/1", r.full, r.narrow, r.identity)
	}
	// A deletion after preparation must still be observed by the send-time check.
	c, _, err := r.CharacterRepository.Load(ctx, "17")
	if err != nil {
		t.Fatal(err)
	}
	c.Stats["delete_flag"] = 1
	if err := r.CharacterRepository.Save(ctx, c); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.validateCurrentGuildMemberProfileAccess(ctx, a); err == nil {
		t.Fatal("send-time validation accepted deleted guild member")
	}
	if r.full != 1 {
		t.Fatalf("send-time identity validation repeated a full character read: %d", r.full)
	}
}

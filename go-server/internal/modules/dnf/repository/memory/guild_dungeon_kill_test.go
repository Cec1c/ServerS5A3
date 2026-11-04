package memory

import (
	"context"
	r "longheng.io/server/internal/modules/dnf/repository"
	"testing"
)

func TestGuildDungeonKillTransactionScopes(t *testing.T) {
	g := NewMemoryGroup()
	ctx := context.Background()
	owner := r.GuildOwner{AccountID: "acc", CharacterID: "77"}
	if err := g.Character.Save(ctx, r.CharacterRecord{CharacterID: "77", AccountID: "acc"}); err != nil {
		t.Fatal(err)
	}
	u := g.CharacterSettlement.(r.GuildDungeonKillUnitOfWork)
	if err := u.WithinGuildDungeonKill(ctx, owner, "run", func(tx r.Group) error {
		for _, scope := range []string{r.GuildRegistryScope, r.GuildDungeonKillReceiptScope(owner, "other")} {
			if _, _, err := tx.Settings.Load(ctx, scope); err == nil {
				t.Fatal("scope read allowed", scope)
			}
			if err := tx.Settings.Save(ctx, r.SettingsRecord{Scope: scope}); err == nil {
				t.Fatal("scope write allowed", scope)
			}
		}
		if _, _, err := tx.Character.Load(ctx, "78"); err == nil {
			t.Fatal("foreign character")
		}
		if _, _, err := tx.Mailbox.Load(ctx, "78"); err == nil {
			t.Fatal("foreign mailbox")
		}
		return tx.Settings.Save(ctx, r.SettingsRecord{Scope: r.GuildDungeonKillReceiptScope(owner, "run"), Values: map[string]string{"receipt": "test"}})
	}); err != nil {
		t.Fatal(err)
	}
	if err := u.WithinGuildDungeonKill(ctx, r.GuildOwner{AccountID: "other", CharacterID: "77"}, "run", func(r.Group) error { return nil }); err == nil {
		t.Fatal("foreign account")
	}
}

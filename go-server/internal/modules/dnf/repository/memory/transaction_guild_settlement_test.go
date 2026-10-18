package memory

import (
	"context"
	"errors"
	"reflect"
	"testing"

	r "longheng.io/server/internal/modules/dnf/repository"
)

type failGuildClearSettings struct {
	r.SettingsRepository
	scope  string
	fail   bool
	cancel context.CancelFunc
}

func (s *failGuildClearSettings) Save(ctx context.Context, record r.SettingsRecord) error {
	if err := s.SettingsRepository.Save(ctx, record); err != nil {
		return err
	}
	if s.fail && record.Scope == s.scope {
		s.fail = false
		if s.cancel != nil {
			s.cancel()
			return nil
		}
		return errors.New("failure after final settings write")
	}
	return nil
}

func TestGuildSettlementMemoryLateSaveRollbackAndScopes(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(map[bool]string{false: "save-error", true: "cancel-after-save"}[cancel], func(t *testing.T) {
			ctx, stop := context.WithCancel(context.Background())
			defer stop()
			g := NewMemoryGroup()
			seedCharacterSettlement(t, ctx, g)
			u := g.CharacterSettlement.(*memoryCharacterSettlementUnitOfWork)
			owner := r.GuildOwner{AccountID: "dnf:1", CharacterID: "77"}
			scope := r.GuildSettlementReceiptScope(owner, "run")
			fault := &failGuildClearSettings{SettingsRepository: g.Settings, scope: scope, fail: true}
			if cancel {
				fault.cancel = stop
			}
			u.settings = fault
			beforeC, _, _ := g.Character.Load(ctx, "77")
			beforeS, _, _ := g.Skill.Load(ctx, "77")
			err := u.WithinGuildSettlement(ctx, owner, "run", func(tx r.Group) error {
				if _, _, err := tx.Settings.Load(ctx, "unrelated"); !errors.Is(err, r.ErrGuildTransaction) {
					t.Fatal("unrelated scope exposed")
				}
				if _, _, err := tx.Character.Load(ctx, "88"); err == nil {
					t.Fatal("another character exposed")
				}
				ch, _, _ := tx.Character.Load(ctx, "77")
				ch.Level++
				sk, _, _ := tx.Skill.Load(ctx, "77")
				sk.Points.TotalSP++
				for _, err := range []error{tx.Character.Save(ctx, ch), tx.Skill.Save(ctx, sk), tx.Settings.Save(ctx, r.SettingsRecord{Scope: r.GuildRegistryScope, Values: map[string]string{"state": "changed"}}), tx.Settings.Save(ctx, r.SettingsRecord{Scope: scope, Values: map[string]string{"receipt": "committed"}})} {
					if err != nil {
						return err
					}
				}
				return nil
			})
			if err == nil {
				t.Fatal("late write succeeded")
			}
			afterC, _, _ := g.Character.Load(context.Background(), "77")
			afterS, _, _ := g.Skill.Load(context.Background(), "77")
			if !reflect.DeepEqual(beforeC, afterC) || !reflect.DeepEqual(beforeS, afterS) {
				t.Fatal("partial character/skill award")
			}
			for _, scope := range []string{r.GuildRegistryScope, scope} {
				if _, found, _ := g.Settings.Load(context.Background(), scope); found {
					t.Fatal("partial settings write")
				}
			}
			// The original settlement API still cannot mutate guild settings.
			if err := u.WithinCharacterSettlement(context.Background(), "77", func(tx r.Group) error {
				if tx.Settings != nil {
					t.Fatal("ordinary UoW widened")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func (s *failGuildClearSettings) Delete(ctx context.Context, scope string) error {
	return s.SettingsRepository.(interface {
		Delete(context.Context, string) error
	}).Delete(ctx, scope)
}

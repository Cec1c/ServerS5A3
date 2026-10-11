package memory

import (
	"context"
	"errors"
	"fmt"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"reflect"
	"sync"
	"testing"
)

func semesterMemoryFixture(t *testing.T) (repo.Group, repo.SemesterClearOwner, []repo.SemesterClearOwner) {
	t.Helper()
	g := NewMemoryGroup()
	owners := []repo.SemesterClearOwner{}
	for i := 1; i <= 4; i++ {
		o := repo.SemesterClearOwner{AccountID: fmt.Sprint("a", i), CharacterID: fmt.Sprint(i)}
		owners = append(owners, o)
		if e := g.Character.Save(context.Background(), repo.CharacterRecord{AccountID: o.AccountID, CharacterID: o.CharacterID, Stats: map[string]int64{"keep": 17}}); e != nil {
			t.Fatal(e)
		}
	}
	if e := g.Quest.Save(context.Background(), repo.QuestRecord{CharacterID: "1", States: map[int64]repo.QuestState{9: {Status: "active", ProgressValue: 1}}}); e != nil {
		t.Fatal(e)
	}
	return g, owners[0], owners
}
func semesterMemoryApply(ctx context.Context, tx repo.Group, owners []repo.SemesterClearOwner) error {
	for _, o := range owners {
		c, _, e := tx.Character.Load(ctx, o.CharacterID)
		if e != nil {
			return e
		}
		c.Stats["days"]++
		if e = tx.Character.Save(ctx, c); e != nil {
			return e
		}
	}
	q, _, e := tx.Quest.Load(ctx, "1")
	if e != nil {
		return e
	}
	q.States[9] = repo.QuestState{Status: "pending"}
	if e = tx.Quest.Save(ctx, q); e != nil {
		return e
	}
	return tx.Settings.Save(ctx, repo.SettingsRecord{Scope: "semester-receipt:one", Values: map[string]string{"committed": "1"}})
}
func assertSemesterUnchanged(t *testing.T, g repo.Group) {
	t.Helper()
	for i := 1; i <= 4; i++ {
		c, _, e := g.Character.Load(context.Background(), fmt.Sprint(i))
		if e != nil || !reflect.DeepEqual(c.Stats, map[string]int64{"keep": 17}) {
			t.Fatalf("character%d %+v err%v", i, c, e)
		}
	}
	q, _, _ := g.Quest.Load(context.Background(), "1")
	if q.States[9].Status != "active" {
		t.Fatal("quest escaped", q)
	}
	if _, found, _ := g.Settings.Load(context.Background(), "semester-receipt:one"); found {
		t.Fatal("receipt escaped")
	}
}

type semesterFailSaveCharacter struct {
	repo.CharacterRepository
	failID string
	fail   bool
}

func (r *semesterFailSaveCharacter) Save(ctx context.Context, c repo.CharacterRecord) error {
	if r.fail && c.CharacterID == r.failID {
		r.fail = false
		if e := r.CharacterRepository.Save(ctx, c); e != nil {
			return e
		}
		return errors.New("physical-character-after-write-fault")
	}
	return r.CharacterRepository.Save(ctx, c)
}

type semesterFailSaveSettings struct {
	repo.SettingsRepository
	fail   bool
	panic  bool
	cancel context.CancelFunc
}

func (r *semesterFailSaveSettings) Delete(ctx context.Context, key string) error {
	return r.SettingsRepository.(interface {
		Delete(context.Context, string) error
	}).Delete(ctx, key)
}

func (r *semesterFailSaveSettings) Save(ctx context.Context, s repo.SettingsRecord) error {
	if r.fail {
		r.fail = false
		if e := r.SettingsRepository.Save(ctx, s); e != nil {
			return e
		}
		if r.cancel != nil {
			r.cancel()
			return nil
		}
		if r.panic {
			panic("physical-receipt-panic")
		}
		return errors.New("physical-receipt-after-write-fault")
	}
	return r.SettingsRepository.Save(ctx, s)
}

func TestSemesterClearMemoryAtomicFailure(t *testing.T) {
	for _, kind := range []string{"callback", "last-character-write", "receipt-write", "cancel-after-write", "callback-panic", "physical-panic"} {
		t.Run(kind, func(t *testing.T) {
			g, source, owners := semesterMemoryFixture(t)
			u := g.SemesterClear.(*memorySemesterClearUnitOfWork)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			apply := func(tx repo.Group) error {
				if e := semesterMemoryApply(ctx, tx, owners); e != nil {
					return e
				}
				if kind == "callback" {
					return errors.New("callback-fault")
				}
				if kind == "callback-panic" {
					panic("callback-panic")
				}
				return nil
			}
			if kind == "last-character-write" {
				u.character = &semesterFailSaveCharacter{CharacterRepository: g.Character, failID: "4", fail: true}
			}
			if kind == "receipt-write" || kind == "physical-panic" || kind == "cancel-after-write" {
				fault := &semesterFailSaveSettings{SettingsRepository: g.Settings, fail: true, panic: kind == "physical-panic"}
				if kind == "cancel-after-write" {
					fault.cancel = cancel
				}
				u.settings = fault
			}
			panicked := false
			var e error
			func() {
				defer func() {
					if recover() != nil {
						panicked = true
					}
				}()
				e = u.WithinSemesterClear(ctx, source, owners, "semester-receipt:one", apply)
			}()
			if kind == "callback-panic" || kind == "physical-panic" {
				if !panicked {
					t.Fatal("panic swallowed")
				}
			} else if e == nil {
				t.Fatal("fault accepted")
			}
			assertSemesterUnchanged(t, g)
			// Recovery also proves the shared mutex is released after either panic.
			e = u.WithinSemesterClear(context.Background(), source, owners, "semester-receipt:one", func(tx repo.Group) error { return semesterMemoryApply(context.Background(), tx, owners) })
			if e != nil {
				t.Fatal("retry", e)
			}
			for _, o := range owners {
				c, _, _ := g.Character.Load(context.Background(), o.CharacterID)
				if c.Stats["days"] != 1 || c.Stats["keep"] != 17 {
					t.Fatal("retry", c)
				}
			}
		})
	}
}
func TestSemesterClearMemoryExactScopes(t *testing.T) {
	g, source, owners := semesterMemoryFixture(t)
	ctx := context.Background()
	for _, kind := range []string{"foreign-character-load", "foreign-character-save", "foreign-account-save", "foreign-quest", "foreign-receipt", "general-repositories"} {
		t.Run(kind, func(t *testing.T) {
			e := g.SemesterClear.WithinSemesterClear(ctx, source, owners, "semester-receipt:one", func(tx repo.Group) error {
				switch kind {
				case "foreign-character-load":
					_, _, e := tx.Character.Load(ctx, "5")
					return e
				case "foreign-character-save":
					return tx.Character.Save(ctx, repo.CharacterRecord{CharacterID: "5", AccountID: "a5"})
				case "foreign-account-save":
					return tx.Character.Save(ctx, repo.CharacterRecord{CharacterID: "1", AccountID: "wrong"})
				case "foreign-quest":
					return tx.Quest.Save(ctx, repo.QuestRecord{CharacterID: "2"})
				case "foreign-receipt":
					return tx.Settings.Save(ctx, repo.SettingsRecord{Scope: "other"})
				default:
					if tx.Account != nil || tx.Inventory != nil || tx.Skill != nil || tx.Mailbox != nil || tx.SemesterClear != nil {
						t.Fatal("repo leaked")
					}
					return errors.New("expected-end")
				}
			})
			if e == nil {
				t.Fatal("out of scope accepted")
			}
			assertSemesterUnchanged(t, g)
		})
	}
}
func TestSemesterClearMemoryConcurrentPhysicalUnit(t *testing.T) {
	g, source, owners := semesterMemoryFixture(t)
	var wg sync.WaitGroup
	errorsOut := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errorsOut <- g.SemesterClear.WithinSemesterClear(context.Background(), source, owners, "semester-receipt:one", func(tx repo.Group) error {
				_, found, e := tx.Settings.Load(context.Background(), "semester-receipt:one")
				if e != nil || found {
					return e
				}
				return semesterMemoryApply(context.Background(), tx, owners)
			})
		}()
	}
	wg.Wait()
	close(errorsOut)
	for e := range errorsOut {
		if e != nil {
			t.Fatal(e)
		}
	}
	for _, o := range owners {
		c, _, _ := g.Character.Load(context.Background(), o.CharacterID)
		if c.Stats["days"] != 1 {
			t.Fatal("concurrent duplication", c)
		}
	}
}

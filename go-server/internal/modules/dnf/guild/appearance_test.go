package guild

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

func appearanceCatalog() AppearanceCatalog {
	return AppearanceCatalog{Marks: 15, FreeMarks: 5, MarkPrice: 2000, AuraMarks: 2, AuraFrames: 3}
}

func TestGuildAppearanceDomainBoundariesAndPermissions(t *testing.T) {
	for _, which := range []string{"member", "deputy", "mask_grant", "wrong_owner", "false_master", "insufficient", "mark_range", "aura_range", "half_clear"} {
		t.Run(which, func(t *testing.T) {
			r, g, master, member := textRegistry(t)
			g.Mileage = 4000
			c := appearanceCatalog()
			owner := master
			mark, frame := uint32(1), uint32(2)
			want := ErrPermissionDenied
			switch which {
			case "member":
				owner = member
			case "deputy", "mask_grant":
				owner = member
				m := g.Members["2"]
				m.Grade = 2
				g.Members["2"] = m
				if which == "mask_grant" {
					g.GradePermissions = map[byte]uint32{2: GradePermissionMask}
				}
			case "wrong_owner":
				owner.AccountID = "other"
				want = ErrNotMember
			case "false_master":
				g.MasterCharacterID = "2"
			case "insufficient":
				g.Mileage = 1999
				want = ErrInsufficientMileage
			case "mark_range":
				c.Marks = 5
				want = ErrInvalidAppearance
			case "aura_range":
				mark = 2
				want = ErrInvalidAppearance
			case "half_clear":
				frame = NoGuildAura
				want = ErrInvalidAppearance
			}
			before := *g
			var err error
			if which == "aura_range" || which == "half_clear" {
				_, err = r.SetAura(owner, mark, frame, c, time.Now())
			} else {
				_, err = r.SetMark(owner, 5, c, time.Now())
			}
			if !errors.Is(err, want) || !reflect.DeepEqual(before, *g) {
				t.Fatalf("err=%v want=%v changed=%t", err, want, !reflect.DeepEqual(before, *g))
			}
		})
	}
}

func TestGuildAppearanceAtomicConcurrentRetryAndRollback(t *testing.T) {
	ctx := context.Background()
	repos := guildTestGroup(t, 2)
	r, g, owner, _ := textRegistry(t)
	g.Mileage = 4000
	now := g.UpdatedAt.Add(time.Hour)
	if err := Save(ctx, repos.Settings, r, now); err != nil {
		t.Fatal(err)
	}
	edit := func(tx repo.GuildUnitOfWork, mark byte, clear bool) error {
		return Update(ctx, tx, []repo.GuildOwner{owner}, now, func(_ repo.Group, r *Registry) error {
			if _, err := r.SetMark(owner, mark, appearanceCatalog(), now); err != nil {
				return err
			}
			a, b := uint32(1), uint32(2)
			if clear {
				a, b = NoGuildAura, NoGuildAura
			}
			_, err := r.SetAura(owner, a, b, appearanceCatalog(), now)
			return err
		})
	}
	boom := errors.New("late rollback")
	if err := edit(lateFailGuildTx{repos.Guild, boom}, 5, false); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	loaded, _ := Load(ctx, repos.Settings)
	if loaded.Guilds[1].Mileage != 4000 || loaded.Guilds[1].Mark != 0 {
		t.Fatal("failed transaction charged")
	}
	var wg sync.WaitGroup
	results := make(chan error, 12)
	for range 12 {
		wg.Go(func() { results <- edit(repos.Guild, 5, false) })
	}
	wg.Wait()
	close(results)
	for e := range results {
		if e != nil {
			t.Fatal(e)
		}
	}
	loaded, _ = Load(ctx, repos.Settings)
	if g := loaded.Guilds[1]; g.Mileage != 2000 || g.Mark != 5 || g.AuraMark != 1 || g.AuraFrame != 2 {
		t.Fatalf("retry charged repeatedly: %+v", g)
	}
	if err := edit(repos.Guild, 5, true); err != nil {
		t.Fatal(err)
	}
	loaded, _ = Load(ctx, repos.Settings)
	if g := loaded.Guilds[1]; g.Mileage != 2000 || g.AuraMark != NoGuildAura || g.AuraFrame != NoGuildAura {
		t.Fatalf("clear: %+v", g)
	}
	character, _, err := repos.Character.Load(ctx, owner.CharacterID)
	if err != nil || character.Stats["gold"] != 900000 {
		t.Fatal("appearance changed character gold", err)
	}
}

func TestGuildAppearanceSessionLostAfterSaveRollsBack(t *testing.T) {
	ctx := context.Background()
	repos := guildTestGroup(t, 2)
	r, g, owner, _ := textRegistry(t)
	g.Mileage = 4000
	now := time.Now()
	if err := Save(ctx, repos.Settings, r, now); err != nil {
		t.Fatal(err)
	}
	for _, stage := range []int{1, 2, 3} {
		before, _, _ := repos.Settings.Load(ctx, RegistryScope)
		calls := 0
		change := AppearanceChange{Owner: owner, Mark: 5, Revalidate: func() bool { calls++; return calls < stage }}
		id, err := ChangeAppearance(ctx, repos.Guild, change, appearanceCatalog(), now)
		if id != 0 || !errors.Is(err, ErrCharacterNotFound) {
			t.Fatalf("stage=%d id=%d err=%v", stage, id, err)
		}
		after, _, _ := repos.Settings.Load(ctx, RegistryScope)
		if !reflect.DeepEqual(before, after) {
			t.Fatal("stale session committed")
		}
	}
}

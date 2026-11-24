package skillcmd

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	darkcombo "longheng.io/server/internal/modules/dnf/darkknightcombo"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func pvpComboFixture(t *testing.T) (repo.Group, []darkcombo.Page) {
	t.Helper()
	ctx := context.Background()
	r := seededSkillOwnerRepositories(t, ctx)
	c, _, _ := r.Character.Load(ctx, "31")
	c.Job = "9"
	if err := r.Character.Save(ctx, c); err != nil {
		t.Fatal(err)
	}
	ordinary, _, _ := r.Skill.Load(ctx, "31")
	ordinary.Skills[118] = repo.SkillState{Level: 1, Enabled: true}
	if err := r.Skill.Save(ctx, ordinary); err != nil {
		t.Fatal(err)
	}
	pages := []darkcombo.Page{{Index: 0, Roots: []darkcombo.Root{{SkillID: 118, Children: []uint16{46}}}}}
	if err := darkcombo.SavePages(ctx, r.Settings, "31", pages, time.Unix(10, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := PreparePVPSkills(ctx, r, "acc-skill", "31"); err != nil {
		t.Fatal(err)
	}
	return r, pages
}

func TestPVPComboSaveAndReloadUsesPKLearnedSkillsAndKeepsOrdinary(t *testing.T) {
	ctx := context.Background()
	r, beforePages := pvpComboFixture(t)
	beforeSkill, _, _ := r.Skill.Load(ctx, "31")
	view := PVPRepositories(r, "acc-skill", "31")
	page := darkcombo.Page{Index: 0, Roots: []darkcombo.Root{{SkillID: 118, Children: []uint16{74, 46}}}}
	// A skill bought only in PK must be visible to the very same settlement
	// that validates/saves the combo and cleans its shortcut ownership.
	err := view.CharacterSettlement.WithinCharacterSettlement(ctx, "31", func(tx repo.Group) error {
		record, _, err := tx.Skill.Load(ctx, "31")
		if err != nil {
			return err
		}
		record.Skills[74] = repo.SkillState{Level: 21, Enabled: true}
		record.Layouts[0] = repo.SkillLayout{0: 118, 150: 74}
		if err := tx.Skill.Save(ctx, record); err != nil {
			return err
		}
		return darkcombo.SavePage(ctx, tx.Settings, "31", page, time.Unix(20, 0))
	})
	if err != nil {
		t.Fatal(err)
	}
	// Recreate the view as on reconnect; no browser or process state is used.
	pk, err := PreparePVPSkills(ctx, r, "acc-skill", "31")
	if err != nil {
		t.Fatal(err)
	}
	view = PVPRepositories(r, "acc-skill", "31")
	saved, found, err := darkcombo.LoadPages(ctx, view.Settings, "31")
	if err != nil || !found || !reflect.DeepEqual(saved, []darkcombo.Page{page}) {
		t.Fatalf("PK combo order not durable: %+v %v", saved, err)
	}
	projected := darkcombo.FilterLearnedPages(pk, saved)
	if !reflect.DeepEqual(projected, saved) || pk.Skills[74].Level != 21 {
		t.Fatalf("PK-only learned skill was filtered away: %+v", projected)
	}
	if _, err := darkcombo.EncodeNotificationBody(projected); err != nil {
		t.Fatal(err)
	}
	ordinaryPages, _, _ := darkcombo.LoadPages(ctx, r.Settings, "31")
	ordinarySkill, _, _ := r.Skill.Load(ctx, "31")
	if !reflect.DeepEqual(ordinaryPages, beforePages) || !reflect.DeepEqual(ordinarySkill, beforeSkill) {
		t.Fatal("PK save altered ordinary combo or skills")
	}
	// Even a later ordinary edit cannot overwrite an already seeded PK order.
	if err := darkcombo.SavePage(ctx, r.Settings, "31", darkcombo.Page{Index: 0, Roots: []darkcombo.Root{{SkillID: 118}}}, time.Unix(30, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := PreparePVPSkills(ctx, r, "acc-skill", "31"); err != nil {
		t.Fatal(err)
	}
	savedAgain, _, _ := darkcombo.LoadPages(ctx, view.Settings, "31")
	if !reflect.DeepEqual(savedAgain, saved) {
		t.Fatal("ordinary edits copied over established PK combo")
	}
}

func TestPVPComboBothPagesAndExplicitEmptyOrderStayIndependent(t *testing.T) {
	ctx := context.Background()
	r, ordinaryPages := pvpComboFixture(t)
	view := PVPRepositories(r, "acc-skill", "31")
	pages := []darkcombo.Page{
		{Index: 0, Roots: []darkcombo.Root{{SkillID: 118}}},
		{Index: 1, Roots: []darkcombo.Root{{SkillID: 118, Children: []uint16{46}}}},
	}
	err := view.CharacterSettlement.WithinCharacterSettlement(ctx, "31", func(tx repo.Group) error {
		return darkcombo.SavePages(ctx, tx.Settings, "31", pages, time.Unix(20, 0))
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tree := range []byte{0, 1} {
		if _, err := PreparePVPSkills(ctx, r, "acc-skill", "31"); err != nil {
			t.Fatal(err)
		}
		view = PVPRepositories(r, "acc-skill", "31", tree)
		record, _, err := view.Skill.Load(ctx, "31")
		saved, _, loadErr := darkcombo.LoadPages(ctx, view.Settings, "31")
		if err != nil || loadErr != nil || !reflect.DeepEqual(darkcombo.FilterLearnedPages(record, saved), pages) {
			t.Fatalf("page %d lost combo or restored empty order: %v %v", tree, err, loadErr)
		}
	}
	after, _, _ := darkcombo.LoadPages(ctx, r.Settings, "31")
	if !reflect.DeepEqual(after, ordinaryPages) {
		t.Fatal("PK page switch changed ordinary order")
	}
}

func TestPVPComboSettlementRollsBackLayoutAndOrderTogether(t *testing.T) {
	ctx := context.Background()
	r, _ := pvpComboFixture(t)
	view := PVPRepositories(r, "acc-skill", "31")
	before, _, _ := view.Skill.Load(ctx, "31")
	pages, _, _ := darkcombo.LoadPages(ctx, view.Settings, "31")
	failure := errors.New("abort after combo and layout changes")
	err := view.CharacterSettlement.WithinCharacterSettlement(ctx, "31", func(tx repo.Group) error {
		if err := darkcombo.SavePage(ctx, tx.Settings, "31", darkcombo.Page{Index: 0, Roots: []darkcombo.Root{{SkillID: 118}}}, time.Unix(40, 0)); err != nil {
			return err
		}
		record, _, _ := tx.Skill.Load(ctx, "31")
		record.Layouts = map[int]repo.SkillLayout{0: {0: 118}}
		if err := tx.Skill.Save(ctx, record); err != nil {
			return err
		}
		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
	after, _, _ := view.Skill.Load(ctx, "31")
	afterPages, _, _ := darkcombo.LoadPages(ctx, view.Settings, "31")
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(pages, afterPages) {
		t.Fatal("failed settlement changed PK combo or layout")
	}
}

func TestPVPComboScopeRejectsForeignSettlement(t *testing.T) {
	r, _ := pvpComboFixture(t)
	view := PVPRepositories(r, "acc-skill", "31")
	for _, scope := range []string{PVPComboScope("32"), darkcombo.SettingsScope("32"), "character:31:pvp_dark_knight_combo_v1:extra"} {
		err := view.CharacterSettlement.WithinCharacterSettlement(context.Background(), "31", func(tx repo.Group) error {
			if _, _, err := tx.Settings.Load(context.Background(), scope); !errors.Is(err, repo.ErrCharacterSettlementTransactionUnavailable) {
				t.Fatalf("foreign read permitted: %s %v", scope, err)
			}
			return tx.Settings.Save(context.Background(), repo.SettingsRecord{Scope: scope})
		})
		if !errors.Is(err, repo.ErrCharacterSettlementTransactionUnavailable) {
			t.Fatalf("foreign write permitted: %s %v", scope, err)
		}
	}
}

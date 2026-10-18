package dnfbridge

import (
	"context"
	"errors"
	"os"
	"reflect"
	"sort"
	"testing"
	"time"

	darkcombo "longheng.io/server/internal/modules/dnf/darkknightcombo"
	dnfguardianshield "longheng.io/server/internal/modules/dnf/guardianshield"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/skillcmd"
)

func TestRealPVFDarkKnightSeedsUnlockedPagesAndPreservesEditedPrimary(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("需要现用PVF")
	}
	ctx := context.Background()
	s := &Service{options: options{pvfPath: path}}
	initial, err := s.initialCharacterSkills(ctx, 9)
	if err != nil {
		t.Fatal(err)
	}
	record := dnfrepo.SkillRecord{CharacterID: "77", Skills: make(map[int64]dnfrepo.SkillState)}
	for _, entry := range initial {
		record.Skills[entry.SkillID] = dnfrepo.SkillState{Level: entry.Level, Enabled: true}
	}
	layout, err := skillcmd.BuildInitialSkillLayout(s.skillCatalog, 9, 0, record.Skills)
	if err != nil {
		t.Fatal(err)
	}
	record.Layouts = map[int]dnfrepo.SkillLayout{0: layout}
	r := dnfrepomemory.NewMemoryGroup()
	character := dnfrepo.CharacterRecord{CharacterID: "77", AccountID: "acc", Job: "9", Level: 1}
	if err := r.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	if err := r.Skill.Save(ctx, record); err != nil {
		t.Fatal(err)
	}
	pages, err := s.loadDarkKnightComboPages(ctx, r, character, s.skillCatalog, record)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 || pages[0].Index != 0 || len(pages[0].Roots) != 6 {
		t.Fatalf("未购买第二页的初始化不符: %+v", pages)
	}
	for index, root := range pages[0].Roots {
		if layout[index] != root.SkillID {
			t.Fatalf("PVF根技能未在前六格: roots=%+v layout=%v", pages, layout)
		}
	}
	pages[0].Roots[0].Children = nil
	if err := darkcombo.SavePages(ctx, r.Settings, "77", pages, time.Now()); err != nil {
		t.Fatal(err)
	}
	want := pages[0]
	record.Trees = dnfrepo.SkillTreeState{Expanded: true, Other: dnfrepo.SkillProfile{Skills: dnfrepo.CloneSkill(record).Skills}}
	if err := r.Skill.Save(ctx, record); err != nil {
		t.Fatal(err)
	}
	pages, err = s.loadDarkKnightComboPages(ctx, r, character, s.skillCatalog, record)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 || !reflect.DeepEqual(pages[0], want) {
		t.Fatalf("补第二页覆盖已有编辑: %+v", pages)
	}
}

func TestRealPVFGuardianShieldLevelRowEquipAndRejectDuplicate(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("需要现用PVF")
	}
	ctx := context.Background()
	s := &Service{options: options{pvfPath: path}}
	resolver, err := s.newCurrentGuardianShieldResolver(ctx)
	if err != nil {
		t.Fatal(err)
	}
	text, err := s.initialEquipmentArchive.ReadText(currentGuardianShieldCatalogPath)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := parseCurrentGuardianShieldCatalog(text)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]int, 0, len(catalog[0]))
	for id := range catalog[0] {
		ids = append(ids, int(id))
	}
	sort.Ints(ids)
	if len(ids) == 0 {
		t.Fatal("现用PVF没有等级解锁盾牌")
	}
	id := uint32(ids[0])
	resolution, err := resolver(ctx, id, 1)
	if err != nil {
		t.Fatal(err)
	}
	r := dnfrepomemory.NewMemoryGroup()
	character := dnfrepo.CharacterRecord{CharacterID: "77", AccountID: "acc", Job: "12", Level: 90, Stats: map[string]int64{"grow_type": 1}}
	if err := r.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	owner, err := dnfguardianshield.NewOwner(r, resolver)
	if err != nil {
		t.Fatal(err)
	}
	result, err := owner.Move(ctx, dnfguardianshield.MoveCommand{CharacterID: 77, AccountID: "acc", SourceListType: 34, SourceShieldItemID: id, DestinationListType: 33})
	if err != nil {
		t.Fatal(err)
	}
	if result.Deck[0] != id || !result.MainChanged {
		t.Fatalf("真实盾牌未穿戴: %+v", result)
	}
	equipment, _, _ := r.Equipment.Load(ctx, "77")
	if got, ok := dnfguardianshield.MainProjectionItemID(equipment); !ok || got != id {
		t.Fatalf("真实盾牌主投影=%d present=%t", got, ok)
	}
	if _, err := owner.SaveDeck(ctx, 77, "acc", dnfguardianshield.Deck{id, id}); !errors.Is(err, dnfguardianshield.ErrDeckDuplicate) {
		t.Fatalf("重复盾牌未拒绝: %v", err)
	}
	character, _, _ = r.Character.Load(ctx, "77")
	character.Level = int(resolution.MinimumLevel) - 1
	if character.Level < 0 {
		t.Fatal("真实等级解锁行无最低等级")
	}
	if err := r.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.SaveDeck(ctx, 77, "acc", result.Deck); !errors.Is(err, dnfguardianshield.ErrLevelInsufficient) {
		t.Fatalf("等级不足未拒绝: %v", err)
	}
}

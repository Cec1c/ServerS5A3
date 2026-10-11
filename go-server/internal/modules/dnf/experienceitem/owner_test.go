package experienceitem

import (
	"context"
	"errors"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

const (
	testAccountID   = "account-1"
	testCharacterID = "19"
)

type testFixture struct {
	owner        *Owner
	repositories dnfrepo.Group
}

func newTestFixture(
	t *testing.T,
	slots map[string]dnfrepo.ItemStack,
	stats map[string]int64,
	points dnfrepo.SkillPointState,
	writeSkill bool,
) testFixture {
	t.Helper()
	repositories := dnfrepomemory.NewMemoryGroup()
	ctx := context.Background()
	if err := repositories.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: testAccountID}); err != nil {
		t.Fatal(err)
	}
	if stats == nil {
		stats = map[string]int64{"exp": 0}
	}
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: testCharacterID,
		AccountID:   testAccountID,
		Level:       50,
		Stats:       stats,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: testCharacterID,
		Slots:       slots,
	}); err != nil {
		t.Fatal(err)
	}
	if writeSkill {
		if err := repositories.Skill.Save(ctx, dnfrepo.SkillRecord{
			CharacterID: testCharacterID,
			Points:      points,
		}); err != nil {
			t.Fatal(err)
		}
	}
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	return testFixture{owner: owner, repositories: repositories}
}

func (f testFixture) command(book Book) Command {
	return Command{
		AccountID:       testAccountID,
		CharacterID:     testCharacterID,
		SourceListType:  dnfrepo.MainInventoryListType,
		SourceSlotIndex: 9,
		ItemID:          book.ItemID,
		PVFTotalSP:      1000,
		PVFTotalTP:      10,
		Book:            book,
		Now:             time.Now().UTC(),
	}
}

func (f testFixture) load(t *testing.T) (dnfrepo.InventoryRecord, dnfrepo.CharacterRecord, dnfrepo.SkillRecord) {
	t.Helper()
	ctx := context.Background()
	inventory, found, err := f.repositories.Inventory.Load(ctx, testCharacterID)
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	character, found, err := f.repositories.Character.Load(ctx, testCharacterID)
	if err != nil || !found {
		t.Fatalf("load character found=%t err=%v", found, err)
	}
	skill, _, err := f.repositories.Skill.Load(ctx, testCharacterID)
	if err != nil {
		t.Fatalf("load skill err=%v", err)
	}
	return inventory, character, skill
}

func spBook(itemID int64, amount int) Book {
	return Book{ItemID: itemID, Kind: KindSP, Amount: amount, PVFPath: "stackable/book_skill1.stk", Name: "SP+5技能书"}
}

// TestUseSkillBookGrantsBonusSPAndKeepsSpentPoints covers the reported defect:
// the book is consumed and the SP total rises by the book amount while the
// already-spent points stay spent.
func TestUseSkillBookGrantsBonusSPAndKeepsSpentPoints(t *testing.T) {
	fixture := newTestFixture(t,
		map[string]dnfrepo.ItemStack{"0:9": {ItemID: 1031, Count: 2}},
		map[string]int64{"exp": 7},
		dnfrepo.SkillPointState{TotalSP: 1000, RemainingSP: 900, TotalTP: 10, RemainingTP: 8, SyncedLevel: 50},
		true,
	)
	result, err := fixture.owner.UseSkillBook(context.Background(), fixture.command(spBook(1031, 5)))
	if err != nil {
		t.Fatalf("UseSkillBook error = %v", err)
	}
	if result.RemainingCount != 1 || result.BonusSP != 5 || result.BonusTP != 0 {
		t.Fatalf("result = %+v, want remaining=1 bonus_sp=5 bonus_tp=0", result)
	}
	want := dnfrepo.SkillPointState{TotalSP: 1005, RemainingSP: 905, TotalTP: 10, RemainingTP: 8, SyncedLevel: 50}
	if result.Points != want {
		t.Fatalf("points = %+v, want %+v", result.Points, want)
	}
	inventory, character, skill := fixture.load(t)
	if stack := inventory.Slots["0:9"]; stack.Count != 1 || stack.ItemID != 1031 {
		t.Fatalf("source row = %+v, want item 1031 count 1", stack)
	}
	if character.Stats["bonus_sp"] != 5 || character.Stats["bonus_tp"] != 0 || character.Stats["exp"] != 7 {
		t.Fatalf("character stats = %+v", character.Stats)
	}
	if skill.Points != want {
		t.Fatalf("persisted points = %+v, want %+v", skill.Points, want)
	}
}

// TestUseSkillBookGrantsBonusTPAndDeletesTheLastUnit covers the TP book family
// and the last-unit slot removal.
func TestUseSkillBookGrantsBonusTPAndDeletesTheLastUnit(t *testing.T) {
	fixture := newTestFixture(t,
		map[string]dnfrepo.ItemStack{"0:9": {ItemID: 1204, Count: 1}},
		map[string]int64{"exp": 0, "bonus_tp": 2},
		dnfrepo.SkillPointState{TotalSP: 1000, RemainingSP: 1000, TotalTP: 12, RemainingTP: 12, SyncedLevel: 50},
		true,
	)
	book := Book{ItemID: 1204, Kind: KindTP, Amount: 1, PVFPath: "stackable/book_fskill1.stk", Name: "TP+1技能书"}
	result, err := fixture.owner.UseSkillBook(context.Background(), fixture.command(book))
	if err != nil {
		t.Fatalf("UseSkillBook error = %v", err)
	}
	if result.RemainingCount != 0 || result.BonusTP != 3 {
		t.Fatalf("result = %+v, want remaining=0 bonus_tp=3", result)
	}
	if result.Points.TotalTP != 13 || result.Points.RemainingTP != 13 {
		t.Fatalf("points = %+v, want total_tp=13 remaining_tp=13", result.Points)
	}
	inventory, _, _ := fixture.load(t)
	if _, found := inventory.Slots["0:9"]; found {
		t.Fatal("exhausted skill book row was not deleted")
	}
}

// TestUseSkillBookRejectsMismatchedItemWithoutMutation keeps the op44-style
// identity contract: the request item must match the durable row.
func TestUseSkillBookRejectsMismatchedItemWithoutMutation(t *testing.T) {
	fixture := newTestFixture(t,
		map[string]dnfrepo.ItemStack{"0:9": {ItemID: 1038, Count: 1}},
		map[string]int64{"exp": 0},
		dnfrepo.SkillPointState{TotalSP: 1000, RemainingSP: 1000, TotalTP: 10, RemainingTP: 10, SyncedLevel: 50},
		true,
	)
	cmd := fixture.command(spBook(1031, 5))
	cmd.ItemID = 1038
	cmd.Book = spBook(1038, 20)
	cmd.SourceSlotIndex = 10
	_, err := fixture.owner.UseSkillBook(context.Background(), cmd)
	if !errors.Is(err, ErrSlotNotFound) {
		t.Fatalf("UseSkillBook error = %v, want ErrSlotNotFound", err)
	}
	inventory, character, _ := fixture.load(t)
	if stack := inventory.Slots["0:9"]; stack.Count != 1 || stack.ItemID != 1038 {
		t.Fatalf("source row changed = %+v", stack)
	}
	if character.Stats["bonus_sp"] != 0 {
		t.Fatalf("bonus_sp changed = %d", character.Stats["bonus_sp"])
	}
}

// TestUseSkillBookRejectsCrossAccountCharacter covers the ownership gate.
func TestUseSkillBookRejectsCrossAccountCharacter(t *testing.T) {
	fixture := newTestFixture(t,
		map[string]dnfrepo.ItemStack{"0:9": {ItemID: 1031, Count: 1}},
		map[string]int64{"exp": 0},
		dnfrepo.SkillPointState{TotalSP: 1000, RemainingSP: 1000, TotalTP: 10, RemainingTP: 10, SyncedLevel: 50},
		true,
	)
	cmd := fixture.command(spBook(1031, 5))
	cmd.AccountID = "account-2"
	_, err := fixture.owner.UseSkillBook(context.Background(), cmd)
	if !errors.Is(err, ErrCharacterOwnerMismatch) {
		t.Fatalf("UseSkillBook error = %v, want ErrCharacterOwnerMismatch", err)
	}
}

// TestUseSkillBookRefusesLedgerRegression keeps a book from ever lowering the
// persisted SP/TP totals when the durable ledger carries an out-of-band grant.
func TestUseSkillBookRefusesLedgerRegression(t *testing.T) {
	fixture := newTestFixture(t,
		map[string]dnfrepo.ItemStack{"0:9": {ItemID: 1031, Count: 1}},
		map[string]int64{"exp": 0},
		dnfrepo.SkillPointState{TotalSP: 3000, RemainingSP: 2900, TotalTP: 10, RemainingTP: 10, SyncedLevel: 50},
		true,
	)
	_, err := fixture.owner.UseSkillBook(context.Background(), fixture.command(spBook(1031, 5)))
	if !errors.Is(err, ErrLedgerRegression) {
		t.Fatalf("UseSkillBook error = %v, want ErrLedgerRegression", err)
	}
	inventory, character, skill := fixture.load(t)
	if stack := inventory.Slots["0:9"]; stack.Count != 1 {
		t.Fatalf("source row consumed on a rejected use = %+v", stack)
	}
	if character.Stats["bonus_sp"] != 0 || skill.Points.TotalSP != 3000 {
		t.Fatalf("state changed on a rejected use: bonus_sp=%d points=%+v", character.Stats["bonus_sp"], skill.Points)
	}
}

// TestUseSkillBookRequiresTheDurableLedger fails closed when the skill record is
// missing instead of inventing a fresh ledger.
func TestUseSkillBookRequiresTheDurableLedger(t *testing.T) {
	fixture := newTestFixture(t,
		map[string]dnfrepo.ItemStack{"0:9": {ItemID: 1031, Count: 1}},
		map[string]int64{"exp": 0},
		dnfrepo.SkillPointState{},
		false,
	)
	_, err := fixture.owner.UseSkillBook(context.Background(), fixture.command(spBook(1031, 5)))
	if !errors.Is(err, ErrLedgerUnavailable) {
		t.Fatalf("UseSkillBook error = %v, want ErrLedgerUnavailable", err)
	}
	inventory, _, _ := fixture.load(t)
	if stack := inventory.Slots["0:9"]; stack.Count != 1 {
		t.Fatalf("source row consumed without a ledger = %+v", stack)
	}
}

// TestBookValidateRejectsInventedDefinitions keeps the PVF-derived contract.
func TestBookValidateRejectsInventedDefinitions(t *testing.T) {
	cases := map[string]Book{
		"zero item":  {ItemID: 0, Kind: KindSP, Amount: 5},
		"bad kind":   {ItemID: 1031, Kind: Kind(9), Amount: 5},
		"zero grant": {ItemID: 1031, Kind: KindSP, Amount: 0},
	}
	for name, book := range cases {
		if err := book.Validate(); !errors.Is(err, ErrBookInvalid) {
			t.Fatalf("%s: Validate error = %v, want ErrBookInvalid", name, err)
		}
	}
}

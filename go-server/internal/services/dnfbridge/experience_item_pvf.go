package dnfbridge

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	dnfexperienceitem "longheng.io/server/internal/modules/dnf/experienceitem"
)

// currentSkillBookNamePattern is the runtime PVF `[name]` shape of every
// SP/TP skill book in Script.pvf:
//
//	1031  stackable/book_skill1.stk              `SP+5技能书`
//	1038  stackable/book_skill2.stk              `SP+20技能书`
//	1204  stackable/book_fskill1.stk             `TP+1技能书`
//	1205  stackable/book_fskill2.stk             `TP+5技能书`
//	80003 stackable/extention/test_book_skill1.stk `SP+5技能书`
var currentSkillBookNamePattern = regexp.MustCompile(`^(SP|TP)\+([0-9]{1,4})技能书$`)

// currentSkillBookUseWav is the PVF `[use wav]` marker shared by exactly those
// five items; it is the second, independent family proof next to the name.
const currentSkillBookUseWav = "SP_UP_ITEM"

// currentSkillBookMaxAmount keeps one book from carrying an implausible grant.
const currentSkillBookMaxAmount = 9999

var errCurrentSkillBookNotResolved = errors.New("dnf skill book item is not a PVF-proven SP/TP book")

// resolveCurrentSkillBook reads the item's own runtime PVF document and proves
// the SP/TP book family. Both the granted family and the amount come from that
// document, so no item id table is invented here.
func (s *Service) resolveCurrentSkillBook(itemID int64) (dnfexperienceitem.Book, error) {
	catalog, err := s.currentPVFItemCatalog()
	if err != nil {
		return dnfexperienceitem.Book{}, err
	}
	if catalog == nil || catalog.source == nil || itemID <= 0 || itemID > 0xffffffff {
		return dnfexperienceitem.Book{}, fmt.Errorf("%w: item=%d", errCurrentSkillBookNotResolved, itemID)
	}
	definition, err := catalog.ResolveItem(uint32(itemID))
	if err != nil {
		return dnfexperienceitem.Book{}, fmt.Errorf("%w: item=%d: %v", errCurrentSkillBookNotResolved, itemID, err)
	}
	if definition.Kind != dungeonDropItemStackable {
		return dnfexperienceitem.Book{}, fmt.Errorf("%w: item=%d kind=%s", errCurrentSkillBookNotResolved, itemID, definition.Kind)
	}
	if !currentSkillBookTagEqual(definition.StackableType, "[etc]") {
		return dnfexperienceitem.Book{}, fmt.Errorf("%w: item=%d stackable_type=%q", errCurrentSkillBookNotResolved, itemID, definition.StackableType)
	}
	document, err := parseDungeonCardPVFDocument(catalog.source, definition.PVFPath)
	if err != nil {
		return dnfexperienceitem.Book{}, fmt.Errorf("parse skill book item=%d path=%s: %w", itemID, definition.PVFPath, err)
	}
	useWav, found := document.Text("use wav")
	if !found || !strings.EqualFold(strings.TrimSpace(useWav), currentSkillBookUseWav) {
		return dnfexperienceitem.Book{}, fmt.Errorf("%w: item=%d use_wav=%q", errCurrentSkillBookNotResolved, itemID, useWav)
	}
	name, _ := document.Text("name")
	name = currentSkillBookName(name)
	matches := currentSkillBookNamePattern.FindStringSubmatch(name)
	if matches == nil {
		return dnfexperienceitem.Book{}, fmt.Errorf("%w: item=%d name=%q", errCurrentSkillBookNotResolved, itemID, name)
	}
	amount, err := strconv.Atoi(matches[2])
	if err != nil || amount <= 0 || amount > currentSkillBookMaxAmount {
		return dnfexperienceitem.Book{}, fmt.Errorf("%w: item=%d amount=%q", errCurrentSkillBookNotResolved, itemID, matches[2])
	}
	kind := dnfexperienceitem.KindSP
	if strings.EqualFold(matches[1], "TP") {
		kind = dnfexperienceitem.KindTP
	}
	book := dnfexperienceitem.Book{
		ItemID:  itemID,
		Kind:    kind,
		Amount:  amount,
		PVFPath: definition.PVFPath,
		Name:    name,
	}
	if err := book.Validate(); err != nil {
		return dnfexperienceitem.Book{}, err
	}
	return book, nil
}

// currentSkillBookName strips the PVF's backtick quoting and trailing spaces.
func currentSkillBookName(raw string) string {
	raw = strings.ReplaceAll(raw, "`", "")
	return strings.TrimSpace(raw)
}

// currentSkillBookTagEqual compares one PVF tag ignoring quoting and spacing.
func currentSkillBookTagEqual(value, want string) bool {
	normalize := func(raw string) string {
		raw = strings.TrimSpace(strings.ReplaceAll(raw, "`", ""))
		return strings.ToLower(strings.Join(strings.Fields(raw), " "))
	}
	return normalize(value) == normalize(want)
}

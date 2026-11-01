package dnfbridge

import (
	"fmt"
	"math"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/inventory"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func currentEventItemPolicy(catalog *pvfDungeonDropCatalog, kind inventory.EventItemKind, source, target repo.ItemStack, now time.Time, place string) (inventory.EventItemPolicy, repo.ItemStack, repo.ItemStack, error) {
	p := inventory.EventItemPolicy{Kind: kind}
	reject := func() (inventory.EventItemPolicy, repo.ItemStack, repo.ItemStack, error) {
		return p, source, target, inventory.ErrEventItem
	}
	if source.ItemID <= 0 || source.ItemID > math.MaxUint32 {
		return reject()
	}
	d, err := catalog.ResolveItem(uint32(source.ItemID))
	if err != nil || d.Kind != dungeonDropItemStackable {
		return reject()
	}
	if v := source.Extra["pvf_path"]; v != "" && !strings.EqualFold(strings.ReplaceAll(v, "\\", "/"), d.PVFPath) {
		return reject()
	}
	if v := source.Extra["item_kind"]; v != "" && v != "stackable" {
		return reject()
	}
	doc, err := parseDungeonCardPVFDocument(catalog.source, d.PVFPath)
	if err != nil {
		return p, source, target, err
	}
	for _, field := range []string{"action usable condition", "need material", "using limit info", "total usable count"} {
		if tokens, _ := doc.Section(field); len(tokens) != 0 {
			return reject()
		}
	}
	jobs := doc.Texts("usable job")
	if len(jobs) != 1 || jobs[0] != "[all]" {
		return reject()
	}
	places := doc.Texts("action usable place")
	if len(places) != 0 {
		allowed := false
		for _, value := range places {
			if value != "[village]" && value != "[seria room]" {
				return reject()
			}
			allowed = allowed || value == place
		}
		if !allowed {
			return reject()
		}
	}
	p.SourceSlots = currentPVFItemPlacementPolicy(d)
	p.MinimumLevel, _ = doc.Int("minimum level")
	switch kind {
	case inventory.EventInventoryExpansion:
		action, _ := doc.Section("action type")
		if d.StackableType != "[etc]" || len(action) != 1 || action[0].Value != "[upgrade inventory]" {
			return reject()
		}
	case inventory.EventSilverCargo:
		if !currentEventSilverCargoDefinition(d, doc) {
			return reject()
		}
	case inventory.EventPermanentWeapon:
		action, _ := doc.Section("action type")
		if d.StackableType != "[etc]" || len(action) != 6 || action[0].Value != "[change growth equipment]" {
			return reject()
		}
		for i, want := range []int64{-1, -1, 0, 9999, -1} {
			if action[i+1].Kind != dnfpvf.TokenInt || action[i+1].Int != want {
				return reject()
			}
		}
		pairs, _ := doc.Section("stackable action pair indexes")
		if len(pairs) == 0 || len(pairs)%2 != 0 {
			return reject()
		}
		seen := make(map[int64]bool)
		for i := 0; i < len(pairs); i += 2 {
			a, b := pairs[i], pairs[i+1]
			if a.Kind != dnfpvf.TokenInt || b.Kind != dnfpvf.TokenInt || a.Int <= 0 || b.Int <= 0 || a.Int > math.MaxUint32 || b.Int > math.MaxUint32 || a.Int == b.Int || seen[a.Int] {
				return reject()
			}
			seen[a.Int] = true
			if target.ItemID == a.Int {
				p.OutputItemID = b.Int
			}
		}
		if p.OutputItemID == 0 {
			return reject()
		}
		from, e1 := catalog.ResolveItem(uint32(target.ItemID))
		to, e2 := catalog.ResolveItem(uint32(p.OutputItemID))
		if e1 != nil || e2 != nil || from.Kind != dungeonDropItemEquipment || to.Kind != dungeonDropItemEquipment || from.EquipmentType != "[weapon]" || to.EquipmentType != "[weapon]" || from.UsablePeriodDays <= 0 || to.UsablePeriodDays != 0 || !to.ExpirationDate.IsZero() || to.DailyDeleteItem {
			return reject()
		}
		if v := target.Extra["pvf_path"]; v != "" && !strings.EqualFold(strings.ReplaceAll(v, "\\", "/"), from.PVFPath) {
			return reject()
		}
		if v := target.Extra["item_kind"]; v != "" && v != "equipment" {
			return reject()
		}
		if currentItemListStackExpire(target) == 0 {
			return reject()
		}
		p.OutputPVFPath, p.TargetSlots = to.PVFPath, currentPVFItemPlacementPolicy(from)
	default:
		return reject()
	}
	source, _ = applyCurrentPVFItemExpirationAt(source, d, now)
	if err := validateCurrentBoosterSourceExpiration(source, d, now); err != nil {
		return p, source, target, err
	}
	return p, source, target, nil
}

// This one virtual booster reward has a verified storage meaning. Do not
// interpret arbitrary low-number rewards as capacities or grant fake items.
func currentEventSilverCargoDefinition(d dungeonDropItemDefinition, doc *dnfpvf.Document) bool {
	if d.Kind != dungeonDropItemStackable || d.Name != "银质金库升级工具" || d.StackableType != "[booster]" {
		return false
	}
	groups, err := parseMagicBoxBoosterGroups(doc)
	if err != nil || len(groups) != 1 || groups[0].DrawCount != 1 || len(groups[0].Entries) != 1 {
		return false
	}
	r := groups[0].Entries[0]
	return r.ItemID == 110 && r.Count == 1 && r.Weight == 30000
}

func (s *Service) currentEventCargoSource(session *gameSession, slot int16) bool {
	g, ok := s.repositoryGroup()
	if !ok || g.Inventory == nil || session == nil || slot < 0 {
		return false
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	bag, found, err := g.Inventory.Load(ctx, fmt.Sprint(session.selectedCharacterID))
	if err != nil || !found {
		return false
	}
	stack := bag.Slots[fmt.Sprintf("0:%d", slot)]
	if stack.ItemID <= 0 || stack.ItemID > math.MaxUint32 {
		return false
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil {
		return false
	}
	d, err := catalog.ResolveItem(uint32(stack.ItemID))
	return err == nil && d.Name == "银质金库升级工具" && d.Kind == dungeonDropItemStackable
}

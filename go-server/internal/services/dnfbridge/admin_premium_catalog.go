package dnfbridge

import (
	"sort"
	"strings"

	"longheng.io/server/internal/modules/dnf/premium"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// GM account membership is separate from an item activation catalog. The
// premium domain already supports administrative black-diamond membership;
// do not invent a consumable item mapping to make that service available.
func (s *Service) currentGMAdministrativePremiumTypes() ([]int64, error) {
	catalog, err := s.currentPremiumCatalog()
	if err != nil {
		return nil, err
	}
	seen := map[int64]bool{}
	for _, contract := range catalog.contractsByItem {
		if contract.DurationSeconds > 0 {
			seen[contract.PremiumType] = true
		}
	}
	s.initialEquipmentMu.Lock()
	archive, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err == nil && currentGMBlackDiamondMachineAvailable(archive) {
		seen[premium.TypeBlackDiamond] = true
	}
	types := make([]int64, 0, len(seen))
	for typ := range seen {
		types = append(types, typ)
	}
	sort.Slice(types, func(i, j int) bool { return types[i] < types[j] })
	return types, nil
}

// The administrative capability is backed by a real registered normal-coin
// machine and its real PVF currency. A missing dependency disables only this
// additional capability, preserving the existing registered contract types.
func currentGMBlackDiamondMachineAvailable(source dnfpvf.Source) bool {
	if source == nil {
		return false
	}
	items, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		return false
	}
	coin, err := items.ResolveItem(7454)
	if err != nil || coin.Kind != dungeonDropItemStackable || coin.StackableType != "[material]" {
		return false
	}
	raw, err := source.ReadText("etc/vendingmachine.lst")
	if err != nil {
		return false
	}
	list, err := dnfpvf.Parse("etc/vendingmachine.lst", raw)
	if err != nil {
		return false
	}
	for _, entry := range dnfpvf.ParseList(list) {
		// The current native machine ID is list index+1; use its registered table.
		if entry.ID != 1 && entry.ID != 6 {
			continue
		}
		path := "etc/" + strings.TrimPrefix(strings.ReplaceAll(entry.Path, "\\", "/"), "etc/")
		text, err := source.ReadText(path)
		if err != nil {
			continue
		}
		document, err := dnfpvf.Parse(path, text)
		if err != nil {
			continue
		}
		if quantity, _, ok := parseVendingGroup(document, 1, 7454); ok && quantity > 0 {
			return true
		}
	}
	return false
}

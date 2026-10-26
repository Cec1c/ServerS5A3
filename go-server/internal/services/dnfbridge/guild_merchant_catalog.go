package dnfbridge

import (
	"math"
	"strings"

	"longheng.io/server/internal/modules/dnf/guild"
	"longheng.io/server/internal/modules/dnf/pvf"
)

// Resolve the native chain: agitwar shop list -> base NPC upgrade level ->
// upgraded NPC item-shop role -> shop NPC and items -> item AP and deadline.
func loadGuildMerchantPurchaseCatalog(items *pvfDungeonDropCatalog) (*guild.MerchantPurchaseCatalog, error) {
	if items == nil {
		return nil, guild.ErrMerchantCatalog
	}
	read := func(root, path string) (*pvf.Document, error) {
		p, text, err := readDungeonDropText(items.source, root, path)
		if err != nil {
			return nil, err
		}
		return pvf.Parse(p, text)
	}
	etc, err := read("etc", "agitwar.etc")
	if err != nil {
		return nil, err
	}
	allowed := map[uint32]bool{}
	for _, id := range etc.Ints("shop list") {
		if id < 201 || id > 220 || allowed[uint32(id)] {
			return nil, guild.ErrMerchantCatalog
		}
		allowed[uint32(id)] = true
	}
	if len(allowed) != 20 {
		return nil, guild.ErrMerchantCatalog
	}
	npcPaths, _, err := loadDungeonDropList(items.source, "npc/npc.lst")
	if err != nil {
		return nil, err
	}
	shopPaths, _, err := loadDungeonDropList(items.source, currentNPCShopItemListPath)
	if err != nil {
		return nil, err
	}
	catalog := &guild.MerchantPurchaseCatalog{Shops: map[uint32]guild.MerchantShop{}}
	for index, base := range []uint32{390, 389} {
		baseDoc, err := read("npc", npcPaths[int64(base)])
		if err != nil {
			return nil, err
		}
		kind, _ := baseDoc.Text("guild agit shop npc")
		if kind != []string{"defence", "mercenary"}[index] {
			return nil, guild.ErrMerchantCatalog
		}
		upgrade, found := baseDoc.Section("upgrade")
		if !found || len(upgrade) != 21 || upgrade[0].Value != "guild agit" {
			return nil, guild.ErrMerchantCatalog
		}
		seenLevels := map[uint8]bool{}
		for i := 1; i < len(upgrade); i += 2 {
			if upgrade[i].Kind != pvf.TokenInt || upgrade[i+1].Kind != pvf.TokenInt || upgrade[i].Int < 1 || upgrade[i].Int > 10 || upgrade[i+1].Int <= 0 || upgrade[i+1].Int > math.MaxUint32 {
				return nil, guild.ErrMerchantCatalog
			}
			level, npcID := uint8(upgrade[i].Int), uint32(upgrade[i+1].Int)
			if seenLevels[level] {
				return nil, guild.ErrMerchantCatalog
			}
			seenLevels[level] = true
			npc, err := read("npc", npcPaths[int64(npcID)])
			if err != nil {
				return nil, err
			}
			role, found := npc.Section("role")
			if !found || len(role) != 2 || role[0].Value != "[item shop]" || role[1].Kind != pvf.TokenInt || role[1].Int < 201 || role[1].Int > 220 {
				return nil, guild.ErrMerchantCatalog
			}
			shopID := uint32(role[1].Int)
			if !allowed[shopID] {
				return nil, guild.ErrMerchantCatalog
			}
			if _, exists := catalog.Shops[shopID]; exists {
				return nil, guild.ErrMerchantCatalog
			}
			doc, err := read("itemshop", shopPaths[int64(shopID)])
			if err != nil {
				return nil, err
			}
			npcAtShop, _ := doc.Int("NPC")
			kind, _ := doc.Text("type")
			if npcAtShop != int64(npcID) || kind != "[agitwar build shop]" {
				return nil, guild.ErrMerchantCatalog
			}
			shop := guild.MerchantShop{ID: shopID, NPCID: npcID, BaseNPCID: base, Index: uint8(index), Level: level, Products: map[uint32]guild.MerchantProduct{}}
			for _, section := range doc.Sections {
				if strings.ToLower(strings.TrimSpace(section.Name)) != "item list" {
					continue
				}
				for _, token := range doc.Tokens[section.Start:section.End] {
					if token.Kind != pvf.TokenInt || token.Int <= 0 || token.Int > math.MaxUint32 {
						return nil, guild.ErrMerchantCatalog
					}
					id := uint32(token.Int)
					def, err := items.ResolveItem(id)
					if err != nil {
						return nil, err
					}
					if def.Kind != dungeonDropItemStackable || !def.DailyDeleteItem || def.UsablePeriodDays != 0 || !def.ExpirationDate.IsZero() {
						return nil, guild.ErrMerchantCatalog
					}
					item, err := parseDungeonCardPVFDocument(items.source, def.PVFPath)
					if err != nil {
						return nil, err
					}
					ap, apOK := item.Int("require ap")
					limit, limitOK := item.Int("stack limit")
					min, minOK := item.Int("minimum level")
					if !apOK || ap <= 0 || ap > math.MaxUint32 || !limitOK || limit <= 0 || limit > math.MaxInt32 || !minOK || min < 0 || min > math.MaxInt32 {
						return nil, guild.ErrMerchantCatalog
					}
					shop.Products[id] = guild.MerchantProduct{ItemID: id, AP: uint32(ap), StackLimit: uint32(limit), MinimumLevel: int(min)}
				}
			}
			catalog.Shops[shopID] = shop
		}
	}
	if err := catalog.Validate(); err != nil {
		return nil, err
	}
	return catalog, nil
}

func (s *Service) currentGuildMerchantPurchaseCatalog(items *pvfDungeonDropCatalog) (*guild.MerchantPurchaseCatalog, error) {
	s.guildDungeonCatalogMu.Lock()
	defer s.guildDungeonCatalogMu.Unlock()
	if s.guildMerchantPurchaseCatalog != nil {
		return s.guildMerchantPurchaseCatalog, nil
	}
	catalog, err := loadGuildMerchantPurchaseCatalog(items)
	if err == nil {
		s.guildMerchantPurchaseCatalog = catalog
	}
	return catalog, err
}

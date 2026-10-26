package dnfbridge

import (
	"strings"

	"longheng.io/server/internal/modules/dnf/guild"
	"longheng.io/server/internal/modules/dnf/pvf"
)

type guildPlacementCatalog struct {
	books        map[uint32]guild.PlacementBook
	buildSeconds uint32
}

func (s *Service) currentGuildPlacementCatalog(items *pvfDungeonDropCatalog) (*guildPlacementCatalog, error) {
	merchants, err := s.currentGuildMerchantPurchaseCatalog(items)
	if err != nil {
		return nil, err
	}
	s.guildDungeonCatalogMu.Lock()
	defer s.guildDungeonCatalogMu.Unlock()
	if s.guildPlacementCatalog != nil {
		return s.guildPlacementCatalog, nil
	}
	result, err := loadGuildPlacementCatalog(items, merchants)
	if err == nil {
		s.guildPlacementCatalog = result
	}
	return result, err
}

func loadGuildPlacementCatalog(items *pvfDungeonDropCatalog, merchants *guild.MerchantPurchaseCatalog) (*guildPlacementCatalog, error) {
	if items == nil || merchants == nil {
		return nil, guild.ErrPlacementCatalog
	}
	paths, _, err := loadDungeonDropList(items.source, "passiveobject/passiveobject.lst")
	if err != nil {
		return nil, err
	}
	_, text, err := readDungeonDropText(items.source, "etc", "agitwar.etc")
	if err != nil {
		return nil, err
	}
	etc, err := pvf.Parse("etc/agitwar.etc", text)
	if err != nil {
		return nil, err
	}
	seconds, ok := etc.Int("building construct time")
	if !ok || seconds <= 0 || seconds > 86400 {
		return nil, guild.ErrPlacementCatalog
	}
	c := &guildPlacementCatalog{books: map[uint32]guild.PlacementBook{}, buildSeconds: uint32(seconds)}
	for _, shop := range merchants.Shops {
		for id := range shop.Products {
			if _, ok := c.books[id]; ok {
				continue
			}
			def, err := items.ResolveItem(id)
			if err != nil {
				return nil, err
			}
			stk, err := parseDungeonCardPVFDocument(items.source, def.PVFPath)
			if err != nil {
				return nil, err
			}
			a, ok := stk.Section("action type")
			if !ok || len(a) != 4 || a[0].Value != "[deploy in isometric cell map]" || a[1].Kind != pvf.TokenInt {
				return nil, guild.ErrPlacementCatalog
			}
			path, ok := paths[a[1].Int]
			if !ok {
				return nil, guild.ErrPlacementCatalog
			}
			path, text, err := readDungeonDropText(items.source, "passiveobject", path)
			if err != nil {
				return nil, err
			}
			obj, err := pvf.Parse(path, text)
			if err != nil {
				return nil, err
			}
			b, err := guild.ParsePlacementBook(id, stk, obj)
			if err != nil {
				return nil, err
			}
			if err = b.Validate(); err != nil {
				return nil, err
			}
			c.books[id] = b
		}
	}
	if len(c.books) == 0 {
		return nil, guild.ErrPlacementCatalog
	}
	return c, nil
}

func loadCurrentGuildPlacementMap(source pvf.Source, path string) (*guild.PlacementMap, error) {
	p := strings.ToLower(strings.ReplaceAll(path, "\\", "/"))
	if !strings.HasPrefix(strings.TrimPrefix(p, "map/"), "guildagit/") {
		return nil, guild.ErrPlacementPosition
	}
	path, text, err := readDungeonDropText(source, "map", path)
	if err != nil {
		return nil, err
	}
	doc, err := pvf.Parse(path, text)
	if err != nil {
		return nil, err
	}
	return guild.ParsePlacementMap(doc)
}

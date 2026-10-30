package dnfbridge

import (
	"errors"
	"fmt"
	"math"
	"strings"

	dnfnpcshop "longheng.io/server/internal/modules/dnf/npcshop"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

const (
	currentNPCShopItemListPath   = "itemshop/itemshop.lst"
	currentNPCShopPriceTablePath = "equipment/pricetable.tbl"
)

var (
	errCurrentNPCShopCatalogUnavailable = errors.New("dnf current NPC shop PVF catalog is unavailable")
	errCurrentNPCShopProductUnavailable = errors.New("dnf current NPC shop product is unavailable")
)

type currentNPCShopCatalog struct {
	source                    dnfpvf.Source
	buyableItems              map[uint32]struct{}
	npcItems                  map[uint32]map[uint32]struct{}
	shopItems                 map[uint32]map[uint32]struct{}
	shopNPCs                  map[uint32]uint32
	tradingProducts           map[uint32]map[uint32]currentNPCTradingProduct
	equipmentSellRatePermille int64
	listedShopCount           int
	loadedShopCount           int
	missingShopCount          int
	firstMissingShopPath      string
}

type currentNPCShopPricing struct {
	PurchaseLimitScope  string
	PurchaseLimitPeriod string
	PurchaseLimitValue  int64
	PurchaseLimitReset  bool
	Definition          dungeonDropItemDefinition
	BuyGold             int64
	SellGold            int64
	Buyable             bool
	MaterialExchange    bool
	NeedMaterialItem    uint32
	NeedMaterialCount   int64
	// ExchangeGold is the explicit [price] charged alongside [need material].
	// A sell [value] alone must not turn a material-only exchange into a gold sale.
	ExchangeGold int64
}

func newCurrentNPCShopCatalog(source dnfpvf.Source) (*currentNPCShopCatalog, error) {
	if source == nil {
		return nil, errCurrentNPCShopCatalogUnavailable
	}
	shopPaths, _, err := loadDungeonDropList(source, currentNPCShopItemListPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errCurrentNPCShopCatalogUnavailable, err)
	}
	buyable := make(map[uint32]struct{})
	npcItems := make(map[uint32]map[uint32]struct{})
	shopItems := make(map[uint32]map[uint32]struct{})
	shopNPCs := make(map[uint32]uint32)
	loadedShopCount := 0
	missingShopCount := 0
	firstMissingShopPath := ""
	for shopID, listedPath := range shopPaths {
		documentPath, text, readErr := readDungeonDropText(source, "itemshop", listedPath)
		if readErr != nil {
			if errors.Is(readErr, platformpvf.ErrFileNotFound) || errors.Is(readErr, dnfpvf.ErrDocNotFound) {
				missingShopCount++
				if firstMissingShopPath == "" {
					firstMissingShopPath = listedPath
				}
				continue
			}
			return nil, fmt.Errorf("%w: shop=%d path=%s: %v", errCurrentNPCShopCatalogUnavailable, shopID, listedPath, readErr)
		}
		document, parseErr := dnfpvf.Parse(documentPath, text)
		if parseErr != nil {
			return nil, fmt.Errorf("%w: shop=%d path=%s: %v", errCurrentNPCShopCatalogUnavailable, shopID, documentPath, parseErr)
		}
		loadedShopCount++
		shopItems[uint32(shopID)] = make(map[uint32]struct{})
		npcID, _ := document.Int("NPC")
		if npcID > 0 && npcID <= math.MaxUint32 {
			shopNPCs[uint32(shopID)] = uint32(npcID)
		}
		if npcID > 0 && npcID <= math.MaxUint32 && npcItems[uint32(npcID)] == nil {
			npcItems[uint32(npcID)] = make(map[uint32]struct{})
		}
		for _, section := range document.Sections {
			if strings.ToLower(strings.TrimSpace(section.Name)) != "item list" || section.Start < 0 || section.End > len(document.Tokens) || section.Start > section.End {
				continue
			}
			for _, token := range document.Tokens[section.Start:section.End] {
				if token.Kind == dnfpvf.TokenInt && token.Int > 0 && token.Int <= math.MaxUint32 {
					buyable[uint32(token.Int)] = struct{}{}
					shopItems[uint32(shopID)][uint32(token.Int)] = struct{}{}
					if npcID > 0 && npcID <= math.MaxUint32 {
						npcItems[uint32(npcID)][uint32(token.Int)] = struct{}{}
					}
				}
			}
		}
	}
	if len(buyable) == 0 {
		return nil, fmt.Errorf("%w: no [item list] entries", errCurrentNPCShopCatalogUnavailable)
	}
	priceTable, err := parseDungeonCardPVFDocument(source, currentNPCShopPriceTablePath)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", errCurrentNPCShopCatalogUnavailable, currentNPCShopPriceTablePath, err)
	}
	rates := priceTable.Ints("rate")
	if len(rates) < 1 || rates[0] <= 0 || rates[0] > math.MaxInt32 {
		return nil, fmt.Errorf("%w: %s [rate] is invalid", errCurrentNPCShopCatalogUnavailable, currentNPCShopPriceTablePath)
	}
	trading, err := loadCurrentNPCTradingProducts(source)
	if err != nil {
		return nil, fmt.Errorf("%w: trading catalog: %v", errCurrentNPCShopCatalogUnavailable, err)
	}
	return &currentNPCShopCatalog{
		tradingProducts:           trading,
		source:                    source,
		buyableItems:              buyable,
		npcItems:                  npcItems,
		shopItems:                 shopItems,
		shopNPCs:                  shopNPCs,
		equipmentSellRatePermille: rates[0],
		listedShopCount:           len(shopPaths),
		loadedShopCount:           loadedShopCount,
		missingShopCount:          missingShopCount,
		firstMissingShopPath:      firstMissingShopPath,
	}, nil
}

func (s *Service) currentNPCShopCatalog() (*currentNPCShopCatalog, error) {
	if s == nil {
		return nil, errCurrentNPCShopCatalogUnavailable
	}
	s.npcShopCatalogMu.Lock()
	defer s.npcShopCatalogMu.Unlock()
	if s.npcShopCatalog != nil || s.npcShopCatalogLoadErr != nil {
		return s.npcShopCatalog, s.npcShopCatalogLoadErr
	}
	s.initialEquipmentMu.Lock()
	archive, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err == nil {
		s.npcShopCatalog, err = newCurrentNPCShopCatalog(archive)
	}
	s.npcShopCatalogLoadErr = err
	if err == nil && s.npcShopCatalog != nil {
		s.logInfo("dnf NPC shop PVF catalog loaded",
			"listed_shops", s.npcShopCatalog.listedShopCount,
			"loaded_shops", s.npcShopCatalog.loadedShopCount,
			"missing_shops", s.npcShopCatalog.missingShopCount,
			"buyable_items", len(s.npcShopCatalog.buyableItems))
		if s.npcShopCatalog.missingShopCount > 0 {
			s.logWarn("dnf NPC shop PVF list contains missing shop files",
				"missing_shops", s.npcShopCatalog.missingShopCount,
				"first_missing_shop", s.npcShopCatalog.firstMissingShopPath)
		}
	}
	return s.npcShopCatalog, s.npcShopCatalogLoadErr
}

func resolveCurrentNPCShopPricing(shop *currentNPCShopCatalog, items *pvfDungeonDropCatalog, itemID uint32) (currentNPCShopPricing, error) {
	if shop == nil || shop.source == nil || items == nil || itemID == 0 {
		return currentNPCShopPricing{}, errCurrentNPCShopCatalogUnavailable
	}
	definition, err := items.ResolveItem(itemID)
	if err != nil {
		return currentNPCShopPricing{}, fmt.Errorf("%w: item=%d: %v", errCurrentNPCShopProductUnavailable, itemID, err)
	}
	document, err := parseDungeonCardPVFDocument(shop.source, definition.PVFPath)
	if err != nil {
		return currentNPCShopPricing{}, fmt.Errorf("%w: item=%d path=%s: %v", errCurrentNPCShopProductUnavailable, itemID, definition.PVFPath, err)
	}
	price, priceFound := document.Int("price")
	value, valueFound := document.Int("value")
	buyGold := int64(0)
	if priceFound && price >= 0 {
		buyGold = price
	} else if valueFound && value >= 0 {
		buyGold = value
	}
	if buyGold > math.MaxInt32 {
		return currentNPCShopPricing{}, fmt.Errorf("%w: item=%d buy_gold=%d", errCurrentNPCShopProductUnavailable, itemID, buyGold)
	}

	sellGold := int64(0)
	switch definition.Kind {
	case dungeonDropItemEquipment:
		base := buyGold
		if valueFound && value >= 0 {
			base = value
		}
		if base > math.MaxInt64/shop.equipmentSellRatePermille {
			return currentNPCShopPricing{}, fmt.Errorf("%w: item=%d sell price overflow", errCurrentNPCShopProductUnavailable, itemID)
		}
		sellGold = base * shop.equipmentSellRatePermille / 1000
		if sellGold < 1 {
			sellGold = 1
		}
	case dungeonDropItemStackable:
		if valueFound && value >= 0 {
			sellGold = value / 5
		} else if priceFound && price > 0 {
			sellGold = price / 5
		}
	default:
		return currentNPCShopPricing{}, fmt.Errorf("%w: item=%d kind=%s", errCurrentNPCShopProductUnavailable, itemID, definition.Kind)
	}
	if sellGold < 0 || sellGold > math.MaxInt32 {
		return currentNPCShopPricing{}, fmt.Errorf("%w: item=%d sell_gold=%d", errCurrentNPCShopProductUnavailable, itemID, sellGold)
	}
	needMaterial := document.Ints("need material")
	materialExchange := len(needMaterial) >= 2 && needMaterial[0] > 0 && needMaterial[0] <= math.MaxUint32 && needMaterial[1] > 0
	_, listed := shop.buyableItems[itemID]
	pricing := currentNPCShopPricing{
		Definition:       definition,
		BuyGold:          buyGold,
		SellGold:         sellGold,
		MaterialExchange: materialExchange,
	}
	// Current client shape: [purchase limit] <scope> <period> <count>, for
	// example "account weekly 70" as declared by the Epic Road talisman
	// 490010214 in itemshop/2018_epicroad_kairyu.shp (NPC 3136). An unknown
	// scope or period fails closed instead of silently dropping the limit.
	if tokens, found := document.Section("purchase limit"); found {
		if len(tokens) != 3 || tokens[0].Kind != dnfpvf.TokenString || tokens[1].Kind != dnfpvf.TokenString ||
			tokens[2].Kind != dnfpvf.TokenInt ||
			(tokens[0].Value != "character" && tokens[0].Value != "account") ||
			(tokens[1].Value != "daily" && tokens[1].Value != "weekly") ||
			tokens[2].Int <= 0 || tokens[2].Int > math.MaxInt32 {
			return currentNPCShopPricing{}, fmt.Errorf("%w: invalid purchase limit item=%d", errCurrentNPCShopProductUnavailable, itemID)
		}
		pricing.PurchaseLimitScope, pricing.PurchaseLimitPeriod, pricing.PurchaseLimitValue = tokens[0].Value, tokens[1].Value, tokens[2].Int
		_, pricing.PurchaseLimitReset = document.Section("reset daily purchase item")
	} else if tokens, found := document.Section("daily purchase limit"); found {
		// Legacy shape: <scope> <count>; Period stays empty so the historical ResetDaily semantics apply.
		if len(tokens) != 2 || tokens[0].Kind != dnfpvf.TokenString || tokens[1].Kind != dnfpvf.TokenInt ||
			(tokens[0].Value != "character" && tokens[0].Value != "account") || tokens[1].Int <= 0 || tokens[1].Int > math.MaxInt32 {
			return currentNPCShopPricing{}, fmt.Errorf("%w: invalid daily purchase limit item=%d", errCurrentNPCShopProductUnavailable, itemID)
		}
		pricing.PurchaseLimitScope, pricing.PurchaseLimitPeriod, pricing.PurchaseLimitValue = tokens[0].Value, "", tokens[1].Int
		_, pricing.PurchaseLimitReset = document.Section("reset daily purchase item")
	}
	if materialExchange {
		pricing.NeedMaterialItem = uint32(needMaterial[0])
		pricing.NeedMaterialCount = needMaterial[1]
		if priceFound {
			if price < 0 {
				return currentNPCShopPricing{}, fmt.Errorf("%w: item=%d negative exchange price", errCurrentNPCShopProductUnavailable, itemID)
			}
			pricing.ExchangeGold = price
		}
	}
	// Material goods may also declare an explicit gold price (e.g. Luke raid
	// equipment). Both costs belong to the same purchase transaction.
	pricing.Buyable = listed && (materialExchange || buyGold > 0)
	return pricing, nil
}

func currentNPCShopContainsItem(catalog *currentNPCShopCatalog, itemID uint32) bool {
	if catalog == nil {
		return false
	}
	_, found := catalog.buyableItems[itemID]
	return found
}

// A global union of every itemshop shelf is useful for indexing, but is not
// purchase authorization. All four op21 fields are client-controlled; bind
// the requested item to the exact PVF shop and its declared NPC before any
// payment or grant. Trading-system NPCs have no itemshop.lst shop number and
// use their own actor-scoped catalog instead.
func validateCurrentNPCShopBuyShelf(shop *currentNPCShopCatalog, request currentNPCShopBuyRequest) error {
	if shop == nil || request.ItemID == 0 {
		return errCurrentNPCShopProductUnavailable
	}
	if shelf, known := shop.shopItems[request.ShopContext]; known {
		if _, listed := shelf[request.ItemID]; !listed {
			return fmt.Errorf("%w: item=%d shop=%d shelf_miss", errCurrentNPCShopProductUnavailable, request.ItemID, request.ShopContext)
		}
		// The native client sends actor=0 for some itemshop tabs.
		// A nonzero actor must match the declared PVF NPC.
		if npc := shop.shopNPCs[request.ShopContext]; npc != 0 && request.ActorContext != 0 && npc != request.ActorContext {
			return fmt.Errorf("%w: item=%d shop=%d actor_mismatch", errCurrentNPCShopProductUnavailable, request.ItemID, request.ShopContext)
		}
		return nil
	}
	if _, listed := shop.tradingProducts[request.ActorContext][request.ItemID]; listed {
		return nil
	}
	return fmt.Errorf("%w: item=%d shop=%d actor=%d unknown_shelf", errCurrentNPCShopProductUnavailable, request.ItemID, request.ShopContext, request.ActorContext)
}

func currentNPCShopPurchaseLimit(pricing currentNPCShopPricing, count uint32) *dnfnpcshop.PurchaseLimit {
	if pricing.PurchaseLimitValue <= 0 {
		return nil
	}
	return &dnfnpcshop.PurchaseLimit{ItemID: pricing.Definition.ItemID, Scope: pricing.PurchaseLimitScope, Period: pricing.PurchaseLimitPeriod, Maximum: pricing.PurchaseLimitValue, ResetDaily: pricing.PurchaseLimitReset, Count: int64(count)}
}

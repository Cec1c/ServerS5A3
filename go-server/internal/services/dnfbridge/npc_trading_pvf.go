package dnfbridge

import (
	"errors"
	"fmt"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"math"
	"strings"
)

type currentNPCTradingProduct struct {
	BuyGold  int64
	SellGold int64
}

// Trading NPCs are defined outside itemshop.lst. Keep their membership scoped
// to the actual actor from tradingsystem.etc rather than making every item buyable.
func loadCurrentNPCTradingProducts(source dnfpvf.Source) (map[uint32]map[uint32]currentNPCTradingProduct, error) {
	doc, err := parseDungeonCardPVFDocument(source, "etc/tradingsystem.etc")
	if errors.Is(err, platformpvf.ErrFileNotFound) || errors.Is(err, dnfpvf.ErrDocNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	result := make(map[uint32]map[uint32]currentNPCTradingProduct)
	var npc uint32
	tokens := doc.Tokens
	for i := 0; i < len(tokens); i++ {
		if tokens[i].Kind != dnfpvf.TokenSection {
			continue
		}
		section := strings.ToLower(strings.TrimSpace(tokens[i].Value))
		if section == "" {
			section = strings.Trim(strings.ToLower(tokens[i].Raw), "[]")
		}
		switch section {
		case "trade item list":
			if i+1 >= len(tokens) || tokens[i+1].Kind != dnfpvf.TokenInt || tokens[i+1].Int <= 0 || tokens[i+1].Int > math.MaxUint32 {
				return nil, fmt.Errorf("invalid trading NPC")
			}
			i++
			npc = uint32(tokens[i].Int)
			if result[npc] == nil {
				result[npc] = make(map[uint32]currentNPCTradingProduct)
			}
		case "/trade item list":
			npc = 0
		case "item list":
			if npc == 0 {
				continue
			}
			for i+1 < len(tokens) && tokens[i+1].Kind != dnfpvf.TokenSection {
				if i+4 >= len(tokens) {
					return nil, fmt.Errorf("incomplete trading item row")
				}
				row := tokens[i+1 : i+5]
				if row[0].Kind != dnfpvf.TokenInt || row[0].Int <= 0 || row[0].Int > math.MaxUint32 || row[1].Kind != dnfpvf.TokenInt || row[1].Int <= 0 || row[3].Kind != dnfpvf.TokenInt {
					return nil, fmt.Errorf("invalid trading item row")
				}
				multiplier := row[2].Float
				if row[2].Kind == dnfpvf.TokenInt {
					multiplier = float64(row[2].Int)
				} else if row[2].Kind != dnfpvf.TokenFloat {
					return nil, fmt.Errorf("invalid trading price multiplier")
				}
				price := math.Round(float64(row[1].Int) * multiplier)
				if math.IsNaN(price) || math.IsInf(price, 0) || price <= 0 || price > math.MaxInt32 || row[1].Int > math.MaxInt32 {
					return nil, fmt.Errorf("invalid trading price")
				}
				result[npc][uint32(row[0].Int)] = currentNPCTradingProduct{BuyGold: int64(price), SellGold: row[1].Int}
				i += 4
			}
		case "shop item":
			if npc == 0 {
				continue
			}
			for i+1 < len(tokens) && tokens[i+1].Kind != dnfpvf.TokenSection {
				i++
				if tokens[i].Kind != dnfpvf.TokenInt || tokens[i].Int <= 0 || tokens[i].Int > math.MaxUint32 {
					return nil, fmt.Errorf("invalid trading shop item")
				}
				result[npc][uint32(tokens[i].Int)] = currentNPCTradingProduct{}
			}
		}
	}
	return result, nil
}

func resolveCurrentNPCShopActorPricing(shop *currentNPCShopCatalog, items *pvfDungeonDropCatalog, itemID, actorID uint32) (currentNPCShopPricing, error) {
	pricing, err := resolveCurrentNPCShopPricing(shop, items, itemID)
	if err != nil {
		return pricing, err
	}
	if product, listed := shop.tradingProducts[actorID][itemID]; listed {
		if product.BuyGold > 0 {
			pricing.BuyGold, pricing.SellGold = product.BuyGold, product.SellGold
		}
		pricing.Buyable = pricing.MaterialExchange || pricing.BuyGold > 0
	}
	return pricing, nil
}

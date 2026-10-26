package guild

import (
	"errors"
	"math"
	"strings"

	"longheng.io/server/internal/modules/dnf/pvf"
)

var (
	ErrHongbaoCatalog        = errors.New("invalid hongbao catalog")
	ErrHongbaoRequest        = errors.New("invalid hongbao request")
	ErrHongbaoIdentity       = errors.New("hongbao operation identity conflicts")
	ErrHongbaoMemberCount    = errors.New("hongbao requires more guild members")
	ErrHongbaoPurchaseLimit  = errors.New("hongbao daily purchase limit reached")
	ErrHongbaoClaimLimit     = errors.New("hongbao account daily claim limit reached")
	ErrHongbaoItem           = errors.New("eligible hongbao inventory item not found")
	ErrHongbaoUnavailable    = errors.New("hongbao is unavailable")
	ErrHongbaoAlreadyClaimed = errors.New("account already claimed this hongbao")
	ErrHongbaoListFull       = errors.New("hongbao visible list is full")
	ErrHongbaoNotReady       = errors.New("hongbao is not ready for settlement")
	ErrHongbaoState          = errors.New("invalid persisted hongbao state")
)

type HongbaoProduct struct {
	ItemID                  int64
	CommodityID             uint32
	Price, ContributionRate uint32
}
type HongbaoCatalog struct {
	products                              []HongbaoProduct
	minMembers, purchaseLimit, claimLimit uint32
}

func LoadHongbaoCatalog(source pvf.Source) (*HongbaoCatalog, error) {
	if source == nil {
		return nil, ErrRepositoryUnavailable
	}
	text, err := source.ReadText(ContributionCatalogPath)
	if err != nil {
		return nil, err
	}
	return ParseHongbaoCatalog(text)
}

func ParseHongbaoCatalog(text string) (*HongbaoCatalog, error) {
	doc, err := pvf.Parse(ContributionCatalogPath, text)
	if err != nil {
		return nil, err
	}
	c := &HongbaoCatalog{}
	seen := map[string]bool{}
	for _, section := range doc.Sections {
		name := strings.ToLower(strings.TrimSpace(section.Name))
		switch name {
		case "hongbao", "min guild member count", "max total cera per day", "max take count per day":
		default:
			continue
		}
		if seen[name] {
			return nil, ErrHongbaoCatalog
		}
		seen[name] = true
		tokens := doc.Tokens[section.Start:section.End]
		for _, t := range tokens {
			if t.Kind != pvf.TokenInt || t.Int <= 0 || t.Int > math.MaxInt32 {
				return nil, ErrHongbaoCatalog
			}
		}
		if name == "hongbao" {
			if len(tokens) != 16 {
				return nil, ErrHongbaoCatalog
			}
			for i := 0; i < len(tokens); i += 4 {
				p := HongbaoProduct{tokens[i].Int, uint32(tokens[i+1].Int), uint32(tokens[i+2].Int), uint32(tokens[i+3].Int)}
				if !validHongbaoProduct(p) {
					return nil, ErrHongbaoCatalog
				}
				if _, ok := c.ProductByItem(p.ItemID); ok {
					return nil, ErrHongbaoCatalog
				}
				if _, ok := c.ProductByCommodity(p.CommodityID); ok {
					return nil, ErrHongbaoCatalog
				}
				c.products = append(c.products, p)
			}
		} else {
			if len(tokens) != 1 {
				return nil, ErrHongbaoCatalog
			}
			v := uint32(tokens[0].Int)
			switch name {
			case "min guild member count":
				c.minMembers = v
			case "max total cera per day":
				c.purchaseLimit = v
			case "max take count per day":
				c.claimLimit = v
			}
		}
	}
	if len(seen) != 4 || c.minMembers > DefaultMemberLimit || c.claimLimit > math.MaxUint8 {
		return nil, ErrHongbaoCatalog
	}
	for _, p := range c.products {
		if p.Price > c.purchaseLimit {
			return nil, ErrHongbaoCatalog
		}
	}
	return c, nil
}
func validHongbaoProduct(p HongbaoProduct) bool {
	return p.ItemID > 0 && p.ItemID <= math.MaxInt32 && p.CommodityID > 0 && p.Price >= 30 && p.Price <= math.MaxInt32 && p.ContributionRate > 0 && p.ContributionRate <= 100
}
func (c *HongbaoCatalog) ProductByCommodity(id uint32) (HongbaoProduct, bool) {
	if c != nil {
		for _, p := range c.products {
			if p.CommodityID == id {
				return p, true
			}
		}
	}
	return HongbaoProduct{}, false
}
func (c *HongbaoCatalog) ProductByItem(id int64) (HongbaoProduct, bool) {
	if c != nil {
		for _, p := range c.products {
			if p.ItemID == id {
				return p, true
			}
		}
	}
	return HongbaoProduct{}, false
}
func (c *HongbaoCatalog) Products() []HongbaoProduct {
	if c == nil {
		return nil
	}
	return append([]HongbaoProduct(nil), c.products...)
}

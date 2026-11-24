package dnfbridge

import (
	"os"
	"path/filepath"
	"testing"

	"longheng.io/server/internal/modules/dnf/festival"
	"longheng.io/server/internal/modules/dnf/pvf"
)

// 11-10 live PVF 0006cfe4 removed 462 packages from etc/cerashop.etc while
// the festival table still listed them; the catalog error made every Cera
// shop purchase fail. Missing or non-paid rows now leave the whitelist only.
func TestPrivateFestivalCatalogSkipsCommoditiesMissingFromShop(t *testing.T) {
	path := filepath.Join("..", "..", "..", "..", "resources", "festival", "chn_cerashop_multibonus.fragment.etc")
	text, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := bridgePVFSource{festival.PVFPath: string(text)}
	full, skipped, err := parsePrivateFestivalCatalog(source, map[uint32]currentCeraShopProduct{})
	if err != nil || full == nil || skipped != 273 || len(full.Commodities) != 0 {
		t.Fatalf("empty shop: catalog=%v skipped=%d err=%v", full != nil, skipped, err)
	}

	doc, err := pvf.Parse(festival.PVFPath, string(text))
	if err != nil {
		t.Fatal(err)
	}
	whole, err := festival.Parse(doc)
	if err != nil {
		t.Fatal(err)
	}
	var keep, free uint32
	for id := range whole.Commodities {
		if keep == 0 {
			keep = id
		} else if free == 0 {
			free = id
		}
	}
	products := map[uint32]currentCeraShopProduct{
		keep: {CommodityID: keep, Count: 1, CeraPrice: 27800},
		free: {CommodityID: free, Count: 1},
	}
	c, skipped, err := parsePrivateFestivalCatalog(source, products)
	if err != nil || skipped != 272 || len(c.Commodities) != 1 || c.Commodities[keep] != whole.Commodities[keep] {
		t.Fatalf("partial shop: skipped=%d commodities=%v err=%v", skipped, c.Commodities, err)
	}
	if len(c.Periods) != 6 {
		t.Fatalf("periods=%d", len(c.Periods))
	}
}

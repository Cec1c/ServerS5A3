package weaponskin

import (
	"testing"
)

func TestGeneratedCatalogRoundTrip(t *testing.T) {
	var jobs uint16
	for position, a := range Catalog() {
		jobs |= a.OwnerJobs
		if source, ok := FromSource(a.SourceID); !ok || source != a {
			t.Fatalf("找不到原武器 %d", a.SourceID)
		}
		if visual, ok := FromAppearance(a.ID); !ok || visual != a || catalogPosition[a.ID] != position {
			t.Fatalf("找不到外观 %d 的位图位置", a.ID)
		}
		for recipe, product := range a.Products {
			if row, ok := FromProduct(uint64(product), BeiyunRecipe(recipe)); !ok || row != a {
				t.Fatalf("成品 %d 无法还原来源", product)
			}
			if _, ok := FromProduct(uint64(product), BeiyunRecipe(1-recipe)); ok {
				t.Fatalf("成品 %d 在另一种配方也可登记", product)
			}
		}
	}
	if jobs != 0xffff || Catalog()[0].ID != SampleID {
		t.Fatalf("职业或既有别云剑位置丢失：%#04x", jobs)
	}
	boxing, ok := FromSource(102030192)
	if !ok || !boxing.Allows("1") || !boxing.Allows("7") || boxing.Allows("0") {
		t.Fatal("拳套未限定到男、女格斗家")
	}
	light, ok := FromSource(101040146)
	if !ok || !light.Allows("0") || !light.Allows("11") || light.Allows("1") {
		t.Fatal("光剑的外观职业范围错误")
	}
}

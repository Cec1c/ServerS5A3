package festival

import (
	"longheng.io/server/internal/modules/dnf/pvf"
	"os"
	"path/filepath"
	"testing"
)

func TestPublishedSixPeriodConfigFragment(t *testing.T) {
	path := filepath.Join("..", "..", "..", "..", "..", "resources", "festival", "chn_cerashop_multibonus.fragment.etc")
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	d, e := pvf.Parse(PVFPath, string(b))
	if e != nil {
		t.Fatal(e)
	}
	c, e := Parse(d)
	if e != nil {
		t.Fatal(e)
	}
	if c == nil || len(c.Periods) != 6 || len(c.Commodities) != 273 {
		t.Fatalf("catalog size mismatch: %+v", c)
	}
	for _, id := range []uint32{201601, 201605, 201607, 201701, 201801, 201807} {
		p, ok := c.Periods[id]
		if !ok || p.ID != id || p.Name == "" || len(p.Tiers) == 0 {
			t.Fatalf("period %d missing rules", id)
		}
		found := false
		for _, pid := range c.Commodities {
			found = found || pid == id
		}
		if !found {
			t.Fatalf("period %d has no products", id)
		}
	}
}

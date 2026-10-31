package dnfbridge

import (
	"context"
	"math"
	"reflect"
	"strings"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestPrivateVoid004AdditionalRowsAndMalformedTables(t *testing.T) {
	items := currentDisjointParseAdditional([]int64{0, 4, 3033, 3034, 3035, 3036, 1, 3166, 1, 3167, 0, 1, 3229, 1, 3332})
	values := []float64{0, .30, 8.70, 8, 5, 50, 50, 8, 10, .19, 5.65, 8, 20, 0, 1.30, 5.50, 6, 15, .19, 5.65, 8, 20}
	got := currentDisjointParseAdditionalConsts(values, items)
	want := []currentDisjointAdditionalConst{{}, {.30, 8.70, 8}, {50, 50, 8}, {.19, 5.65, 8}, {}, {1.30, 5.50, 6}, {.19, 5.65, 8}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("multiple disabled rarities lost alignment: %+v", got)
	}
	for _, bad := range [][]float64{nil, values[:len(values)-1], append(append([]float64{}, values...), 0), {math.NaN()}, {math.Inf(1)}, {-1}} {
		if currentDisjointParseAdditionalConsts(bad, items) != nil {
			t.Fatalf("accepted malformed table: %v", bad)
		}
	}
	for _, invalid := range []float64{math.NaN(), math.Inf(1), -1} {
		bad := append([]float64(nil), values...)
		bad[15] = invalid
		if currentDisjointParseAdditionalConsts(bad, items) != nil {
			t.Fatal("accepted nonfinite/negative enabled-row value")
		}
	}
	// Use the existing op26 transaction, not only a parser assertion.
	ctx := context.Background()
	original := dnfrepo.ItemStack{ItemID: 5000, Count: 1, Extra: map[string]string{"quality_seed": "fixed"}}
	repos, catalog := mustCurrentDisjointPlacementAssets(t, map[string]dnfrepo.ItemStack{"0:9": original})
	seedCurrentDisjointAccount(t, repos, nil)
	source := catalog.source.(bridgePVFSource)
	source["etc/disjoint.etc"] = strings.Replace(source["etc/disjoint.etc"], "0.19 5.65 8 20\n[/additional result const]", "0.19 5.65 8\n[/additional result const]", 1)
	before, _, _ := repos.Inventory.Load(ctx, "19")
	accountBefore, _, _ := repos.AccountInventory.Load(ctx, "account-1")
	conn := &bufferConn{}
	if err := currentDisjointTestService(repos, catalog).handleCurrentDisjointItem(&gameSession{conn: conn, selectedCharacterID: 19}, currentDisjointItemTestBody(9, 0, -1, 0)); err != nil {
		t.Fatal(err)
	}
	ack, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
	after, _, _ := repos.Inventory.Load(ctx, "19")
	accountAfter, _, _ := repos.AccountInventory.Load(ctx, "account-1")
	if len(ack.Body) == 0 || ack.Body[0] != 0 || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(accountBefore, accountAfter) {
		t.Fatal("malformed quantities destroyed equipment or changed assets")
	}
}

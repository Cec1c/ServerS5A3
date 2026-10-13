package dnfbridge

import (
	"context"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"reflect"
	"strings"
	"testing"
)

// These counted rows are copied from the matching PVF. Chronicle (rarity 5)
// has only a zero count, so legendary's item must remain at rarity 6.
func TestDisjointExpandRowsKeepLegendaryAfterDisabledChronicle(t *testing.T) {
	ids := []int64{1, 10100115, 1, 10100116, 1, 10099773, 1, 10099774, 1, 10158124, 0, 1, 10099775}
	values := []float64{25, 8.7, 100, 25, 8.7, 100, 20, 50, 100, 17, .19, 100, 9999, .19, 100, 0, 16, .19, 100}
	rows := currentDisjointParseExpands(ids, values)
	want := []uint32{10100115, 10100116, 10099773, 10099774, 10158124, 0, 10099775}
	if len(rows) != len(want) {
		t.Fatalf("lost rarity rows: %+v", rows)
	}
	for i, id := range want {
		if rows[i].ItemID != id || rows[i].Enabled != (id > 0) {
			t.Fatalf("rarity%d=%+v want item%d", i, rows[i], id)
		}
	}
	if rows[6].LevelDivisor != 16 || rows[6].NormalChance != 100 || rows[6].GreatChance != .19 {
		t.Fatalf("legendary probabilities shifted: %+v", rows[6])
	}
	if currentDisjointRarityExpandIndex(6) != 6 {
		t.Fatal("legendary selected the disabled chronicle row")
	}
	for _, bad := range []struct {
		ids    []int64
		values []float64
	}{
		{ids[:len(ids)-1], values}, {ids, values[:len(values)-1]}, {append(append([]int64{}, ids...), 1), values},
		{[]int64{2, 10099775, 10099774}, []float64{16, 0, 100}}, {[]int64{1, -1}, []float64{16, 0, 100}},
	} {
		if got := currentDisjointParseExpands(bad.ids, bad.values); got != nil {
			t.Fatalf("partial/malformed table accepted: %+v", got)
		}
	}
}

func TestRealPVFLegendaryDisjointCommitsSoulWarehouseAndRemovesOnlySource(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("requires matching real PVF")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	const itemID = 101020542
	definition, err := catalog.ResolveItem(itemID)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := parseDungeonCardPVFDocument(archive, definition.PVFPath)
	if err != nil {
		t.Fatal(err)
	}
	rarity, _ := doc.Int("rarity")
	if rarity != 6 {
		t.Fatalf("sample must be legendary: rarity%d path%s", rarity, definition.PVFPath)
	}
	config, err := loadCurrentDisjointPVFConfig(archive)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		rewards, err := currentEquipmentDisjointRewards(config, doc)
		if err != nil {
			t.Fatal(err)
		}
		var soul uint32
		for _, r := range rewards {
			if r.ItemID == 10099775 {
				soul += r.Count
			}
		}
		if soul == 0 {
			t.Fatalf("legendary disjoint omitted its PVF soul: %+v", rewards)
		}
	}
	keep := dnfrepo.ItemStack{ItemID: itemID, Count: 1, Extra: map[string]string{"keep": "unchanged"}}
	repos := mustCurrentNPCShopRepositories(t, 0, map[string]dnfrepo.ItemStack{"0:9": {ItemID: itemID, Count: 1}, "0:10": keep})
	seedCurrentDisjointAccount(t, repos, nil)
	service := currentDisjointTestService(repos, catalog)
	connection := &bufferConn{}
	session := &gameSession{conn: connection, selectedCharacterID: 19}
	if err := service.handleCurrentDisjointItem(session, currentDisjointItemTestBody(9, 0, -1, 0)); err != nil {
		t.Fatal(err)
	}
	ack, _ := splitGameServerUpperPacket(t, connection.write.Bytes())
	if len(ack.Body) == 0 || ack.Body[0] != 1 {
		t.Fatalf("disjoint rejected: %x", ack.Body)
	}
	soul := currentDisjointRewardRowForItem(t, parseCurrentDisjointRewardRows(t, ack.Body), 10099775)
	if soul.Slot != 364 || soul.Granted == 0 {
		t.Fatalf("soul popup missing/wrong warehouse: %+v", soul)
	}
	ctx := context.Background()
	inv, _, err := repos.Inventory.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	if inv.Slots["0:9"].ItemID != 0 || !reflect.DeepEqual(inv.Slots["0:10"], keep) {
		t.Fatalf("source or unrelated item wrong: %+v", inv.Slots)
	}
	account, _, err := repos.AccountInventory.Load(ctx, "account-1")
	if err != nil {
		t.Fatal(err)
	}
	if stack := account.Slots["0:364"]; stack.ItemID != 10099775 || stack.Count != int64(soul.Granted) {
		t.Fatalf("committed soul differs from popup: %+v", stack)
	}
	t.Logf("legendary %s soul10099775 x%d committed to account warehouse364", definition.PVFPath, soul.Granted)
}

func TestMalformedSoulTableDoesNotConsumeEquipmentOrGrantRewards(t *testing.T) {
	ctx := context.Background()
	original := dnfrepo.ItemStack{ItemID: 5000, Count: 1, Extra: map[string]string{"quality_seed": "123456"}}
	repos, catalog := mustCurrentDisjointPlacementAssets(t, map[string]dnfrepo.ItemStack{"0:9": original})
	seedCurrentDisjointAccount(t, repos, nil)
	source := catalog.source.(bridgePVFSource)
	source["etc/disjoint.etc"] = strings.Replace(source["etc/disjoint.etc"], "0 1 10099775\n[/additional result expand]", "0 1\n[/additional result expand]", 1)
	beforeInv, _, err := repos.Inventory.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	beforeAccount, _, err := repos.AccountInventory.Load(ctx, "account-1")
	if err != nil {
		t.Fatal(err)
	}
	service := currentDisjointTestService(repos, catalog)
	conn := &bufferConn{}
	if err := service.handleCurrentDisjointItem(&gameSession{conn: conn, selectedCharacterID: 19}, currentDisjointItemTestBody(9, 0, -1, 0)); err != nil {
		t.Fatal(err)
	}
	ack, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
	if len(ack.Body) == 0 || ack.Body[0] != 0 {
		t.Fatalf("malformed pool allowed destruction: %x", ack.Body)
	}
	afterInv, _, err := repos.Inventory.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	afterAccount, _, err := repos.AccountInventory.Load(ctx, "account-1")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(beforeInv, afterInv) || !reflect.DeepEqual(beforeAccount, afterAccount) {
		t.Fatal("invalid reward table changed source inventory or account rewards")
	}
}

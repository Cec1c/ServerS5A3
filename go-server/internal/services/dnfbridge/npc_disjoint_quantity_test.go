package dnfbridge

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func npcDisjointQuantityCatalog(t *testing.T) *pvfDungeonDropCatalog {
	t.Helper()
	catalog := aikenDisjointTestCatalog(t, aikenDisjointValidCustomInfo)
	source := catalog.source.(bridgePVFSource)
	source[currentNPCShopPriceTablePath] = "[rate] 200 150 30\n"
	source["equipment/aiken_coat.equ"] = strings.Replace(source["equipment/aiken_coat.equ"], "[value] 1000", "[value] 68400", 1)
	source["equipment/ordinary_legend_coat.equ"] = strings.Replace(source["equipment/ordinary_legend_coat.equ"], "[value] 1000", "[value] 91200", 1)
	source["stackable/stackable.lst"] = strings.Replace(source["stackable/stackable.lst"], "3166", "3332", 1)
	// Force the high-quantity branch for ordinary equipment. Aiken must still
	// use its normal divisor; a probabilistic test could miss this regression.
	source["etc/disjoint.etc"] = strings.ReplaceAll(source["etc/disjoint.etc"], "3166", "3332")
	source["etc/disjoint.etc"] = strings.Replace(source["etc/disjoint.etc"], "0.19 5.65 8 20", "0.19 5.65 100 20", 1)
	return catalog
}

func disjointQuantityForItem(rewards []currentDisjointReward, id uint32) uint32 {
	var count uint32
	for _, row := range rewards {
		if row.ItemID == id {
			count += row.Count
		}
	}
	return count
}

func TestNPCDisjointCubeUsesPVFSellRate(t *testing.T) {
	for _, tc := range []struct {
		name  string
		price string
		rate  int64
		want  uint32
	}{
		{"coat", "[value] 68400", 200, 139},
		{"accessory", "[value] 91200", 200, 186},
		{"weapon", "[value] 115200", 200, 235},
		{"alternate-rate", "[value] 68400", 350, 244},
		{"price-fallback", "[price] 91200", 200, 186},
		{"value-before-price", "[value] 68400\n[price] 91200", 200, 139},
		{"zero-value-fallback", "[value] 0\n[price] 91200", 200, 186},
		{"negative-value-fallback", "[value] -1\n[price] 91200", 200, 186},
		{"missing-prices", "", 200, 1},
		{"nonpositive-prices", "[value] 0\n[price] -1", 200, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			catalog := npcDisjointQuantityCatalog(t)
			catalog.source.(bridgePVFSource)[currentNPCShopPriceTablePath] = fmt.Sprintf("[rate] %d 150 30\n", tc.rate)
			config, err := catalog.currentDisjointPVFConfig()
			if err != nil {
				t.Fatal(err)
			}
			doc, err := dnfpvf.Parse("equipment/quantity.equ", "[rarity] 6\n[minimum level] 90\n"+tc.price+"\n")
			if err != nil {
				t.Fatal(err)
			}
			rewards, err := currentEquipmentDisjointRewards(config, doc)
			if err != nil || disjointQuantityForItem(rewards, 3037) != tc.want {
				t.Fatalf("rewards=%+v err=%v want clear=%d", rewards, err, tc.want)
			}
			if config.EquipmentSellRatePermille != tc.rate {
				t.Fatal("PVF rate was ignored")
			}
		})
	}
}

func TestNPCDisjointRejectsUnsafeCubeArithmetic(t *testing.T) {
	catalog := npcDisjointQuantityCatalog(t)
	config, err := catalog.currentDisjointPVFConfig()
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"9223372036854775807", "5000000000000"} {
		doc, err := dnfpvf.Parse("equipment/overflow.equ", "[rarity] 6\n[value] "+value+"\n")
		if err != nil {
			t.Fatal(err)
		}
		if rewards, err := currentEquipmentDisjointRewards(config, doc); err == nil {
			t.Fatalf("unsafe sell multiplication or cube conversion accepted: %+v", rewards)
		}
	}
	if _, err := currentEquipmentSellGold(math.MaxInt64, 200); err == nil {
		t.Fatal("sell overflow accepted")
	}
}

func TestNPCDisjointAikenCannotRollGreatAndDoesNotAlterOrdinaryRules(t *testing.T) {
	catalog := npcDisjointQuantityCatalog(t)
	config, err := catalog.currentDisjointPVFConfig()
	if err != nil {
		t.Fatal(err)
	}
	before := append([]currentDisjointAdditionalConst(nil), config.AdditionalConsts...)
	for _, tc := range []struct {
		custom string
		aiken  bool
	}{
		{"warriors dungeon", true}, {"", false}, {" WARRIORS DUNGEON ", true}, {"epic production", false}, {"warriors dungeon", true},
	} {
		t.Run(tc.custom, func(t *testing.T) {
			body := "[rarity] 6\n[minimum level] 90\n[value] 68400\n"
			if tc.custom != "" {
				body += "[custom ability type] `" + tc.custom + "`\n"
			}
			doc, err := dnfpvf.Parse("equipment/custom.equ", body)
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 8; i++ {
				rewards, err := currentEquipmentDisjointRewards(config, doc)
				if err != nil {
					t.Fatal(err)
				}
				amount := disjointQuantityForItem(rewards, 3332)
				if tc.aiken && (amount < 15 || amount > 16) || !tc.aiken && (amount < 473 || amount > 474) {
					t.Fatalf("custom=%q amount=%d", tc.custom, amount)
				}
				if (disjointQuantityForItem(rewards, 10099775) == 0) != tc.aiken {
					t.Fatalf("soul filtering changed: %+v", rewards)
				}
				if disjointQuantityForItem(rewards, 3037) != 139 {
					t.Fatalf("cube count changed: %+v", rewards)
				}
			}
		})
	}
	if !reflect.DeepEqual(config.AdditionalConsts, before) {
		t.Fatal("Aiken mutated the shared rarity rules")
	}
	cached, err := catalog.currentDisjointPVFConfig()
	if err != nil || !reflect.DeepEqual(cached.AdditionalConsts, before) {
		t.Fatal("cached rules changed", err)
	}
}

func TestNPCDisjointQuantitySingleAndCompactBatchCommitOnlyGrantedAmount(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(fmt.Sprint(batch), func(t *testing.T) {
			catalog := npcDisjointQuantityCatalog(t)
			slots := map[string]dnfrepo.ItemStack{"0:60": {ItemID: 5000, Count: 1}, "0:121": {ItemID: 3332, Count: 1000}}
			wantCubes := uint32(139)
			minEndless, maxEndless := uint32(15), uint32(16)
			body := currentDisjointItemTestBody(60, 0, -1, 317)
			if batch {
				slots["0:61"] = dnfrepo.ItemStack{ItemID: 5001, Count: 1}
				wantCubes, minEndless, maxEndless = 325, 488, 490
				body = currentDisjointBatchBody([]int16{60, 61}, -1, 317)
			}
			repos := aikenDisjointTestRepositories(t, slots)
			seedCurrentDisjointAccount(t, repos, map[string]dnfrepo.ItemStack{"0:358": {ItemID: 3037, Count: 5000}})
			conn := &bufferConn{}
			s := currentDisjointTestService(repos, catalog)
			p := &gameSession{conn: conn, selectedCharacterID: 19}
			if err := s.handleCurrentDisjointItem(p, body); err != nil {
				t.Fatal(err)
			}
			ack, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
			rows := parseCurrentDisjointRewardRows(t, ack.Body)
			cube := currentDisjointRewardRowForItem(t, rows, 3037)
			endless := currentDisjointRewardRowForItem(t, rows, 3332)
			if cube.Granted != wantCubes || endless.Granted < minEndless || endless.Granted > maxEndless {
				t.Fatalf("rows=%+v", rows)
			}
			ctx := context.Background()
			bag, _, _ := repos.Inventory.Load(ctx, "19")
			account, _, _ := repos.AccountInventory.Load(ctx, "account-1")
			if bag.Slots["0:60"].ItemID != 0 || batch && bag.Slots["0:61"].ItemID != 0 {
				t.Fatal("source remains")
			}
			if bag.Slots["0:121"].Count != 1000+int64(endless.Granted) || account.Slots["0:358"].Count != 5000+int64(wantCubes) {
				t.Fatal("ACK differs from committed delta")
			}
			conn.write.Reset()
			if err := s.handleCurrentDisjointItem(p, body); err != nil {
				t.Fatal(err)
			}
			rejected, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
			afterBag, _, _ := repos.Inventory.Load(ctx, "19")
			afterAccount, _, _ := repos.AccountInventory.Load(ctx, "account-1")
			if rejected.Body[0] != 0 || !reflect.DeepEqual(bag, afterBag) || !reflect.DeepEqual(account, afterAccount) {
				t.Fatal("repeated source granted again")
			}
		})
	}
}

func TestNPCDisjointBadSellRateRejectsBeforeAssetMutation(t *testing.T) {
	for _, table := range []string{"", "[rate] 0", "[rate] -1", "[rate] 2147483648", "[rate] `invalid`"} {
		t.Run(table, func(t *testing.T) {
			catalog := npcDisjointQuantityCatalog(t)
			if table == "" {
				delete(catalog.source.(bridgePVFSource), currentNPCShopPriceTablePath)
			} else {
				catalog.source.(bridgePVFSource)[currentNPCShopPriceTablePath] = table
			}
			repos := aikenDisjointTestRepositories(t, map[string]dnfrepo.ItemStack{"0:60": {ItemID: 5000, Count: 1}})
			ctx := context.Background()
			beforeBag, _, _ := repos.Inventory.Load(ctx, "19")
			beforeAccount, _, _ := repos.AccountInventory.Load(ctx, "account-1")
			conn := &bufferConn{}
			if err := currentDisjointTestService(repos, catalog).handleCurrentDisjointItem(&gameSession{conn: conn, selectedCharacterID: 19}, currentDisjointItemTestBody(60, 0, -1, 317)); err != nil {
				t.Fatal(err)
			}
			ack, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
			afterBag, _, _ := repos.Inventory.Load(ctx, "19")
			afterAccount, _, _ := repos.AccountInventory.Load(ctx, "account-1")
			if ack.Body[0] != 0 || !reflect.DeepEqual(beforeBag, afterBag) || !reflect.DeepEqual(beforeAccount, afterAccount) {
				t.Fatal("bad rate consumed source or granted rewards")
			}
		})
	}
}

func TestNPCDisjointNativeBatchQuantityReplayAndRollback(t *testing.T) {
	for _, failure := range []string{"", "共享仓库"} {
		t.Run(failure, func(t *testing.T) {
			s, p, repos, trace, packets := nativeNPCBatchFixture(t, 2)
			s.pvfItemCatalog = npcDisjointQuantityCatalog(t)
			ctx := context.Background()
			bag, _, _ := repos.Inventory.Load(ctx, "19")
			bag.Slots["0:11"] = dnfrepo.ItemStack{ItemID: 5001, Count: 1}
			bag.Slots["0:121"] = dnfrepo.ItemStack{ItemID: 3332, Count: 1000}
			if err := repos.Inventory.Save(ctx, bag); err != nil {
				t.Fatal(err)
			}
			seedCurrentDisjointAccount(t, repos, map[string]dnfrepo.ItemStack{"0:358": {ItemID: 3037, Count: 5000}})
			beforeBag, _, _ := repos.Inventory.Load(ctx, "19")
			beforeAccount, _, _ := repos.AccountInventory.Load(ctx, "account-1")
			trace.failure = failure
			packets = append(packets, packets[0])
			if handled, err := s.handleNativeNPCDisjointBatch(p, packets); !handled || err != nil {
				t.Fatal(handled, err)
			}
			if err := s.finishNativeResultDelivery(p, "quantity regression"); err != nil {
				t.Fatal(err)
			}
			wire := p.conn.(*channelSceneWriteConn).write.Bytes()
			var cubes, endless uint32
			for i := range packets {
				ack, rest := splitGameServerUpperPacketWithHeader(t, wire, s.gameUpperHeaderSize())
				wire = rest
				if failure != "" || i == 2 {
					if ack.Body[0] != 0 {
						t.Fatal("failed or duplicate input succeeded")
					}
					continue
				}
				rows := parseCurrentDisjointRewardRows(t, ack.Body)
				cube := currentDisjointRewardRowForItem(t, rows, 3037).Granted
				amount := currentDisjointRewardRowForItem(t, rows, 3332).Granted
				if i == 0 && (cube != 139 || amount < 15 || amount > 16) || i == 1 && (cube != 186 || amount < 473 || amount > 474) {
					t.Fatalf("item=%d rows=%+v", i, rows)
				}
				cubes += cube
				endless += amount
			}
			afterBag, _, _ := repos.Inventory.Load(ctx, "19")
			afterAccount, _, _ := repos.AccountInventory.Load(ctx, "account-1")
			if failure != "" {
				if !reflect.DeepEqual(beforeBag, afterBag) || !reflect.DeepEqual(beforeAccount, afterAccount) {
					t.Fatal("failed save changed assets")
				}
			} else if cubes != 325 || afterBag.Slots["0:121"].Count != 1000+int64(endless) || afterAccount.Slots["0:358"].Count != 5000+int64(cubes) {
				t.Fatal("native batch delta differs from ACK sum")
			}
			if len(wire) != 0 || trace.transactions != 1 || p.nativeNPCDisjointReplay != nil {
				t.Fatal("batch boundary changed")
			}
		})
	}
}

package dnfbridge

import (
	"bytes"
	"context"
	"os"
	"reflect"
	"testing"

	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// P133: exercise dispatch, the asset transaction and the published wallet with
// real reward definitions. These isolated balances are not player DB data.
func TestRealPVFR134DelilahDispatchWallet(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE for the P133 real-PVF regression")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	const itemID = 101020542 // Existing real-PVF legendary disjoint fixture.
	definition, err := catalog.ResolveItem(itemID)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := parseDungeonCardPVFDocument(archive, definition.PVFPath)
	if err != nil {
		t.Fatal(err)
	}
	if rarity, _ := doc.Int("rarity"); rarity != 6 {
		t.Fatalf("real fixture rarity=%d path=%s", rarity, definition.PVFPath)
	}
	t.Logf("real equipment id=%d path=%s", itemID, definition.PVFPath)
	for _, route := range []string{"handler", "upper", "legacy-current", "legacy-with-trailer"} {
		for _, tc := range []struct {
			name       string
			count      int
			gold, want int64
			context    uint32
			active     bool
			success    bool
		}{
			{"single", 1, 2000, 1500, 1000, false, true},
			{"batch", 2, 2000, 1000, 1000, false, true},
			{"exact-balance", 1, 500, 0, 1000, false, true},
			{"insufficient", 1, 499, 499, 1000, false, false},
			{"batch-insufficient", 2, 999, 999, 1000, false, false},
			{"ordinary-context", 1, 100, 100, 0, false, true},
			{"active-dungeon", 1, 2000, 2000, 1000, true, false},
		} {
			t.Run(route+"/"+tc.name, func(t *testing.T) {
				ctx := context.Background()
				keep := dnfrepo.ItemStack{ItemID: itemID, Count: 1, Extra: map[string]string{"keep": "unchanged"}}
				slots := map[string]dnfrepo.ItemStack{"0:9": {ItemID: itemID, Count: 1}, "0:11": keep, "0:0": {ItemID: 0, Count: 7}}
				if tc.count == 2 {
					slots["0:10"] = dnfrepo.ItemStack{ItemID: itemID, Count: 1}
				}
				repos := mustCurrentNPCShopRepositories(t, 0, slots)
				seedCurrentDisjointAccount(t, repos, nil)
				setDelilahCostTestGold(t, repos, tc.gold)
				service := currentDisjointTestService(repos, catalog)
				// NPC fees must not switch to inventory 0:0 under this option.
				service.options.expertJobGoldSource = expertJobGoldInventoryWallet
				service.options.gameUpperClientBodyCodec = gameUpperClientBodyCodecPlain
				conn := &bufferConn{}
				session := &gameSession{conn: conn, selectedCharacterID: 19}
				if tc.active {
					attachDelilahCostActiveRuntime(t, session)
				} else {
					attachDelilahCostCompletedRuntime(t, session)
				}
				body := currentDisjointItemTestBody(9, 0, -1, tc.context)
				if tc.count == 2 {
					body = currentDisjointBatchBody([]int16{9, 10}, -1, tc.context)
				}
				invoke := func() {
					t.Helper()
					var err error
					switch route {
					case "handler":
						err = service.handleCurrentDisjointItem(session, body)
					case "upper":
						var raw []byte
						raw, err = dnfproto.BuildChannelPacket(26, body, 1, 1)
						if err == nil {
							err = service.handleGameUpper(session, raw)
						}
					case "legacy-current":
						err = service.handleLegacyGamePacket(session, buildLegacyGamePacketForBridgeTest(1, 26, 1, body))
					case "legacy-with-trailer":
						wire := append([]byte(nil), body...)
						// The existing legacy single-item contract has a 4-byte
						// transport trailer; compact batch bodies have none.
						if tc.count == 1 {
							wire = append(wire, 0xb8, 0x1a, 0, 0)
						}
						err = service.handleLegacyGamePacket(session, buildLegacyGamePacketForBridgeTest(1, 26, 1, wire))
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				beforeChar := loadDelilahCostTestCharacter(t, repos)
				beforeInv, _, _ := repos.Inventory.Load(ctx, "19")
				beforeAccount, _, _ := repos.AccountInventory.Load(ctx, "account-1")
				invoke()
				ack, tail := splitGameServerUpperPacket(t, conn.write.Bytes())
				if ack.Header.MsgID != 26 || len(ack.Body) == 0 || (ack.Body[0] == 1) != tc.success {
					t.Fatalf("ACK=%+v body=%x", ack.Header, ack.Body)
				}
				afterChar := loadDelilahCostTestCharacter(t, repos)
				afterInv, _, _ := repos.Inventory.Load(ctx, "19")
				afterAccount, _, _ := repos.AccountInventory.Load(ctx, "account-1")
				if afterChar.Stats["gold"] != tc.want {
					t.Fatalf("persisted gold=%d want=%d", afterChar.Stats["gold"], tc.want)
				}
				if !tc.success {
					if len(tail) != 0 || !reflect.DeepEqual(beforeChar, afterChar) || !reflect.DeepEqual(beforeInv, afterInv) || !reflect.DeepEqual(beforeAccount, afterAccount) {
						t.Fatal("rejected request changed assets or sent success refresh")
					}
					return
				}
				if _, exists := afterInv.Slots["0:9"]; exists {
					t.Fatal("source remains")
				}
				if tc.count == 2 {
					if _, exists := afterInv.Slots["0:10"]; exists {
						t.Fatal("second source remains")
					}
				}
				if !reflect.DeepEqual(afterInv.Slots["0:11"], keep) || afterInv.Slots["0:0"].Count != 7 {
					t.Fatal("unrelated item or personal-machine wallet changed")
				}
				if soul := afterAccount.Slots["0:364"]; soul.ItemID != 10099775 || soul.Count <= 0 {
					t.Fatalf("real PVF legendary reward missing: %+v", soul)
				}
				walletCount := 0
				for len(tail) > 0 {
					frame, rest := splitGameServerUpperPacket(t, tail)
					if frame.Header.MsgID == 14 && frame.Header.Classification == 0 && bytes.Equal(frame.Body, buildCurrentGoldStateBody(tc.want)) {
						walletCount++
						if len(rest) != 0 {
							t.Fatal("wallet refresh was not last")
						}
					}
					tail = rest
				}
				wantWallet := 0
				if tc.context == 1000 {
					wantWallet = 1
				}
				if walletCount != wantWallet {
					t.Fatalf("wallet count=%d want=%d", walletCount, wantWallet)
				}
				// A replay after consumption must neither debit nor grant again.
				conn.write.Reset()
				invoke()
				replay, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
				if replay.Header.MsgID != 26 || len(replay.Body) == 0 || replay.Body[0] != 0 || len(rest) != 0 {
					t.Fatal("consumed-source replay accepted or published refresh")
				}
				replayInv, _, _ := repos.Inventory.Load(ctx, "19")
				replayAccount, _, _ := repos.AccountInventory.Load(ctx, "account-1")
				if !reflect.DeepEqual(afterChar, loadDelilahCostTestCharacter(t, repos)) || !reflect.DeepEqual(afterInv, replayInv) || !reflect.DeepEqual(afterAccount, replayAccount) {
					t.Fatal("replay changed committed assets")
				}
			})
		}
	}
}

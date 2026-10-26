package dnfbridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"reflect"
	"strconv"
	"testing"

	dnfexpertjob "longheng.io/server/internal/modules/dnf/expertjob"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// Isolated official-24.9 actual-handler regression with a hash-pinned PVF.
// Memory actors are synthetic; this proves outbound rows and committed assets,
// not a native popup reproduction. Legacy-profile tests remain unchanged.
func TestRealPVFExpertJobEnchantIncremental601(t *testing.T) {
	const (
		pvfSHA         = "369d072b82c715aea4e02813d89ad9225686983e147bc7be0ee0cae2e296bb06"
		targetID       = int64(100220137)
		cardID         = int64(3621)
		recipeID       = int64(10015129)
		decoyID        = int64(28309)
		seedExperience = int64(1195)
		seedEndurance  = int64(173)
	)
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to execute the pinned real-resource replay")
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	_, hashErr := io.Copy(hash, file)
	closeErr := file.Close()
	if hashErr != nil || closeErr != nil {
		t.Fatalf("hash archive: read=%v close=%v", hashErr, closeErr)
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != pvfSHA {
		t.Fatalf("PVF sha256=%s want %s", got, pvfSHA)
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	jobs, err := dnfexpertjob.Load(ctx, archive)
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	config, ok := jobs.Enchanter()
	if !ok || config == nil {
		t.Fatal("real enchanter configuration unavailable")
	}
	recipe, ok := config.CardRecipes[recipeID]
	if !ok || recipe.RequiredLevel != 1 || recipe.Qualification != 0 {
		t.Fatalf("fixed recipe=%+v found=%t", recipe, ok)
	}
	wantMaterials := map[int64]int64{3227: 5, 3166: 20}
	if len(recipe.Materials) != len(wantMaterials) {
		t.Fatalf("unexpected fixed recipe materials: %+v", recipe.Materials)
	}
	seenMaterials := map[int64]bool{}
	for _, material := range recipe.Materials {
		if seenMaterials[material.ItemID] || wantMaterials[material.ItemID] != material.Count {
			t.Fatalf("unexpected or duplicate fixed material: %+v", material)
		}
		seenMaterials[material.ItemID] = true
	}
	card, ok := config.Cards[cardID]
	level := config.Recipes.Level(seedExperience)
	rule, ruleOK := config.CardExperienceByLevel[level]
	if !ok || !ruleOK || card.Qualification != 0 || rule.SuccessRates[0] != 100 || rule.MinimumExperienceGain <= 0 || level < config.EnduranceMinimumLevel || config.EnduranceReduction <= 0 {
		t.Fatalf("fixed ordinary card must succeed deterministically: card=%+v rule=%+v", card, rule)
	}
	resolution, err := resolveCurrentEnchantCardMetadata(items, archive, cardID, targetID)
	if err != nil || resolution.CardItemID != cardID || resolution.TargetEquipmentType != "[waist]" ||
		!currentExpertJobStoreStringContainsFold(resolution.AllowedEquipmentTypes, resolution.TargetEquipmentType) ||
		(len(resolution.UpgradeCounts) > 0 && !currentExpertJobStoreIntContains(resolution.UpgradeCounts, 0)) {
		t.Fatalf("fixed card/waist metadata mismatch: resolution=%+v err=%v", resolution, err)
	}
	for _, id := range []int64{targetID, decoyID} {
		definition, resolveErr := items.ResolveItem(uint32(id))
		if resolveErr != nil || definition.Kind != dungeonDropItemEquipment {
			t.Fatalf("fixed equipment %d: kind=%v err=%v", id, definition.Kind, resolveErr)
		}
		t.Logf("fixed equipment id=%d pvf_path=%s", id, definition.PVFPath)
	}
	t.Logf("offline memory replay only; PVF=%s recipe=%d card=%d target=%d slot=28 decoy=%d slot=9 card_slot=237", pvfSHA, recipeID, cardID, targetID, decoyID)

	for _, test := range []struct {
		name      string
		self      bool
		menu      bool
		exhausted bool
		short     bool
		shared    bool
	}{
		{name: "menu_self_success", self: true, menu: true},
		{name: "entered_peer_exhausted_success", exhausted: true},
		{name: "registered_self_success", self: true},
		{name: "entered_peer_material_short", short: true},
		{name: "menu_self_account_material_exhausted", self: true, menu: true, exhausted: true, shared: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			// The observed ordinary recipe has no account-shared ingredients.
			// A separate verified real-catalog recipe covers that owner
			// boundary without adding invented materials to recipe10015129.
			cardID, recipeID, recipe := cardID, recipeID, recipe
			if test.shared {
				// Verified from the same frozen catalog: card3677 fits waist;
				// recipe10015132 consumes gold cubes3262 in fixed account slot359.
				recipeID, cardID = 10015132, 3677
				var found bool
				recipe, found = config.CardRecipes[recipeID]
				want := map[int64]int64{3167: 40, 3227: 30, 3262: 10}
				if !found || len(recipe.Materials) != len(want) {
					t.Fatalf("shared recipe missing/changed: %+v", recipe)
				}
				for _, m := range recipe.Materials {
					if want[m.ItemID] != m.Count {
						t.Fatalf("unexpected shared recipe material: %+v", m)
					}
					delete(want, m.ItemID)
				}
				if len(want) != 0 {
					t.Fatal("incomplete shared recipe materials")
				}
				card, found := config.Cards[cardID]
				if !found || card.Qualification != recipe.Qualification || recipe.Qualification < 0 || recipe.Qualification >= len(rule.SuccessRates) || rule.SuccessRates[recipe.Qualification] != 100 || recipe.RequiredLevel > level {
					t.Fatal("shared recipe must guarantee real success")
				}
				metadata, err := resolveCurrentEnchantCardMetadata(items, archive, cardID, targetID)
				if err != nil || metadata.CardItemID != cardID || metadata.TargetEquipmentType != "[waist]" || !currentExpertJobStoreStringContainsFold(metadata.AllowedEquipmentTypes, metadata.TargetEquipmentType) || (len(metadata.UpgradeCounts) > 0 && !currentExpertJobStoreIntContains(metadata.UpgradeCounts, 0)) {
					t.Fatalf("shared card/waist incompatible: %+v err=%v", metadata, err)
				}
				t.Logf("real shared-material recipe=%d card=%d materials=%+v", recipeID, cardID, recipe.Materials)
			}

			// IDs 19/20 and account-19/account-20 are invented fixture owners,
			// deliberately unrelated to historical/new live characters.
			repos := dnfrepomemory.NewMemoryGroup()
			for _, id := range []string{"19", "20"} {
				character := dnfrepo.CharacterRecord{CharacterID: id, AccountID: "account-" + id, Level: 90,
					Stats: map[string]int64{"gold": 100000, "expert_job_type": 1, "expert_job_exp": seedExperience, currentExpertJobMachineEnduranceStat: seedEndurance}}
				if id == "19" && !test.self {
					character.Stats["expert_job_type"] = 0 // A peer customer need not be an enchanter.
				}
				inventory := dnfrepo.InventoryRecord{CharacterID: id, Slots: map[string]dnfrepo.ItemStack{}}
				account := dnfrepo.AccountInventoryRecord{AccountID: character.AccountID, Slots: map[string]dnfrepo.ItemStack{}}
				if id == "19" {
					for slot, stack := range map[int16]dnfrepo.ItemStack{
						28:  {ItemID: targetID, Count: 1, Extra: map[string]string{"item_kind": "equipment", "quality_seed": "999999998", "durability": "25"}},
						9:   {ItemID: decoyID, Count: 1, Extra: map[string]string{"item_kind": "equipment", "quality_seed": "111111111"}},
						237: {ItemID: cardID, Count: 2, Extra: map[string]string{"item_kind": "stackable"}},
					} {
						if slot == 237 && test.exhausted {
							stack.Count = 1
						}
						entry := currentItemListEntryFromStack(0, slot, stack)
						stack.RawEntry = append([]byte(nil), entry.data[:]...)
						inventory.Slots[currentCeraShopInventorySlotKey(0, slot)] = stack
					}
					for index, material := range recipe.Materials {
						count := material.Count * 2
						if test.exhausted {
							count = material.Count
						}
						if test.short && material.ItemID == 3166 {
							count = material.Count - 1
						}
						stack := dnfrepo.ItemStack{ItemID: material.ItemID, Count: count, Extra: map[string]string{"item_kind": "stackable"}}
						if fixed, shared := dnfrepo.AccountWarehouseSlotForItem(material.ItemID); shared {
							account.Slots[dnfrepo.AccountSharedInventorySlotKey(fixed)] = stack
						} else {
							// Two ordinary stacks; ascending first slot exhausts even
							// when the total supply exceeds one recipe.
							first := stack
							first.Count = material.Count - 1
							second := stack
							second.Count = count - first.Count
							inventory.Slots[fmt.Sprintf("0:%d", 150+2*index)] = first
							if second.Count > 0 {
								inventory.Slots[fmt.Sprintf("0:%d", 151+2*index)] = second
							}
						}
					}
				}
				if err := repos.Character.Save(ctx, character); err != nil {
					t.Fatal(err)
				}
				if err := repos.Inventory.Save(ctx, inventory); err != nil {
					t.Fatal(err)
				}
				if err := repos.AccountInventory.Save(ctx, account); err != nil {
					t.Fatal(err)
				}
				if err := repos.Equipment.Save(ctx, dnfrepo.EquipmentRecord{CharacterID: id}); err != nil {
					t.Fatal(err)
				}
			}
			requesterConn, ownerConn := &bufferConn{}, &bufferConn{}
			session := &gameSession{conn: requesterConn, selectedCharacterID: 19, accountID: "account-19"}
			ownerID := uint16(20)
			ownerSession := &gameSession{conn: ownerConn, selectedCharacterID: ownerID, accountID: "account-20"}
			if test.self {
				ownerID, ownerSession = 19, session
			}
			store := &currentExpertJobStore{Kind: currentExpertJobEnchantStoreKind, OwnerCharacterID: ownerID,
				OwnerSession: ownerSession, Endurance: seedEndurance, Cost: 100, Qualifications: config.Qualifications(seedExperience), TownID: 2, AreaID: 5}
			service := &Service{repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true },
				expertJobCatalog: jobs, pvfItemCatalog: items, initialEquipmentArchive: archive,
				onlinePlayers: newOnlinePlayerManager(), expertJobStores: map[uint16]*currentExpertJobStore{ownerID: store}}
			service.options.currentClientVersion = "official-24.9"
			service.options.gameUpperHeader = gameUpperHeaderChannel13
			if test.menu {
				service.expertJobStores = nil
			}
			service.options.gameUpperBodyCodec = gameUpperBodyCodecPlain
			service.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 19, Session: session, TownID: 2, AreaID: 5})
			if !test.self {
				service.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: ownerID, Session: ownerSession, TownID: 2, AreaID: 5})
			}
			if !test.menu {
				if _, entered := service.enterCurrentExpertJobStore(session, ownerID); !entered {
					t.Fatal("fixture failed store entry")
				}
				if _, admitted := service.currentExpertJobEnteredStore(session, ownerID, currentExpertJobEnchantStoreKind); !admitted {
					t.Fatal("fixture admission failed")
				}
			}
			// Model a previously logged-in inventory using the real projection
			// builder before snapshotting. No ACK or refresh is sent here.
			for _, listType := range currentSelectedItemListTypes {
				if _, _, _, known := service.buildCurrentItemListBodyForSession(ctx, session, listType); !known {
					t.Fatalf("fixture cannot project required container %d", listType)
				}
			}
			if test.shared {
				// Inject AFTER login projection so setup cannot silently remove the
				// stale character-owned copy before the real handler sees it.
				fixed, shared := dnfrepo.AccountWarehouseSlotForItem(3262)
				if !shared || fixed != 359 {
					t.Fatalf("gold cube fixed account slot=%d shared=%t", fixed, shared)
				}
				inventory, found, err := repos.Inventory.Load(ctx, "19")
				if err != nil || !found {
					t.Fatalf("load stale-shadow fixture: found=%t err=%v", found, err)
				}
				shadow := dnfrepo.ItemStack{ItemID: 3262, Count: 777, Extra: map[string]string{"item_kind": "stackable"}}
				entry := currentItemListEntryFromStack(0, fixed, shadow)
				shadow.RawEntry = append([]byte(nil), entry.data[:]...)
				inventory.Slots[dnfrepo.AccountSharedInventorySlotKey(fixed)] = shadow
				if err := repos.Inventory.Save(ctx, inventory); err != nil {
					t.Fatal(err)
				}
			}
			snapshot := func() [2]incrementalEnchantSnapshot {
				t.Helper()
				var all [2]incrementalEnchantSnapshot
				for index, id := range []string{"19", "20"} {
					var found bool
					var loadErr error
					all[index].character, found, loadErr = repos.Character.Load(ctx, id)
					if loadErr != nil || !found {
						t.Fatalf("load character %s: found=%t err=%v", id, found, loadErr)
					}
					all[index].inventory, found, loadErr = repos.Inventory.Load(ctx, id)
					if loadErr != nil || !found {
						t.Fatalf("load inventory %s: found=%t err=%v", id, found, loadErr)
					}
					all[index].account, found, loadErr = repos.AccountInventory.Load(ctx, "account-"+id)
					if loadErr != nil || !found {
						t.Fatalf("load account %s: found=%t err=%v", id, found, loadErr)
					}
					all[index].equipment, found, loadErr = repos.Equipment.Load(ctx, id)
					if loadErr != nil || !found {
						t.Fatalf("load equipment %s: found=%t err=%v", id, found, loadErr)
					}
				}
				return all
			}
			before := snapshot()
			if test.shared && (before[0].inventory.Slots["0:359"].Count != 777 || before[0].account.Slots["0:359"].ItemID != 3262 || before[0].account.Slots["0:359"].Count != 10) {
				t.Fatal("shared fixture must retain distinct stale character777 and authoritative account10 immediately before handler")
			}
			beforeStore := cloneCurrentExpertJobStore(store)
			requesterConn.write.Reset()
			ownerConn.write.Reset()
			// Current server contract, not an assertion about a newly sampled
			// native self request: u16 owner, i32 recipe, tag2, target list/slot,
			// card list/slot. The historical target/card tail is 1c0000ed00.
			body := make([]byte, 13)
			binary.LittleEndian.PutUint16(body[0:2], ownerID)
			binary.LittleEndian.PutUint32(body[2:6], uint32(recipeID))
			body[6] = 2
			binary.LittleEndian.PutUint16(body[8:10], 28)
			binary.LittleEndian.PutUint16(body[11:13], 237)
			if err := service.handleCurrentExpertJobStoreEnchant(session, 601, body); err != nil {
				t.Fatalf("actual 601 handler: %v", err)
			}
			after := snapshot()
			ackCount, packetIndex, refreshCount, walletCount := 0, 0, 0, 0
			var ackBody []byte
			// Independent expected affected slots from the pinned recipe and fixture,
			// never from the product refresh helper/result.ItemRefreshes.
			expected := map[uint16]int64{28: targetID, 237: cardID}
			remaining := map[uint16]int64{237: 1}
			if test.exhausted {
				remaining[237] = 0
			}
			sharedMaterials := 0
			for i, material := range recipe.Materials {
				slots := []int16{int16(150 + 2*i), int16(151 + 2*i)}
				if fixed, shared := dnfrepo.AccountWarehouseSlotForItem(material.ItemID); shared {
					slots = []int16{fixed}
					sharedMaterials++
				}
				for j, slot := range slots {
					if _, collision := expected[uint16(slot)]; collision {
						t.Fatalf("fixture slot collision %d", slot)
					}
					expected[uint16(slot)] = material.ItemID
					remaining[uint16(slot)] = material.Count
					if test.exhausted || (len(slots) == 2 && j == 0) {
						remaining[uint16(slot)] = 0
					}
				}
			}

			if test.shared && sharedMaterials == 0 {
				t.Fatal("fixture must exercise authoritative account material")
			}
			seenRows := map[uint16]bool{}
			for wire := requesterConn.write.Bytes(); len(wire) > 0; packetIndex++ {
				if len(wire) < dnfproto.GameServerUpperHeaderSize {
					t.Fatalf("truncated frame: %x", wire)
				}
				id := binary.LittleEndian.Uint16(wire[1:3])
				if id == 13 {
					t.Fatalf("official-24.9 emitted forbidden op13 full-container refresh after ACK=%x; want only affected op14 rows", ackBody)
				}
				packet, rest := splitGameServerUpperPacketWithHeader(t, wire, dnfproto.GameServerUpperHeaderSize)
				wire = rest
				if id == 601 {
					ackCount++
					ackBody = packet.Body
					if packetIndex != 0 || packet.Header.Classification != 1 {
						t.Fatalf("601 must be first/class1: index=%d class=%d", packetIndex, packet.Header.Classification)
					}
					continue
				}
				if test.short {
					t.Fatalf("rejected request emitted non-ACK packet %d: %x", id, packet.Body)
				}
				if ackCount != 1 || len(ackBody) != 11 || ackBody[0] != 1 || ackBody[1] != 1 {
					t.Fatalf("notification before original successful 11-byte ACK: %x", ackBody)
				}
				if id != 14 {
					continue
				}
				if packet.Header.Classification != 0 || len(packet.Body) < 3 || packet.Body[0] != 0 {
					t.Fatalf("invalid op14: class=%d body=%x", packet.Header.Classification, packet.Body)
				}
				count := int(binary.LittleEndian.Uint16(packet.Body[1:3]))
				if count == 0 || len(packet.Body) != 3+count*currentItemListEntryWireSize {
					t.Fatalf("invalid op14 rows: %x", packet.Body)
				}
				for i := 0; i < count; i++ {
					row := packet.Body[3+i*currentItemListEntryWireSize : 3+(i+1)*currentItemListEntryWireSize]
					slot := binary.LittleEndian.Uint16(row[:2])
					item := binary.LittleEndian.Uint32(row[2:6])
					value := binary.LittleEndian.Uint32(row[6:10])
					// Existing peer fee notification is a separate gold-only op14,
					// not an item refresh. Preserve and assert this old contract.
					if slot == 0 {
						walletCount++
						if test.self || count != 1 || item != 0 || value != 99900 || refreshCount == 0 {
							t.Fatalf("unexpected wallet row: %x", row)
						}
						continue
					}
					wantID, allowed := expected[slot]
					if !allowed || slot == 9 || seenRows[slot] {
						t.Fatalf("unexpected/duplicate item refresh slot=%d item=%d; decoy must never refresh", slot, item)
					}
					seenRows[slot] = true
					refreshCount++
					if slot == 28 {
						if item != uint32(targetID) || value != 999999998 || binary.LittleEndian.Uint32(row[0x0e:0x12]) != uint32(cardID) || row[0x12] != 0 {
							t.Fatalf("wrong target identity/quality/enchant: %x", row)
						}
					} else if remaining[slot] == 0 {
						if item != 0xffffffff || value != 0 {
							t.Fatalf("exhausted slot%d must clear with FFFFFFFF/0: %x", slot, row)
						}
					} else if item != uint32(wantID) || int64(value) != remaining[slot] {
						t.Fatalf("slot%d committed count/id wrong: %x", slot, row)
					}
				}
			}

			if ackCount != 1 {
				t.Fatalf("actual handler emitted %d ACKs, want1", ackCount)
			}
			if test.short {
				if !bytes.Equal(ackBody, []byte{0, currentExpertJobStoreError(errCurrentExpertJobInsufficientMaterials)}) {
					t.Fatalf("material rejection ACK=%x", ackBody)
				}
				if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(beforeStore, store) || ownerConn.write.Len() != 0 {
					t.Fatal("material rejection changed persisted assets/store or notified owner")
				}
				t.Logf("rejected exact601 body=%x ACK=%x; both actors/inventories/accounts/equipment unchanged; no op13/op14", body, ackBody)
				return
			}
			if len(seenRows) != len(expected) || refreshCount != len(expected) || (!test.self && walletCount != 1) || (test.self && walletCount != 0) || len(ackBody) != 11 || ackBody[0] != 1 || ackBody[1] != 1 || ackBody[6] != 0 {
				t.Fatalf("success ACK/incremental refresh mismatch: ACK=%x refreshed_rows=%d", ackBody, refreshCount)
			}
			inventory := after[0].inventory
			if inventory.Slots["0:28"].ItemID != targetID || inventory.Slots["0:28"].Count != 1 ||
				inventory.Slots["0:28"].Extra["enchant_card_id"] != strconv.FormatInt(cardID, 10) ||
				!reflect.DeepEqual(before[0].inventory.Slots["0:9"], inventory.Slots["0:9"]) ||
				(!test.exhausted && inventory.Slots["0:237"].ItemID != cardID) || inventory.Slots["0:237"].Count != remaining[237] {
				t.Fatal("persisted target/decoy/card identities or consumption wrong")
			}
			persistedTarget := inventory.Slots["0:28"].RawEntry
			if len(persistedTarget) != currentItemListEntryWireSize || binary.LittleEndian.Uint16(persistedTarget[0:2]) != 28 ||
				binary.LittleEndian.Uint32(persistedTarget[2:6]) != uint32(targetID) || binary.LittleEndian.Uint32(persistedTarget[0x0e:0x12]) != uint32(cardID) || persistedTarget[0x12] != 0 {
				t.Fatalf("persisted target raw row identity/enchant mismatch: %x", persistedTarget)
			}
			for key, stack := range inventory.Slots {
				if key != "0:28" && (stack.Extra["enchant_card_id"] != before[0].inventory.Slots[key].Extra["enchant_card_id"]) {
					t.Fatalf("non-target %s received persisted enchant: %+v", key, stack)
				}
			}
			for _, material := range recipe.Materials {
				remaining := int64(0)
				if fixed, shared := dnfrepo.AccountWarehouseSlotForItem(material.ItemID); shared {
					remaining = after[0].account.Slots[dnfrepo.AccountSharedInventorySlotKey(fixed)].Count
				} else {
					for _, stack := range inventory.Slots {
						if stack.ItemID == material.ItemID {
							remaining += stack.Count
						}
					}
				}
				wantRemaining := material.Count
				if test.exhausted {
					wantRemaining = 0
				}
				if remaining != wantRemaining {
					t.Fatalf("material%d remaining=%d want%d (exactly one attempt consumed)", material.ItemID, remaining, wantRemaining)
				}
			}
			ownerIndex := 1
			wantGold := int64(99900)
			if test.self {
				ownerIndex, wantGold = 0, 100000
			}
			owner := after[ownerIndex].character
			experience := owner.Stats["expert_job_exp"]
			wantEndurance := seedEndurance
			if level >= config.EnduranceMinimumLevel {
				wantEndurance -= config.EnduranceReduction
			}
			if after[0].character.Stats["gold"] != wantGold || experience < seedExperience+int64(rule.MinimumExperienceGain) || experience > seedExperience+int64(rule.MaximumExperienceGain) ||
				owner.Stats[currentExpertJobMachineEnduranceStat] != wantEndurance || (!test.menu && store.Endurance != wantEndurance) ||
				int64(binary.LittleEndian.Uint32(ackBody[2:6])) != experience || int64(binary.LittleEndian.Uint32(ackBody[7:11])) != wantEndurance {
				t.Fatalf("ACK/persisted gold/experience/endurance mismatch: ACK=%x requester=%+v owner=%+v", ackBody, after[0].character.Stats, owner.Stats)
			}
			if !reflect.DeepEqual(before[0].equipment, after[0].equipment) || !reflect.DeepEqual(before[1].equipment, after[1].equipment) {
				t.Fatal("backpack enchant changed equipped items")
			}
			if test.self {
				if !reflect.DeepEqual(before[1], after[1]) || ownerConn.write.Len() != 0 {
					t.Fatal("registered self enchant touched the unrelated fixture actor")
				}
			} else if !reflect.DeepEqual(before[1].inventory.Slots, after[1].inventory.Slots) || !reflect.DeepEqual(before[1].inventory.Warehouse, after[1].inventory.Warehouse) || !reflect.DeepEqual(before[1].account, after[1].account) ||
				after[1].character.Stats["gold"] != 100100 || ownerConn.write.Len() == 0 {
				t.Fatal("peer store mutated owner's items or lost owner fee/notification")
			}
			if test.menu && (len(service.expertJobStores) != 0 || len(service.expertJobVisitors) != 0) {
				t.Fatal("menu self registered a public store/visitor")
			}
			if test.shared {
				if _, occupied := after[0].account.Slots["0:359"]; occupied || !seenRows[359] {
					t.Fatal("exhausted authoritative account slot must be absent and explicitly refreshed; stale character copy must not substitute it")
				}
				// The row loop above already requires FFFFFFFF/0 for slot359.
				// This proves the immediate update cannot resurrect the shadow;
				// it does not claim that old durable copies are physically deleted.
				t.Logf("shared slot359 account10->absent; stale character777->%d; emitted FFFFFFFF/0, no shadow fallback", after[0].inventory.Slots["0:359"].Count)
			}
			t.Logf("official-24.9 request=%x ACK=%x op13=0 affected_op14_rows=%d wallet_rows=%d target28=%d decoy9_unchanged=%d exhausted=%t exp=%d endurance=%d", body, ackBody, refreshCount, walletCount, targetID, decoyID, test.exhausted, experience, wantEndurance)
		})
	}
}

type incrementalEnchantSnapshot struct {
	character dnfrepo.CharacterRecord
	inventory dnfrepo.InventoryRecord
	account   dnfrepo.AccountInventoryRecord
	equipment dnfrepo.EquipmentRecord
}

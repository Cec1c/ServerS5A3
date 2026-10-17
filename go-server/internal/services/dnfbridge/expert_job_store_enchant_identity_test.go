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

// Exact-input backend regression, developed against cf3943f.
// All characters, accounts, store registrations, and connections below are
// independent memory fixtures. Only the item/recipe IDs and slots match the
// historical input. This does not reconstruct that incident's actors, native
// selection globals, object lifetimes, icons, or actual self-entry protocol.
// No socket/listener or SQL repository is created.
func TestR4RealPVFEnchant601HandlerReplay(t *testing.T) {
	const (
		pvfSHA   = "369d072b82c715aea4e02813d89ad9225686983e147bc7be0ee0cae2e296bb06"
		targetID = int64(100220137)
		cardID   = int64(3621)
		recipeID = int64(10015129)
		decoyID  = int64(28309)
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
	level := config.Recipes.Level(0)
	rule, ruleOK := config.CardExperienceByLevel[level]
	if !ok || !ruleOK || card.Qualification != 0 || rule.SuccessRates[0] != 100 {
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
		name  string
		self  bool
		short bool
	}{
		{name: "entered_peer_success"},
		{name: "registered_self_success", self: true},
		{name: "entered_peer_material_short", short: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			// IDs 19/20 and account-19/account-20 are invented fixture owners,
			// deliberately unrelated to historical/new live characters.
			repos := dnfrepomemory.NewMemoryGroup()
			for _, id := range []string{"19", "20"} {
				character := dnfrepo.CharacterRecord{CharacterID: id, AccountID: "account-" + id, Level: 90,
					Stats: map[string]int64{"gold": 100000, "expert_job_type": 1, "expert_job_exp": 0, currentExpertJobMachineEnduranceStat: 300}}
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
						entry := currentItemListEntryFromStack(0, slot, stack)
						stack.RawEntry = append([]byte(nil), entry.data[:]...)
						inventory.Slots[currentCeraShopInventorySlotKey(0, slot)] = stack
					}
					for index, material := range recipe.Materials {
						count := material.Count * 2
						if test.short && material.ItemID == 3166 {
							count = material.Count - 1
						}
						stack := dnfrepo.ItemStack{ItemID: material.ItemID, Count: count, Extra: map[string]string{"item_kind": "stackable"}}
						if fixed, shared := dnfrepo.AccountWarehouseSlotForItem(material.ItemID); shared {
							account.Slots[dnfrepo.AccountSharedInventorySlotKey(fixed)] = stack
						} else {
							inventory.Slots[fmt.Sprintf("0:%d", 150+index)] = stack
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
				OwnerSession: ownerSession, Endurance: 300, Cost: 100, Qualifications: config.Qualifications(0), TownID: 2, AreaID: 5}
			service := &Service{repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true },
				expertJobCatalog: jobs, pvfItemCatalog: items, initialEquipmentArchive: archive,
				onlinePlayers: newOnlinePlayerManager(), expertJobStores: map[uint16]*currentExpertJobStore{ownerID: store}}
			service.options.gameUpperHeader = gameUpperHeaderChannel13
			service.options.gameUpperBodyCodec = gameUpperBodyCodecPlain
			service.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 19, Session: session, TownID: 2, AreaID: 5})
			if !test.self {
				service.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: ownerID, Session: ownerSession, TownID: 2, AreaID: 5})
			}
			if _, entered := service.enterCurrentExpertJobStore(session, ownerID); !entered {
				t.Fatal("fixture failed to enter its registered store")
			}
			if _, admitted := service.currentExpertJobEnteredStore(session, ownerID, currentExpertJobEnchantStoreKind); !admitted {
				t.Fatal("fixture did not meet the actual handler admission requirements")
			}
			// Model a previously logged-in inventory using the real projection
			// builder before snapshotting. No ACK or refresh is sent here.
			for _, listType := range currentSelectedItemListTypes {
				if _, _, _, known := service.buildCurrentItemListBodyForSession(ctx, session, listType); !known {
					t.Fatalf("fixture cannot project required container %d", listType)
				}
			}
			snapshot := func() [2]r4EnchantReplaySnapshot {
				t.Helper()
				var all [2]r4EnchantReplaySnapshot
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
			ackCount, ackIndex, mainCount, packetIndex := 0, -1, 0, 0
			var ackBody []byte
			for wire := requesterConn.write.Bytes(); len(wire) > 0; packetIndex++ {
				if len(wire) < dnfproto.GameServerUpperHeaderSize {
					t.Fatalf("truncated serialized frame: %x", wire)
				}
				id := binary.LittleEndian.Uint16(wire[1:3])
				headerSize := dnfproto.GameServerUpperHeaderSize
				if id == 13 {
					headerSize = dnfproto.GameServerUpperHeaderSize16
				}
				packet, rest := splitGameServerUpperPacketWithHeader(t, wire, headerSize)
				wire = rest
				if id == 601 {
					ackCount++
					ackIndex, ackBody = packetIndex, packet.Body
					if packet.Header.Classification != 1 {
						t.Fatalf("601 ACK classification=%d want1", packet.Header.Classification)
					}
					continue
				}
				if test.short && (id == 13 || id == 14) {
					t.Fatalf("material rejection emitted successful inventory refresh op%d: %x", id, packet.Body)
				}
				if id != 13 {
					continue
				}
				if ackCount != 1 || ackIndex >= packetIndex || len(ackBody) != 11 || ackBody[0] != 1 || ackBody[1] != 1 {
					t.Fatalf("serialized op13 was not after one successful 601 ACK: ack=%x", ackBody)
				}
				if packet.Header.Classification != 0 || len(packet.Body) < 1 {
					t.Fatalf("malformed op13 class=%d body=%x", packet.Header.Classification, packet.Body)
				}
				if packet.Body[0] != 0 {
					continue
				}
				if len(packet.Body) < 5 {
					t.Fatalf("malformed main op13 body=%x", packet.Body)
				}
				mainCount++
				count := int(binary.LittleEndian.Uint16(packet.Body[3:5]))
				if len(packet.Body) != 5+count*currentItemListEntryWireSize {
					t.Fatalf("serialized main op13 has invalid row framing: %x", packet.Body)
				}
				seen := map[uint16]bool{}
				for index := 0; index < count; index++ {
					row := packet.Body[5+index*currentItemListEntryWireSize : 5+(index+1)*currentItemListEntryWireSize]
					slot := binary.LittleEndian.Uint16(row[0:2])
					if seen[slot] {
						t.Fatalf("serialized main op13 has duplicate slot%d", slot)
					}
					seen[slot] = true
					item := int64(binary.LittleEndian.Uint32(row[2:6]))
					cardWord := binary.LittleEndian.Uint32(row[0x0e:0x12])
					switch slot {
					case 28:
						if item != targetID || cardWord != uint32(cardID) || binary.LittleEndian.Uint32(row[6:10]) != 999999998 {
							t.Fatalf("serialized target slot28 identity/quality/enchant wrong: %x", row)
						}
					case 9:
						if item != decoyID || cardWord != 0 || binary.LittleEndian.Uint32(row[6:10]) != 111111111 {
							t.Fatalf("serialized decoy slot9 identity/quality/enchant changed: %x", row)
						}
					case 237:
						if item != cardID || binary.LittleEndian.Uint32(row[6:10]) != 1 {
							t.Fatalf("serialized card slot237 identity/count wrong: %x", row)
						}
					}
				}
				if !seen[28] || !seen[9] || !seen[237] {
					t.Fatalf("serialized main op13 omitted target/decoy/card slots: %v", seen)
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
			if mainCount != 1 || len(ackBody) != 11 || ackBody[0] != 1 || ackBody[1] != 1 || ackBody[6] != 0 {
				t.Fatalf("success ACK/main refresh mismatch: ACK=%x main_op13=%d", ackBody, mainCount)
			}
			inventory := after[0].inventory
			if inventory.Slots["0:28"].ItemID != targetID || inventory.Slots["0:28"].Count != 1 ||
				inventory.Slots["0:28"].Extra["enchant_card_id"] != strconv.FormatInt(cardID, 10) ||
				!reflect.DeepEqual(before[0].inventory.Slots["0:9"], inventory.Slots["0:9"]) ||
				inventory.Slots["0:237"].ItemID != cardID || inventory.Slots["0:237"].Count != 1 {
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
				if remaining != material.Count {
					t.Fatalf("material%d remaining=%d want%d (exactly one attempt consumed)", material.ItemID, remaining, material.Count)
				}
			}
			ownerIndex := 1
			wantGold := int64(99900)
			if test.self {
				ownerIndex, wantGold = 0, 100000
			}
			owner := after[ownerIndex].character
			experience := owner.Stats["expert_job_exp"]
			wantEndurance := int64(300)
			if level >= config.EnduranceMinimumLevel {
				wantEndurance -= config.EnduranceReduction
			}
			if after[0].character.Stats["gold"] != wantGold || experience < int64(rule.MinimumExperienceGain) || experience > int64(rule.MaximumExperienceGain) ||
				owner.Stats[currentExpertJobMachineEnduranceStat] != wantEndurance || store.Endurance != wantEndurance ||
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
			t.Logf("accepted exact601 body=%x ACK=%x main_op13=%d; serialized28=%d serialized9=%d card237=%d/1", body, ackBody, mainCount, targetID, decoyID, cardID)
		})
	}
}

type r4EnchantReplaySnapshot struct {
	character dnfrepo.CharacterRecord
	inventory dnfrepo.InventoryRecord
	account   dnfrepo.AccountInventoryRecord
	equipment dnfrepo.EquipmentRecord
}

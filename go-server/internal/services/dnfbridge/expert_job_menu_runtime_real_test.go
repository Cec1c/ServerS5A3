package dnfbridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"reflect"
	"testing"

	dnfexpertjob "longheng.io/server/internal/modules/dnf/expertjob"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// Adapted from the real-resource menu regression in 1a906a0. These are isolated
// memory actors, not live character 72. Exercise the actual 601 handler with a
// registered town session and no public store, without any feature environment
// switch. The only environment variable selects the hash-pinned test resource.
func TestRealPVFExpertJobMenuRuntimeEnchant(t *testing.T) {
	const (
		pvfSHA         = "369d072b82c715aea4e02813d89ad9225686983e147bc7be0ee0cae2e296bb06"
		targetID       = int64(100220137)
		cardID         = int64(3621)
		recipeID       = int64(10015129)
		seedExperience = int64(1195)
		seedEndurance  = int64(173) // Deliberately not the PVF initial endurance.
		seedGold       = int64(100000)
	)
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the pinned real PVF")
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	_, hashErr := io.Copy(hash, file)
	closeErr := file.Close()
	if hashErr != nil || closeErr != nil {
		t.Fatalf("hash PVF: read=%v close=%v", hashErr, closeErr)
	}
	if got := fmt.Sprintf("%x", hash.Sum(nil)); got != pvfSHA {
		t.Fatalf("PVF SHA=%s want %s", got, pvfSHA)
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
		t.Fatal("missing real enchanter config")
	}
	recipe, ok := config.CardRecipes[recipeID]
	if !ok || recipe.Qualification != 0 || recipe.RequiredLevel != 1 {
		t.Fatalf("recipe=%+v found=%t", recipe, ok)
	}
	wantMaterials := map[int64]int64{3227: 5, 3166: 20}
	seen := map[int64]bool{}
	for _, material := range recipe.Materials {
		if seen[material.ItemID] || wantMaterials[material.ItemID] != material.Count {
			t.Fatalf("unexpected real material %+v", material)
		}
		seen[material.ItemID] = true
	}
	if len(seen) != len(wantMaterials) {
		t.Fatalf("incomplete real recipe: %+v", recipe)
	}
	level := config.Recipes.Level(seedExperience)
	rule, ruleOK := config.CardExperienceByLevel[level]
	card, cardOK := config.Cards[cardID]
	if !ruleOK || !cardOK || card.Qualification != recipe.Qualification || rule.SuccessRates[0] != 100 ||
		rule.MinimumExperienceGain <= 0 || rule.MaximumExperienceGain < rule.MinimumExperienceGain ||
		level < config.EnduranceMinimumLevel || config.EnduranceReduction <= 0 || seedEndurance == config.InitialEndurance {
		t.Fatalf("fixture must guarantee success, experience gain and real endurance consumption: level=%d rule=%+v card=%+v reduction=%d initial=%d", level, rule, card, config.EnduranceReduction, config.InitialEndurance)
	}
	metadata, err := resolveCurrentEnchantCardMetadata(items, archive, cardID, targetID)
	if err != nil || metadata.CardItemID != cardID || metadata.TargetEquipmentType != "[waist]" ||
		!currentExpertJobStoreStringContainsFold(metadata.AllowedEquipmentTypes, metadata.TargetEquipmentType) ||
		(len(metadata.UpgradeCounts) > 0 && !currentExpertJobStoreIntContains(metadata.UpgradeCounts, 0)) {
		t.Fatalf("real card/target incompatible: %+v err=%v", metadata, err)
	}
	t.Logf("pinned PVF=%s recipe=%d target=%d card=%d materials=%v level=%d exp_gain=%d..%d endurance_reduction=%d", pvfSHA, recipeID, targetID, cardID, wantMaterials, level, rule.MinimumExperienceGain, rule.MaximumExperienceGain, config.EnduranceReduction)
	for _, tc := range []struct {
		name          string
		owner         uint16
		shortMaterial int64
		accountMode   string
		zeroEndurance bool
		lockedTarget  bool
	}{
		{name: "self_no_store_success", owner: 72},
		{name: "self_missing_3227_unchanged", owner: 72, shortMaterial: 3227},
		{name: "self_missing_3166_unchanged", owner: 72, shortMaterial: 3166},
		{name: "other_owner_cannot_use_self_entry", owner: 73},
		{name: "empty_session_account_unchanged", owner: 72, accountMode: "empty"},
		{name: "mismatched_session_account_unchanged", owner: 72, accountMode: "mismatch"},
		{name: "zero_endurance_unchanged", owner: 72, zeroEndurance: true},
		{name: "locked_target_unchanged", owner: 72, lockedTarget: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repos := dnfrepomemory.NewMemoryGroup()
			for _, id := range []string{"72", "73"} {
				character := dnfrepo.CharacterRecord{CharacterID: id, AccountID: "menu-runtime-" + id, Level: 90,
					Stats: map[string]int64{"gold": seedGold, "expert_job_type": int64(dnfexpertjob.EnchanterType), "expert_job_exp": seedExperience, currentExpertJobMachineEnduranceStat: seedEndurance}}
				inventory := dnfrepo.InventoryRecord{CharacterID: id, Slots: map[string]dnfrepo.ItemStack{}}
				account := dnfrepo.AccountInventoryRecord{AccountID: character.AccountID, Slots: map[string]dnfrepo.ItemStack{}}
				if id == "72" {
					if tc.zeroEndurance {
						character.Stats[currentExpertJobMachineEnduranceStat] = 0
					}
					for slot, stack := range map[int16]dnfrepo.ItemStack{
						28:  {ItemID: targetID, Count: 1, Extra: map[string]string{"item_kind": "equipment", "quality_seed": "999999998", "durability": "25"}},
						237: {ItemID: cardID, Count: 2, Extra: map[string]string{"item_kind": "stackable"}},
					} {
						if slot == 28 && tc.lockedTarget {
							stack.Extra["equipment_lock_state"] = "locked"
						}
						entry := currentItemListEntryFromStack(0, slot, stack)
						stack.RawEntry = append([]byte(nil), entry.data[:]...)
						inventory.Slots[currentCeraShopInventorySlotKey(0, slot)] = stack
					}
					for index, material := range recipe.Materials {
						count := material.Count * 2
						if tc.shortMaterial == material.ItemID {
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
			conn, otherConn := &bufferConn{}, &bufferConn{}
			session := &gameSession{conn: conn, selectedCharacterID: 72, accountID: "menu-runtime-72"}
			otherSession := &gameSession{conn: otherConn, selectedCharacterID: 73, accountID: "menu-runtime-73"}
			service := &Service{repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }, expertJobCatalog: jobs,
				pvfItemCatalog: items, initialEquipmentArchive: archive, onlinePlayers: newOnlinePlayerManager()}
			service.options.gameUpperHeader = gameUpperHeaderChannel13
			service.options.gameUpperBodyCodec = gameUpperBodyCodecPlain
			service.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 72, Session: session, TownID: 2, AreaID: 5})
			service.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 73, Session: otherSession, TownID: 2, AreaID: 5})
			assertNoStore := func() {
				t.Helper()
				service.expertJobStoreMu.Lock()
				defer service.expertJobStoreMu.Unlock()
				if len(service.expertJobStores) != 0 || len(service.expertJobVisitors) != 0 {
					t.Fatal("menu use created a public store or visitor registration")
				}
			}
			assertNoStore()
			// Normalize the logged-in container projection before taking snapshots,
			// so rejection checks detect transaction changes, not fixture setup.
			for _, list := range currentSelectedItemListTypes {
				if _, _, _, known := service.buildCurrentItemListBodyForSession(ctx, session, list); !known {
					t.Fatalf("cannot project container %d", list)
				}
			}
			snapshot := func() [2]menuRuntimeEnchantSnapshot {
				t.Helper()
				var all [2]menuRuntimeEnchantSnapshot
				for index, id := range []string{"72", "73"} {
					var found bool
					var err error
					all[index].character, found, err = repos.Character.Load(ctx, id)
					if err != nil || !found {
						t.Fatalf("character %s found=%t err=%v", id, found, err)
					}
					all[index].inventory, found, err = repos.Inventory.Load(ctx, id)
					if err != nil || !found {
						t.Fatalf("inventory %s found=%t err=%v", id, found, err)
					}
					all[index].account, found, err = repos.AccountInventory.Load(ctx, "menu-runtime-"+id)
					if err != nil || !found {
						t.Fatalf("account %s found=%t err=%v", id, found, err)
					}
					all[index].equipment, found, err = repos.Equipment.Load(ctx, id)
					if err != nil || !found {
						t.Fatalf("equipment %s found=%t err=%v", id, found, err)
					}
				}
				return all
			}
			before := snapshot()
			// Corrupt only the request session after normal login projection. The
			// persisted owner/account remain valid, testing admission isolation.
			if tc.accountMode == "empty" {
				session.accountID = ""
			}
			if tc.accountMode == "mismatch" {
				session.accountID = "menu-runtime-73"
			}
			body := make([]byte, 13)
			binary.LittleEndian.PutUint16(body[0:2], tc.owner)
			binary.LittleEndian.PutUint32(body[2:6], uint32(recipeID))
			body[6] = 2
			binary.LittleEndian.PutUint16(body[8:10], 28)
			binary.LittleEndian.PutUint16(body[11:13], 237)
			if err := service.handleCurrentExpertJobStoreEnchant(session, 601, body); err != nil {
				t.Fatal(err)
			}
			after := snapshot()
			assertNoStore()
			if !reflect.DeepEqual(before[1], after[1]) || otherConn.write.Len() != 0 {
				t.Fatal("menu enchant changed or notified unrelated owner")
			}
			var ack, jobInfo []byte
			infoCount := 0
			ackCount, mainRefresh := 0, 0
			rejected := tc.shortMaterial != 0 || tc.owner != 72 || tc.accountMode != "" || tc.zeroEndurance || tc.lockedTarget
			for wire := conn.write.Bytes(); len(wire) > 0; {
				if len(wire) < dnfproto.GameServerUpperHeaderSize {
					t.Fatalf("truncated frame %x", wire)
				}
				id := binary.LittleEndian.Uint16(wire[1:3])
				header := dnfproto.GameServerUpperHeaderSize
				if id == 13 {
					header = dnfproto.GameServerUpperHeaderSize16
				}
				packet, rest := splitGameServerUpperPacketWithHeader(t, wire, header)
				wire = rest
				switch id {
				case 533, 538, 539, 544:
					t.Fatalf("self menu emitted owner/public-store notification %d", id)
				case 205:
					infoCount++
					jobInfo = packet.Body
					if rejected || ackCount != 1 || packet.Header.Classification != 0 {
						t.Fatalf("unexpected profession refresh class=%d rejected=%t ACKs=%d", packet.Header.Classification, rejected, ackCount)
					}
				}
				if id == 601 {
					ackCount++
					ack = packet.Body
					if packet.Header.Classification != 1 {
						t.Fatalf("ACK class=%d", packet.Header.Classification)
					}
				}
				if rejected && (id == 13 || id == 14) {
					t.Fatalf("rejection emitted inventory refresh %d", id)
				}
				if id == 13 {
					if ackCount != 1 || len(ack) != 11 || ack[0] != 1 || ack[1] != 1 {
						t.Fatalf("refresh before successful ACK: %x", ack)
					}
					if len(packet.Body) == 0 {
						t.Fatal("empty inventory refresh")
					}
					if packet.Body[0] == 0 {
						mainRefresh++
					}
				}
			}
			if ackCount != 1 {
				t.Fatalf("601 ACK count=%d", ackCount)
			}
			if rejected {
				wantCode := byte(19)
				if tc.shortMaterial != 0 {
					wantCode = currentExpertJobStoreError(errCurrentExpertJobInsufficientMaterials)
				}
				if tc.zeroEndurance {
					wantCode = currentExpertJobStoreError(dnfexpertjob.ErrMachineEndurance)
				}
				if tc.lockedTarget {
					wantCode = currentExpertJobStoreError(dnfexpertjob.ErrExtractionInvalid)
				}
				if !bytes.Equal(ack, []byte{0, wantCode}) || !reflect.DeepEqual(before, after) {
					t.Fatalf("rejection ACK=%x want=00%02x assets_unchanged=%t", ack, wantCode, reflect.DeepEqual(before, after))
				}
				t.Logf("request=%x ACK=%x; both actors' full assets unchanged; no public store", body, ack)
				return
			}
			target := after[0].inventory.Slots["0:28"]
			if target.ItemID != targetID || target.Count != 1 || target.Extra["enchant_card_id"] != "3621" || target.Extra["enchant_upgrade_count"] != "0" ||
				target.Extra["quality_seed"] != "999999998" || target.Extra["durability"] != "25" {
				t.Fatalf("persisted target=%+v", target)
			}
			row := target.RawEntry
			if len(row) != currentItemListEntryWireSize || binary.LittleEndian.Uint16(row[0:2]) != 28 || binary.LittleEndian.Uint32(row[2:6]) != uint32(targetID) || binary.LittleEndian.Uint32(row[0x0e:0x12]) != uint32(cardID) || row[0x12] != 0 {
				t.Fatalf("persisted enchant row=%x", row)
			}
			if card := after[0].inventory.Slots["0:237"]; card.ItemID != cardID || card.Count != 1 {
				t.Fatalf("card deduction=%+v", card)
			}
			for _, material := range recipe.Materials {
				remaining := int64(0)
				if fixed, shared := dnfrepo.AccountWarehouseSlotForItem(material.ItemID); shared {
					remaining = after[0].account.Slots[dnfrepo.AccountSharedInventorySlotKey(fixed)].Count
				} else {
					for _, stack := range after[0].inventory.Slots {
						if stack.ItemID == material.ItemID {
							remaining += stack.Count
						}
					}
				}
				if remaining != material.Count {
					t.Fatalf("material %d remaining=%d want=%d", material.ItemID, remaining, material.Count)
				}
			}
			stats := after[0].character.Stats
			experience := stats["expert_job_exp"]
			wantEndurance := seedEndurance - config.EnduranceReduction
			// ACK byte6 is reserved; profession level is carried by op205.
			if len(ack) != 11 || ack[0] != 1 || ack[1] != 1 || ack[6] != 0 || mainRefresh != 1 ||
				int64(binary.LittleEndian.Uint32(ack[2:6])) != experience || int64(binary.LittleEndian.Uint32(ack[7:11])) != wantEndurance ||
				stats["gold"] != seedGold || stats[currentExpertJobMachineEnduranceStat] != wantEndurance ||
				experience < seedExperience+int64(rule.MinimumExperienceGain) || experience > seedExperience+int64(rule.MaximumExperienceGain) {
				t.Fatalf("ACK=%x stats=%v main_refresh=%d", ack, stats, mainRefresh)
			}
			if infoCount != 1 || len(jobInfo) < 4 || jobInfo[0] != dnfexpertjob.EnchanterType || jobInfo[1] != dnfexpertjob.EnchanterType {
				t.Fatalf("expected one self profession refresh: count=%d body=%x", infoCount, jobInfo)
			}
			qualificationOffset := 3 + 4*int(jobInfo[2])
			if qualificationOffset >= len(jobInfo) {
				t.Fatalf("truncated op205 recipes: %x", jobInfo)
			}
			levelOffset := qualificationOffset + 1 + int(jobInfo[qualificationOffset])
			if levelOffset+16 != len(jobInfo) || !bytes.Equal(jobInfo[qualificationOffset+1:levelOffset], config.Qualifications(experience)) ||
				int(binary.LittleEndian.Uint32(jobInfo[levelOffset:levelOffset+4])) != config.Recipes.Level(experience) ||
				int64(binary.LittleEndian.Uint32(jobInfo[levelOffset+4:levelOffset+8])) != wantEndurance ||
				string(jobInfo[levelOffset+8:levelOffset+12]) != "EJXP" || int64(binary.LittleEndian.Uint32(jobInfo[levelOffset+12:])) != experience {
				t.Fatalf("op205 qualifications/level/endurance/server EXP footer disagree with persisted state: %x", jobInfo)
			}
			if !reflect.DeepEqual(before[0].equipment, after[0].equipment) {
				t.Fatal("backpack enchant changed equipped items")
			}
			t.Logf("request=%x ACK=%x; card2->1 materials each consumed once; gold=%d exp=%d->%d endurance=%d->%d; no public store", body, ack, stats["gold"], seedExperience, experience, seedEndurance, wantEndurance)
		})
	}
}

type menuRuntimeEnchantSnapshot struct {
	character dnfrepo.CharacterRecord
	inventory dnfrepo.InventoryRecord
	account   dnfrepo.AccountInventoryRecord
	equipment dnfrepo.EquipmentRecord
}

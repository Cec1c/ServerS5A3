package dnfbridge

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/binary"
	"fmt"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/raidcurrency"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// monsterTarotFixedRandReader implements the fixed-byte crypto/rand.Reader used
// by one subtests. crypto/rand.Int reads a big-endian integer and masks the high
// bits, so returning the desired in-range roll makes the real router deterministic
// without changing production randomness.
type monsterTarotFixedRandReader struct {
	value uint64
}

func (r *monsterTarotFixedRandReader) Read(p []byte) (int, error) {
	for index := range p {
		p[index] = 0
	}
	value := r.value
	for index := len(p) - 1; index >= 0 && value != 0; index-- {
		p[index] = byte(value)
		value >>= 8
	}
	return len(p), nil
}

type monsterTarotInventoryRewardDelta struct {
	listType byte
	slot     int16
	delta    int64
}

func monsterTarotInventoryRewardDeltas(
	before, after map[string]dnfrepo.ItemStack,
	itemID int64,
) ([]monsterTarotInventoryRewardDelta, int64) {
	var deltas []monsterTarotInventoryRewardDelta
	var total int64
	for key, stack := range after {
		if stack.ItemID != itemID {
			continue
		}
		listType, slot, parsed := parseSceneInventorySlotKey(key)
		if !parsed {
			continue
		}
		var previous int64
		if prior, found := before[key]; found && prior.ItemID == itemID {
			previous = prior.Count
		}
		delta := stack.Count - previous
		if delta <= 0 {
			continue
		}
		deltas = append(deltas, monsterTarotInventoryRewardDelta{listType: listType, slot: slot, delta: delta})
		total += delta
	}
	return deltas, total
}

func monsterTarotRewardContainerEvidence(definition dungeonDropItemDefinition) string {
	container := currentPVFItemTargetContainer(definition)
	placement, err := currentBoosterPlacement(definition)
	sharedSlot, shared := currentDisjointWarehouseFixedSlots[definition.ItemID]
	return fmt.Sprintf(
		"kind=%s pvf=%s list=%d range=%d..%d pet_consumable=%t pet_body=%t pet_artifact=%t avatar=%t guild_medal=%t guardian_gem=%t account_shared=%t/%d placement=%v placement_err=%v fate=%t",
		definition.Kind,
		definition.PVFPath,
		container.ListType,
		container.SlotStart,
		container.SlotEnd,
		container.PetConsumable,
		container.PetBody,
		container.PetArtifact,
		container.Avatar,
		container.GuildMedal,
		container.GuardianGem,
		shared,
		sharedSlot,
		placement,
		err,
		definition.ItemID == raidcurrency.FateItemID,
	)
}

// TestRealPVFMonsterTarotOp27EveryRewardContainerAndSlot exercises every
// positive-weight reward in every real monster-tarot PVF document. Each case
// replaces only crypto/rand.Reader with a fixed in-range value, runs the real
// upper-packet op27 handler, and checks the real ACK row against the actual
// committed per-slot delta. Production roll resolution remains random.
func TestRealPVFMonsterTarotOp27EveryRewardContainerAndSlot(t *testing.T) {
	catalog := loadMonsterTarotRealPVFCatalog(t)
	originalReader := cryptorand.Reader
	defer func() { cryptorand.Reader = originalReader }()

	seenSources := 0
	seenReachableRewards := 0
	sequence := uint16(1)
	for sourceID := uint32(10159697); sourceID <= 10159718; sourceID++ {
		sourceID := sourceID
		definition, err := resolveCurrentBoosterDefinition(catalog, sourceID, currentBoosterRequestRandom)
		if err != nil {
			t.Fatalf("source=%d resolve definition: %v", sourceID, err)
		}
		if definition.Source.ItemID != sourceID || definition.StackableType != "[monster tarot card]" || definition.Random.Kind != "random" || len(definition.Random.Groups) != 1 {
			t.Fatalf("source=%d unexpected definition=%+v", sourceID, definition)
		}
		if definition.LotteryGoldCost < 0 || definition.LotteryGoldCost > 234567 {
			t.Fatalf("source=%d invalid opening cost=%d", sourceID, definition.LotteryGoldCost)
		}
		wantGoldAfter := int64(234567) - definition.LotteryGoldCost
		group := definition.Random.Groups[0]
		if group.DrawCount != 1 || len(group.Entries) == 0 {
			t.Fatalf("source=%d unexpected group=%+v", sourceID, group)
		}

		var totalWeight int64
		for _, entry := range group.Entries {
			if entry.Weight > 0 {
				totalWeight += entry.Weight
			}
		}
		if totalWeight <= 0 {
			t.Fatalf("source=%d has no positive-weight reward", sourceID)
		}

		cursor := int64(0)
		for entryIndex, entry := range group.Entries {
			entryIndex, entry := entryIndex, entry
			if entry.ItemID <= 0 || entry.ItemID > int64(^uint32(0)) || entry.Weight < 0 || entry.Count <= 0 || entry.Count > int64(^uint32(0)) {
				t.Fatalf("source=%d reward=%d invalid: %+v", sourceID, entryIndex, entry)
			}
			rewardDefinition, err := catalog.ResolveItem(uint32(entry.ItemID))
			if err != nil {
				t.Fatalf("source=%d reward=%d item=%d resolve: %v", sourceID, entryIndex, entry.ItemID, err)
			}
			containerEvidence := monsterTarotRewardContainerEvidence(rewardDefinition)
			t.Logf("source=%d reward=%d item=%d count=%d weight=%d %s", sourceID, entryIndex, entry.ItemID, entry.Count, entry.Weight, containerEvidence)
			if entry.Weight == 0 {
				continue
			}

			targetRoll := cursor
			cursor += entry.Weight
			seenReachableRewards++
			t.Run(fmt.Sprintf("%d_reward_%02d_%d", sourceID, entryIndex, entry.ItemID), func(t *testing.T) {
				frameSequence := sequence
				sequence++
				request := currentBoosterOpenRequest{Kind: currentBoosterRequestRandom, SourceSlot: 75, AllowLotteryGold: true}
				rollCalls := 0
				amounts, _, err := resolveCurrentBoosterRewardAmounts(definition, request, func(limit int64) (int64, error) {
					rollCalls++
					if limit != totalWeight {
						return 0, fmt.Errorf("roll limit=%d, want %d", limit, totalWeight)
					}
					return targetRoll, nil
				})
				if err != nil || rollCalls != 1 {
					t.Fatalf("source=%d reward=%d deterministic resolver calls=%d err=%v", sourceID, entryIndex, rollCalls, err)
				}
				wantCount, selected := amounts[uint32(entry.ItemID)]
				if !selected || wantCount != uint32(entry.Count) || len(amounts) != 1 {
					t.Fatalf("source=%d reward=%d target=%d resolver amounts=%v want item=%d count=%d container=%s",
						sourceID, entryIndex, targetRoll, amounts, entry.ItemID, entry.Count, containerEvidence)
				}

				repositories := monsterTarotTestAssets(t, 234567, 1)
				if err := repositories.Account.Save(context.Background(), dnfrepo.AccountRecord{AccountID: "account-1"}); err != nil {
					t.Fatal(err)
				}
				if err := repositories.AccountInventory.Save(context.Background(), dnfrepo.AccountInventoryRecord{
					AccountID: "account-1",
					Slots:     map[string]dnfrepo.ItemStack{},
				}); err != nil {
					t.Fatal(err)
				}
				seededInventory, found, err := repositories.Inventory.Load(context.Background(), "19")
				if err != nil || !found {
					t.Fatalf("load seed inventory found=%t err=%v", found, err)
				}
				seededInventory.Slots["0:75"] = dnfrepo.ItemStack{ItemID: int64(sourceID), Count: 1}
				if err := repositories.Inventory.Save(context.Background(), seededInventory); err != nil {
					t.Fatal(err)
				}
				beforeInventory, found, err := repositories.Inventory.Load(context.Background(), "19")
				if err != nil || !found {
					t.Fatalf("load before inventory found=%t err=%v", found, err)
				}
				service := currentBoosterTestService(catalog, repositories)
				service.options.currentClientVersion = "official-24.9"
				connection := &bufferConn{}
				session := &gameSession{
					conn:                connection,
					connID:              fmt.Sprintf("monster-tarot-real-%d-%d", sourceID, entryIndex),
					selectedCharacterID: 19,
				}

				var handlerErr error
				func() {
					previous := cryptorand.Reader
					defer func() { cryptorand.Reader = previous }()
					cryptorand.Reader = &monsterTarotFixedRandReader{value: uint64(targetRoll)}
					handlerErr = service.handleGameUpper(session, monsterTarotRouteOp27Frame(t, 0, 75, frameSequence))
				}()
				if handlerErr != nil {
					t.Fatalf("source=%d reward=%d handler_error=%v container=%s", sourceID, entryIndex, handlerErr, containerEvidence)
				}

				packets := drainUpperPackets(t, connection)
				var ack []byte
				for _, packet := range packets {
					if packet.msgID != 27 {
						continue
					}
					if ack != nil {
						t.Fatalf("source=%d reward=%d duplicate op27 packets=%+v", sourceID, entryIndex, packets)
					}
					ack = packet.body
				}
				if len(ack) != 1+2+currentItemListEntryWireSize+4 || ack[0] != 1 || binary.LittleEndian.Uint16(ack[1:3]) != 75 {
					t.Fatalf("source=%d reward=%d item=%d target=%d container=%s ack=%x packets=%+v",
						sourceID, entryIndex, entry.ItemID, targetRoll, containerEvidence, ack, packets)
				}

				afterInventory, found, err := repositories.Inventory.Load(context.Background(), "19")
				if err != nil || !found {
					t.Fatalf("load after inventory found=%t err=%v", found, err)
				}
				if source, present := afterInventory.Slots["0:75"]; present && source.ItemID == int64(sourceID) {
					t.Fatalf("source=%d reward=%d consumed tarot remains: %+v", sourceID, entryIndex, source)
				}
				character, found, err := repositories.Character.Load(context.Background(), "19")
				if err != nil || !found || character.Stats["gold"] != wantGoldAfter || character.Stats["fatigue"] != 120 {
					t.Fatalf("source=%d reward=%d character found=%t stats=%v want_gold=%d err=%v", sourceID, entryIndex, found, character.Stats, wantGoldAfter, err)
				}
				if entry.ItemID == int64(raidcurrency.FateItemID) {
					account, found, err := repositories.Account.Load(context.Background(), "account-1")
					if err != nil || !found {
						t.Fatalf("load Fate account found=%t err=%v", found, err)
					}
					state, err := raidcurrency.Read(account, time.Now().UTC())
					if err != nil || state.Balance != wantCount || state.Earned != wantCount {
						t.Fatalf("source=%d reward=%d Fate state=%+v err=%v want=%d", sourceID, entryIndex, state, err, wantCount)
					}
					ackSlot := int16(binary.LittleEndian.Uint16(ack[3:5]))
					ackItem := binary.LittleEndian.Uint32(ack[5:9])
					ackDelta := binary.LittleEndian.Uint32(ack[9:13])
					if ackSlot != 0 || ackItem != raidcurrency.FateItemID || ackDelta != wantCount {
						t.Fatalf("source=%d reward=%d Fate ACK slot/item/delta=%d/%d/%d want=0/%d/%d",
							sourceID, entryIndex, ackSlot, ackItem, ackDelta, raidcurrency.FateItemID, wantCount)
					}
					return
				}
				deltas, landed := monsterTarotInventoryRewardDeltas(beforeInventory.Slots, afterInventory.Slots, entry.ItemID)
				if landed != int64(wantCount) {
					t.Fatalf("source=%d reward=%d item=%d committed delta=%d want=%d container=%s ack=%x packets=%+v",
						sourceID, entryIndex, entry.ItemID, landed, wantCount, containerEvidence, ack, packets)
				}

				wantSlot := int16(-1)
				var wantDelta int64
				mainDeltaFound := false
				for _, delta := range deltas {
					if delta.listType != dnfrepo.MainInventoryListType || delta.delta <= 0 {
						continue
					}
					if !mainDeltaFound || delta.delta > wantDelta || (delta.delta == wantDelta && delta.slot < wantSlot) {
						wantSlot, wantDelta = delta.slot, delta.delta
						mainDeltaFound = true
					}
				}
				if !mainDeltaFound {
					t.Fatalf("source=%d reward=%d item has no main-list delta container=%s deltas=%+v", sourceID, entryIndex, containerEvidence, deltas)
				}
				ackSlot := int16(binary.LittleEndian.Uint16(ack[3:5]))
				ackItem := binary.LittleEndian.Uint32(ack[5:9])
				ackDelta := binary.LittleEndian.Uint32(ack[9:13])
				countIsValid := rewardDefinition.Kind != dungeonDropItemStackable || ackDelta == uint32(wantDelta)
				if ackSlot != wantSlot || ackItem != uint32(entry.ItemID) || !countIsValid {
					t.Fatalf("source=%d reward=%d ACK real slot/item/delta=%d/%d/%d want=%d/%d/%d deltas=%+v container=%s",
						sourceID, entryIndex, ackSlot, ackItem, ackDelta, wantSlot, entry.ItemID, wantDelta, deltas, containerEvidence)
				}
				if currency := binary.LittleEndian.Uint32(ack[3+currentItemListEntryWireSize:]); currency != 0 {
					t.Fatalf("source=%d reward=%d item reward carried currency=%d", sourceID, entryIndex, currency)
				}
			})
		}
		seenSources++
	}
	if seenSources != 22 || seenReachableRewards == 0 {
		t.Fatalf("sources=%d reachable_rewards=%d", seenSources, seenReachableRewards)
	}
}

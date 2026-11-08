package dnfbridge

import (
	"errors"
	"math"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/premium"
)

func newDungeonCardFinalGoldMultiplierTestBundle(gold int64) dungeonCardRewardBundle {
	return dungeonCardRewardBundle{
		Gold: gold,
		Cost: 10780,
		Items: []dungeonCardItemReward{{
			AllowQuickSlots:  true,
			PreferQuickSlots: true,
			StackLimit:       999,
			ItemID:           3227,
			Count:            2,
			Stackable:        true,
			Bind:             true,
			SlotStart:        currentDungeonPickupQuickSlotStart,
			SlotEnd:          currentDungeonPickupQuickSlotEnd,
			ExpireAt:         time.Unix(1_900_000_000, 123_000_000).UTC(),
			RawEntry:         make([]byte, currentItemListEntryWireSize),
			Extra: map[string]string{
				"source":  "final_gold_multiplier_fixture",
				"consume": "paid_card",
			},
		}},
		ConsumePremiumDaily: true,
		PremiumDailySlot:    premium.DevilSlotGoldCard,
	}
}

func TestApplyCurrentDungeonCardFinalGoldMultiplierMultipliesGold(t *testing.T) {
	const maxMultipliableGold int64 = math.MaxInt64 / 3

	tests := []struct {
		name     string
		gold     int64
		wantGold int64
	}{
		{name: "zero", gold: 0, wantGold: 0},
		{name: "ordinary reward", gold: 1243, wantGold: 3729},
		{name: "reward after no-item compensation", gold: 1100, wantGold: 3300},
		{name: "max int64 divided by three", gold: maxMultipliableGold, wantGold: math.MaxInt64 - 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bundle := newDungeonCardFinalGoldMultiplierTestBundle(test.gold)
			if err := applyCurrentDungeonCardFinalGoldMultiplier(&bundle); err != nil {
				t.Fatalf("apply multiplier: %v", err)
			}
			if bundle.Gold != test.wantGold {
				t.Fatalf("multiplied gold = %d, want %d", bundle.Gold, test.wantGold)
			}

			bundle.Gold = 0
			wantNonGold := newDungeonCardFinalGoldMultiplierTestBundle(0)
			if !reflect.DeepEqual(bundle, wantNonGold) {
				t.Fatalf("non-gold fields changed:\n got: %#v\nwant: %#v", bundle, wantNonGold)
			}
		})
	}
}

func TestApplyCurrentDungeonCardFinalGoldMultiplierRejectsInvalidWithoutMutation(t *testing.T) {
	const maxMultipliableGold int64 = math.MaxInt64 / 3

	tests := []struct {
		name string
		gold int64
	}{
		{name: "negative", gold: -1},
		{name: "above max int64 divided by three", gold: maxMultipliableGold + 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bundle := newDungeonCardFinalGoldMultiplierTestBundle(test.gold)
			want := newDungeonCardFinalGoldMultiplierTestBundle(test.gold)
			err := applyCurrentDungeonCardFinalGoldMultiplier(&bundle)
			if !errors.Is(err, errDungeonCardPlanInvalid) {
				t.Fatalf("apply multiplier error = %v, want %v", err, errDungeonCardPlanInvalid)
			}
			if !reflect.DeepEqual(bundle, want) {
				t.Fatalf("invalid bundle changed:\n got: %#v\nwant: %#v", bundle, want)
			}
		})
	}

	if err := applyCurrentDungeonCardFinalGoldMultiplier(nil); !errors.Is(err, errDungeonCardPlanInvalid) {
		t.Fatalf("nil bundle error = %v, want %v", err, errDungeonCardPlanInvalid)
	}
}

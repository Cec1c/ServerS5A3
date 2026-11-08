package dnfbridge

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestPrivateAikenAvatarCouponRealPVF(t *testing.T) {
	const (
		specialID       uint32 = 990031104
		deliveredID     uint32 = 2681593
		shopID          uint32 = 247
		materialID      uint32 = 10161538
		materialCount   int64  = 40
		specialPVFPath         = "stackable/private_aiken_exchange/990031104.stk"
		originalPVFPath        = "stackable/cash/chn_avatar_gift_certificate/chn_gift_certificate_upper_avatar_unlimited.stk"
	)

	shop, items := realPVF535(t)
	originalDefinition, err := items.ResolveItem(deliveredID)
	if err != nil {
		t.Fatalf("resolve original permanent advanced avatar voucher: %v", err)
	}
	if originalDefinition.ItemID != deliveredID || originalDefinition.Kind != dungeonDropItemStackable || strings.ToLower(originalDefinition.PVFPath) != originalPVFPath {
		t.Fatalf("original voucher definition=%+v want item=%d kind=%s path=%s", originalDefinition, deliveredID, dungeonDropItemStackable, originalPVFPath)
	}
	if !originalDefinition.ExpirationDate.IsZero() || originalDefinition.UsablePeriodDays != 0 || originalDefinition.DailyDeleteItem {
		t.Fatalf("original advanced avatar voucher is not permanent: %+v", originalDefinition)
	}
	if got := currentCeraShopAvatarCouponItemIDs[2][3]; got != int64(deliveredID) {
		t.Fatalf("advanced permanent checkout coupon=%d want=%d", got, deliveredID)
	}

	for _, actorID := range []uint32{0, 486} {
		actorID := actorID
		t.Run(fmt.Sprintf("valid_actor_%d", actorID), func(t *testing.T) {
			request := currentNPCShopBuyRequest{
				ItemID:       specialID,
				Count:        2,
				ShopContext:  shopID,
				ActorContext: actorID,
			}
			pricing, err := resolveCurrentNPCShopActorPricing(shop, items, specialID, actorID)
			if err != nil {
				t.Fatal(err)
			}
			if !pricing.Buyable || !pricing.MaterialExchange || pricing.NeedMaterialItem != materialID || pricing.NeedMaterialCount != materialCount || pricing.ExchangeGold != 0 {
				t.Fatalf("real actor %d pricing=%+v want material=%d x%d gold=0", actorID, pricing, materialID, materialCount)
			}
			if pricing.Definition.ItemID != specialID || pricing.Definition.PVFPath != specialPVFPath {
				t.Fatalf("special shelf definition=%+v", pricing.Definition)
			}

			gotPricing, gotRequest, err := privateAikenAvatarCouponGrant(items, pricing, request)
			if err != nil {
				t.Fatal(err)
			}
			if gotRequest.ItemID != deliveredID {
				t.Fatalf("delivered item=%d want=%d", gotRequest.ItemID, deliveredID)
			}
			wantPricing := pricing
			wantPricing.Definition = originalDefinition
			if !reflect.DeepEqual(gotPricing, wantPricing) {
				t.Fatalf("grant changed shelf cost or original definition: got=%+v want=%+v", gotPricing, wantPricing)
			}
			wantRequest := request
			wantRequest.ItemID = deliveredID
			if !reflect.DeepEqual(gotRequest, wantRequest) {
				t.Fatalf("grant changed request beyond delivery id: got=%+v want=%+v", gotRequest, wantRequest)
			}
		})
	}

	baseRequest := currentNPCShopBuyRequest{
		ItemID:       specialID,
		Count:        1,
		ShopContext:  shopID,
		ActorContext: 486,
	}
	basePricing, err := resolveCurrentNPCShopActorPricing(shop, items, specialID, baseRequest.ActorContext)
	if err != nil {
		t.Fatal(err)
	}

	invalid := []struct {
		name   string
		mutate func(*currentNPCShopBuyRequest, *currentNPCShopPricing)
	}{
		{"wrong_shop", func(request *currentNPCShopBuyRequest, _ *currentNPCShopPricing) { request.ShopContext = shopID + 1 }},
		{"wrong_actor", func(request *currentNPCShopBuyRequest, _ *currentNPCShopPricing) { request.ActorContext = 1 }},
		{"wrong_material_item", func(_ *currentNPCShopBuyRequest, pricing *currentNPCShopPricing) { pricing.NeedMaterialItem++ }},
		{"wrong_material_count", func(_ *currentNPCShopBuyRequest, pricing *currentNPCShopPricing) { pricing.NeedMaterialCount-- }},
		{"nonzero_gold", func(_ *currentNPCShopBuyRequest, pricing *currentNPCShopPricing) { pricing.ExchangeGold = 1 }},
		{"wrong_special_path", func(_ *currentNPCShopBuyRequest, pricing *currentNPCShopPricing) {
			pricing.Definition.PVFPath = "stackable/private_aiken_exchange/foreign.stk"
		}},
	}
	for _, testCase := range invalid {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			request := baseRequest
			pricing := basePricing
			testCase.mutate(&request, &pricing)
			gotPricing, gotRequest, err := privateAikenAvatarCouponGrant(items, pricing, request)
			if !errors.Is(err, errCurrentNPCShopProductUnavailable) {
				t.Fatalf("error=%v want product unavailable", err)
			}
			if !reflect.DeepEqual(gotPricing, pricing) || !reflect.DeepEqual(gotRequest, request) {
				t.Fatalf("rejection changed input: pricing=%+v request=%+v", gotPricing, gotRequest)
			}
		})
	}

	for _, itemID := range []uint32{990031101, 990031102, 990031103} {
		itemID := itemID
		t.Run(fmt.Sprintf("other_sku_%d", itemID), func(t *testing.T) {
			request := currentNPCShopBuyRequest{
				ItemID:       itemID,
				Count:        1,
				ShopContext:  shopID,
				ActorContext: 486,
			}
			pricing, err := resolveCurrentNPCShopActorPricing(shop, items, itemID, request.ActorContext)
			if err != nil {
				t.Fatal(err)
			}
			gotPricing, gotRequest, err := privateAikenAvatarCouponGrant(items, pricing, request)
			if err != nil {
				t.Fatalf("other SKU %d rejected: %v", itemID, err)
			}
			if !reflect.DeepEqual(gotPricing, pricing) || !reflect.DeepEqual(gotRequest, request) {
				t.Fatalf("other SKU %d changed: pricing=%+v request=%+v", itemID, gotPricing, gotRequest)
			}
		})
	}
}

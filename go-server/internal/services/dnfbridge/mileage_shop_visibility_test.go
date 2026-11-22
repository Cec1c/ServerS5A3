package dnfbridge

import (
	"testing"

	"longheng.io/server/internal/platform/config"
)

func TestMileageShopSwitchDefaultsOffAndFollowsEnvironment(t *testing.T) {
	t.Setenv("DNFBRIDGE_MILEAGE_SHOP_ENABLED", "false")
	if optionsFromConfig(config.ServiceConfig{}).mileageShopEnabled {
		t.Fatal("mileage shop enabled by default")
	}
	t.Setenv("DNFBRIDGE_MILEAGE_SHOP_ENABLED", "true")
	if !optionsFromConfig(config.ServiceConfig{}).mileageShopEnabled {
		t.Fatal("mileage shop switch was not read")
	}
}

func TestMileageShopVisibilityEventCountsAndParameters(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		body, err := buildCurrentActivityEventInfoBodyWithRuntimeActivitiesPortedAndBattlePass(
			[]uint32{1}, 3600, false, false, false, false, true, 0,
			false, false, false, false, false, nil, false, false, false, false,
			currentPortedCatalog{}, false, enabled)
		if err != nil {
			t.Fatal(err)
		}
		shape := parseCurrentSpendTimeEventInfoBody(t, body)
		want := 1
		if enabled {
			want = 2
		}
		if len(shape.baseRows) != want || len(shape.activeRows) != want || shape.hasEvent(266) != enabled {
			t.Fatalf("enabled=%t shape=%+v", enabled, shape)
		}
		if enabled {
			if shape.baseRows[1].eventID != 266 || shape.activeRows[1].eventID != 266 || shape.baseRows[1].hasExtended {
				t.Fatal("mileage native event was replaced by a generic menu")
			}
			for _, value := range shape.activeRows[1].params {
				if value != 0 {
					t.Fatal("unexpected mileage event parameter")
				}
			}
		}
	}
}

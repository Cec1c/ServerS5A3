package dnfbridge

import (
	"context"
	"reflect"
	"testing"
)

func TestTeleportPotionExplicitAreaKeepsMapCoordinatePair(t *testing.T) {
	for _, area := range []byte{0, 1, 2} {
		t.Run(string(rune('0'+area)), func(t *testing.T) {
			service, session, repositories := newTeleportPotionTownCatalogTest(t,
				"31 `CentralPark.twn`", map[string]string{"town/CentralPark.twn": centralParkBoundaryTownDocument()})
			// The same coordinate pair may belong to several maps. The packet's
			// area remains authoritative, including an explicitly selected gate.
			if err := service.handleTeleportPotion(session, buildTeleportPotionAreaClickRequest(65, 2600014, 31, area, 474, 234)); err != nil {
				t.Fatal(err)
			}
			stored, found, err := repositories.Character.Load(context.Background(), "29")
			if err != nil || !found || stored.Stats["town_id"] != 31 || stored.Stats["area_id"] != int64(area) || stored.Stats["pos_x"] != 474 || stored.Stats["pos_y"] != 234 {
				t.Fatalf("explicit area=%d stored=%+v found=%v err=%v", area, stored.Stats, found, err)
			}
			splitTownTransitionAndPostState(t, session.conn.(*bufferConn).write.Bytes(), session, 29, 31, area, 474, 234, 0, 3, townMoveSkillProjectionBody(t, repositories, "29"), false)
		})
	}
}

func TestTeleportPotionInvalidExplicitAreaDoesNotConsumeOrMove(t *testing.T) {
	for _, tc := range []struct {
		name string
		area byte
		x, y int64
	}{
		{"missing_area", 255, 485, 382},
		{"no_own_default_position", 1, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, session, repositories := newTeleportPotionTownCatalogTest(t,
				"31 `CentralPark.twn`", map[string]string{"town/CentralPark.twn": centralParkBoundaryTownDocument()})
			before, _, err := repositories.Character.Load(context.Background(), "29")
			if err != nil {
				t.Fatal(err)
			}
			inventoryBefore, _, err := repositories.Inventory.Load(context.Background(), "29")
			if err != nil {
				t.Fatal(err)
			}
			if err := service.handleTeleportPotion(session, buildTeleportPotionAreaClickRequest(65, 2600014, 31, tc.area, tc.x, tc.y)); err != nil {
				t.Fatal(err)
			}
			after, _, err := repositories.Character.Load(context.Background(), "29")
			if err != nil {
				t.Fatal(err)
			}
			inventoryAfter, _, err := repositories.Inventory.Load(context.Background(), "29")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(inventoryBefore, inventoryAfter) {
				t.Fatal("rejected explicit destination changed character or inventory")
			}
			assertTeleportPotionFailure(t, session)
		})
	}
}

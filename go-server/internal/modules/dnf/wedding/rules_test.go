package wedding

import (
	"os"
	"testing"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

// Opt in with the exact paired archive. Public source never embeds the PVF.
func TestCurrentPVFMarriageRules(t *testing.T) {
	path := os.Getenv("DNF_WEDDING_TEST_PVF")
	if path == "" {
		t.Skip("DNF_WEDDING_TEST_PVF not supplied")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	r, err := Load(a)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Rings) != 6 || len(r.RoomTiers) != 7 || r.MinimumLevel != 30 || r.MaxLovePoint != 999 {
		t.Fatalf("unexpected current rules: %+v", r)
	}
	if len(r.LovePointItems) != 2 || r.LovePointItems[10002901].Points != 1 || r.LovePointItems[490702176].Points != 520 || !r.LovePointItems[10002901].Village || !r.LovePointItems[10002901].SeriaRoom {
		t.Fatalf("unexpected current love-point coupons: %+v", r.LovePointItems)
	}
	for kind, id := range map[byte]int64{0: 10002629, 1: 10002630, 2: 10002631} {
		if gift := r.CeremonyGifts[kind]; gift.ItemID != id || gift.Count != 1 {
			t.Fatalf("ceremony gift %d: %+v", kind, gift)
		}
		t.Logf("ceremony=%d initial_ring=%d invitations=%d", kind, r.Ceremonies[kind].Ring, r.Ceremonies[kind].Invitations)
	}
	if r.ForceMove != ([2]CeremonyPosition{{X: 670, Y: 330, Direction: 0}, {X: 685, Y: 360, Direction: 0}}) {
		t.Fatalf("ceremony positions %+v", r.ForceMove)
	}
	full, err := r.Attributes(5, 1000, true, false)
	if err != nil {
		t.Fatal(err)
	}
	want := Bonuses{Strength: 15, Intelligence: 15, Vitality: 10, Spirit: 10, HP: 200, MP: 200, PhysicalAttack: 10, MagicalAttack: 10, IndependentAttack: 20, ElementalAttack: 8, PhysicalCritical: 2, MagicalCritical: 2, MoveSpeed: 20, AttackSpeed: 20, CastSpeed: 20, HitRecovery: 200, TownMoveSpeed: 10}
	if full != want {
		t.Fatalf("full marriage bonuses=%+v, want %+v", full, want)
	}
	for _, input := range []struct{ married, pvp bool }{{false, false}, {false, true}, {true, true}} {
		got, err := r.Attributes(5, 1000, input.married, input.pvp)
		if err != nil || got != (Bonuses{}) {
			t.Fatalf("inactive bonuses=%+v err=%v", got, err)
		}
	}
	for _, tc := range []struct{ points, hp, mp, elemental, independent int64 }{{0, 0, 0, 0, 0}, {9, 0, 0, 0, 0}, {10, 100, 0, 0, 0}, {39, 100, 0, 0, 0}, {40, 100, 100, 0, 0}, {199, 150, 150, 0, 0}, {200, 200, 200, 1, 0}, {799, 200, 200, 3, 0}, {800, 200, 200, 5, 5}, {999, 200, 200, 5, 5}, {1000, 200, 200, 8, 20}, {2000, 200, 200, 8, 20}} {
		b, e := r.Attributes(0, tc.points, true, false)
		if e != nil || b.HP != tc.hp || b.MP != tc.mp || b.ElementalAttack != tc.elemental || b.IndependentAttack != tc.independent {
			t.Fatalf("room points %d: %+v %v", tc.points, b, e)
		}
	}
	if r.Rings[1].TicketID != 10002611 || r.Rings[5].TicketID != 10002615 || r.Ceremonies[2].TicketID != 2683401 {
		t.Fatal("current marriage ticket mismatch")
	}
	if _, err = r.Attributes(6, 0, true, false); err == nil {
		t.Fatal("accepted nonexisting ring stage")
	}
}

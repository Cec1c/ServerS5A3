package dnfbridge

import (
	"os"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func newR133WeaponCandidateFixture(t *testing.T) *loginWearBridgeFixture {
	t.Helper()
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE for the real placement dependency")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	fixture := newLoginWearBridgeFixture(t)
	fixture.service.initialEquipmentArchive = archive
	fixture.service.options.currentClientVersion = "official-24.9"
	fixture.service.openEquipLoginWearWindow(fixture.session, "r133-protocol-candidate")
	fixture.session.equipLoginWearWindowOpenedAt = fixture.service.gameplayNow().Add(-time.Second)
	return fixture
}

func sendR133WeaponCandidate(t *testing.T, fixture *loginWearBridgeFixture, body []byte) {
	t.Helper()
	fixture.buffer.write.Reset()
	handled, err := fixture.service.handleAlignedGameCommand(
		fixture.session,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketMoveItemspace),
		body,
	)
	if err != nil || !handled {
		t.Fatalf("op19 handled=%t err=%v", handled, err)
	}
}

func assertR133WeaponWorn(t *testing.T, fixture *loginWearBridgeFixture) {
	t.Helper()
	if got := fixture.wornItemID(t); got != int64(loginWearBridgeWeaponItemID) {
		t.Fatalf("beamsword %d must remain worn, got %d", loginWearBridgeWeaponItemID, got)
	}
	if fixture.bagHoldsWeapon(t) {
		t.Fatal("beamsword unexpectedly moved into the bag")
	}
}

func assertR133WeaponBagged(t *testing.T, fixture *loginWearBridgeFixture) {
	t.Helper()
	if got := fixture.wornItemID(t); got != 0 {
		t.Fatalf("worn slot must be empty, got %d", got)
	}
	if !fixture.bagHoldsWeapon(t) {
		t.Fatal("beamsword was not placed in the bag")
	}
}

// This regression deliberately uses the complete aligned command dispatcher.
// The existing login-wear repeat tests call equipLoginWearFamilyWindowOpenForOp19
// directly and therefore never exercise noteWornSlotClaimOccurrence.
func TestR133ProtocolSameSlotTailZeroDuplicateKeepsWeapon(t *testing.T) {
	setDefaultTier(t)
	t.Setenv(wornSlotClaimRetryTierEnv, "")
	t.Setenv(op19PlayerFlagEnv, "")

	fixture := newR133WeaponCandidateFixture(t)
	body := loginWearBridgeFamilyBody()
	if len(body) != 28 || body[26] != 0 || body[27] != 0 {
		t.Fatalf("fixture body tail=(%d,%d), len=%d; want tail=(0,0), len=28", body[26], body[27], len(body))
	}

	sendR133WeaponCandidate(t, fixture, body)
	if got := fixture.session.equipWornSlotClaimOccurrence; got != 1 {
		t.Fatalf("first claim decision=%d, want 1", got)
	}
	if got := fixture.session.equipWornSlotClaimCounts[loginWearBridgeWornSlot]; got != 1 {
		t.Fatalf("first claim raw per-slot count=%d, want 1", got)
	}
	assertR133WeaponWorn(t, fixture)

	sendR133WeaponCandidate(t, fixture, body)
	if got := fixture.session.equipWornSlotClaimOccurrence; got != 1 {
		t.Fatalf("second claim decision=%d, want 1 (swallow after identical repeat)", got)
	}
	if got := fixture.session.equipWornSlotClaimCounts[loginWearBridgeWornSlot]; got != 2 {
		t.Fatalf("second claim raw per-slot count=%d, want 2", got)
	}
	assertR133WeaponWorn(t, fixture)
}

func TestR133ProtocolLateIdenticalTailZeroStillKeepsWeapon(t *testing.T) {
	setDefaultTier(t)
	t.Setenv(wornSlotClaimRetryTierEnv, "")
	t.Setenv(op19PlayerFlagEnv, "")

	fixture := newR133WeaponCandidateFixture(t)
	body := loginWearBridgeFamilyBody()
	sendR133WeaponCandidate(t, fixture, body)
	assertR133WeaponWorn(t, fixture)

	fixture.session.equipLoginWearWindowOpenedAt = fixture.service.gameplayNow().Add(-20 * time.Second)
	sendR133WeaponCandidate(t, fixture, body)
	if got := fixture.session.equipWornSlotClaimCounts[loginWearBridgeWornSlot]; got != 2 {
		t.Fatalf("late repeat raw count=%d, want 2", got)
	}
	if got := fixture.session.equipWornSlotClaimOccurrence; got != 1 {
		t.Fatalf("late identical repeat decision=%d, want 1", got)
	}
	assertR133WeaponWorn(t, fixture)
}

func TestR133ProtocolChangedStatementTailZeroStillUnequips(t *testing.T) {
	setDefaultTier(t)
	t.Setenv(wornSlotClaimRetryTierEnv, "")
	t.Setenv(op19PlayerFlagEnv, "")

	fixture := newR133WeaponCandidateFixture(t)
	sendR133WeaponCandidate(t, fixture, loginWearBridgeFamilyBody())
	assertR133WeaponWorn(t, fixture)

	changed := loginWearBridgeBody(0, loginWearBridgeBagSlot+1, 0, 3, loginWearBridgeWornSlot, loginWearBridgeWeaponItemID)
	sendR133WeaponCandidate(t, fixture, changed)
	if got := fixture.session.equipWornSlotClaimOccurrence; got != 2 {
		t.Fatalf("changed statement decision=%d, want 2", got)
	}
	assertR133WeaponBagged(t, fixture)
}

func TestR133ProtocolTail2AfterSwallowedClaimStillUnequips(t *testing.T) {
	setDefaultTier(t)
	t.Setenv(wornSlotClaimRetryTierEnv, "")
	t.Setenv(op19PlayerFlagEnv, "")

	fixture := newR133WeaponCandidateFixture(t)
	body := loginWearBridgeFamilyBody()
	sendR133WeaponCandidate(t, fixture, body)
	assertR133WeaponWorn(t, fixture)

	flagged := flaggedBody(t, body, op19CurrentUIPlayerFlag)
	if flagged[26] != op19CurrentUIPlayerFlag {
		t.Fatalf("tail marker=%d, want %d", flagged[26], op19CurrentUIPlayerFlag)
	}
	sendR133WeaponCandidate(t, fixture, flagged)
	assertR133WeaponBagged(t, fixture)
}

func TestR133ProtocolTailZeroSingleOutsideWindowStillUnequips(t *testing.T) {
	setDefaultTier(t)
	t.Setenv(wornSlotClaimRetryTierEnv, "")
	t.Setenv(op19PlayerFlagEnv, "")

	fixture := newR133WeaponCandidateFixture(t)
	fixture.session.equipLoginWearWindowOpenedAt = fixture.service.gameplayNow().Add(-20 * time.Second)
	sendR133WeaponCandidate(t, fixture, loginWearBridgeFamilyBody())
	assertR133WeaponBagged(t, fixture)
}

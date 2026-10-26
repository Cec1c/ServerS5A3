package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/channelinfo"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	dnftown "longheng.io/server/internal/modules/dnf/town"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// Existing incident identifiers describe shape only. All stores here are memory.
// No claim that this generated op19 reproduces the missing 00:58 packet.
func TestR142WeaponMasteryActualHandlers(t *testing.T) {
	ctx := context.Background()
	pvfPath := strings.TrimSpace(os.Getenv("DNFBRIDGE_WEAPON_RECEIPT_REAL_PVF"))
	channelPath := strings.TrimSpace(os.Getenv("DNFBRIDGE_WEAPON_RECEIPT_CHANNEL"))
	if pvfPath == "" && channelPath == "" {
		t.Skip("select real PVF and matching channel input for weapon receipt test")
	}
	if pvfPath == "" || channelPath == "" {
		t.Fatal("both real PVF and matching channel input required")
	}
	a, e := platformpvf.Open(pvfPath)
	if e != nil {
		t.Fatal(e)
	}
	if a.Snapshot().Checksum != "25675fba8711216b72800e5ff034df61209b37e7c22d3e4c3b3ad7b1906fc348" {
		t.Fatal("PVF mismatch")
	}
	catalog, e := buildSkillCatalogFromSource(ctx, a)
	if e != nil {
		t.Fatal(e)
	}
	initial, e := parseInitialCharacterSkillsWithCatalog(a, 0, catalog)
	if e != nil {
		t.Fatal(e)
	}
	sp, tp, e := parseInitialSkillPointTablesFromSource(a)
	if e != nil {
		t.Fatal(e)
	}
	table, e := worldmap.LoadSource(ctx, a, worldmap.Options{})
	if e != nil {
		t.Fatal(e)
	}
	resolver, e := worldmap.NewResolver(table)
	if e != nil {
		t.Fatal(e)
	}
	towns, e := dnftown.Load(ctx, a, dnftown.Options{})
	if e != nil {
		t.Fatal(e)
	}
	channelBytes, e := os.ReadFile(channelPath)
	if e != nil {
		t.Fatal(e)
	}
	channelIndex, e := channelinfo.Parse(channelBytes)
	if e != nil {
		t.Fatal(e)
	}
	channels, e := channelcatalog.New(channelIndex, channelcatalog.Options{ServerID: 1})
	if e != nil {
		t.Fatal(e)
	}
	ch, ok := channels.Channel(17)
	if !ok || ch.Type != 11 {
		t.Fatalf("real channel17: %+v", ch)
	}
	cases := []struct {
		name         string
		level        int
		item         int64
		light, blunt int
		contract     bool
	}{
		{"light75_overequip_mastery33", 75, 101040332, 10, 0, true},
		{"light75_overequip_mastery33_and15", 75, 101040332, 10, 1, true},
		{"light85_mastery33", 85, 101040332, 10, 0, false},
		{"katana75_no_weapon_mastery", 75, 101010912, 0, 0, false},
		{"club90_no_weapon_mastery", 90, 101020668, 0, 0, false},
		{"club90_blunt15", 90, 101020668, 0, 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := memory.NewMemoryGroup()
			account := repo.AccountRecord{AccountID: "dnf:1", Metadata: map[string]string{}}
			if tc.contract {
				account.Metadata["premium_expire_22"] = strconv.FormatInt(time.Now().Add(24*time.Hour).Unix(), 10)
			}
			if e := r.Account.Save(ctx, account); e != nil {
				t.Fatal(e)
			}
			for _, id := range []uint16{1047, 1024} {
				level, grow, item, light, blunt, slot := 90, int64(33), int64(101040493), 10, 0, 0
				if id == 1024 {
					level, grow, item, light, blunt, slot = tc.level, 17, tc.item, tc.light, tc.blunt, 1
				}
				cid := strconv.Itoa(int(id))
				c := repo.CharacterRecord{CharacterID: cid, AccountID: "dnf:1", Name: "R142-offline-" + cid, Slot: slot, Job: "0", Level: level, Stats: map[string]int64{"grow_type": grow, "town_id": 38, "area_id": 1, "pos_x": 450, "pos_y": 234, "direction": 5, "area_state": 3, "fatigue": 156, currentDungeonTutorialCompletedKey: currentDungeonTutorialCompleteFlag}}
				if e := r.Character.Save(ctx, c); e != nil {
					t.Fatal(e)
				}
				if e := r.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: cid, Slots: map[string]repo.ItemStack{"0:26": {ItemID: 101010912, Count: 1, Extra: map[string]string{"item_kind": "equipment", "equipment_type": "[weapon]"}}}}); e != nil {
					t.Fatal(e)
				}
				raw := buildInitialEquipmentRawEntry(12, item, 40)
				if e := r.Equipment.Save(ctx, repo.EquipmentRecord{CharacterID: cid, Entries: map[string]repo.EquipmentEntry{"12": {SlotIndex: 12, ItemID: item, RawEntry: raw, Extra: map[string]string{"item_kind": "equipment", "equipment_type": "[weapon]", "equipped_slot": "12"}}}}); e != nil {
					t.Fatal(e)
				}
				skills := map[int64]repo.SkillState{}
				for _, v := range initial {
					skills[int64(v.SkillID)] = repo.SkillState{Level: v.Level, Enabled: true}
				}
				delete(skills, 33)
				delete(skills, 15)
				if light > 0 {
					skills[33] = repo.SkillState{Level: light, Enabled: true}
				}
				if blunt > 0 {
					skills[15] = repo.SkillState{Level: blunt, Enabled: true}
				}
				// Missing layout is intentional: selection must rebuild it from real PVF.
				if e := r.Skill.Save(ctx, repo.SkillRecord{CharacterID: cid, Skills: skills}); e != nil {
					t.Fatal(e)
				}
			}
			s := &Service{options: options{joustEventDisabled: true, accountID: "dnf:1", accountPrefix: defaultAccountPrefix, currentClientVersion: "official-24.9", gameUpperHeader: gameUpperHeaderServer16, gameUpperBodyCodec: gameUpperBodyCodecPlain}, repositoryProvider: func() (repo.Group, bool) { return r, true }, initialEquipmentArchive: a, skillCatalog: catalog, initialSkillsByJob: map[byte][]initialSkillEntry{0: initial}, initialSPTable: sp, initialTPTable: tp, townCatalog: towns, worldMapTable: table, worldMapResolver: resolver, onlinePlayers: newOnlinePlayerManager()}
			clock := newFakeCurrentDungeonDeathTimerQueue()
			clock.now = time.Now()
			s.gameplayTimers = clock
			dir := filepath.Join(os.Getenv("R142_EVIDENCE_DIR"), tc.name)
			if e := os.MkdirAll(dir, 0700); e != nil {
				t.Fatal(e)
			}
			logger, e := openPacketLogger(filepath.Join(dir, "events.log"))
			if e != nil {
				t.Fatal(e)
			}
			s.packetLog = logger
			t.Cleanup(func() { logger.Close() })
			conn := &bufferConn{}
			// Authenticated endpoint is the fixture boundary; op4 establishes character owner.
			se := &gameSession{conn: conn, connID: "R142-" + tc.name, accountID: "dnf:1", loginPacketSeen: true, accountFromToken: true, channel: ch, residentChannel: ch, connectionTownActorOwnerChannel: 17, townActorOwnerChannel: 17, currentChannelResidentNoticeSent: true, gameEndpointSuccessSent: true}
			dump := func(label string) {
				t.Helper()
				if e := os.WriteFile(filepath.Join(dir, label+".bin"), conn.write.Bytes(), 0600); e != nil {
					t.Fatal(e)
				}
			}
			checkWeapon := func(id uint16, want int64) {
				t.Helper()
				v, _, err := r.Equipment.Load(ctx, strconv.Itoa(int(id)))
				if err != nil || v.Entries["12"].ItemID != want {
					t.Fatalf("weapon owner id%d got%+v want%d err%v", id, v.Entries["12"], want, err)
				}
			}
			checkSkillWire := func(wantLight, wantBlunt int) {
				t.Helper()
				found := false
				for stream := conn.write.Bytes(); len(stream) > 0; {
					if len(stream) < 16 {
						t.Fatal("short server16 packet")
					}
					total := int(binary.LittleEndian.Uint32(stream[3:7]))
					if total < 16 || total > len(stream) {
						t.Fatal("invalid server16 total")
					}
					class, op, body := stream[0], binary.LittleEndian.Uint16(stream[1:3]), stream[16:total]
					stream = stream[total:]
					if class != 0 || op != 19 {
						continue
					}
					if len(body) < 4 {
						t.Fatal("short skill19")
					}
					size := int(binary.LittleEndian.Uint32(body))
					if size != len(body)-4 {
						t.Fatalf("not skill proto19: %x", body)
					}
					_, trees := consumeCurrentSkillInfoFields(t, body[4:])
					if len(trees[2]) != 2 {
						t.Fatal("missing skill trees")
					}
					_, fields := consumeCurrentSkillInfoFields(t, trees[2][0])
					got := map[uint64]uint64{}
					for _, raw := range fields[3] {
						f, _ := consumeCurrentSkillInfoFields(t, raw)
						got[f[2][0]] = f[3][0]
					}
					if got[33] != uint64(wantLight) || got[15] != uint64(wantBlunt) {
						t.Fatalf("skill page wrong %v want33=%d/15=%d", got, wantLight, wantBlunt)
					}
					found = true
				}
				if !found {
					t.Fatal("no actual class0 skill19 projected")
				}
			}
			selectReady := func(id uint16, label string) {
				t.Helper()
				conn.write.Reset()
				scope44Select(t, s, se, id)
				// Current same-channel op4 without roster drives channel-reconnect route;
				// no direct owner/boundary/helper calls to synthesize a completed town.
				if e := s.handleLegacyGamePacket(se, buildLegacyTownSceneReadyAckPacket()); e != nil {
					t.Fatal(e)
				}
				dump(label)
				if se.initialTownRouteStage < currentInitialTownRoutePlayerStateSent {
					t.Fatalf("town not completed stage=%d", se.initialTownRouteStage)
				}
				if id == 1047 {
					checkWeapon(id, 101040493)
					checkSkillWire(10, 0)
				} else {
					checkWeapon(id, tc.item)
					checkSkillWire(tc.light, tc.blunt)
				}
			}
			selectReady(1047, "01-select1047")
			scope44Upper(t, s, se, 7, nil)
			scope44Upper(t, s, se, 8, nil)
			selectReady(1024, "02-switch1024")
			beforeEq, _, _ := r.Equipment.Load(ctx, "1024")
			beforeInv, _, _ := r.Inventory.Load(ctx, "1024")
			claim := loginWearBridgeBody(0, 24, 0, 3, 12, int32(tc.item))
			for i := 0; i < 3; i++ {
				if i == 2 {
					clock.now = clock.now.Add(5 * time.Second)
				}
				conn.write.Reset()
				scope44Upper(t, s, se, 19, claim)
				dump(fmt.Sprintf("03-repeat%d", i))
				checkWeapon(1024, tc.item)
			}
			afterEq, _, _ := r.Equipment.Load(ctx, "1024")
			afterInv, _, _ := r.Inventory.Load(ctx, "1024")
			if !reflect.DeepEqual(beforeEq, afterEq) || !reflect.DeepEqual(beforeInv, afterInv) {
				t.Fatal("reassert mutated assets")
			}
			scope44Upper(t, s, se, 7, nil)
			scope44Upper(t, s, se, 8, nil)
			selectReady(1047, "04-switchback1047")
			scope44Upper(t, s, se, 7, nil)
			scope44Upper(t, s, se, 8, nil)
			selectReady(1024, "05-reselect1024")
			// New connection, same repositories: fresh scene/skill rebuild and repeat owner.
			scope44Upper(t, s, se, 7, nil)
			scope44Upper(t, s, se, 8, nil)
			conn = &bufferConn{}
			se = &gameSession{conn: conn, connID: "R142-cold-" + tc.name, accountID: "dnf:1", loginPacketSeen: true, accountFromToken: true, channel: ch, residentChannel: ch, connectionTownActorOwnerChannel: 17, townActorOwnerChannel: 17, currentChannelResidentNoticeSent: true, gameEndpointSuccessSent: true}
			selectReady(1024, "06-cold1024")
			conn.write.Reset()
			scope44Upper(t, s, se, 19, claim)
			checkWeapon(1024, tc.item)
			dump("07-cold-repeat")
			explicit := append([]byte(nil), claim...)
			explicit[26] = 2
			// An actual late Equipment.Save failure must roll back inventory and
			// preserve the earlier accepted statement; no receipt for a failure.
			bi, _, _ := r.Inventory.Load(ctx, "1024")
			be, _, _ := r.Equipment.Load(ctx, "1024")
			bad := r
			saves := 0
			bad.CharacterItems = r142FailItems{r.CharacterItems, &saves}
			s.repositoryProvider = func() (repo.Group, bool) { return bad, true }
			scope44Upper(t, s, se, 19, explicit)
			ai, _, _ := r.Inventory.Load(ctx, "1024")
			ae, _, _ := r.Equipment.Load(ctx, "1024")
			if saves != 1 || !reflect.DeepEqual(bi, ai) || !reflect.DeepEqual(be, ae) {
				t.Fatal("save rollback failed")
			}
			s.repositoryProvider = func() (repo.Group, bool) { return r, true }
			scope44Upper(t, s, se, 19, claim)
			checkWeapon(1024, tc.item)
			conn.write.Reset()
			scope44Upper(t, s, se, 19, explicit)
			dump("08-explicit-unequip")
			checkWeapon(1024, 0)
			inv, _, _ := r.Inventory.Load(ctx, "1024")
			if inv.Slots["0:24"].ItemID != tc.item {
				t.Fatal("explicit unequip not committed to requested bag")
			}
			// Rewear requires the current real PVF level gate, including type22 at75.
			wear := loginWearBridgeBody(0, 24, int32(tc.item), 3, 12, 0)
			wear[26] = 2
			conn.write.Reset()
			scope44Upper(t, s, se, 19, wear)
			dump("09-explicit-rewear")
			checkWeapon(1024, tc.item)
			// Fresh successful noop, then a changed nominated bag endpoint remains
			// a legal unflagged move under the existing R133 contract.
			scope44Upper(t, s, se, 7, nil)
			scope44Upper(t, s, se, 8, nil)
			selectReady(1024, "10-new-generation")
			scope44Upper(t, s, se, 19, claim)
			changed := loginWearBridgeBody(0, 25, 0, 3, 12, int32(tc.item))
			scope44Upper(t, s, se, 19, changed)
			checkWeapon(1024, 0)
			wear25 := loginWearBridgeBody(0, 25, int32(tc.item), 3, 12, 0)
			wear25[26] = 2
			scope44Upper(t, s, se, 19, wear25)
			checkWeapon(1024, tc.item)
			// Leave a live success receipt immediately before selecting the
			// same character again; the failed next owner must not inherit it.
			scope44Upper(t, s, se, 7, nil)
			scope44Upper(t, s, se, 8, nil)
			selectReady(1024, "10b-before-generation-reset")
			scope44Upper(t, s, se, 19, claim)
			checkWeapon(1024, tc.item)
			if !equipLoginWearReassertNoopEnabled() && len(se.equipWornClaimReceipts) == 0 {
				t.Fatal("prior generation did not have a live receipt")
			}
			// Fail the first transaction in a new character generation. The next
			// request must not become an accepted-repeat merely from that failure.
			scope44Upper(t, s, se, 7, nil)
			scope44Upper(t, s, se, 8, nil)
			selectReady(1024, "11-failed-first-claim")
			bad = r
			failed := 0
			bad.CharacterItems = r142RejectItems{r.CharacterItems, &failed}
			s.repositoryProvider = func() (repo.Group, bool) { return bad, true }
			scope44Upper(t, s, se, 19, claim)
			checkWeapon(1024, tc.item)
			s.repositoryProvider = func() (repo.Group, bool) { return r, true }
			if len(se.equipWornClaimReceipts) != 0 {
				t.Fatal("failed first owner created receipt")
			}
			if failed != 1 {
				t.Fatal("failed transaction not reached")
			}
			if !equipLoginWearReassertNoopEnabled() {
				scope44Upper(t, s, se, 19, claim)
				checkWeapon(1024, 0)
			} // enabled legacy window has its own pre-owner absorption semantics.

			// Test a real deadline from the first typed success, not from the last
			// repeat. The optional old NOOP=true ledger retains its prior contract.
			restore := func() {
				t.Helper()
				eq, _, _ := r.Equipment.Load(ctx, "1024")
				if eq.Entries["12"].ItemID == 0 {
					scope44Upper(t, s, se, 19, wear)
				}
				checkWeapon(1024, tc.item)
			}
			fresh := func(label string) {
				t.Helper()
				restore()
				oldGeneration := se.characterGeneration
				scope44Upper(t, s, se, 7, nil)
				scope44Upper(t, s, se, 8, nil)
				selectReady(1024, label)
				if se.characterGeneration == oldGeneration {
					t.Fatal("same-ID selection did not advance generation")
				}
			}
			fresh("12-fixed-deadline")
			firstAt := clock.now
			scope44Upper(t, s, se, 19, claim)
			checkWeapon(1024, tc.item)
			clock.now = firstAt.Add(s.loginWearReassertWindowDuration())
			scope44Upper(t, s, se, 19, claim)
			checkWeapon(1024, tc.item)
			clock.now = clock.now.Add(time.Nanosecond)
			scope44Upper(t, s, se, 19, claim)
			if equipLoginWearReassertNoopEnabled() {
				checkWeapon(1024, tc.item) // pre-existing R133 repeat, not the new receipt
				scope44Upper(t, s, se, 19, explicit)
				checkWeapon(1024, 0)
			} else {
				checkWeapon(1024, 0)
			}
			dump("12-deadline-result")
			fresh("13-other-move")
			scope44Upper(t, s, se, 19, claim)
			checkWeapon(1024, tc.item)
			other := loginWearBridgeBody(0, 26, 101010912, 0, 27, 0)
			other[26] = 2
			scope44Upper(t, s, se, 19, other)
			moved, _, _ := r.Inventory.Load(ctx, "1024")
			if moved.Slots["0:27"].ItemID != 101010912 || moved.Slots["0:26"].ItemID != 0 {
				t.Fatal("other op19 was not a committed inventory move")
			}
			if len(se.equipWornClaimReceipts) != 0 {
				t.Fatal("other committed op19 left receipt alive")
			}
			scope44Upper(t, s, se, 19, claim)
			if !equipLoginWearReassertNoopEnabled() {
				checkWeapon(1024, 0)
			} else {
				checkWeapon(1024, tc.item)
			}
			// Gate disables this new receipt at both write and read boundaries.
			t.Setenv(loginWearReassertRepeatEnv, "gate")
			fresh("14-repeat-gate")
			scope44Upper(t, s, se, 19, claim)
			checkWeapon(1024, tc.item)
			if len(se.equipWornClaimReceipts) != 0 {
				t.Fatal("repeat=gate recorded receipt")
			}
			scope44Upper(t, s, se, 19, claim)
			checkWeapon(1024, 0)
			t.Setenv(loginWearReassertRepeatEnv, "absorb")
			// WINDOW=once cannot acquire the new receipt. The older enabled
			// ledger is intentionally unchanged and is tested separately.
			t.Setenv(loginWearReassertWindowModeEnv, "once")
			fresh("15-window-once")
			scope44Upper(t, s, se, 19, claim)
			checkWeapon(1024, tc.item)
			if len(se.equipWornClaimReceipts) != 0 {
				t.Fatal("window=once recorded receipt")
			}
			if !equipLoginWearReassertNoopEnabled() {
				scope44Upper(t, s, se, 19, claim)
				checkWeapon(1024, 0)
			}
			t.Setenv(loginWearReassertWindowModeEnv, "repeat")
			// A different item statement cannot be accepted as an identical receipt.
			fresh("16-different-item")
			scope44Upper(t, s, se, 19, claim)
			checkWeapon(1024, tc.item)
			changedItem := loginWearBridgeBody(0, 24, 0, 3, 12, 123456789)
			scope44Upper(t, s, se, 19, changedItem)
			// Current endpoint owner applies this changed statement as unequip;
			// the receipt must not override or widen that existing domain rule.
			checkWeapon(1024, 0)
			if len(se.equipWornClaimReceipts) != 0 {
				t.Fatal("changed item move retained receipt")
			}
			restore()
			skill, _, _ := r.Skill.Load(ctx, "1024")
			if skill.Skills[33].Level != tc.light || skill.Skills[15].Level != tc.blunt {
				t.Fatalf("mastery changed %+v", skill.Skills)
			}
			t.Logf("actual op4/7/8/1345/19 matrix complete: level%d item%d skill33=%d skill15=%d overequip=%t", tc.level, tc.item, tc.light, tc.blunt, tc.contract)

		})
	}
}

type r142FailEquipment struct {
	repo.EquipmentRepository
	calls *int
}

func (r r142FailEquipment) Save(ctx context.Context, v repo.EquipmentRecord) error {
	*r.calls++
	return errors.New("r142 late equipment save failure")
}

type r142FailItems struct {
	repo.CharacterItemUnitOfWork
	calls *int
}

func (u r142FailItems) WithinCharacterItems(ctx context.Context, id string, fn func(repo.InventoryRepository, repo.EquipmentRepository) error) error {
	return u.CharacterItemUnitOfWork.WithinCharacterItems(ctx, id, func(i repo.InventoryRepository, e repo.EquipmentRepository) error {
		return fn(i, r142FailEquipment{e, u.calls})
	})
}

type r142RejectItems struct {
	repo.CharacterItemUnitOfWork
	calls *int
}

func (u r142RejectItems) WithinCharacterItems(ctx context.Context, id string, fn func(repo.InventoryRepository, repo.EquipmentRepository) error) error {
	*u.calls++
	return errors.New("r142 transaction unavailable")
}

// Clock/config bounds complement the complete request tests above; these are
// receipt state tests and do not claim to simulate a client or a whole login.
func TestR142ReceiptClockAndOwnerBounds(t *testing.T) {
	t.Setenv(loginWearReassertRepeatEnv, "absorb")
	t.Setenv(loginWearReassertWindowModeEnv, "repeat")
	t.Setenv(loginWearReassertWindowMillisEnv, "1000")
	clock := newFakeCurrentDungeonDeathTimerQueue()
	clock.now = time.Unix(1700000000, 0)
	s := &Service{options: options{currentClientVersion: "official-24.9"}, gameplayTimers: clock}
	se := &gameSession{connID: "receipt", selectedCharacterID: 1024, characterGeneration: 1, townSceneReadyCharacterID: 1024}
	body := loginWearBridgeBody(0, 24, 0, 3, 12, 101040332)
	st, _ := loginWearReassertStatementOf(body)
	noop := &alignedcmd.EquipmentMoveFact{WornClaimNoop: true}
	s.noteCurrentWornClaimResult(se, body, nil, false)
	s.noteCurrentWornClaimResult(se, body, &alignedcmd.EquipmentMoveFact{}, false)
	s.noteCurrentWornClaimResult(se, body, noop, true)
	if len(se.equipWornClaimReceipts) != 0 {
		t.Fatal("failure/non-worn/player created receipt")
	}
	s.noteCurrentWornClaimResult(se, body, noop, false)
	original := se.equipWornClaimReceipts[12]
	clock.now = clock.now.Add(time.Second)
	if !s.currentWornClaimReceiptMatches(se, 12, st) {
		t.Fatal("exact boundary did not match")
	}
	s.noteCurrentWornClaimResult(se, body, noop, false)
	if se.equipWornClaimReceipts[12] != original {
		t.Fatal("repeat slid deadline")
	}
	clock.now = clock.now.Add(time.Nanosecond)
	if s.currentWornClaimReceiptMatches(se, 12, st) {
		t.Fatal("expired receipt matched")
	}
	s.noteCurrentWornClaimResult(se, body, noop, false)
	if se.equipWornClaimReceipts[12] != original {
		t.Fatal("expired receipt reopened")
	}
	t.Setenv(loginWearReassertWindowMillisEnv, "60000")
	if s.currentWornClaimReceiptMatches(se, 12, st) {
		t.Fatal("config increase extended frozen deadline")
	}
	clock.now = original.acceptedAt.Add(500 * time.Millisecond)
	t.Setenv(loginWearReassertWindowMillisEnv, "100")
	if s.currentWornClaimReceiptMatches(se, 12, st) {
		t.Fatal("config shrink ignored")
	}
	t.Setenv(loginWearReassertWindowMillisEnv, "1000")
	clock.now = original.acceptedAt.Add(-time.Nanosecond)
	if s.currentWornClaimReceiptMatches(se, 12, st) {
		t.Fatal("backward clock matched")
	}
	clock.now = original.acceptedAt
	t.Setenv(loginWearReassertRepeatEnv, "false")
	if s.currentWornClaimReceiptMatches(se, 12, st) {
		t.Fatal("repeat rollback ignored")
	}
	t.Setenv(loginWearReassertRepeatEnv, "absorb")
	se.equipLoginWearReassertRound++
	if s.currentWornClaimReceiptMatches(se, 12, st) {
		t.Fatal("new scene round matched")
	}
	s.noteCurrentWornClaimResult(se, body, noop, false)
	if !s.currentWornClaimReceiptMatches(se, 12, st) {
		t.Fatal("new round could not own new success")
	}
	se.characterGeneration++
	if s.currentWornClaimReceiptMatches(se, 12, st) {
		t.Fatal("same ID new generation matched")
	}
	s.prepareCurrentWornClaimReceiptOwner(se)
	if len(se.equipWornClaimReceipts) != 0 {
		t.Fatal("generation did not clear")
	}
	s.noteCurrentWornClaimResult(se, body, noop, false)
	se.selectedCharacterID = 1047
	if s.currentWornClaimReceiptMatches(se, 12, st) {
		t.Fatal("other character matched")
	}
}

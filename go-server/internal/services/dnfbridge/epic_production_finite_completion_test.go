package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

const finiteEpicTestTargetItemID uint32 = 108000520

var (
	finiteEpic7Once    sync.Once
	finiteEpic7Archive *platformpvf.Archive
	finiteEpic7Err     error
)

// Load the actual finite candidate without rewriting its quota in the test.
// The resource retains its existing weekly policy; only completion is selected.
func loadFiniteEpic7Archive(t *testing.T) *platformpvf.Archive {
	t.Helper()
	path := os.Getenv("DNF_EPIC_COMPLETION_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNF_EPIC_COMPLETION_PVF_SMOKE")
	}
	finiteEpic7Once.Do(func() {
		source, err := platformpvf.Open(path)
		if err != nil {
			finiteEpic7Err = err
			return
		}
		catalog, err := parseCurrentEpicProductionCatalog(source)
		if err != nil {
			finiteEpic7Err = err
			return
		}
		if catalog.weeklyLimit != 7 {
			finiteEpic7Err = fmt.Errorf("weekly limit=%d, want 7", catalog.weeklyLimit)
			return
		}
		finiteEpic7Archive = source
	})
	if finiteEpic7Err != nil {
		t.Fatal(finiteEpic7Err)
	}
	return finiteEpic7Archive
}

func finiteEpicRealFixture(t *testing.T, used, remainingCharge int64) (*Service, *gameSession, dnfrepo.Group, *currentEpicProductionCatalog) {
	t.Helper()
	ctx := context.Background()
	archive := loadFiniteEpic7Archive(t)
	catalog, err := parseCurrentEpicProductionCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	if !catalog.allowsAnyJob(finiteEpicTestTargetItemID) || catalog.maxChargePoint <= uint32(remainingCharge) {
		t.Fatalf("candidate catalog lacks target %d or charge capacity %d", finiteEpicTestTargetItemID, catalog.maxChargePoint)
	}
	now := time.Now().UTC()
	g := memory.NewMemoryGroup()
	character := dnfrepo.CharacterRecord{
		CharacterID: "2",
		AccountID:   "finite-completion-test",
		Level:       int(catalog.levelLimit),
		Stats: map[string]int64{
			currentEpicProductionTargetStat:      int64(finiteEpicTestTargetItemID),
			currentEpicProductionChargeStat:      int64(catalog.maxChargePoint) - remainingCharge,
			currentEpicProductionWeeklyCountStat: used,
			currentEpicProductionWeekStartStat:   currentEpicProductionWeekStart(now),
		},
	}
	if err := g.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	inventory := dnfrepo.InventoryRecord{CharacterID: "2", Slots: map[string]dnfrepo.ItemStack{
		"0:122": {ItemID: 10163130, Count: 2000},
		"0:127": {ItemID: 10163135, Count: 2000},
	}}
	if err := g.Inventory.Save(ctx, inventory); err != nil {
		t.Fatal(err)
	}
	service := &Service{
		options:                 options{currentClientVersion: "official-24.9", gameUpperHeader: gameUpperHeaderServer16, gameUpperBodyCodec: gameUpperBodyCodecPlain},
		initialEquipmentArchive: archive,
		repositoryProvider:      func() (dnfrepo.Group, bool) { return g, true },
	}
	session := &gameSession{conn: &bufferConn{}, accountID: character.AccountID, selectedCharacterID: 2}
	return service, session, g, catalog
}

func finiteEpicProcessBody() []byte {
	body := make([]byte, 2+currentEpicProductionProcessRowSize)
	binary.LittleEndian.PutUint16(body[0:2], 1)
	binary.LittleEndian.PutUint32(body[2:6], 10163130)
	binary.LittleEndian.PutUint32(body[6:10], 122)
	return body
}

func TestEpicProductionFinite7CompletionAwardsOnceAndNotifies(t *testing.T) {
	service, session, repositories, catalog := finiteEpicRealFixture(t, 6, 1)
	connection := session.conn.(*bufferConn)
	if err := service.handleCurrentEpicProductionProcess(session, finiteEpicProcessBody()); err != nil {
		t.Fatal(err)
	}

	raw := connection.write.Bytes()
	ids := make([]uint16, 0, 4)
	for len(raw) > 0 {
		packet, rest := splitLongHengGameServerUpperPacket(t, raw)
		raw = rest
		ids = append(ids, packet.Header.MsgID)
		switch packet.Header.MsgID {
		case 1417:
			if packet.Header.Classification != 1 || len(packet.Body) != 7 || packet.Body[0] != 1 ||
				binary.LittleEndian.Uint32(packet.Body[1:5]) != catalog.maxChargePoint || packet.Body[5] != 7 {
				t.Fatalf("process ACK=%x", packet.Body)
			}
		case 1415:
			if packet.Header.Classification != 1 || len(packet.Body) != 9 || packet.Body[0] != 1 ||
				binary.LittleEndian.Uint32(packet.Body[1:5]) != finiteEpicTestTargetItemID ||
				binary.LittleEndian.Uint32(packet.Body[5:9]) != catalog.maxChargePoint {
				t.Fatalf("completion ACK=%x", packet.Body)
			}
		case 1354:
			if len(packet.Body) != 32 || binary.LittleEndian.Uint32(packet.Body[4:8]) != 0 ||
				binary.LittleEndian.Uint32(packet.Body[8:12]) != 0 || binary.LittleEndian.Uint32(packet.Body[12:16]) != 0 ||
				packet.Body[17] != 7 {
				t.Fatalf("completion info=%x", packet.Body)
			}
		}
	}
	if !reflect.DeepEqual(ids, []uint16{1417, 14, 1415, 1354}) {
		t.Fatalf("packet ids=%v", ids)
	}

	ctx := context.Background()
	character, found, err := repositories.Character.Load(ctx, "2")
	if err != nil || !found {
		t.Fatalf("load character found=%t err=%v", found, err)
	}
	if character.Stats[currentEpicProductionWeeklyCountStat] != 7 ||
		character.Stats[currentEpicProductionTargetStat] != 0 || character.Stats[currentEpicProductionChargeStat] != 0 {
		t.Fatalf("completed stats=%v", character.Stats)
	}
	inventory, _, err := repositories.Inventory.Load(ctx, "2")
	if err != nil {
		t.Fatal(err)
	}
	weapons := 0
	for _, stack := range inventory.Slots {
		if stack.ItemID == int64(finiteEpicTestTargetItemID) {
			weapons++
			if stack.Count != 1 || len(stack.RawEntry) == 0 {
				t.Fatalf("completed weapon=%+v", stack)
			}
		}
	}
	if weapons != 1 || inventory.Slots["0:122"].Count != 1999 || inventory.Slots["0:127"].Count != 1999 {
		t.Fatalf("weapons=%d inventory=%v", weapons, inventory.Slots)
	}

	beforeCharacter := character
	beforeInventory := dnfrepo.CloneInventory(inventory)
	connection.write.Reset()
	if err := service.handleCurrentEpicProductionProcess(session, finiteEpicProcessBody()); err != nil {
		t.Fatal(err)
	}
	reply, rest := splitLongHengGameServerUpperPacket(t, connection.write.Bytes())
	if len(rest) != 0 || reply.Header.MsgID != 1417 || reply.Header.Classification != 1 ||
		!reflect.DeepEqual(reply.Body, []byte{0, currentEpicProductionGenericError}) {
		t.Fatalf("replay reply msg=%d class=%d body=%x rest=%x", reply.Header.MsgID, reply.Header.Classification, reply.Body, rest)
	}
	afterCharacter, _, _ := repositories.Character.Load(ctx, "2")
	afterInventory, _, _ := repositories.Inventory.Load(ctx, "2")
	if !reflect.DeepEqual(beforeCharacter, afterCharacter) || !reflect.DeepEqual(beforeInventory, afterInventory) {
		t.Fatal("replayed completion changed character or inventory")
	}
}

func TestEpicProductionFiniteCompletionFullBagRollsBack(t *testing.T) {
	service, session, repositories, catalog := finiteEpicRealFixture(t, 2, 1)
	ctx := context.Background()
	items, err := service.currentPVFItemCatalog()
	if err != nil {
		t.Fatal(err)
	}
	definition, err := items.ResolveItem(finiteEpicTestTargetItemID)
	if err != nil {
		t.Fatal(err)
	}
	characterBefore, _, _ := repositories.Character.Load(ctx, "2")
	inventoryBefore, _, _ := repositories.Inventory.Load(ctx, "2")
	end := definition.SlotEnd
	if end < 8 {
		end = 8
	}
	for slot := int16(3); slot <= end; slot++ {
		key := currentCeraShopInventorySlotKey(dnfrepo.MainInventoryListType, slot)
		if _, occupied := inventoryBefore.Slots[key]; !occupied {
			inventoryBefore.Slots[key] = dnfrepo.ItemStack{ItemID: 999, Count: 1}
		}
	}
	if err := repositories.Inventory.Save(ctx, inventoryBefore); err != nil {
		t.Fatal(err)
	}
	characterBefore, _, _ = repositories.Character.Load(ctx, "2")
	inventoryBefore, _, _ = repositories.Inventory.Load(ctx, "2")
	_, err = service.commitCurrentEpicProductionProcess(ctx, session, catalog,
		currentEpicProductionProcessRequest{Materials: []currentEpicProductionProcessMaterial{{ItemID: 10163130, Slot: 122}}}, time.Now().UTC())
	if !errors.Is(err, errDungeonPickupInventoryFull) {
		t.Fatalf("full-bag completion err=%v", err)
	}
	characterAfter, _, _ := repositories.Character.Load(ctx, "2")
	inventoryAfter, _, _ := repositories.Inventory.Load(ctx, "2")
	if !reflect.DeepEqual(characterBefore, characterAfter) || !reflect.DeepEqual(inventoryBefore, inventoryAfter) {
		t.Fatal("failed completion did not roll back target, charge, weekly count, or materials")
	}
}

var errFiniteEpicUnexpectedWeaponResourceLoad = errors.New("finite non-completion must not load weapon resources")

func finiteEpicLightweightCatalog(weeklyLimit, maxChargePoint uint32) *currentEpicProductionCatalog {
	return &currentEpicProductionCatalog{
		targetsByJob: map[string]map[uint32]struct{}{
			"[test]": {finiteEpicTestTargetItemID: {}},
		},
		catalysts: map[uint32]currentEpicProductionCatalyst{
			10163130: {ItemID: 10163130, GetPoint: 1000, GroupKey: 0},
		},
		levelLimit:                1,
		weeklyLimit:               weeklyLimit,
		maxChargePoint:            maxChargePoint,
		processMaterialNeedCount:  1,
		requiredMaterialItemID:    10163135,
		requiredMaterialItemCount: 1,
		bigChanceMaxRate:          1,
	}
}

func finiteEpicLightweightFixture(t *testing.T, used, charge int64) (*Service, *gameSession, dnfrepo.Group) {
	t.Helper()
	ctx := context.Background()
	g := memory.NewMemoryGroup()
	character := dnfrepo.CharacterRecord{
		CharacterID: "3",
		AccountID:   "finite-lightweight-test",
		Level:       90,
		Stats: map[string]int64{
			currentEpicProductionTargetStat:      int64(finiteEpicTestTargetItemID),
			currentEpicProductionChargeStat:      charge,
			currentEpicProductionWeeklyCountStat: used,
			currentEpicProductionWeekStartStat:   currentEpicProductionWeekStart(time.Now().UTC()),
		},
	}
	if err := g.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	inventory := dnfrepo.InventoryRecord{CharacterID: "3", Slots: map[string]dnfrepo.ItemStack{
		"0:122": {ItemID: 10163130, Count: 10},
		"0:127": {ItemID: 10163135, Count: 10},
	}}
	if err := g.Inventory.Save(ctx, inventory); err != nil {
		t.Fatal(err)
	}
	service := &Service{
		options:                 options{currentClientVersion: "official-24.9"},
		initialEquipmentLoadErr: errFiniteEpicUnexpectedWeaponResourceLoad,
		repositoryProvider:      func() (dnfrepo.Group, bool) { return g, true },
	}
	session := &gameSession{accountID: character.AccountID, selectedCharacterID: 3}
	return service, session, g
}

func TestEpicProductionFiniteUnderProgressDoesNotLoadWeaponResources(t *testing.T) {
	catalog := finiteEpicLightweightCatalog(7, 1380000)
	service, session, repositories := finiteEpicLightweightFixture(t, 0, 0)
	result, err := service.commitCurrentEpicProductionProcess(context.Background(), session, catalog,
		currentEpicProductionProcessRequest{Materials: []currentEpicProductionProcessMaterial{{ItemID: 10163130, Slot: 122}}}, time.Now().UTC())
	if err != nil {
		t.Fatalf("ordinary finite progress unexpectedly required equipment resources: %v", err)
	}
	if result.ChargePoint != 1000 || result.WeeklyCount != 1 || result.CompletedItemID != 0 {
		t.Fatalf("ordinary progress result=%+v", result)
	}
	character, _, _ := repositories.Character.Load(context.Background(), "3")
	inventory, _, _ := repositories.Inventory.Load(context.Background(), "3")
	if character.Stats[currentEpicProductionTargetStat] != int64(finiteEpicTestTargetItemID) ||
		character.Stats[currentEpicProductionChargeStat] != 1000 || character.Stats[currentEpicProductionWeeklyCountStat] != 1 ||
		inventory.Slots["0:122"].Count != 9 || inventory.Slots["0:127"].Count != 9 {
		t.Fatalf("ordinary progress stats=%v inventory=%v", character.Stats, inventory.Slots)
	}
}

func TestEpicProductionFiniteWeeklyLimitAndOfficialU8Boundary(t *testing.T) {
	for _, tc := range []struct {
		name        string
		limit       uint32
		used        int64
		wantSuccess bool
		wantUsed    uint32
	}{
		{name: "limit_7_last_use", limit: 7, used: 6, wantSuccess: true, wantUsed: 7},
		{name: "limit_7_at_cap", limit: 7, used: 7, wantSuccess: false},
		{name: "official_u8_last_use", limit: 300, used: 254, wantSuccess: true, wantUsed: 255},
		{name: "official_u8_exhausted", limit: 300, used: 255, wantSuccess: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			catalog := finiteEpicLightweightCatalog(tc.limit, 1380000)
			service, session, repositories := finiteEpicLightweightFixture(t, tc.used, 0)
			ctx := context.Background()
			characterBefore, _, _ := repositories.Character.Load(ctx, "3")
			inventoryBefore, _, _ := repositories.Inventory.Load(ctx, "3")
			result, err := service.commitCurrentEpicProductionProcess(ctx, session, catalog,
				currentEpicProductionProcessRequest{Materials: []currentEpicProductionProcessMaterial{{ItemID: 10163130, Slot: 122}}}, time.Now().UTC())
			if tc.wantSuccess {
				if err != nil {
					t.Fatal(err)
				}
				if result.WeeklyCount != tc.wantUsed || result.CompletedItemID != 0 {
					t.Fatalf("result=%+v want weekly count %d", result, tc.wantUsed)
				}
				ack := service.buildCurrentEpicProductionProcessSuccessBody(result)
				if len(ack) != 6 || ack[4] != byte(tc.wantUsed) {
					t.Fatalf("official ACK=%x want used byte %d", ack, tc.wantUsed)
				}
				characterAfter, _, _ := repositories.Character.Load(ctx, "3")
				if characterAfter.Stats[currentEpicProductionWeeklyCountStat] != int64(tc.wantUsed) {
					t.Fatalf("stored weekly count=%d want %d", characterAfter.Stats[currentEpicProductionWeeklyCountStat], tc.wantUsed)
				}
				return
			}
			if !errors.Is(err, errCurrentEpicProductionWeeklyLimit) {
				t.Fatalf("err=%v", err)
			}
			characterAfter, _, _ := repositories.Character.Load(ctx, "3")
			inventoryAfter, _, _ := repositories.Inventory.Load(ctx, "3")
			if !reflect.DeepEqual(characterBefore, characterAfter) || !reflect.DeepEqual(inventoryBefore, inventoryAfter) {
				t.Fatal("weekly-limit rejection changed character or inventory")
			}
		})
	}
}

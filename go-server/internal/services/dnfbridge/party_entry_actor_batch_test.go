package dnfbridge

import (
	"context"
	"encoding/binary"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestPartyEntryCreatesAllUsersBeforeAnyActorBinding(t *testing.T) {
	s, leader, follower, _, _, trace := partyDungeonSyncFixture(t)
	runPartyDungeonSyncEntry(t, s, leader)
	assertPartyEntryActorBatch(t, trace, []*gameSession{leader, follower})
}

func assertPartyEntryActorBatch(t *testing.T, trace *partyDungeonSyncTrace, receivers []*gameSession) {
	t.Helper()
	steps := trace.orderedSteps(t)
	for _, receiver := range receivers {
		id := receiver.selectedCharacterID
		mode0 := firstPartyDungeonSyncStep(steps, id, 2, func(step partyDungeonSyncStep) bool {
			return step.Classification == 0 && len(step.Body) > 3 && step.Body[0] == 0
		})
		mode1 := firstPartyDungeonSyncStep(steps, id, 2, func(step partyDungeonSyncStep) bool {
			return step.Classification == 0 && len(step.Body) > 3 && step.Body[0] == 1
		})
		op28 := firstPartyDungeonSyncStep(steps, id, 28, nil)
		if mode0 < 0 || mode1 <= mode0 || op28 <= mode1 {
			t.Fatalf("receiver %d: mode0=%d mode1=%d op28=%d", id, mode0, mode1, op28)
		}
		for _, index := range []int{mode0, mode1} {
			if count := binary.LittleEndian.Uint16(steps[index].Body[1:3]); int(count) != len(receivers) {
				t.Errorf("receiver %d mode%d contains %d users, want %d committed dungeon members", id, steps[index].Body[0], count, len(receivers))
			}
		}
		// Native row readers consume 71 bytes before the mode0 UID and 16
		// before the mode1 UID, after route/owner. The first row is the receiver.
		if binary.LittleEndian.Uint16(steps[mode0].Body[76:78]) != id || binary.LittleEndian.Uint16(steps[mode1].Body[21:23]) != id {
			t.Fatalf("receiver %d was not first in both native row tables", id)
		}
	}
}

type decodedPartyEntryMode0Row struct {
	route          byte
	owner          byte
	objectKey      uint16
	name           string
	equipSlots     map[byte]uint32
	equipCount     int
	nameTagItemID  uint32
	creatureItemID uint32
	creatureName   string
	wireLen        int
}

type decodedPartyEntryMode1Row struct {
	route         byte
	owner         byte
	objectKey     uint16
	statLen       uint32
	createCount   byte
	updateCount   byte
	nameTagItemID uint32
	creatureLevel byte
	wireLen       int
}

func decodePartyEntryMode0Batch(t *testing.T, body []byte) []decodedPartyEntryMode0Row {
	t.Helper()
	if len(body) < 3 || body[0] != 0 {
		t.Fatalf("invalid mode0 batch header: len=%d", len(body))
	}
	count := int(binary.LittleEndian.Uint16(body[1:3]))
	pos := 3
	readBytes := func(n int) []byte {
		if n < 0 || pos+n > len(body) {
			t.Fatalf("mode0 batch truncated at offset %d (need %d, len %d)", pos, n, len(body))
		}
		out := body[pos : pos+n]
		pos += n
		return out
	}
	readDstr := func() []byte {
		l := int(binary.LittleEndian.Uint32(readBytes(4)))
		return readBytes(l)
	}
	rows := make([]decodedPartyEntryMode0Row, 0, count)
	for i := 0; i < count; i++ {
		rowStart := pos
		route := readBytes(1)[0]
		owner := readBytes(1)[0]
		_ = readBytes(0x47) // rawState
		objectKey := binary.LittleEndian.Uint16(readBytes(2))
		nameBytes := readDstr()
		_ = readBytes(6) // job, grow, level, 3 zero bytes
		equipCount := int(readBytes(1)[0])
		equipSlots := make(map[byte]uint32, equipCount)
		for j := 0; j < equipCount; j++ {
			slot := readBytes(1)[0]
			itemID := binary.LittleEndian.Uint32(readBytes(4))
			rawLen := int(binary.LittleEndian.Uint32(readBytes(4)))
			_ = readBytes(rawLen)
			_ = readBytes(10) // packedFlags(1) + optExpire(4) + auxVal(4) + auxFlag(1)
			equipSlots[slot] = itemID
		}
		_ = readBytes(8) // v113(4) + v114/v115/v104/v97(4)
		nameTagItemID := binary.LittleEndian.Uint32(readBytes(4))
		_ = readBytes(4) // nameTagExpireTime
		_ = readBytes(6) // sub_2003A20(1) + guildID(4) + v116(1)
		creatureItemID := binary.LittleEndian.Uint32(readBytes(4))
		creatureName := readDstr()
		_ = readBytes(7) // creatureAlive(1) + sub_2003B80(1) + orgLevel(1) + guildMark(4)
		_ = readDstr()   // orgName
		// Fixed post-organization trailer in character_scene_object_tail.go:
		// chaos(4)+u16(2)+eventArg(4)+v122(1)+disguise(2)+expert(5)+v125(1)+v126(4)+v128(2)+
		// sub4E00(4)+honor(12)+visual(1)+flag(4)+int197(1)+sub2691(2)+localRosterServer(1)+
		// sub29A0(3)+emotion(2)+skillTree(1)+returnee(1)+int267(4) = 61 bytes.
		_ = readBytes(61)
		rows = append(rows, decodedPartyEntryMode0Row{
			route:          route,
			owner:          owner,
			objectKey:      objectKey,
			name:           string(nameBytes),
			equipSlots:     equipSlots,
			equipCount:     equipCount,
			nameTagItemID:  nameTagItemID,
			creatureItemID: creatureItemID,
			creatureName:   string(creatureName),
			wireLen:        pos - rowStart,
		})
	}
	if pos != len(body) {
		t.Fatalf("mode0 batch trailing bytes: decoded=%d total=%d", pos, len(body))
	}
	return rows
}

func decodePartyEntryMode1Batch(t *testing.T, body []byte) []decodedPartyEntryMode1Row {
	t.Helper()
	if len(body) < 3 || body[0] != 1 {
		t.Fatalf("invalid mode1 batch header: len=%d", len(body))
	}
	count := int(binary.LittleEndian.Uint16(body[1:3]))
	pos := 3
	readBytes := func(n int) []byte {
		if n < 0 || pos+n > len(body) {
			t.Fatalf("mode1 batch truncated at offset %d (need %d, len %d)", pos, n, len(body))
		}
		out := body[pos : pos+n]
		pos += n
		return out
	}
	rows := make([]decodedPartyEntryMode1Row, 0, count)
	for i := 0; i < count; i++ {
		rowStart := pos
		route := readBytes(1)[0]
		owner := readBytes(1)[0]
		_ = readBytes(16) // raw16
		objectKey := binary.LittleEndian.Uint16(readBytes(2))
		_ = readBytes(4) // totalExperience
		statLen := binary.LittleEndian.Uint32(readBytes(4))
		_ = readBytes(int(statLen))
		_ = readBytes(1) // extraEquipmentSlotState
		createCount := readBytes(1)[0]
		if createCount != 0 {
			t.Fatalf("unexpected non-zero createCount=%d in actor-binding mode1 row %d", createCount, i)
		}
		_ = readBytes(4) // sub_1D77560 final state dword
		_ = readBytes(2) // sub_2007B00 object key
		updateCount := readBytes(1)[0]
		if updateCount != 0 {
			t.Fatalf("unexpected non-zero updateCount=%d in actor-binding mode1 row %d", updateCount, i)
		}
		nameTagItemID := binary.LittleEndian.Uint32(readBytes(4))
		_ = readBytes(4) // nameTagExpireTime
		_ = readBytes(1) // skill selected resource group
		g0Count := int(readBytes(1)[0])
		if g0Count != 0 {
			t.Fatalf("unexpected non-zero skill group0 count=%d in row %d", g0Count, i)
		}
		g1Count := int(readBytes(1)[0])
		if g1Count != 0 {
			t.Fatalf("unexpected non-zero skill group1 count=%d in row %d", g1Count, i)
		}
		creatureLevel := readBytes(1)[0]
		// Post-creature tail: 5 bytes + u32(4) + adventureLevel(4) + u32(4) + 2 bytes = 19 bytes.
		_ = readBytes(19)
		rows = append(rows, decodedPartyEntryMode1Row{
			route:         route,
			owner:         owner,
			objectKey:     objectKey,
			statLen:       statLen,
			createCount:   createCount,
			updateCount:   updateCount,
			nameTagItemID: nameTagItemID,
			creatureLevel: creatureLevel,
			wireLen:       pos - rowStart,
		})
	}
	if pos != len(body) {
		t.Fatalf("mode1 batch trailing bytes: decoded=%d total=%d", pos, len(body))
	}
	return rows
}

func TestPartyEntryActorBatchFourMembersHeterogeneousEquipmentDecodesEveryRow(t *testing.T) {
	saved := partyDungeonLoadingBarrierDispatch
	partyDungeonLoadingBarrierDispatch = func(run func()) { run() }
	defer func() { partyDungeonLoadingBarrierDispatch = saved }()

	s, a, b, _, _, trace := partyDungeonSyncFixture(t)
	g, _ := s.repositoryGroup()
	ctx := context.Background()

	members := []*gameSession{a, b}
	state := runtimePartyStateForSyncFixture()
	for _, id := range []uint16{101, 102} {
		ch, _, _ := g.Character.Load(ctx, "99")
		ch.CharacterID = strconv.Itoa(int(id))
		ch.Stats = dnfrepo.CloneCharacter(ch).Stats
		if err := g.Character.Save(ctx, ch); err != nil {
			t.Fatal(err)
		}
		if err := g.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: ch.CharacterID, Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
			t.Fatal(err)
		}
		conn := &partyDungeonSyncConn{charID: id, trace: trace}
		p := &gameSession{conn: conn, channel: a.channel, residentChannel: a.residentChannel, selectedCharacterID: id}
		s.bindGameSessionCharacter(p, id)
		s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: id, TownID: 38, AreaID: 1, Session: p})
		bindDungeonSelectorOriginForTestAt(t, s, p, 38, 1, 450, 234)
		members = append(members, p)
		state.Members = append(state.Members, alignedcmd.PartyMemberState{UserID: id, UserState: 1, HPPercent: 100, MPPercent: 100})
	}

	// Configure genuinely heterogeneous names, worn equipment, timed name tag, and creature across all 4 members.
	const (
		weapon100   uint32 = 100001001
		coat100     uint32 = 100001002
		weapon101   uint32 = 100002001
		nameTag101  uint32 = 490001234
		weapon102   uint32 = 100003001
		creature102 uint32 = 630001001
	)
	futureExpire := time.Now().UTC().Unix() + 3600

	names := map[uint16]string{
		99:  "A",
		100: "BravoTwo",
		101: "CharlieThree",
		102: "DeltaFourHero",
	}
	for id, name := range names {
		ch, _, err := g.Character.Load(ctx, strconv.Itoa(int(id)))
		if err != nil {
			t.Fatal(err)
		}
		ch.Name = name
		if ch.Stats == nil {
			ch.Stats = map[string]int64{}
		}
		if id == 101 {
			ch.Stats["name_tag_item_id"] = int64(nameTag101)
			ch.Stats["name_tag_expire_time"] = futureExpire
		}
		if err := g.Character.Save(ctx, ch); err != nil {
			t.Fatal(err)
		}
	}

	// Member 99: no equipment record (roster fallback).
	// Member 100: weapon (slot 12) + coat (slot 14).
	if err := g.Equipment.Save(ctx, dnfrepo.EquipmentRecord{
		CharacterID: "100",
		Entries: map[string]dnfrepo.EquipmentEntry{
			"12": {SlotIndex: 12, ItemID: int64(weapon100)},
			"14": {SlotIndex: 14, ItemID: int64(coat100)},
		},
	}); err != nil {
		t.Fatal(err)
	}
	// Member 101: weapon + 4 armor slots + active slot-30 timed name tag -> 15 mode0 appearance rows (incl. slot 28).
	if err := g.Equipment.Save(ctx, dnfrepo.EquipmentRecord{
		CharacterID: "101",
		Entries: map[string]dnfrepo.EquipmentEntry{
			"12": {SlotIndex: 12, ItemID: int64(weapon101)},
			"13": {SlotIndex: 13, ItemID: 100002002},
			"14": {SlotIndex: 14, ItemID: 100002003},
			"15": {SlotIndex: 15, ItemID: 100002004},
			"16": {SlotIndex: 16, ItemID: 100002005},
			"30": {SlotIndex: 30, ItemID: int64(nameTag101), ExpireAt: time.Unix(futureExpire, 0).UTC()},
		},
	}); err != nil {
		t.Fatal(err)
	}
	// Member 102: weapon + equipped creature in slot 26 with PetRecord name "MiniDragon" & level 50.
	if err := g.Equipment.Save(ctx, dnfrepo.EquipmentRecord{
		CharacterID: "102",
		Entries: map[string]dnfrepo.EquipmentEntry{
			"12": {SlotIndex: 12, ItemID: int64(weapon102)},
			"26": {
				SlotIndex: 26,
				ItemID:    int64(creature102),
				Extra:     map[string]string{"creature_serial": "778899"},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := g.Pet.Save(ctx, dnfrepo.PetRecord{
		CharacterID: "102",
		EquippedKey: "pet_1",
		Entries: map[string]dnfrepo.PetEntry{
			"pet_1": {
				PetKey: "pet_1",
				ItemID: int64(creature102),
				Name:   "MiniDragon",
				Level:  50,
				Extra:  map[string]string{"creature_serial": "778899"},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	for _, p := range members {
		storeRuntimePartyState(p, state)
	}
	runPartyDungeonSyncEntry(t, s, a)
	assertPartyEntryActorBatch(t, trace, members)

	steps := trace.orderedSteps(t)
	for _, receiver := range members {
		recvID := receiver.selectedCharacterID
		mode0Idx := firstPartyDungeonSyncStep(steps, recvID, 2, func(step partyDungeonSyncStep) bool {
			return step.Classification == 0 && len(step.Body) > 3 && step.Body[0] == 0
		})
		mode1Idx := firstPartyDungeonSyncStep(steps, recvID, 2, func(step partyDungeonSyncStep) bool {
			return step.Classification == 0 && len(step.Body) > 3 && step.Body[0] == 1
		})
		mode0Rows := decodePartyEntryMode0Batch(t, steps[mode0Idx].Body)
		mode1Rows := decodePartyEntryMode1Batch(t, steps[mode1Idx].Body)

		if len(mode0Rows) != 4 || len(mode1Rows) != 4 {
			t.Fatalf("receiver %d: decoded rows mode0=%d mode1=%d, want 4/4", recvID, len(mode0Rows), len(mode1Rows))
		}
		if mode0Rows[0].objectKey != recvID || mode1Rows[0].objectKey != recvID {
			t.Fatalf("receiver %d not first row: mode0[0]=%d mode1[0]=%d", recvID, mode0Rows[0].objectKey, mode1Rows[0].objectKey)
		}

		// Confirm heterogeneous row lengths across the 4 members in mode0.
		distinctLengths := map[int]bool{}
		seenMode0 := map[uint16]decodedPartyEntryMode0Row{}
		for _, row := range mode0Rows {
			distinctLengths[row.wireLen] = true
			seenMode0[row.objectKey] = row
		}
		if len(distinctLengths) < 4 {
			t.Fatalf("receiver %d expected 4 distinct heterogeneous mode0 row lengths, got %d (%v)", recvID, len(distinctLengths), distinctLengths)
		}
		seenMode1 := map[uint16]decodedPartyEntryMode1Row{}
		for _, row := range mode1Rows {
			seenMode1[row.objectKey] = row
		}

		for id, wantName := range names {
			m0, ok0 := seenMode0[id]
			m1, ok1 := seenMode1[id]
			if !ok0 || !ok1 {
				t.Fatalf("receiver %d missing member %d in decoded batch (mode0=%v mode1=%v)", recvID, id, ok0, ok1)
			}
			if m0.name != wantName {
				t.Fatalf("receiver %d member %d mode0 name=%q want %q", recvID, id, m0.name, wantName)
			}
			if m1.statLen != currentMode1StatBlobWireSize {
				t.Fatalf("receiver %d member %d mode1 statLen=%d want %d", recvID, id, m1.statLen, currentMode1StatBlobWireSize)
			}
		}

		// Member 100: weapon + coat in mode0 equipment summary (14 slots).
		if seenMode0[100].equipCount != 14 {
			t.Fatalf("receiver %d member 100 equipCount=%d want 14", recvID, seenMode0[100].equipCount)
		}
		// Member 101: 15 mode0 appearance rows (14 base + slot 28 timed name tag) + nameTagItemID in mode0 & mode1.
		if seenMode0[101].equipCount != 15 || seenMode0[101].equipSlots[currentNameTagAppearanceSlot] != nameTag101 {
			t.Fatalf("receiver %d member 101 mode0 timed name tag mismatch: count=%d slot28=%d",
				recvID, seenMode0[101].equipCount, seenMode0[101].equipSlots[currentNameTagAppearanceSlot])
		}
		if seenMode0[101].nameTagItemID != nameTag101 || seenMode1[101].nameTagItemID != nameTag101 {
			t.Fatalf("receiver %d member 101 nameTag mismatch: mode0=%d mode1=%d want %d",
				recvID, seenMode0[101].nameTagItemID, seenMode1[101].nameTagItemID, nameTag101)
		}
		// Member 102: creature item ID + "MiniDragon" in mode0, creature level 50 in mode1.
		if seenMode0[102].creatureItemID != creature102 || seenMode0[102].creatureName != "MiniDragon" {
			t.Fatalf("receiver %d member 102 mode0 creature mismatch: item=%d name=%q",
				recvID, seenMode0[102].creatureItemID, seenMode0[102].creatureName)
		}
		if seenMode1[102].creatureLevel != 50 {
			t.Fatalf("receiver %d member 102 mode1 creatureLevel=%d want 50", recvID, seenMode1[102].creatureLevel)
		}
	}
}

func TestPartyEntryActorBatchRejectsReboundPeerBeforeAnyWrite(t *testing.T) {
	s, leader, follower, conn, _, _ := partyDungeonSyncFixture(t)
	runPartyDungeonSyncEntry(t, s, leader)
	runtime := dungeonRuntimePointerForUserState(leader)
	before := len(conn.frames())
	s.bindGameSessionCharacter(&gameSession{conn: &bufferConn{}, channel: follower.channel}, follower.selectedCharacterID)
	if err := s.sendPartyEntryActorBatch(context.Background(), leader, runtime, "test_rebound_peer"); err == nil {
		t.Fatal("accepted entry snapshot from replaced peer connection")
	}
	if len(conn.frames()) != before {
		t.Fatal("wrote partial actor batch for replaced peer")
	}
}

func TestPartyEntryActorBatchRejectsMembershipChangeDuringPreparation(t *testing.T) {
	s, leader, follower, conn, _, _ := partyDungeonSyncFixture(t)
	runPartyDungeonSyncEntry(t, s, leader)
	runtime := dungeonRuntimePointerForUserState(leader)
	repos, _ := s.repositoryGroup()
	changed := false
	repos.Equipment = &remoteActorSnapshotFaultEquipment{EquipmentRepository: repos.Equipment, onLoad: func() {
		if changed {
			return
		}
		changed = true
		state := runtimePartyStateSnapshot(leader)
		state.Members = state.Members[:1]
		storeRuntimePartyState(leader, state)
	}}
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	before := len(conn.frames())
	err := s.preparePartyEntryActorBatch(context.Background(), leader, runtime, []*runtimePartyPreparedDungeonEntry{{session: follower, runtime: dungeonRuntimePointerForUserState(follower)}})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("equipment preparation was not exercised")
	}
	if err := s.sendPartyEntryActorBatch(context.Background(), leader, runtime, "test_changed_membership"); err == nil || len(conn.frames()) != before {
		t.Fatalf("obsolete membership published: err=%v frames=%d before=%d", err, len(conn.frames()), before)
	}
}

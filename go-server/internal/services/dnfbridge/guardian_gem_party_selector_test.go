package dnfbridge

import (
	"context"
	"encoding/binary"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"strconv"
	"testing"
)

func TestP161LegacyPartyFollowerSelectorHasNoGuardianOp14BeforeOp27(t *testing.T) {
	t.Setenv(partySelectorPreserveActorEnv, "off")
	repositories, service, leader, member, _, memberConn := newTownCoPresenceTestService(t)
	service.options.gameUpperHeader = gameUpperHeaderServer16
	storeRuntimePartyState(leader, townCoPresencePartyState())
	storeRuntimePartyState(member, townCoPresencePartyState())

	raw := currentGuardianGemTestRaw(int16(currentGuildMedalActorSlot), 100380017, 1)
	binary.LittleEndian.PutUint16(
		raw[currentGuardianGemRawSocketOffset:currentGuardianGemRawSocketOffset+currentGuardianGemRawSocketWidth],
		3,
	)
	if err := repositories.Equipment.Save(context.Background(), dnfrepo.EquipmentRecord{
		CharacterID: "1002",
		Entries: map[string]dnfrepo.EquipmentEntry{
			"32": {SlotIndex: 32, ItemID: 100380017, RawEntry: raw},
		},
	}); err != nil {
		t.Fatal(err)
	}
	memberConn.write.Reset()

	prepared, err := service.prepareRuntimePartyFollowerEnterSelect(
		member, 1001, 77, "test_selector_gem_boundary",
	)
	if err != nil || !prepared {
		t.Fatalf("prepare=%t err=%v", prepared, err)
	}

	stream := memberConn.write.Bytes()
	mode1Index, op27Index := -1, -1
	for index := 0; len(stream) > 0; index++ {
		packet, rest := splitLongHengGameServerUpperPacket(t, stream)
		stream = rest
		if packet.Header.Classification != 0 {
			continue
		}
		if op27Index < 0 &&
			packet.Header.MsgID == uint16(dnfenum.CmdPacketWalkoutPartyMember) &&
			len(packet.Body) == 3+currentItemListEntryWireSize &&
			packet.Body[0] == currentSocketListEquipment &&
			binary.LittleEndian.Uint16(packet.Body[3:5]) == uint16(currentGuildMedalActorSlot) {
			t.Fatalf("guardian gem op14 appeared before op27: body=%x", packet.Body)
		}
		if packet.Header.MsgID == uint16(dnfenum.CmdPacketSetUDPIPPort) &&
			len(packet.Body) > currentMode1CreateCountOffset &&
			packet.Body[0] == 1 {
			if packet.Body[currentMode1CreateCountOffset] != 0 {
				t.Fatalf("selector mode1 unexpectedly created equipment rows: body=%x", packet.Body)
			}
			mode1Index = index
		}
		if packet.Header.MsgID == currentDungeonContextMsgID {
			op27Index = index
		}
	}
	if mode1Index < 0 || op27Index < 0 || mode1Index >= op27Index {
		t.Fatalf("mode1=%d op27=%d, want binding mode1 before op27", mode1Index, op27Index)
	}
}

func TestP161PartyFollowerDungeonEntryRehydratesGuardianGemAfterFullMode1(t *testing.T) {
	t.Setenv(partySelectorPreserveActorEnv, "off")
	service, leader, follower, _, _, trace := partyDungeonSyncFixture(t)

	restoreDispatch := partyDungeonLoadingBarrierDispatch
	partyDungeonLoadingBarrierDispatch = func(run func()) { run() }
	defer func() { partyDungeonLoadingBarrierDispatch = restoreDispatch }()

	repositories, ok := service.repositoryGroup()
	if !ok {
		t.Fatal("repository group missing")
	}
	raw := currentGuardianGemTestRaw(int16(currentGuildMedalActorSlot), 100380017, 1)
	binary.LittleEndian.PutUint16(
		raw[currentGuardianGemRawSocketOffset:currentGuardianGemRawSocketOffset+currentGuardianGemRawSocketWidth],
		3,
	)
	if err := repositories.Equipment.Save(context.Background(), dnfrepo.EquipmentRecord{
		CharacterID: strconv.Itoa(int(follower.selectedCharacterID)),
		Entries: map[string]dnfrepo.EquipmentEntry{
			"32": {SlotIndex: 32, ItemID: 100380017, RawEntry: raw},
		},
	}); err != nil {
		t.Fatal(err)
	}

	runPartyDungeonSyncEntry(t, service, leader)
	for _, session := range []*gameSession{leader, follower} {
		if err := service.sendFinishLoadingStatus(session, make([]byte, currentFinishLoadingLegacyRequestBodySize)); err != nil {
			t.Fatal(err)
		}
	}
	steps := trace.orderedSteps(t)

	op27 := firstPartyDungeonSyncStep(steps, follower.selectedCharacterID, currentDungeonContextMsgID, nil)
	op28 := firstPartyDungeonSyncStep(steps, follower.selectedCharacterID, currentDungeonInfoNotification, nil)
	op29 := firstPartyDungeonSyncStep(steps, follower.selectedCharacterID, currentDungeonStartNotification, nil)
	fullMode1, gemOp14 := -1, -1
	op27Count, gemCount := 0, 0
	for index, step := range steps {
		if step.CharID != follower.selectedCharacterID || step.Classification != 0 {
			continue
		}
		switch step.MsgID {
		case currentDungeonContextMsgID:
			op27Count++
		case uint16(dnfenum.CmdPacketSetUDPIPPort):
			if op29 >= 0 && index > op29 &&
				len(step.Body) > currentMode1CreateCountOffset &&
				step.Body[0] == 1 &&
				step.Body[currentMode1CreateCountOffset] > 0 &&
				fullMode1 < 0 {
				fullMode1 = index
			}
		case uint16(dnfenum.CmdPacketWalkoutPartyMember):
			if op29 >= 0 && index > op29 &&
				len(step.Body) == 3+currentItemListEntryWireSize &&
				step.Body[0] == currentSocketListEquipment &&
				binary.LittleEndian.Uint16(step.Body[1:3]) == 1 &&
				binary.LittleEndian.Uint16(step.Body[3:5]) == uint16(currentGuildMedalActorSlot) &&
				binary.LittleEndian.Uint32(step.Body[5:9]) == 100380017 &&
				binary.LittleEndian.Uint16(step.Body[3+currentGuardianGemRawSocketOffset:3+currentGuardianGemRawSocketOffset+currentGuardianGemRawSocketWidth]) == 3 {
				gemCount++
				gemOp14 = index
			}
		}
	}
	if op27 < 0 || op28 < 0 || op29 < 0 || fullMode1 < 0 || gemOp14 < 0 ||
		!(op27 < op28 && op28 < op29 && op29 < fullMode1 && fullMode1 < gemOp14) {
		t.Fatalf("order op27=%d op28=%d op29=%d full_mode1=%d gem_op14=%d", op27, op28, op29, fullMode1, gemOp14)
	}
	if op27Count != 1 || gemCount != 1 {
		t.Fatalf("op27_count=%d gem_op14_count=%d", op27Count, gemCount)
	}
}

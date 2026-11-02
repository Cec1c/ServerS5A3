package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/progression"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

const (
	experienceCapsuleBridgeCharacterID uint16 = 29
	experienceCapsuleBridgeSlot        int16  = 9
	experienceCapsuleBridgeAccount            = "dnf:1"

	experienceCapsuleBridgeFixedID   int64 = 490010001
	experienceCapsuleBridgeFiveID    int64 = 490010002
	experienceCapsuleBridgeFifteenID int64 = 490010003
	experienceCapsuleBridgeTableID   int64 = 490010004
	experienceCapsuleBridgeCrackID   int64 = 490010005
	experienceCapsuleBridgeLevelID   int64 = 490010006
	experienceCapsuleBridgeSPBookID  int64 = 1031
)

type experienceCapsuleBridgePVFSource map[string]string

func (s experienceCapsuleBridgePVFSource) ReadText(path string) (string, error) {
	value, found := s[experienceCapsuleBridgePath(path)]
	if !found {
		return "", fmt.Errorf("experience capsule synthetic PVF is missing %s", path)
	}
	return value, nil
}

func experienceCapsuleBridgePath(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	value = strings.TrimPrefix(value, "./")
	return strings.ToLower(value)
}

func experienceCapsuleBridgeItemPath(itemID int64) string {
	return fmt.Sprintf("stackable/experience_capsule_%d.stk", itemID)
}

func experienceCapsuleBridgeItemDocument(effect string, minimum, maximum int) string {
	return "[name]\n`synthetic experience capsule`\n" +
		"[stackable type]\n`[etc]` 0\n" +
		"[usable job]\n`[all]`\n[/usable job]\n" +
		"[increase status type]\n" + effect + "\n[/increase status type]\n" +
		fmt.Sprintf("[minimum level]\n%d\n[maximum level]\n%d\n", minimum, maximum)
}

func experienceCapsuleBridgeSyntheticPVF() experienceCapsuleBridgePVFSource {
	var list, thresholds, sp, tp, quest strings.Builder
	for level := 1; level <= 100; level++ {
		fmt.Fprintf(&thresholds, "%d\n", level*1000)
		fmt.Fprintf(&sp, "%d 10\n", level)
		fmt.Fprintf(&tp, "%d 1\n", level)
	}
	quest.WriteString("[difficulty]\n`N` 100\n[/difficulty]\n")
	quest.WriteString("[exp reward table]\n")
	for level := 1; level <= 100; level++ {
		quest.WriteString("100 -1\n")
	}
	quest.WriteString("[/exp reward table]\n")
	quest.WriteString("[gold reward table]\n10\n[/gold reward table]\n")
	quest.WriteString("[green level penalty]\n7 11 80\n[/green level penalty]\n")
	quest.WriteString("[grey level penalty]\n12 90 30\n[/grey level penalty]\n")

	for _, itemID := range []int64{
		experienceCapsuleBridgeFixedID,
		experienceCapsuleBridgeFiveID,
		experienceCapsuleBridgeFifteenID,
		experienceCapsuleBridgeTableID,
		experienceCapsuleBridgeCrackID,
		experienceCapsuleBridgeLevelID,
	} {
		fmt.Fprintf(&list, "%d `%s`\n", itemID, strings.TrimPrefix(experienceCapsuleBridgeItemPath(itemID), "stackable/"))
	}
	fmt.Fprintf(&list, "%d `book_skill1.stk`\n", experienceCapsuleBridgeSPBookID)

	source := experienceCapsuleBridgePVFSource{
		"monster/monster.lst":     "",
		"equipment/equipment.lst": "",
		"stackable/stackable.lst": list.String(),
		experienceCapsuleBridgePath(progression.ExperienceTablePath): thresholds.String(),
		experienceCapsuleBridgePath(progression.SkillPointTablePath): "[sp table]\n" + sp.String() + "[/sp table]\n[tp table]\n" + tp.String() + "[/tp table]\n",
		experienceCapsuleBridgePath(progression.QuestParameterPath):  quest.String(),
		"stackable/book_skill1.stk": "[name]\n`SP+5技能书`\n" +
			"[explain]\n`使用后， 可以增加5点SP。`\n" +
			"[stackable type]\n`[etc]` 8\n[use wav]\n`SP_UP_ITEM`\n",
	}
	source[experienceCapsuleBridgePath(experienceCapsuleBridgeItemPath(experienceCapsuleBridgeFixedID))] =
		experienceCapsuleBridgeItemDocument("`[expUp]` 25", 1, 90)
	source[experienceCapsuleBridgePath(experienceCapsuleBridgeItemPath(experienceCapsuleBridgeFiveID))] =
		experienceCapsuleBridgeItemDocument("`[expPercentUp]` 5", 1, 90)
	source[experienceCapsuleBridgePath(experienceCapsuleBridgeItemPath(experienceCapsuleBridgeFifteenID))] =
		experienceCapsuleBridgeItemDocument("`[expUpByPercent]` 15", 1, 90)
	source[experienceCapsuleBridgePath(experienceCapsuleBridgeItemPath(experienceCapsuleBridgeTableID))] =
		experienceCapsuleBridgeItemDocument("`[expUpByTable]` 1 100 75", 1, 90)
	source[experienceCapsuleBridgePath(experienceCapsuleBridgeItemPath(experienceCapsuleBridgeCrackID))] =
		experienceCapsuleBridgeItemDocument("`[expUpByCrackOfDimension]`", 1, 90)
	source[experienceCapsuleBridgePath(experienceCapsuleBridgeItemPath(experienceCapsuleBridgeLevelID))] =
		experienceCapsuleBridgeItemDocument("`[expPercentUp]` 5", 51, 90)
	return source
}

func experienceCapsuleBridgeFixture(
	t *testing.T,
	itemID int64,
	level int,
	experience int64,
) (*Service, *gameSession, repo.Group) {
	t.Helper()
	source := experienceCapsuleBridgeSyntheticPVF()
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	definition, err := catalog.ResolveItem(uint32(itemID))
	if err != nil {
		t.Fatalf("resolve synthetic item %d: %v", itemID, err)
	}

	service, session, _ := newTownMoveTest(t)
	repositories := memory.NewMemoryGroup()
	ctx := context.Background()
	save := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	save(repositories.Character.Save(ctx, repo.CharacterRecord{
		CharacterID: fmt.Sprint(experienceCapsuleBridgeCharacterID),
		AccountID:   experienceCapsuleBridgeAccount,
		Job:         "0",
		Level:       level,
		Stats: map[string]int64{
			"exp":         experience,
			"grow_type":   0,
			"delete_flag": 0,
		},
	}))
	save(repositories.Skill.Save(ctx, repo.SkillRecord{
		CharacterID: fmt.Sprint(experienceCapsuleBridgeCharacterID),
		Points: repo.SkillPointState{
			TotalSP:     1000,
			RemainingSP: 900,
			TotalTP:     10,
			RemainingTP: 8,
			SyncedLevel: level,
		},
	}))
	save(repositories.Inventory.Save(ctx, repo.InventoryRecord{
		CharacterID: fmt.Sprint(experienceCapsuleBridgeCharacterID),
		Slots: map[string]repo.ItemStack{
			fmt.Sprintf("0:%d", experienceCapsuleBridgeSlot): {
				ItemID: itemID,
				Count:  2,
				Extra: map[string]string{
					"item_kind": "stackable",
					"pvf_path":  definition.PVFPath,
				},
			},
		},
	}))

	service.repositoryProvider = func() (repo.Group, bool) { return repositories, true }
	service.pvfItemCatalog = catalog
	service.options.accountID = experienceCapsuleBridgeAccount
	service.options.gameUpperHeader = gameUpperHeaderChannel13
	service.options.gameUpperBodyCodec = gameUpperBodyCodecPlain
	service.initialSkillsMu.Lock()
	service.initialSPTable = map[int]int{level: 1000}
	service.initialTPTable = map[int]int{level: 10}
	service.initialSkillsMu.Unlock()

	session.accountID = experienceCapsuleBridgeAccount
	session.characterGeneration = uint64(experienceCapsuleBridgeCharacterID)
	session.selectedCharacterID = experienceCapsuleBridgeCharacterID
	session.townSceneReadyCharacterID = experienceCapsuleBridgeCharacterID
	session.townPositionSnapshot = currentTownPositionSnapshot{
		CharacterID:   experienceCapsuleBridgeCharacterID,
		TownID:        38,
		AreaID:        0,
		PositionX:     500,
		PositionY:     220,
		MovementCode:  5,
		PositionValid: true,
	}
	service.mu.Lock()
	service.gameSessions = map[uint16]*gameSession{experienceCapsuleBridgeCharacterID: session}
	service.mu.Unlock()
	session.conn.(*bufferConn).write.Reset()
	return service, session, repositories
}

func experienceCapsuleBridgeCall(t *testing.T, service *Service, session *gameSession) []dnfproto.ChannelPacket {
	t.Helper()
	body := make([]byte, currentExperienceItemUseBodySize)
	binary.LittleEndian.PutUint16(body, uint16(experienceCapsuleBridgeSlot))
	handled, err := currentGameplayModules.DispatchUpper(
		service,
		session,
		uint16(dnfenum.CmdPacketIncreaseStatus),
		dnfproto.DefaultChannelClassification,
		body,
	)
	if err != nil || !handled {
		t.Fatalf("real op30 handler handled=%t err=%v", handled, err)
	}
	return experienceCapsuleBridgePackets(t, session.conn.(*bufferConn))
}

func experienceCapsuleBridgePackets(t *testing.T, connection *bufferConn) []dnfproto.ChannelPacket {
	t.Helper()
	packets := make([]dnfproto.ChannelPacket, 0, 3)
	rest := append([]byte(nil), connection.write.Bytes()...)
	for len(rest) > 0 {
		packet, trailing := splitCurrentGameServerUpperPacketAuto(t, rest)
		packets = append(packets, packet)
		if len(trailing) >= len(rest) {
			t.Fatalf("experience capsule packet split made no progress: rest=%x", rest)
		}
		rest = trailing
	}
	return packets
}

func experienceCapsuleBridgeAssertSuccess(
	t *testing.T,
	packets []dnfproto.ChannelPacket,
	level int,
	experience uint32,
) {
	t.Helper()
	if len(packets) != 3 {
		t.Fatalf("success packet sequence = %v, want exactly op30, op14, op37", experienceCapsuleBridgeOpIDs(packets))
	}
	ack, refresh, state := packets[0], packets[1], packets[2]
	if ack.Header.Classification != dnfproto.DefaultChannelClassification ||
		ack.Header.MsgID != uint16(dnfenum.CmdPacketIncreaseStatus) {
		t.Fatalf("success ack header = %+v", ack.Header)
	}
	wantAck := []byte{0x01, 29, 0x00, 0xff, 0, 0, 0, 0, 0, 0, 0, 0}
	if !bytes.Equal(ack.Body, wantAck) {
		t.Fatalf("success ack body = %x, want %x", ack.Body, wantAck)
	}
	if refresh.Header.Classification != 0 ||
		refresh.Header.MsgID != uint16(dnfenum.CmdPacketWalkoutPartyMember) {
		t.Fatalf("source refresh header = %+v, want class0/op14", refresh.Header)
	}
	if state.Header.Classification != 0 || state.Header.MsgID != uint16(currentDungeonCharacterStateMsgID) {
		t.Fatalf("character state header = %+v, want class0/op%d", state.Header, currentDungeonCharacterStateMsgID)
	}
	if len(state.Body) < 5 || int(state.Body[0]) != level ||
		binary.LittleEndian.Uint32(state.Body[1:5]) != experience {
		t.Fatalf("character state body = %x, want level=%d exp=%d", state.Body, level, experience)
	}
}

func experienceCapsuleBridgeOpIDs(packets []dnfproto.ChannelPacket) []uint16 {
	opcodes := make([]uint16, 0, len(packets))
	for _, packet := range packets {
		opcodes = append(opcodes, packet.Header.MsgID)
	}
	return opcodes
}

func TestCurrentExperienceCapsuleTownPVFExpTypesUseRealHandler(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		itemID int64
		gain   uint32
	}{
		{name: "expUp_fixed", itemID: experienceCapsuleBridgeFixedID, gain: 25},
		{name: "expPercentUp_five", itemID: experienceCapsuleBridgeFiveID, gain: 50},
		{name: "expUpByPercent_fifteen", itemID: experienceCapsuleBridgeFifteenID, gain: 150},
		{name: "expUpByTable", itemID: experienceCapsuleBridgeTableID, gain: 75},
		{name: "expUpByCrackOfDimension", itemID: experienceCapsuleBridgeCrackID, gain: 56},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			service, session, repositories := experienceCapsuleBridgeFixture(t, testCase.itemID, 50, 1000)
			packets := experienceCapsuleBridgeCall(t, service, session)
			wantExperience := uint32(1000) + testCase.gain
			experienceCapsuleBridgeAssertSuccess(t, packets, 50, wantExperience)

			character, found, err := repositories.Character.Load(context.Background(), fmt.Sprint(experienceCapsuleBridgeCharacterID))
			if err != nil || !found {
				t.Fatalf("load character found=%t err=%v", found, err)
			}
			if character.Level != 50 || character.Stats["exp"] != int64(wantExperience) {
				t.Fatalf("persisted progression = level %d exp %d, want 50/%d",
					character.Level, character.Stats["exp"], wantExperience)
			}
			inventory, found, err := repositories.Inventory.Load(context.Background(), fmt.Sprint(experienceCapsuleBridgeCharacterID))
			if err != nil || !found {
				t.Fatalf("load inventory found=%t err=%v", found, err)
			}
			stack := inventory.Slots[fmt.Sprintf("0:%d", experienceCapsuleBridgeSlot)]
			if stack.ItemID != testCase.itemID || stack.Count != 1 {
				t.Fatalf("source stack = %+v, want item %d count 1", stack, testCase.itemID)
			}
		})
	}
}

func TestCurrentExperienceCapsuleRejectsInvalidStateWithoutDebit(t *testing.T) {
	type rejectionCase struct {
		name   string
		itemID int64
	}
	cases := []rejectionCase{
		{name: "level_outside_definition", itemID: experienceCapsuleBridgeLevelID},
		{name: "pvp_room"},
		{name: "pvp_matching"},
		{name: "town_not_ready"},
		{name: "town_position_missing"},
		{name: "town_scene_changing"},
		{name: "dungeon_scene_changing"},
		{name: "pvp_state_changing"},
		{name: "expired"},
		{name: "locked"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			itemID := testCase.itemID
			if itemID == 0 {
				itemID = experienceCapsuleBridgeFiveID
			}
			service, session, repositories := experienceCapsuleBridgeFixture(t, itemID, 50, 1000)
			switch testCase.name {
			case "pvp_room":
				session.pvpRoom = &currentPVPRoom{}
			case "pvp_matching":
				service.pvpMatching = []*currentPVPMatchingRequest{{
					member: &currentPVPMember{session: session},
				}}
			case "town_not_ready":
				session.townSceneReadyCharacterID = 0
			case "town_position_missing":
				session.townPositionSnapshot.PositionValid = false
			case "town_scene_changing":
				session.townMu.Lock()
				defer session.townMu.Unlock()
			case "dungeon_scene_changing":
				session.dungeon.mu.Lock()
				defer session.dungeon.mu.Unlock()
			case "pvp_state_changing":
				service.pvpMu.Lock()
				defer service.pvpMu.Unlock()
			case "expired", "locked":
				inventory, found, err := repositories.Inventory.Load(context.Background(), fmt.Sprint(experienceCapsuleBridgeCharacterID))
				if err != nil || !found {
					t.Fatalf("load inventory found=%t err=%v", found, err)
				}
				stack := inventory.Slots[fmt.Sprintf("0:%d", experienceCapsuleBridgeSlot)]
				if testCase.name == "expired" {
					stack.ExpireAt = service.gameplayNow().Add(-time.Second)
				} else {
					stack.Extra["equipment_lock_state"] = "1"
				}
				inventory.Slots[fmt.Sprintf("0:%d", experienceCapsuleBridgeSlot)] = stack
				if err := repositories.Inventory.Save(context.Background(), inventory); err != nil {
					t.Fatal(err)
				}
			}
			characterID := fmt.Sprint(experienceCapsuleBridgeCharacterID)
			beforeCharacter, _, err := repositories.Character.Load(context.Background(), characterID)
			if err != nil {
				t.Fatal(err)
			}
			beforeSkill, _, err := repositories.Skill.Load(context.Background(), characterID)
			if err != nil {
				t.Fatal(err)
			}
			beforeInventory, _, err := repositories.Inventory.Load(context.Background(), characterID)
			if err != nil {
				t.Fatal(err)
			}

			packets := experienceCapsuleBridgeCall(t, service, session)
			if len(packets) != 1 || packets[0].Header.Classification != dnfproto.DefaultChannelClassification ||
				packets[0].Header.MsgID != uint16(dnfenum.CmdPacketIncreaseStatus) ||
				!bytes.Equal(packets[0].Body, []byte{0, currentExperienceItemUseUnknownCode}) {
				t.Fatalf("failure reply = %v %+v, want one op30 body 00 %02x",
					experienceCapsuleBridgeOpIDs(packets), packets, currentExperienceItemUseUnknownCode)
			}

			afterCharacter, _, err := repositories.Character.Load(context.Background(), characterID)
			if err != nil {
				t.Fatal(err)
			}
			afterSkill, _, err := repositories.Skill.Load(context.Background(), characterID)
			if err != nil {
				t.Fatal(err)
			}
			afterInventory, _, err := repositories.Inventory.Load(context.Background(), characterID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(beforeCharacter, afterCharacter) ||
				!reflect.DeepEqual(beforeSkill, afterSkill) ||
				!reflect.DeepEqual(beforeInventory, afterInventory) {
				t.Fatalf("rejected use mutated persistent state: character=%+v->%+v skill=%+v->%+v inventory=%+v->%+v",
					beforeCharacter, afterCharacter, beforeSkill, afterSkill, beforeInventory, afterInventory)
			}
		})
	}
}

func TestCurrentExperienceItemSPBookStillUsesRealHandler(t *testing.T) {
	service, session, repositories := experienceCapsuleBridgeFixture(t, experienceCapsuleBridgeSPBookID, 50, 1000)
	packets := experienceCapsuleBridgeCall(t, service, session)
	experienceCapsuleBridgeAssertSuccess(t, packets, 50, 1000)

	characterID := fmt.Sprint(experienceCapsuleBridgeCharacterID)
	character, _, err := repositories.Character.Load(context.Background(), characterID)
	if err != nil {
		t.Fatal(err)
	}
	if character.Stats["bonus_sp"] != 5 {
		t.Fatalf("persisted bonus_sp = %d, want 5", character.Stats["bonus_sp"])
	}
	skill, _, err := repositories.Skill.Load(context.Background(), characterID)
	if err != nil {
		t.Fatal(err)
	}
	wantPoints := repo.SkillPointState{
		TotalSP:     1005,
		RemainingSP: 905,
		TotalTP:     10,
		RemainingTP: 8,
		SyncedLevel: 50,
	}
	if skill.Points != wantPoints {
		t.Fatalf("SP book points = %+v, want %+v", skill.Points, wantPoints)
	}
	inventory, _, err := repositories.Inventory.Load(context.Background(), characterID)
	if err != nil {
		t.Fatal(err)
	}
	if stack := inventory.Slots[fmt.Sprintf("0:%d", experienceCapsuleBridgeSlot)]; stack.ItemID != experienceCapsuleBridgeSPBookID || stack.Count != 1 {
		t.Fatalf("SP book source stack = %+v, want item %d count 1", stack, experienceCapsuleBridgeSPBookID)
	}
}

func TestCurrentExperienceCapsuleDungeonSynchronizesRuntime(t *testing.T) {
	service, session, repositories := experienceCapsuleBridgeFixture(t, experienceCapsuleBridgeFiveID, 50, 1000)
	characterID := fmt.Sprint(experienceCapsuleBridgeCharacterID)
	beforeCharacter, _, err := repositories.Character.Load(context.Background(), characterID)
	if err != nil {
		t.Fatal(err)
	}
	beforeSkill, _, err := repositories.Skill.Load(context.Background(), characterID)
	if err != nil {
		t.Fatal(err)
	}
	runtimeState := &runtimeDungeonState{Character: beforeCharacter}
	session.dungeon.runtime = runtimeState

	packets := experienceCapsuleBridgeCall(t, service, session)
	experienceCapsuleBridgeAssertSuccess(t, packets, 50, 1050)

	afterCharacter, _, err := repositories.Character.Load(context.Background(), characterID)
	if err != nil {
		t.Fatal(err)
	}
	afterSkill, _, err := repositories.Skill.Load(context.Background(), characterID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(runtimeState.Character, afterCharacter) {
		t.Fatalf("runtime character = %+v, want new persisted character %+v", runtimeState.Character, afterCharacter)
	}
	// Dungeon progression loads skill points from the repository for each
	// commit; it intentionally has no duplicate runtime Skill cache.
	loadedSkill, found, err := loadCurrentDungeonExperienceSkillPoints(context.Background(), repositories.Skill, characterID)
	if err != nil || !found || loadedSkill.Points != afterSkill.Points || afterSkill.Points != beforeSkill.Points {
		t.Fatalf("next dungeon skill read=%+v persisted=%+v err=%v", loadedSkill, afterSkill, err)
	}
}

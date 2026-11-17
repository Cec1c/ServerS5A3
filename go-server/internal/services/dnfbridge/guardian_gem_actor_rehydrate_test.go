package dnfbridge

import (
	"context"
	"encoding/binary"
	"os"
	"strconv"
	"strings"
	"testing"

	dnfenum "longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

const (
	p161CharacterID          = "6"
	p161PetItemID     uint32 = 400990167
	p161PetSerial     uint32 = 37
	p161PetCardItemID uint32 = 10008663
	p161PetUpgrade    byte   = 2
)

// All four IDs are real grade-2 [flag gem] entries in the runtime
// stackable/flaggem catalog. Their raw socket words are derived with the same
// production mapping used by the guardian-gem mutation path.
var p161RealGuardianGemItemIDs = [4]uint32{90002, 90012, 90022, 90032}

// P161: every current-EXE actor generation that recreates equipped objects
// must replay the medal's list-3 0x77 row. Mode0/mode1 create rows do not
// carry the guardian-gem words at +0x65..+0x6C, so those words disappear from
// both the medal UI and the client-side attribute calculation until the full
// list-3 row is replayed.
func TestP161LoginActorGenerationRebuildRestoresWornMedalRawSockets(t *testing.T) {
	t.Setenv(loginActorGenerationRebuildEnv, "")
	service, session, connection, _ := newSceneReadyReplayFixture(t)
	p161BindSessionOwner(t, service, session)
	medalItemID, want := p161SeedWornMedalSockets(t, service)
	p161MakeFixturePetValid(t, service)

	armLoginRouteUpToOp24(session)
	session.initialTownRouteStage = currentInitialTownRouteTransitionSent
	session.initialTownLegacySceneReadyAccepted = true
	connection.write.Reset()
	if err := service.rebuildLoginActorGenerationAfterSceneCommit(session, "p161_login_generation", 6); err != nil {
		t.Fatal(err)
	}
	p161RequireWornMedalRefresh(t, connection.write.Bytes(), medalItemID, want)
}

func TestP161DungeonEntryRestoresWornMedalRawSocketsAfterFinishLoading(t *testing.T) {
	service, session, connection, _ := newSceneReadyReplayFixture(t)
	p161EnableFinishLoadingSkillInfo(t, service)
	p161BindSessionOwner(t, service, session)
	medalItemID, want := p161SeedWornMedalSockets(t, service)
	p161MakeFixturePetValid(t, service)
	session.preDungeonContextPlayerStateSent = true
	session.selectedCreatureStateTableSent = true
	connection.write.Reset()
	if err := service.sendCurrentPostStartMapPlayerPlacement(
		session,
		nil,
		worldmap.DungeonRoomScene{},
		"p161_dungeon_entry",
	); err != nil {
		t.Fatal(err)
	}
	if rows := p161WornMedalRows(t, connection.write.Bytes(), medalItemID); len(rows) != 0 {
		t.Fatalf("dungeon entry replayed the medal before room activation: rows=%d stream=%x", len(rows), connection.write.Bytes())
	}
	if !session.pendingGuardianGemAwaitFinishLoading || session.pendingGuardianGemRow == nil {
		t.Fatal("dungeon entry did not retain the mode1-generation medal row for finish-loading")
	}
	beforeFinishLoading := connection.write.Len()
	if err := service.sendCurrentFinishLoadingCharacterState(session, "p161_dungeon_finish_loading"); err != nil {
		t.Fatal(err)
	}

	stream := connection.write.Bytes()
	p161RequireWornMedalRefresh(t, stream, medalItemID, want)
	rows := p161WornMedalRows(t, stream, medalItemID)
	if len(rows) != 1 {
		t.Fatalf("finish-loading medal replay rows=%d, want=1", len(rows))
	}
	row := rows[0]
	if row.offset < beforeFinishLoading {
		t.Fatalf("medal replay still preceded the finish-loading request: row=%d finish=%d", row.offset, beforeFinishLoading)
	}

	op37End, op30End, lastOp120End, skillEnd := -1, -1, -1, -1
	for _, frame := range loginActorGenerationSplit(t, stream) {
		if p161FrameClassification(t, frame) != 0 {
			continue
		}
		switch frame.msgID {
		case uint16(dnfenum.CmdPacketFinishLoading):
			op37End = frame.end
		case currentIncreaseStatusResultMsgID:
			op30End = frame.end
		case currentSkillInfoMsgID:
			skillEnd = frame.end
		case uint16(dnfenum.CmdPacketRequestBlacklist):
			lastOp120End = frame.end
		}
	}
	if op37End < 0 || op37End > row.offset || op30End < 0 || op30End > row.offset ||
		lastOp120End < beforeFinishLoading || lastOp120End > row.offset {
		t.Fatalf("medal replay order op37=%d op30=%d post_op120=%d medal=%d finish_boundary=%d", op37End, op30End, lastOp120End, row.offset, beforeFinishLoading)
	}
	if skillEnd < 0 || skillEnd > row.offset {
		t.Fatalf("medal replay order op19=%d medal=%d", skillEnd, row.offset)
	}
	if session.pendingGuardianGemAwaitFinishLoading || session.pendingGuardianGemRow != nil {
		t.Fatal("successful finish-loading replay did not consume the deferred medal generation")
	}
}

func p161EnableFinishLoadingSkillInfo(t *testing.T, service *Service) {
	p161EnableCharacterFinishLoadingSkillInfo(t, service, p161CharacterID, 7)
}

func p161EnableCharacterFinishLoadingSkillInfo(t *testing.T, service *Service, characterID string, level int) {
	t.Helper()
	repositories, ok := service.repositoryGroup()
	if !ok {
		t.Fatal("p161 repository unavailable")
	}
	ctx := context.Background()
	character, found, err := repositories.Character.Load(ctx, characterID)
	if err != nil || !found {
		t.Fatalf("load p161 character: found=%t err=%v", found, err)
	}
	character.Job = "15"
	character.Level = level
	if err := repositories.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Skill.Save(ctx, dnfrepo.SkillRecord{
		CharacterID: characterID,
		Points: dnfrepo.SkillPointState{
			TotalSP: 11, RemainingSP: 11,
			TotalTP: 14, RemainingTP: 14,
			SyncedLevel: level,
		},
		Skills: map[int64]dnfrepo.SkillState{
			1: {Level: 1, Enabled: true},
		},
	}); err != nil {
		t.Fatal(err)
	}
	catalog, err := buildSkillCatalogFromSource(ctx, initialEquipmentMemSource{
		"skill/skilllist.lst":     "15 `job15.lst`\n",
		"skill/job15.lst":         "1 `job15/one.skl`\n",
		"skill/job15/one.skl":     "[skill type]\n`active`\n[skill class]\n1\n[required level]\n1\n",
		"character/character.lst": "15 `job15.chr`\n",
		"character/job15.chr":     "[job]\n`[gunblader]`\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	service.initialSkillsByJob = map[byte][]initialSkillEntry{15: {{SkillID: 1, Level: 1}}}
	service.initialSPTable = map[int]int{1: 11}
	service.initialTPTable = map[int]int{1: 14}
	service.skillCatalog = catalog
}

func TestP161TownReturnPostTransitionRestoresWornMedalRawSockets(t *testing.T) {
	service, session, connection, _ := newSceneReadyReplayFixture(t)
	p161BindSessionOwner(t, service, session)
	medalItemID, want := p161SeedWornMedalSockets(t, service)
	p161MakeFixturePetValid(t, service)

	session.initialTownSkillInfoPrepared = true
	session.initialTownSkillInfo = currentSceneSkillInfoProjection{
		characterID: p161CharacterID,
		body:        []byte{0, 0},
	}
	connection.write.Reset()
	service.armCurrentTownPostTransitionPlayerState(session, "p161_town_return")
	if err := service.sendCurrentTownPostTransitionPlayerState(session, "p161_town_return"); err != nil {
		t.Fatal(err)
	}
	p161RequireWornMedalRefresh(t, connection.write.Bytes(), medalItemID, want)
}

// This control proves the fixture itself is accepted by the production
// list-3 projector. It deliberately does not claim that any actor-generation
// call site invokes that projector.
func TestP161FixtureWornMedalProjectsToTheProvenList3Row(t *testing.T) {
	service, session, connection, _ := newSceneReadyReplayFixture(t)
	p161BindSessionOwner(t, service, session)
	medalItemID, want := p161SeedWornMedalSockets(t, service)
	p161MakeFixturePetValid(t, service)

	connection.write.Reset()
	if err := service.sendSelectedGuardianGemWornMedalRefresh(session, "p161_fixture_projection"); err != nil {
		t.Fatalf("project fixture medal: %v", err)
	}
	rows := p161WornMedalRows(t, connection.write.Bytes(), medalItemID)
	if len(rows) != 1 {
		t.Fatalf("fixture projection rows=%d, want=1; stream=%x", len(rows), connection.write.Bytes())
	}
	p161RequireSocketWords(t, rows[0], want)
}

// The fixture must use a character/account pair that the session actually
// owns, a real PVF [creature] item with a nonzero instance serial, and real
// PVF [flag gem] IDs. This check is independent of actor-generation routing.
func TestP161FixtureOwnershipPetAndGemIDsAreConsistent(t *testing.T) {
	service, session, _, _ := newSceneReadyReplayFixture(t)
	p161BindSessionOwner(t, service, session)
	medalItemID, want := p161SeedWornMedalSockets(t, service)
	p161MakeFixturePetValid(t, service)

	repositories, ok := service.repositoryGroup()
	if !ok {
		t.Fatal("p161 repository unavailable")
	}
	ctx := context.Background()
	character, found, err := repositories.Character.Load(ctx, p161CharacterID)
	if err != nil || !found {
		t.Fatalf("load p161 character: found=%t err=%v", found, err)
	}
	if got := service.accountIDForSession(session); got != strings.TrimSpace(character.AccountID) {
		t.Fatalf("p161 session owner=%q, character owner=%q", got, character.AccountID)
	}
	if medalItemID == 0 || len(want) != currentGuardianGemSocketCount {
		t.Fatalf("p161 medal fixture item=%d sockets=%v", medalItemID, want)
	}

	snapshot, err := service.currentEquippedCreatureForCharacterWithError(ctx, p161CharacterID)
	if err != nil {
		t.Fatalf("load p161 worn creature: %v", err)
	}
	if !snapshot.valid() || snapshot.itemID != p161PetItemID || snapshot.serialOrHandle != p161PetSerial {
		t.Fatalf("p161 worn creature=%+v, want item=%d serial=%d", snapshot, p161PetItemID, p161PetSerial)
	}

	for index, itemID := range p161RealGuardianGemItemIDs {
		value, err := currentGuardianGemSocketValue(itemID)
		if err != nil {
			t.Fatalf("p161 gem[%d] item=%d is not encodable: %v", index, itemID, err)
		}
		if value != want[index] {
			t.Fatalf("p161 gem[%d] item=%d word=%d, want=%d", index, itemID, value, want[index])
		}
	}
}

// Optional read-only check against the runtime PVF. Set
// DNFBRIDGE_REAL_PVF_SMOKE to Script.pvf to run it.
func TestP161FixtureIDsExistInRealPVF(t *testing.T) {
	pvfPath := strings.TrimSpace(os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE"))
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the runtime Script.pvf")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	medal, err := catalog.ResolveItem(100380058)
	if err != nil {
		t.Fatalf("resolve p161 medal 100380058: %v", err)
	}
	if medal.Kind != dungeonDropItemEquipment || normalizeEquipmentPlacementPVFType(medal.EquipmentType) != currentGuildMedalPVFType {
		t.Fatalf("p161 medal definition=%+v", medal)
	}
	pet, err := catalog.ResolveItem(p161PetItemID)
	if err != nil {
		t.Fatalf("resolve p161 pet %d: %v", p161PetItemID, err)
	}
	if pet.Kind != dungeonDropItemEquipment || normalizeEquipmentPlacementPVFType(pet.EquipmentType) != "[creature]" {
		t.Fatalf("p161 pet definition=%+v", pet)
	}
	for index, itemID := range p161RealGuardianGemItemIDs {
		gem, err := resolveCurrentGuardianGem(catalog, itemID)
		if err != nil {
			t.Fatalf("resolve p161 gem[%d] %d: %v", index, itemID, err)
		}
		if gem.Grade != 2 {
			t.Fatalf("p161 gem[%d] item=%d grade=%d, want=2", index, itemID, gem.Grade)
		}
	}
}

func p161BindSessionOwner(t *testing.T, service *Service, session *gameSession) {
	t.Helper()
	repositories, ok := service.repositoryGroup()
	if !ok {
		t.Fatal("p161 repository unavailable")
	}
	character, found, err := repositories.Character.Load(context.Background(), p161CharacterID)
	if err != nil || !found {
		t.Fatalf("load p161 character: found=%t err=%v", found, err)
	}
	accountID := strings.TrimSpace(character.AccountID)
	if accountID == "" {
		t.Fatal("p161 character has no account owner")
	}
	if session.selectedCharacterID == 0 || strconv.Itoa(int(session.selectedCharacterID)) != p161CharacterID {
		t.Fatalf("p161 selected character=%d, want=%s", session.selectedCharacterID, p161CharacterID)
	}
	session.accountID = accountID
	if got := service.accountIDForSession(session); got != accountID {
		t.Fatalf("p161 session owner=%q, want=%q", got, accountID)
	}
}

func p161SeedWornMedalSockets(t *testing.T, service *Service) (uint32, [4]uint16) {
	t.Helper()
	repositories, ok := service.repositoryGroup()
	if !ok {
		t.Fatal("p161 repository unavailable")
	}
	ctx := context.Background()
	record, found, err := repositories.Equipment.Load(ctx, p161CharacterID)
	if err != nil || !found {
		t.Fatalf("load p161 equipment: found=%t err=%v", found, err)
	}
	medal, ok := record.Entries["32"]
	if !ok || medal.ItemID <= 0 {
		t.Fatal("p161 fixture has no worn medal")
	}
	if medal.SlotIndex != int16(currentGuildMedalActorSlot) {
		t.Fatalf("p161 worn medal slot=%d, want=%d", medal.SlotIndex, currentGuildMedalActorSlot)
	}
	if len(medal.RawEntry) != currentItemListEntryWireSize {
		medal.RawEntry = currentGuardianGemTestRaw(32, uint32(medal.ItemID), 1)
	}
	var want [4]uint16
	for index, itemID := range p161RealGuardianGemItemIDs {
		value, err := currentGuardianGemSocketValue(itemID)
		if err != nil {
			t.Fatalf("p161 gem[%d] item=%d is not encodable: %v", index, itemID, err)
		}
		want[index] = value
		offset := currentGuardianGemRawSocketOffset + index*currentGuardianGemRawSocketWidth
		binary.LittleEndian.PutUint16(medal.RawEntry[offset:offset+currentGuardianGemRawSocketWidth], value)
	}
	currentGuardianGemSyncRawSocketExtra(&medal.Extra, medal.RawEntry)
	record.Entries["32"] = medal
	if err := repositories.Equipment.Save(ctx, record); err != nil {
		t.Fatal(err)
	}
	return uint32(medal.ItemID), want
}

func p161MakeFixturePetValid(t *testing.T, service *Service) {
	t.Helper()
	repositories, ok := service.repositoryGroup()
	if !ok {
		t.Fatal("p161 repository unavailable")
	}
	ctx := context.Background()
	record, found, err := repositories.Equipment.Load(ctx, p161CharacterID)
	if err != nil || !found {
		t.Fatalf("load p161 equipment: found=%t err=%v", found, err)
	}
	serial := strconv.FormatUint(uint64(p161PetSerial), 10)
	record.Entries["26"] = dnfrepo.EquipmentEntry{
		SlotIndex: 26,
		ItemID:    int64(p161PetItemID),
		RawEntry: testCurrentEquippedCreatureEnchantRaw(
			26,
			p161PetItemID,
			p161PetSerial,
			p161PetCardItemID,
			p161PetUpgrade,
		),
		Extra: map[string]string{
			"equipment_slot":             "26",
			"current_exe_equipment_type": "26",
			"creature_serial_or_handle":  serial,
			"equipment_type":             "[creature]",
			"pvf_path":                   "equipment/creature/chn_2018_springfestival/chn_400990167.equ",
			"pet_enchant_card_item_id":   strconv.FormatUint(uint64(p161PetCardItemID), 10),
			"pet_enchant_upgrade_count":  strconv.FormatUint(uint64(p161PetUpgrade), 10),
		},
	}
	if err := repositories.Equipment.Save(ctx, record); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Pet.Save(ctx, dnfrepo.PetRecord{
		CharacterID: p161CharacterID,
		EquippedKey: serial,
		Entries: map[string]dnfrepo.PetEntry{
			serial: {
				PetKey:      serial,
				CreatureKey: p161PetSerial,
				ItemID:      int64(p161PetItemID),
				Level:       7,
				Extra: map[string]string{
					"creature_serial_or_handle": serial,
					"pet_enchant_card_item_id":  strconv.FormatUint(uint64(p161PetCardItemID), 10),
					"enchant_upgrade_count":     strconv.FormatUint(uint64(p161PetUpgrade), 10),
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
}

func p161RequireWornMedalRefresh(
	t *testing.T,
	stream []byte,
	medalItemID uint32,
	want [4]uint16,
) {
	t.Helper()
	frames := loginActorGenerationSplit(t, stream)
	// Resolve the end of the LAST class0 mode1 in the complete stream before
	// looking for the medal row. A row after an earlier mode1 but before a
	// later rebuild is not an authoritative refresh.
	lastMode1End := -1
	for _, frame := range frames {
		if p161FrameClassification(t, frame) != 0 {
			continue
		}
		if frame.msgID == uint16(dnfenum.CmdPacketSetUDPIPPort) && frame.mode == 1 {
			lastMode1End = frame.end
		}
	}
	rows := p161WornMedalRows(t, stream, medalItemID)
	if len(rows) == 0 {
		t.Fatalf(
			"P161 actor generation omitted list-3 slot-%d medal item=%d with gem words=%v (gems=%v); frames=%d last_mode1_end=%d",
			currentGuildMedalActorSlot,
			medalItemID,
			want,
			p161RealGuardianGemItemIDs,
			len(frames),
			lastMode1End,
		)
	}
	for _, row := range rows {
		if lastMode1End < 0 || row.offset < lastMode1End {
			t.Fatalf("P161 medal raw-socket refresh precedes the final mode1: offset=%d last_mode1_end=%d", row.offset, lastMode1End)
		}
		p161RequireSocketWords(t, row, want)
	}
}

func p161WornMedalRows(t *testing.T, stream []byte, medalItemID uint32) []loginActorGenerationFrame {
	t.Helper()
	rows := make([]loginActorGenerationFrame, 0, 1)
	for _, frame := range loginActorGenerationSplit(t, stream) {
		if p161FrameClassification(t, frame) != 0 {
			continue
		}
		if frame.msgID != uint16(dnfenum.CmdPacketWalkoutPartyMember) ||
			len(frame.body) < 3+currentItemListEntryWireSize {
			continue
		}
		if frame.body[0] != currentSocketListEquipment ||
			binary.LittleEndian.Uint16(frame.body[3:5]) != uint16(currentGuildMedalActorSlot) ||
			binary.LittleEndian.Uint32(frame.body[5:9]) != medalItemID {
			continue
		}
		rows = append(rows, frame)
	}
	return rows
}

func p161FrameClassification(t *testing.T, frame loginActorGenerationFrame) byte {
	t.Helper()
	packet, rest := splitGameServerUpperPacketWithHeader(t, frame.packet, dnfproto.GameServerUpperHeaderSize16)
	if len(rest) != 0 {
		t.Fatalf("P161 split frame msg=%d left %d trailing bytes", frame.msgID, len(rest))
	}
	return packet.Header.Classification
}

func p161RequireSocketWords(t *testing.T, frame loginActorGenerationFrame, want [4]uint16) {
	t.Helper()
	for index, value := range want {
		offset := 3 + currentGuardianGemRawSocketOffset + index*currentGuardianGemRawSocketWidth
		if offset+currentGuardianGemRawSocketWidth > len(frame.body) {
			t.Fatalf("P161 socket[%d] truncated: body_len=%d offset=%d", index, len(frame.body), offset)
		}
		got := binary.LittleEndian.Uint16(frame.body[offset : offset+currentGuardianGemRawSocketWidth])
		if got != value {
			t.Fatalf(
				"P161 socket[%d]=%d, want=%d (gem=%d); body=%x",
				index,
				got,
				value,
				p161RealGuardianGemItemIDs[index],
				frame.body,
			)
		}
	}
}

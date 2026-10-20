package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"reflect"
	"strconv"
	"testing"

	"longheng.io/server/internal/modules/dnf/buffswap"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	repo "longheng.io/server/internal/modules/dnf/repository"
	memory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// SOURCEONLY D23: registration body 26001a070000 is anchored to ROOT's owned
// collect-once03 record at offset906586806, body SHA256
// b5f2ab03f4be5be7ca8f9e1347c1d49dddf7d0545d2a52687fa6895c174e4b8a.
// Character19/dnf:1/job0, item400990167, serial37/38, physical instances and
// skill definitions are explicit syntheticfixtures, NOT q7a assets, a Snow
// Bunny Tia definition, or a tooltip/Lv+1 assertion. Skill38 comes from this
// newly sampled body, not historical skill-switch data.
// Main's current1426 parser rejects list7; the positive registration assertion
// is expected RED by source inspection. D22 + this file is pending an allowed
// run. No test/compile result is claimed by adding this source.
func TestD23SyntheticPetBuffSameProcessReselect(t *testing.T) {
	for _, scenario := range []string{"unworn_bag", "bag_move", "equip_unequip_move_old_slot_replaced"} {
		t.Run(scenario, func(t *testing.T) {
			s, session, conn, r := d23PetBuffFixture(t, nil)
			ctx := context.Background()
			// Seed the second same-template pet BEFORE registration. Subsequent
			// moves use the real op19 handler; Inventory.Save is never used as a move.
			if scenario == "equip_unequip_move_old_slot_replaced" {
				inv, found, err := r.Inventory.Load(ctx, "19")
				if err != nil || !found {
					t.Fatalf("synthetic inventory: found=%v err=%v", found, err)
				}
				second := repo.CloneInventory(inv).Slots["7:0"]
				second.Extra["creature_serial_or_handle"] = "38"
				binary.LittleEndian.PutUint16(second.RawEntry, 3)
				inv.Slots["7:3"] = second
				if err := r.Inventory.Save(ctx, inv); err != nil {
					t.Fatal(err)
				}
				pet, found, err := r.Pet.Load(ctx, "19")
				if err != nil || !found {
					t.Fatalf("synthetic pet: found=%v err=%v", found, err)
				}
				entry := pet.Entries["37"]
				entry.PetKey, entry.CreatureKey = "38", 38
				entry.Name, entry.NameRaw = "synthetic-second", []byte("synthetic-second")
				entry.SourceSlotIndex = 3
				pet.Entries["38"] = entry
				if err := r.Pet.Save(ctx, pet); err != nil {
					t.Fatal(err)
				}
			}

			d23PetBuffRegister(t, s, session)
			state, found, err := buffswap.Load(ctx, r, "19", "dnf:1")
			if err != nil || !found || state.Skill != 38 || state.WireProfile != buffswap.CurrentWireProfile ||
				len(state.Slots) != 1 || state.Slots[26].ItemID != 400990167 || state.Slots[26].Instance == "" {
				t.Fatalf("current-only pet registration must persist before reselect: found=%v err=%v state=%+v", found, err, state)
			}
			d23AssertPetBuffWire(t, conn.write.Bytes(), true, 0, false)
			conn.write.Reset()
			before := d23PetBuffSettings(t, r)
			instance := state.Slots[26].Instance
			wantSlot := uint16(0)
			switch scenario {
			case "bag_move":
				d23PetBuffMove(t, s, session, conn, buildOnlineItemTradeMoveRequest(7, 0, 37, 1, 7, 2))
				wantSlot = 2
			case "equip_unequip_move_old_slot_replaced":
				d23PetBuffMove(t, s, session, conn, petMoveRefreshEquipBody(0, 37))
				worn, found, err := r.Equipment.Load(ctx, "19")
				if err != nil || !found || worn.Entries["26"].Extra["native_buff_instance"] != instance {
					t.Fatalf("normal equip lost registered identity: found=%v err=%v worn=%+v", found, err, worn)
				}
				// Same endpoint order as inventory.TestHandlerMovePetUnequipKeepsCurrentEXEEndpointOrder:
				// empty bag source / worn destination, NOT an unsupported list3->7 request.
				unequip := petMoveRefreshEquipBody(1, 0)
				binary.LittleEndian.PutUint32(unequip[14:18], 37)
				d23PetBuffMove(t, s, session, conn, unequip)
				d23RequireUnwornPet(t, r, 1, instance)
				d23PetBuffMove(t, s, session, conn, buildOnlineItemTradeMoveRequest(7, 1, 37, 1, 7, 2))
				d23PetBuffMove(t, s, session, conn, buildOnlineItemTradeMoveRequest(7, 3, 38, 1, 7, 0))
				inv, _, err := r.Inventory.Load(ctx, "19")
				if err != nil || inv.Slots["7:0"].ItemID != 400990167 ||
					inv.Slots["7:0"].Extra["creature_serial_or_handle"] != "38" ||
					inv.Slots["7:0"].Extra["native_buff_instance"] == instance {
					t.Fatalf("old slot must contain a different same-template instance: err=%v inv=%+v", err, inv)
				}
				wantSlot = 2
			}
			d23RequireUnwornPet(t, r, wantSlot, instance)
			d23RequirePetBuffSettings(t, r, before)
			d23RunCharacterSelectionPackets(t, s, session, conn, r, before, true, wantSlot)
			d23RequireUnwornPet(t, r, wantSlot, instance)
		})
	}
}

func TestD23SyntheticPetBuffReselectRejectsStaleReplacementAndForeignUID(t *testing.T) {
	for _, boundary := range []string{"absent_original_same_template_replacement", "foreign_legacy_uid"} {
		t.Run(boundary, func(t *testing.T) {
			var foreign []repo.OwnedLegacyItem
			binding := `{"list":7,"index":0,"item_id":400990167,"instance":"d23-original-absent","source_kind":"current_instance"}`
			if boundary == "foreign_legacy_uid" {
				// Exact UID exists, but belongs to synthetic character20. Keep
				// list7 empty in current storage to exercise owned Legacy lookup.
				row := reviewOwnedLegacyFixture(7)
				row.OwnerID, row.CharacterID = "20", "20"
				row.Item.SlotIndex, row.Item.ItemTemplateID = 0, 400990167
				foreign = []repo.OwnedLegacyItem{row}
				binding = `{"list":7,"index":0,"item_id":400990167,"source_kind":"legacy_uid","legacy_item_uid":7007}`
			}
			s, session, conn, r := d23PetBuffFixture(t, foreign)
			if boundary == "absent_original_same_template_replacement" {
				// Initial negative fixture: the remaining pet has a different
				// physical identity. This write precedes the stale Settings seed.
				inv, found, err := r.Inventory.Load(context.Background(), "19")
				if err != nil || !found {
					t.Fatalf("replacement fixture: found=%v err=%v", found, err)
				}
				item := inv.Slots["7:0"]
				item.Extra["native_buff_instance"] = "d23-replacement-present"
				inv.Slots["7:0"] = item
				if err := r.Inventory.Save(context.Background(), inv); err != nil {
					t.Fatal(err)
				}
			}
			// Explicit synthetic stale persistence, NOT a simulated delete,
			// trade, ownership transfer, or ordinary registration operation.
			before := repo.SettingsRecord{Scope: buffswap.Scope("19"), Values: map[string]string{
				"state":  `{"wire_profile":"current1426-1368","skill":38,"slots":{"26":` + binding + `}}`,
				"source": "d23-synthetic-stale-persistence",
			}}
			if err := r.Settings.Save(context.Background(), before); err != nil {
				t.Fatal(err)
			}
			before = d23PetBuffSettings(t, r)
			if boundary == "foreign_legacy_uid" {
				// A current1426 attempt at the foreign row cannot overwrite the
				// saved UID or return a success snapshot.
				d23PetBuffRegister(t, s, session)
				if conn.write.Len() != 0 {
					t.Fatal("foreign UID registration emitted a success packet")
				}
				d23RequirePetBuffSettings(t, r, before)
			}
			d23RunCharacterSelectionPackets(t, s, session, conn, r, before, false, 0)
		})
	}
}

func d23PetBuffFixture(t *testing.T, foreign []repo.OwnedLegacyItem) (*Service, *gameSession, *bufferConn, repo.Group) {
	t.Helper()
	t.Setenv(buffSwapReapplyEnv, "on")
	t.Setenv(loginActorGenerationRebuildEnv, loginActorGenerationRebuildAfterSceneCommit)
	// Fix the synthetic actor owner to channel19 independently of the caller's environment.
	t.Setenv("DNFBRIDGE_OWNER_CONTEXT_USE_CLIENT_CHANNEL", "on")
	s, session, conn := newPetMoveRefreshTestService(t, "")
	r, ok := s.repositoryGroup()
	if !ok {
		t.Fatal("pet fixture repository unavailable")
	}
	ctx := context.Background()
	character, found, err := r.Character.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("synthetic character: found=%v err=%v", found, err)
	}
	if len(foreign) != 0 {
		// Use the existing real memory owned-snapshot implementation, with
		// foreign rows fixed at fixture construction; no repository stub.
		r = memory.NewMemoryGroupWithBuffLegacyItems(foreign)
		s.repositoryProvider = func() (repo.Group, bool) { return r, true }
		for _, err := range []error{
			r.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "19", Slots: map[string]repo.ItemStack{}}),
			r.Equipment.Save(ctx, repo.EquipmentRecord{CharacterID: "19", Entries: map[string]repo.EquipmentEntry{}}),
			r.Pet.Save(ctx, repo.PetRecord{CharacterID: "19", Entries: map[string]repo.PetEntry{}}),
		} {
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	character.Name, character.Job, character.Level = "D23-syntheticfixture", "0", 1
	character.Stats = map[string]int64{currentDungeonTutorialCompletedKey: currentDungeonTutorialCompleteFlag,
		"town_id": 38, "area_id": 1, "pos_x": 450, "pos_y": 234, "direction": 5, "area_state": 3}
	if err := r.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	if err := r.Account.Save(ctx, repo.AccountRecord{AccountID: "dnf:1", Metadata: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	enableTownMoveSkillProjection(t, s, r, "19")
	// Add a synthetic active definition/base level for the newly observed
	// skill38 field; this does not import q7a's job or actual pet bonus data.
	s.skillCatalog, err = buildSkillCatalogFromSource(ctx, initialEquipmentMemSource{
		"skill/skilllist.lst":       "0 `job0.lst`\n",
		"skill/job0.lst":            "38 `job0/registered.skl`\n46 `job0/active.skl`\n",
		"skill/job0/active.skl":     "[skill type]\n`active`\n",
		"skill/job0/registered.skl": "[skill type]\n`active`\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	skills, found, err := r.Skill.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("synthetic skills: found=%v err=%v", found, err)
	}
	skills.Skills[38] = repo.SkillState{Level: 1, Enabled: true}
	if err := r.Skill.Save(ctx, skills); err != nil {
		t.Fatal(err)
	}
	s.initialSkillsByJob[0] = append(s.initialSkillsByJob[0], initialSkillEntry{SkillID: 38, Level: 1})
	loadTownSeriaFixtureService(t, s, townMoveTestSource{
		"town/town.lst": "38 `new_Elvengard.twn`", "town/new_Elvengard.twn": townSeriaFixtureElvengard,
	})
	s.questCatalog = buildQuestListTestCatalog(t)
	s.adventureGroupTable = loadAdventureGroupTestTables(t)
	s.premiumCatalog = &currentPremiumCatalog{
		contractsByItem: make(map[int64]currentPremiumContractInfo),
		devilSlots:      make(map[uint32]currentPremiumDevilSlotInfo),
		crystalCubeIDs:  [6]int64{3033, 3034, 3035, 3036, 3037, 3262},
	}
	s.options.accountPrefix = defaultAccountPrefix
	// Match the real town-route tests: ordinary packets use channel13;
	// fixed scene item packets remain server16 and use the existing auto splitter.
	s.options.gameUpperHeader = gameUpperHeaderChannel13
	s.options.gameUpperClientBodyCodec = gameUpperClientBodyCodecPlain
	session.connID = "D23-synthetic-same-process"
	session.channel = channelcatalog.Channel{ServerID: 1, ID: 19, Type: 1, Name: "synthetic-ch19", Port: 10019}
	session.residentChannel = session.channel
	s.bindGameSessionCharacter(session, 19)
	t.Cleanup(func() { s.resetGameSessionForCharacterSelect(session) })
	return s, session, conn, r
}

// This helper only submits real production packet entrypoints. In particular,
// it never calls markReturnSelectTownReentry, sets ready flags, replaces the
// Service/session, or directly invokes LoadResolved/sendBuffSwapReapply.
func d23RunCharacterSelectionPackets(t *testing.T, s *Service, session *gameSession, conn *bufferConn, r repo.Group, saved repo.SettingsRecord, bound bool, index uint16) {
	t.Helper()
	oldGeneration, oldConnectionID := session.characterGeneration, session.connID
	conn.write.Reset()
	d23UpperPacket(t, s, session, uint16(dnfenum.CmdPacketReturnSelectCharacter), nil)
	ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if ack.Header.Classification != 1 || ack.Header.MsgID != 7 || !bytes.Equal(ack.Body, []byte{1}) || len(rest) != 0 ||
		session.selectedCharacterID != 0 || s.gameSessions[19] != nil || session.characterGeneration == oldGeneration {
		t.Fatal("real return-select must ACK and retire the previous generation")
	}
	d23RequirePetBuffSettings(t, r, saved)
	retiredGeneration := session.characterGeneration
	conn.write.Reset()
	d23UpperPacket(t, s, session, uint16(dnfenum.UpperMsgGetUserInfo), nil)
	if !session.rosterRequested || session.selectedCharacterID != 0 {
		t.Fatal("op8 must request the actual roster while unselected")
	}
	conn.write.Reset()
	// Existing 2B authoritative-character-ID compatibility shape; char19 is
	// this fixture only. An empty slot19 falls back to real character19.
	d23UpperPacket(t, s, session, uint16(dnfenum.UpperMsgSelectCharacter), []byte{19, 0})
	selectAck, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
	if selectAck.Header.Classification != 1 || selectAck.Header.MsgID != 4 || len(selectAck.Body) == 0 || selectAck.Body[0] != 1 {
		t.Fatalf("actual SelectCharacter ACK missing: header=%+v body=%x", selectAck.Header, selectAck.Body)
	}
	if session.selectedCharacterID != 19 || s.gameSessions[19] != session ||
		session.characterGeneration <= retiredGeneration || session.conn != conn || session.connID != oldConnectionID ||
		session.initialTownRouteStage != currentInitialTownRoutePlayerStateSent {
		t.Fatal("real SelectCharacter did not bind a new generation and complete the pre-op24 town route")
	}
	d23RequirePetBuffSettings(t, r, saved)
	all := append([]byte(nil), conn.write.Bytes()...)
	d23RequirePetBuffFinishGates(t, session, false)
	conn.write.Reset()
	if err := s.handleGameStreamPacket(session, loginActorGenerationLegacyStream(buildLegacyTownSceneReadyAckPacket())); err != nil {
		t.Fatal(err)
	}
	all = append(all, conn.write.Bytes()...)
	d23AssertPetBuffStageWire(t, conn.write.Bytes(), "scene-ready1345")
	d23RequirePetBuffFinishGates(t, session, false)
	if !session.initialTownLegacySceneReadyAccepted || !session.sceneBootstrapTailSent || session.sceneBootstrapTailDeferred ||
		!session.loginActorGenerationRebuildArmed || session.loginActorGenerationRebuildSent {
		t.Fatal("real type1345 must commit only the scene tail and arm the next client packet")
	}
	conn.write.Reset()
	// The stream entrypoint owns the after-first-client-packet mode1 rebuild;
	// calling handleGameUpper alone would miss this production hook.
	d23UpperPacket(t, s, session, uint16(dnfenum.UpperMsgCheckUserConnection), nil)
	d23AssertPetBuffStageWire(t, conn.write.Bytes(), "first-client-packet")
	d23AssertPetBuffWire(t, conn.write.Bytes(), bound, index, true)
	all = append(all, conn.write.Bytes()...)
	if !session.loginActorGenerationRebuildSent || session.loginActorGenerationRebuildArmed {
		t.Fatal("scene-commit actor generation rebuild was not consumed")
	}
	d23RequirePetBuffFinishGates(t, session, false)
	conn.write.Reset()
	// Real legacy finish-loading request, eight semantic zero bytes. Do not
	// assign postFinishLoadingPlayerStateSent or call the release tail directly.
	finish := buildLegacyGamePacketForBridgeTest(byte(dnfenum.GameCmdCommand), uint16(dnfenum.GameTypeFinishLoading), 0, make([]byte, 8))
	if err := s.handleGameStreamPacket(session, loginActorGenerationLegacyStream(finish)); err != nil {
		t.Fatal(err)
	}
	d23AssertPetBuffStageWire(t, conn.write.Bytes(), "finish-loading")
	d23RequirePetBuffFinishGates(t, session, true)
	d23AssertPetBuffWire(t, conn.write.Bytes(), bound, index, false)
	all = append(all, conn.write.Bytes()...)
	d23AssertPetBuffWire(t, all, bound, index, true)
	d23RequirePetBuffSettings(t, r, saved)
}

func d23UpperPacket(t *testing.T, s *Service, session *gameSession, opcode uint16, body []byte) {
	t.Helper()
	raw, err := dnfproto.BuildChannelPacket(opcode, body, 0, dnfproto.DefaultChannelClassification)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.handleGameStreamPacket(session, dnfproto.LatestGameStreamPacket{Kind: dnfproto.LatestGameStreamUpper, Data: raw}); err != nil {
		t.Fatal(err)
	}
}

func d23PetBuffRegister(t *testing.T, s *Service, session *gameSession) {
	t.Helper()
	// Same legacy1426 transport and six-byte semantic shape as collect-once03;
	// sequence/checksum/session ownership here belong to the synthetic fixture.
	body := reviewCurrentBuffCommand(38, 26, 7, 0)
	raw := buildLegacyGamePacketForBridgeTest(byte(dnfenum.GameCmdCommand), 1426, 0, body)
	if err := s.handleGameStreamPacket(session, loginActorGenerationLegacyStream(raw)); err != nil {
		t.Fatal(err)
	}
}

func d23PetBuffMove(t *testing.T, s *Service, session *gameSession, conn *bufferConn, body []byte) {
	t.Helper()
	conn.write.Reset()
	// Real aligned handler/repository coverage; this helper does not exercise op19 stream decoding.
	runPetMoveRefreshOp19(t, s, session, body)
	if conn.write.Len() == 0 {
		t.Fatal("normal pet move must emit its committed ACK")
	}
	ack, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
	if ack.Header.Classification != 1 || ack.Header.MsgID != 19 || len(ack.Body) < 1 || ack.Body[0] != 1 {
		t.Fatalf("pet move rejected: header=%+v body=%x", ack.Header, ack.Body)
	}
	conn.write.Reset()
}

func d23RequireUnwornPet(t *testing.T, r repo.Group, index uint16, instance string) {
	t.Helper()
	inv, found, err := r.Inventory.Load(context.Background(), "19")
	if err != nil || !found {
		t.Fatalf("inventory: found=%v err=%v", found, err)
	}
	key := "7:" + strconv.Itoa(int(index))
	item := inv.Slots[key]
	if item.ItemID != 400990167 || item.Count != 1 || item.Extra["creature_serial_or_handle"] != "37" || item.Extra["native_buff_instance"] != instance {
		t.Fatalf("registered instance lost at %s: %+v", key, item)
	}
	eq, found, err := r.Equipment.Load(context.Background(), "19")
	if err != nil || !found || eq.Entries["26"].ItemID != 0 {
		t.Fatalf("pet must remain unworn: found=%v err=%v equipment=%+v", found, err, eq)
	}
}

func d23PetBuffSettings(t *testing.T, r repo.Group) repo.SettingsRecord {
	t.Helper()
	value, found, err := r.Settings.Load(context.Background(), buffswap.Scope("19"))
	if err != nil || !found || !json.Valid([]byte(value.Values["state"])) {
		t.Fatalf("BUFF Settings: found=%v err=%v record=%+v", found, err, value)
	}
	return value
}

func d23RequirePetBuffSettings(t *testing.T, r repo.Group, before repo.SettingsRecord) {
	t.Helper()
	if after := d23PetBuffSettings(t, r); !reflect.DeepEqual(after, before) {
		t.Fatalf("lifecycle/move changed persistent BUFF Settings: before=%+v after=%+v", before, after)
	}
}

func d23RequirePetBuffFinishGates(t *testing.T, session *gameSession, completed bool) {
	t.Helper()
	if session.currentFinishLoadingStateSent != completed || session.currentFinishLoadingCompletionSent != completed ||
		session.postFinishLoadingPlayerStateSent != completed {
		t.Fatalf("request-owned finish gates: state=%t completion=%t placement=%t want all=%t",
			session.currentFinishLoadingStateSent, session.currentFinishLoadingCompletionSent,
			session.postFinishLoadingPlayerStateSent, completed)
	}
}

func d23AssertPetBuffStageWire(t *testing.T, stream []byte, stage string) {
	t.Helper()
	// These are isolated response segments, not a complete EXE packet-list oracle.
	// 1345: game_select_scene_deferred_tail.go:98-126 and initial_town_legacy_scene_ready_test.go:43.
	// First packet: login_actor_generation_rebuild.go:358-371,442; existing rebuild tests:327.
	// Finish: game_select_scene_request_handlers.go:64,107 and scene_finish_loading.go:43-80,192-206.
	var sequence []string
	for packetIndex := 0; len(stream) != 0; packetIndex++ {
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, stream)
		stream = rest
		class, opcode := packet.Header.Classification, packet.Header.MsgID
		actor := class == 0 && opcode == 2 && len(packet.Body) != 0 && (packet.Body[0] == 0 || packet.Body[0] == 1 || packet.Body[0] == 3)
		finishFrame := opcode == 37 || (class == 0 && (opcode == 30 || opcode == 120))
		if stage == "scene-ready1345" || stage == "first-client-packet" {
			if finishFrame || (class == 0 && opcode == 105) || (stage == "scene-ready1345" && actor) {
				t.Fatalf("%s prematurely emitted actor/finish frame at %d: class=%d op=%d body=%x", stage, packetIndex, class, opcode, packet.Body)
			}
		}
		if stage == "first-client-packet" && actor {
			if packet.Body[0] == 3 {
				t.Fatal("first client packet unexpectedly emitted personal-info mode3")
			}
			sequence = append(sequence, "mode"+strconv.Itoa(int(packet.Body[0])))
		}
		if stage == "finish-loading" {
			if class == 1 && opcode == 37 {
				if !bytes.Equal(packet.Body, []byte{1}) {
					t.Fatalf("finish ACK37 must carry only success discriminator: %x", packet.Body)
				}
				sequence = append(sequence, "ACK37")
			}
			if class == 0 {
				switch opcode {
				case 37:
					if len(packet.Body) != 87 {
						t.Fatalf("finish state37 width=%d want 87", len(packet.Body))
					}
					sequence = append(sequence, "state37")
				case 30:
					if !bytes.Equal(packet.Body, []byte{0, 0, 0, 0, 0}) {
						t.Fatalf("finish op30 completion body=%x", packet.Body)
					}
					sequence = append(sequence, "op30")
				case 120:
					if !bytes.Equal(packet.Body, []byte{0, 0}) {
						t.Fatalf("finish op120 placement body=%x", packet.Body)
					}
					sequence = append(sequence, "op120")
				}
			}
		}
		if class == 0 && opcode == 1368 && stage != "scene-ready1345" {
			sequence = append(sequence, "1368")
		}
	}
	var want []string
	switch stage {
	case "scene-ready1345":
		return
	case "first-client-packet":
		want = []string{"mode0", "mode1", "1368"}
	case "finish-loading":
		want = []string{"ACK37", "state37", "op30", "op120", "1368"}
	default:
		t.Fatalf("unknown lifecycle oracle stage %q", stage)
	}
	if !reflect.DeepEqual(sequence, want) {
		t.Fatalf("%s key-frame order/count=%v want=%v (unrelated frames may interleave)", stage, sequence, want)
	}
}

func d23AssertPetBuffWire(t *testing.T, stream []byte, bound bool, index uint16, requireMode1 bool) {
	t.Helper()
	// Independent protocol oracle: skill38, 33 current1368 rows, only slot26
	// bound. Do not build expectations with nativeBuffSwapSnapshotBody/resolver.
	want := []byte{38, 0, 33}
	for slot := byte(0); slot < 33; slot++ {
		row := []byte{0, slot, 45, 255, 255}
		if bound && slot == 26 {
			row = []byte{1, 26, 7, byte(index), byte(index >> 8)}
		}
		want = append(want, row...)
	}
	mode1Count, snapshots, lastMode1, lastSnapshot := 0, 0, -1, -1
	var lastMode1Body, lastSnapshotBody []byte
	for packetIndex := 0; len(stream) != 0; packetIndex++ {
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, stream)
		stream = rest
		if packet.Header.Classification != 0 {
			continue
		}
		if packet.Header.MsgID == 1368 {
			snapshots++
			lastSnapshot = packetIndex
			lastSnapshotBody = packet.Body
		}
		if packet.Header.MsgID != 2 || len(packet.Body) == 0 || packet.Body[0] != 1 {
			continue
		}
		mode1Count++
		lastMode1 = packetIndex
		lastMode1Body = packet.Body
	}
	if snapshots == 0 || (requireMode1 && mode1Count == 0) || lastSnapshot <= lastMode1 {
		t.Fatalf("final reapply must follow last actual mode1: mode1=%d snapshots=%d positions=%d/%d", mode1Count, snapshots, lastMode1, lastSnapshot)
	}
	if !bytes.Equal(lastSnapshotBody, want) {
		t.Fatalf("last actual1368 misbound/cleared pet: got=%x want=%x", lastSnapshotBody, want)
	}
	if mode1Count == 0 {
		return
	}
	// Only the last actor projection owns the final UI state. Earlier preview
	// bodies may be incomplete; do not turn that into an unrelated test failure.
	body := lastMode1Body
	// Independent zero-equipment fixture layout, not production offset constants:
	// mode/count/route/owner(5)+raw16+objectKey(2)+EXP(4)+statLen(4)+stat(92)+extra(1).
	// Reader/writer evidence: userinfo_current_modes.go:31-46,162-197;
	// current_honor_expert.go:116-125. createCount124, rows125, final DWORD125..128, BUFF129.
	const createCountOffset = 124
	const createRowsOffset = 125
	const offset = createRowsOffset + 4
	if len(body) < offset+3+8 || !bytes.Equal(body[:5], []byte{1, 1, 0, 0, 19}) ||
		binary.LittleEndian.Uint16(body[21:23]) != 19 || binary.LittleEndian.Uint32(body[27:31]) != 92 ||
		body[createCountOffset] != 0 || binary.LittleEndian.Uint32(body[createRowsOffset:offset]) != 0 {
		t.Fatalf("synthetic unworn actor mode1 envelope/create rows unexpected: %x", body)
	}
	count := byte(0)
	if bound {
		count = 1
	}
	if !bytes.Equal(body[offset:offset+3], []byte{38, 0, count}) {
		t.Fatalf("last actual mode1 BUFF header: %x", body[offset:])
	}
	end := offset + 3
	if bound {
		end += 4 + 119
		if len(body) < end+8 || !bytes.Equal(body[offset+3:offset+7], []byte{26, 7, byte(index), byte(index >> 8)}) {
			t.Fatalf("last actual mode1 pet location/full119B row missing: %x", body[offset:])
		}
		raw := body[offset+7 : end]
		if binary.LittleEndian.Uint16(raw) != index || binary.LittleEndian.Uint32(raw[2:6]) != 400990167 || binary.LittleEndian.Uint32(raw[6:10]) != 37 {
			t.Fatalf("last actual mode1 adopted a replacement or lost pet serial: %x", raw)
		}
	}
	if !bytes.Equal(body[end:end+8], make([]byte, 8)) {
		t.Fatal("BUFF registration incorrectly equipped/resurrected an unworn pet")
	}
}

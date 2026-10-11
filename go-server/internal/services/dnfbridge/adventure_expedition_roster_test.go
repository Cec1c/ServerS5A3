package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/adventuregroup"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func expeditionRosterTestCharacters() []repo.CharacterRecord {
	return []repo.CharacterRecord{
		{CharacterID: "1", AccountID: "owner", Slot: 0, Name: "first", Job: "0", Level: 90, Stats: map[string]int64{"grow_type": 0x21}},
		{CharacterID: "10029", AccountID: "owner", Slot: 1, Name: "second", Job: "1", Level: 90, Stats: map[string]int64{"grow_type": 0x21}},
	}
}

func decodeExpeditionRosterTestRegions(t *testing.T, body []byte) map[byte]adventuregroup.Expedition {
	t.Helper()
	regions := make(map[byte]adventuregroup.Expedition)
	offset := 1
	for i := 0; i < int(body[0]); i++ {
		area := body[offset]
		e := adventuregroup.Expedition{Area: area, State: body[offset+1], StartedAt: int64(binary.LittleEndian.Uint32(body[offset+2:])), EndsAt: int64(binary.LittleEndian.Uint32(body[offset+6:]))}
		offset += 10
		n := int(body[offset])
		offset++
		e.Attributes = append([]byte(nil), body[offset:offset+n]...)
		offset += n
		members := int(binary.LittleEndian.Uint16(body[offset:]))
		offset += 2
		for j := 0; j < members; j++ {
			m := adventuregroup.ExpeditionMember{CharacterID: binary.LittleEndian.Uint16(body[offset:])}
			offset += 2
			n := int(binary.LittleEndian.Uint32(body[offset:]))
			offset += 4
			m.Name = string(body[offset : offset+n])
			offset += n
			m.Level = binary.LittleEndian.Uint32(body[offset:])
			offset += 4
			m.Job, m.GrowType, m.Status = body[offset], body[offset+1], body[offset+2]
			offset += 3
			e.Members = append(e.Members, m)
		}
		regions[area] = e
	}
	if offset != len(body) {
		t.Fatalf("region body remainder %d/%d", offset, len(body))
	}
	return regions
}

func TestAdventureExpeditionIdleRosterAndSlotCollision(t *testing.T) {
	rows := expeditionRosterTestCharacters()
	now := time.Unix(1800000000, 0)
	state := adventuregroup.RuntimeState{Version: 1, Expeditions: map[string]adventuregroup.Expedition{}}
	before, _ := state.Marshal()
	body, view, err := buildCurrentAdventureExpeditionRosterBody(state, rows, "owner", now)
	if err != nil || len(view) != 2 || body[0] != 1 || body[1] != 0 || body[2] != 0 || binary.LittleEndian.Uint16(body[12:14]) != 2 {
		t.Fatalf("idle area0 missing: body=%x view=%+v err=%v", body, view, err)
	}
	if after, _ := state.Marshal(); after != before {
		t.Fatal("read-only projection changed runtime")
	}
	request := []currentAdventureExpeditionMemberRequest{{Selector: 1, Job: 1, GrowType: 1}}
	members, _, ok := resolveCurrentAdventureExpeditionMembers(request, rows, state)
	if !ok || len(members) != 1 || members[0].CharacterID != 1 || members[0].CharacterRecordID != "10029" || members[0].Name != "second" {
		t.Fatalf("ID1/slot1 collision remains: %+v %v", members, ok)
	}
	state.Expeditions["1"] = adventuregroup.Expedition{Area: 1, Members: members, StartedAt: now.Unix(), EndsAt: now.Add(time.Hour).Unix(), Reward: 42}
	_, view, err = buildCurrentAdventureExpeditionRosterBody(state, rows, "owner", now)
	if err != nil || len(view) != 1 || view[0].RecordID != "1" {
		t.Fatalf("busy real identity is still selectable: %+v %v", view, err)
	}
	rows[1].Slot = 3
	if _, _, ok := resolveCurrentAdventureExpeditionMembers([]currentAdventureExpeditionMemberRequest{{Selector: 3, Job: 1, GrowType: 1}}, rows, state); ok {
		t.Fatal("busy real character became available after slot move")
	}
	value, _ := state.Marshal()
	loaded, err := adventuregroup.ParseRuntimeState(repo.AccountRecord{Metadata: map[string]string{adventuregroup.RuntimeStateMetadataKey: value}}, adventuregroup.RuntimeConfig{}, now)
	if err != nil || loaded.Expeditions["1"].Members[0].CharacterRecordID != "10029" || loaded.Expeditions["1"].EndsAt != state.Expeditions["1"].EndsAt || loaded.Expeditions["1"].Reward != 42 {
		t.Fatal("new identity persistence changed historical time/reward", loaded, err)
	}
}

func TestAdventureExpeditionQ7aPackedGrowthAndLegacyProjection(t *testing.T) {
	c := repo.CharacterRecord{CharacterID: "31", AccountID: "10029", Slot: 0, Name: "q7a", Job: "0", Level: 90, Stats: map[string]int64{"grow_type": 33}}
	now := time.Unix(1800000000, 0)
	body, view, err := buildCurrentAdventureExpeditionRosterBody(adventuregroup.RuntimeState{}, []repo.CharacterRecord{c}, "10029", now)
	if err != nil || body[len(body)-2] != 1 || view[0].Grow != 1 || view[0].PackedGrow != 33 || c.Stats["grow_type"] != 33 {
		t.Fatalf("actual q7a projection wrong: body=%x view=%+v err=%v", body, view, err)
	}
	if dir := os.Getenv("YKCN90_EXPEDITION_NATIVE_DIR"); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "expedition441-q7a-branch.bin"), body, 0644); err != nil {
			t.Fatal(err)
		}
	}
	for _, realID := range []string{"", "31"} {
		state := adventuregroup.RuntimeState{Version: 1, Expeditions: map[string]adventuregroup.Expedition{"1": {Area: 1, State: 1, StartedAt: now.Unix(), EndsAt: now.Add(time.Hour).Unix(), Reward: 42, Members: []adventuregroup.ExpeditionMember{{CharacterRecordID: realID, CharacterID: 0, Name: "q7a", Level: 90, Job: 0, GrowType: 33}}}}}
		before, _ := state.Marshal()
		projected, members, err := buildCurrentAdventureExpeditionRosterBody(state, []repo.CharacterRecord{c}, "10029", now)
		after, _ := state.Marshal()
		if err != nil || len(members) != 0 || projected[len(projected)-2] != 1 || before != after {
			t.Fatalf("old packed runtime not projected read-only: body=%x err=%v", projected, err)
		}
		if _, _, ok := resolveCurrentAdventureExpeditionMembers([]currentAdventureExpeditionMemberRequest{{Selector: 0, Job: 0, GrowType: 1}}, []repo.CharacterRecord{c}, state); ok {
			t.Fatal("old packed active identity dispatched again")
		}
	}
	for _, packed := range []int64{0, 6, 0x26, -1, 256} {
		c.Stats["grow_type"] = packed
		if _, valid := currentAdventureExpeditionBranch(c); valid {
			t.Fatalf("invalid expedition branch accepted %d", packed)
		}
	}
	delete(c.Stats, "grow_type")
	if _, valid := currentAdventureExpeditionBranch(c); valid {
		t.Fatal("missing authoritative growth accepted")
	}
}

func TestAdventureExpeditionWireUsesRealHonorExpert(t *testing.T) {
	rows := expeditionRosterTestCharacters()
	now := time.Unix(1800000000, 0)
	for _, level := range []int64{0, 50, 90} {
		rows[0].Stats[currentHonorExpertLevelStatKey] = level
		rows[0].Stats[currentHonorExpertProgressExperienceStatKey] = 123
		body, _, err := buildCurrentAdventureExpeditionRosterBody(adventuregroup.RuntimeState{}, rows, "owner", now)
		if err != nil {
			t.Fatal(err)
		}
		idle := decodeExpeditionRosterTestRegions(t, body)[0].Members[0]
		if idle.Level != uint32(level) || idle.CharacterID != 0 || idle.Job != 0 || idle.GrowType != 1 {
			t.Fatalf("honor projection changed identity: %+v", idle)
		}
		members, inputs, ok := resolveCurrentAdventureExpeditionMembers([]currentAdventureExpeditionMemberRequest{{Selector: 0, Job: 0, GrowType: 1}}, rows, adventuregroup.RuntimeState{})
		if !ok || members[0].Level != uint32(level) || inputs[0].Level != 90 {
			t.Fatalf("honor and eligibility source mixed: %+v %+v", members, inputs)
		}
		legacy := adventuregroup.RuntimeState{Version: 1, Expeditions: map[string]adventuregroup.Expedition{"1": {Area: 1, State: 1, StartedAt: now.Unix(), EndsAt: now.Add(time.Hour).Unix(), Reward: 171, Members: []adventuregroup.ExpeditionMember{{CharacterID: 0, Name: "first", Job: 0, GrowType: 33, Level: 90}}}}}
		before, _ := legacy.Marshal()
		body, _, err = buildCurrentAdventureExpeditionRosterBody(legacy, rows, "owner", now)
		after, _ := legacy.Marshal()
		if err != nil || before != after || decodeExpeditionRosterTestRegions(t, body)[1].Members[0].Level != uint32(level) {
			t.Fatal("legacy honor projection mutated saved reward/time/member", err)
		}
	}
}

func TestAdventureExpeditionLegacyBusyAndDuplicateIdentity(t *testing.T) {
	rows := expeditionRosterTestCharacters()
	state := adventuregroup.RuntimeState{Expeditions: map[string]adventuregroup.Expedition{"1": {Members: []adventuregroup.ExpeditionMember{{CharacterID: 1, Name: "second", Job: 1, GrowType: 0x21}}}}}
	busy, ok := currentAdventureExpeditionBusyIdentities(state, rows)
	if !ok || !busy["10029"] || busy["1"] {
		t.Fatal("legacy slot was reinterpreted as ID", busy, ok)
	}
	rows[1].Name = "changed"
	if _, ok := currentAdventureExpeditionBusyIdentities(state, rows); ok {
		t.Fatal("ambiguous legacy identity guessed after rename")
	}
	if _, _, ok := resolveCurrentAdventureExpeditionMembers([]currentAdventureExpeditionMemberRequest{{Selector: 0, Job: 0, GrowType: 1}}, rows, state); ok {
		t.Fatal("unresolved old identity permits unsafe reuse")
	}
	rows = expeditionRosterTestCharacters()
	rows[1].CharacterID = rows[0].CharacterID
	rows[1].Job = "0"
	if _, _, ok := resolveCurrentAdventureExpeditionMembers([]currentAdventureExpeditionMemberRequest{{Selector: 0, Job: 0, GrowType: 1}, {Selector: 1, Job: 0, GrowType: 1}}, rows, adventuregroup.RuntimeState{}); ok {
		t.Fatal("same real identity dispatched twice")
	}
	if _, _, err := buildCurrentAdventureExpeditionRosterBody(adventuregroup.RuntimeState{}, rows, "owner", time.Now()); err == nil {
		t.Fatal("duplicate real identity roster accepted")
	}
}

type expeditionRosterFailConn struct{ bufferConn }

func (c *expeditionRosterFailConn) Write([]byte) (int, error) {
	return 0, errors.New("injected expedition write failure")
}

func TestAdventureExpeditionPreselectionViewBindingAndFailedSend(t *testing.T) {
	ctx := context.Background()
	g := memory.NewMemoryGroup()
	rows := expeditionRosterTestCharacters()
	account := repo.AccountRecord{AccountID: "owner", Metadata: map[string]string{"keep": "yes"}}
	if err := g.Account.Save(ctx, account); err != nil {
		t.Fatal(err)
	}
	for _, c := range rows {
		if err := g.Character.Save(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	s := &Service{options: options{currentClientVersion: "official-24.9", accountID: "fallback"}, adventureGroupTable: loadAdventureGroupTestTables(t), repositoryProvider: func() (repo.Group, bool) { return g, true }}
	session := &gameSession{accountID: "owner", accountFromToken: true, loginPacketSeen: true, conn: &bufferConn{}}
	if err := s.handleCurrentAdventureExpeditionInfo(session, []byte{0}); err != nil {
		t.Fatal(err)
	}
	if len(session.currentAdventureExpeditionView.Members) != 2 || session.selectedCharacterID != 0 {
		t.Fatal("preselect did not publish real roster or selected a fake character")
	}
	loaded, _, _ := g.Account.Load(ctx, "owner")
	savedRows, _ := g.Character.ListByAccount(ctx, "owner", defaultCharacterSlots)
	if !reflect.DeepEqual(loaded, account) || !reflect.DeepEqual(savedRows, rows) {
		t.Fatal("preselect read wrote account/characters")
	}
	r := []currentAdventureExpeditionMemberRequest{{Selector: 1, Job: 1, GrowType: 1}}
	if !s.validateCurrentAdventureExpeditionSentMembers(session, r, rows) {
		t.Fatal("published owner view invalid")
	}
	rows[1].Stats["grow_type"] = 0x11
	if s.validateCurrentAdventureExpeditionSentMembers(session, r, rows) {
		t.Fatal("same branch with changed awakening accepted stale view")
	}
	rows[1].Stats["grow_type"] = 0x21
	session.characterGeneration++
	if s.validateCurrentAdventureExpeditionSentMembers(session, r, rows) {
		t.Fatal("old generation accepted")
	}
	session.characterGeneration--
	s.bindGameSessionCharacter(session, 1)
	if s.validateCurrentAdventureExpeditionSentMembers(session, r, rows) {
		t.Fatal("preselection view survived character binding")
	}
	session.conn = &expeditionRosterFailConn{}
	if err := s.sendCurrentAdventureExpeditionRosterAfterSelection(session); err == nil || s.validateCurrentAdventureExpeditionSentMembers(session, r, rows) {
		t.Fatal("failed selected refresh committed stale view", err)
	}
	session.conn = &bufferConn{}
	if err := s.sendCurrentAdventureExpeditionRosterAfterSelection(session); err != nil || !s.validateCurrentAdventureExpeditionSentMembers(session, r, rows) {
		t.Fatal("selected refresh without second 441 failed", err)
	}
	session.accountID = "other"
	if s.validateCurrentAdventureExpeditionSentMembers(session, r, rows) {
		t.Fatal("old account accepted")
	}
	session.accountID = "owner"
	rows[1].Slot = 2
	if s.validateCurrentAdventureExpeditionSentMembers(session, r, rows) {
		t.Fatal("changed slot accepted from stale view")
	}
	session.conn = &expeditionRosterFailConn{}
	session.currentAdventureExpeditionView = currentAdventureExpeditionSentView{}
	if err := s.handleCurrentAdventureExpeditionInfo(session, []byte{0}); err == nil || session.currentAdventureExpeditionView.Members != nil {
		t.Fatal("failed send committed selectable view", err)
	}
}

func TestAdventureExpeditionPreselectRosterRefreshBeforeTypedTown24(t *testing.T) {
	s, session, repositories := newTownMoveTest(t)
	s.options.currentClientVersion = "official-24.9"
	s.adventureGroupTable = loadAdventureGroupTestTables(t)
	session.accountID, session.accountFromToken, session.loginPacketSeen = "dnf:1", true, true
	session.selectedCharacterID = 0
	c, _, _ := repositories.Character.Load(context.Background(), "29")
	c.Name, c.Job, c.Level = "real-owner", "0", 90
	c.Stats["grow_type"] = 0x21
	c.Stats[currentDungeonTutorialCompletedKey] = currentDungeonTutorialCompleteFlag
	if err := repositories.Character.Save(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Account.Save(context.Background(), repo.AccountRecord{AccountID: "dnf:1", Metadata: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	roster, err := dnfproto.BuildChannelPacket(uint16(dnfenum.UpperMsgCharacterRoster), nil, 0, dnfproto.DefaultChannelClassification)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.handleGameUpper(session, roster); err != nil {
		t.Fatal(err)
	}
	if err := s.handleCurrentAdventureExpeditionInfo(session, []byte{0}); err != nil {
		t.Fatal(err)
	}
	var request [2]byte
	binary.LittleEndian.PutUint16(request[:], 29)
	selection, err := dnfproto.BuildChannelPacket(uint16(dnfenum.UpperMsgSelectCharacter), request[:], 0, dnfproto.DefaultChannelClassification)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.handleGameUpper(session, selection); err != nil {
		t.Fatal(err)
	}
	conn := session.conn.(*bufferConn)
	conn.write.Reset()
	if err := s.handleDungeonTutorialFlag(session, []byte{currentDungeonTutorialFinalPrefix, 36, 0, 0, 0, currentDungeonTutorialFinalCommit}); err != nil {
		t.Fatal(err)
	}
	barrier, err := dnfproto.BuildChannelPacket(uint16(dnfenum.UpperMsgCheckUserConnection), nil, 0, dnfproto.DefaultChannelClassification)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.handleGameUpper(session, barrier); err != nil {
		t.Fatal(err)
	}
	refresh, transition := -1, -1
	index := 0
	for stream := conn.write.Bytes(); len(stream) > 0; index++ {
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, stream)
		stream = rest
		if packet.Header.MsgID == currentAdventureExpeditionStateMsgID {
			refresh = index
		}
		if packet.Header.MsgID == currentSceneTransitionMsgID {
			transition = index
		}
	}
	if refresh < 0 || transition <= refresh || !s.currentAdventureExpeditionViewCurrent(session, session.currentAdventureExpeditionView) || len(session.currentAdventureExpeditionView.Members) != 1 {
		t.Fatalf("normal selection route did not refresh before typed24: refresh=%d transition=%d view=%+v", refresh, transition, session.currentAdventureExpeditionView)
	}
}

func TestRealPVFAdventureExpeditionDispatchReloadCancelAndReward(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires published PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	tables, err := adventuregroup.LoadComplete(context.Background(), archive)
	if err != nil {
		t.Fatal(err)
	}
	if len(tables.Runtime().CharacterAttributes[[2]byte{0, 1}]) == 0 {
		t.Fatal("real PVF job0 branch1 has no expedition attributes")
	}
	config := tables.Runtime()
	if config.AttributeIDs["[group c allow]"] != 255 || !reflect.DeepEqual(config.AttributeAliases[255], []byte{20, 21, 22, 23}) {
		t.Fatalf("real PVF ALLOW lost: %+v %+v", config.AttributeIDs, config.AttributeAliases)
	}
	for _, date := range []time.Time{time.Date(2026, 10, 9, 4, 0, 0, 0, time.UTC), time.Date(2026, 10, 10, 4, 0, 0, 0, time.UTC)} {
		if !reflect.DeepEqual(config.AreaAttributes(1, date), []byte{255}) {
			t.Fatal("ALLOW became random daily attribute")
		}
	}
	withoutAliases := config.Clone()
	withoutAliases.AttributeAliases = nil
	rewardWith, ok := config.ExpeditionReward(1, 21600, []adventuregroup.ExpeditionMemberInput{{Level: 90, Job: 0, GrowType: 1}}, time.Date(2026, 10, 9, 4, 0, 0, 0, time.UTC))
	rewardWithout, _ := withoutAliases.ExpeditionReward(1, 21600, []adventuregroup.ExpeditionMemberInput{{Level: 90, Job: 0, GrowType: 1}}, time.Date(2026, 10, 9, 4, 0, 0, 0, time.UTC))
	if !ok || rewardWith != 1 || rewardWithout != 1 {
		t.Fatalf("real PVF alias reward not matched: %d/%d", rewardWith, rewardWithout)
	}
	// Real PVF has identical rates for zero and one match. Distinguish the
	// match-count paths with a test-only rate table without altering its rules.
	probe := config.Clone()
	probe.AttributeRewardRates = []float64{1, 2, 3, 4, 5}
	probeReward, _ := probe.ExpeditionReward(1, 21600, []adventuregroup.ExpeditionMemberInput{{Level: 90, Job: 0, GrowType: 1}}, time.Date(2026, 10, 9, 4, 0, 0, 0, time.UTC))
	if probeReward != 2 {
		t.Fatalf("ALLOW must count as exactly one match: %d", probeReward)
	}
	configClone := config.Clone()
	configClone.AttributeAliases[255][0] = 1
	if config.AttributeAliases[255][0] != 20 {
		t.Fatal("alias Clone shares mutable slice")
	}
	projectionTime := time.Date(2026, 10, 9, 4, 0, 0, 0, time.UTC)
	idleRuntime := adventuregroup.RuntimeState{Version: 1, Expeditions: map[string]adventuregroup.Expedition{}}
	idleBefore, _ := idleRuntime.Marshal()
	idleBody, _, err := buildCurrentAdventureExpeditionRosterBody(idleRuntime, expeditionRosterTestCharacters(), "owner", projectionTime, config)
	if err != nil {
		t.Fatal(err)
	}
	regions := decodeExpeditionRosterTestRegions(t, idleBody)
	if len(regions) != len(config.ExpeditionAreas)+1 || len(regions[0].Members) != 2 {
		t.Fatalf("full real idle roster missing: %+v", regions)
	}
	for _, area := range config.ExpeditionAreas {
		e := regions[area.Index]
		if e.State != 0 || e.StartedAt != 0 || e.EndsAt != 0 || len(e.Members) != 0 || !reflect.DeepEqual(e.Attributes, config.AreaAttributes(area.Index, projectionTime)) {
			t.Fatalf("native idle shape wrong %+v", e)
		}
	}
	idleAfter, _ := idleRuntime.Marshal()
	if idleBefore != idleAfter {
		t.Fatal("idle regions persisted")
	}
	if dir := os.Getenv("YKCN90_EXPEDITION_NATIVE_DIR"); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "expedition441-full-real-idle.bin"), idleBody, 0644); err != nil {
			t.Fatal(err)
		}
	}
	activeRuntime := adventuregroup.RuntimeState{Expeditions: map[string]adventuregroup.Expedition{"1": {Area: 1, State: 1, StartedAt: projectionTime.Unix() - 10, EndsAt: projectionTime.Unix() + 10, Attributes: []byte{23}, Reward: 42}}}
	activeBody, _, err := buildCurrentAdventureExpeditionRosterBody(activeRuntime, expeditionRosterTestCharacters(), "owner", projectionTime, config)
	if err != nil {
		t.Fatal(err)
	}
	active := decodeExpeditionRosterTestRegions(t, activeBody)[1]
	if active.State != 1 || !reflect.DeepEqual(active.Attributes, []byte{23}) || active.EndsAt != projectionTime.Unix()+10 || activeRuntime.Expeditions["1"].Reward != 42 {
		t.Fatal("active frozen attributes/time/reward changed")
	}
	ctx := context.Background()
	g := memory.NewMemoryGroup()
	rows := expeditionRosterTestCharacters()
	queue := newFakeCurrentDungeonDeathTimerQueue()
	if err := g.Account.Save(ctx, repo.AccountRecord{AccountID: "owner", Metadata: map[string]string{"keep": "yes"}}); err != nil {
		t.Fatal(err)
	}
	for _, c := range rows {
		if err := g.Character.Save(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	s := &Service{options: options{currentClientVersion: "official-24.9"}, adventureGroupTable: tables, gameplayTimers: queue, repositoryProvider: func() (repo.Group, bool) { return g, true }}
	session := &gameSession{accountID: "owner", accountFromToken: true, loginPacketSeen: true, conn: &bufferConn{}}
	if err := s.handleCurrentAdventureExpeditionInfo(session, []byte{0}); err != nil {
		t.Fatal(err)
	}
	s.bindGameSessionCharacter(session, 1)
	if err := s.sendCurrentAdventureExpeditionRosterAfterSelection(session); err != nil {
		t.Fatal(err)
	}
	state, err := s.currentAdventureSelectedState(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	var area byte
	var duration uint32
	for _, a := range state.Config.ExpeditionAreas {
		if a.RequiredManageLevel <= state.Summary.ManageLevel {
			area = a.Index
			for d := range a.RewardRates {
				if duration == 0 || d < duration {
					duration = d
				}
			}
			break
		}
	}
	if area == 0 || duration == 0 {
		t.Fatalf("real PVF fixture has no eligible area: %+v", state.Summary)
	}
	start := func() {
		t.Helper()
		var w packetWriter
		w.writeByte(area)
		w.writeUint32(duration)
		w.writeUint16(1)
		w.writeByte(1)
		w.writeByte(1)
		w.writeByte(1)
		if err := s.handleCurrentAdventureExpeditionStart(session, w.bytes()); err != nil {
			t.Fatal(err)
		}
	}
	load := func() adventuregroup.RuntimeState {
		t.Helper()
		a, _, err := g.Account.Load(ctx, "owner")
		if err != nil || a.Metadata["keep"] != "yes" {
			t.Fatal("unrelated metadata lost", err)
		}
		r, err := adventuregroup.ParseRuntimeState(a, tables.Runtime(), queue.Now())
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	start()
	running := load()
	exp, ok := running.Expeditions[adventuregroup.ExpeditionKey(area)]
	if !ok || exp.Members[0].CharacterRecordID != "10029" || exp.Members[0].CharacterID != 1 || exp.EndsAt-exp.StartedAt != int64(duration) || area != 1 || duration != 21600 || exp.Reward != 1 || exp.Members[0].Level != 0 {
		t.Fatal("real dispatch failed or wrong identity", running)
	}
	if exp.Members[0].GrowType != 1 {
		t.Fatal("new dispatch persisted packed growth")
	}
	// Replay r60 durable packed records through the real relogin/cancel/reward paths.
	legacyPacked := func() {
		t.Helper()
		r := load()
		e := r.Expeditions[adventuregroup.ExpeditionKey(area)]
		e.Members[0].GrowType = 0x21
		r.Expeditions[adventuregroup.ExpeditionKey(area)] = e
		a, _, err := g.Account.Load(ctx, "owner")
		if err != nil {
			t.Fatal(err)
		}
		value, err := r.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		a.Metadata[adventuregroup.RuntimeStateMetadataKey] = value
		if err := g.Account.Save(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	legacyPacked()
	// A fresh connection reads durable active and idle members without migration.
	relogin := &gameSession{accountID: "owner", accountFromToken: true, loginPacketSeen: true, conn: &bufferConn{}}
	if err := s.handleCurrentAdventureExpeditionInfo(relogin, []byte{0}); err != nil || len(relogin.currentAdventureExpeditionView.Members) != 1 {
		t.Fatal("relogin did not retain busy/idle roster", err)
	}
	if err := s.handleCurrentAdventureExpeditionCancel(session, []byte{area}); err != nil {
		t.Fatal(err)
	}
	if len(load().Expeditions) != 0 || len(session.currentAdventureExpeditionView.Members) != 2 {
		t.Fatal("cancel push did not restore idle roster")
	}
	start()
	legacyPacked()
	exp = load().Expeditions[adventuregroup.ExpeditionKey(area)]
	queue.mu.Lock()
	queue.now = time.Unix(exp.EndsAt, 0)
	queue.mu.Unlock()
	if err := s.handleCurrentAdventureExpeditionReward(session, []byte{area}); err != nil {
		t.Fatal(err)
	}
	rewarded := load()
	if len(rewarded.Expeditions) != 0 || rewarded.ShopPoints[adventuregroup.ShopPointGlory] != 1 || len(session.currentAdventureExpeditionView.Members) != 2 {
		t.Fatal("reward did not settle/restock roster", rewarded)
	}
	if err := s.handleCurrentAdventureExpeditionReward(session, []byte{area}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(load(), rewarded) {
		t.Fatal("duplicate claim modified points")
	}
	if dir := os.Getenv("YKCN90_EXPEDITION_NATIVE_DIR"); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		idle, _, err := buildCurrentAdventureExpeditionRosterBody(rewarded, rows, "owner", queue.Now())
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "expedition441-real-idle.bin"), idle, 0644); err != nil {
			t.Fatal(err)
		}
	}
}

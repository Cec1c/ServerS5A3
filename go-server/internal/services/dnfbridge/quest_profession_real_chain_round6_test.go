package dnfbridge

import (
	"context"
	"encoding/binary"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfprofession "longheng.io/server/internal/modules/dnf/profession"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// Round-6 offline chain coverage for "转职任务完成后职业/技能栏即时刷新".
//
// The existing bridge regression TestCurrentFinishQuestProfessionCommitPublishesCurrentEXEStateOrder
// proves the op34 -> mode0 identity -> op37 -> op19 -> op574 -> op356 -> op21 order,
// but it feeds a fabricated in-memory quest catalog, a fabricated Gunner.chr grant
// block and a hand-written skill table. The real-PVF cases in this package stop at
// the profession domain layer (PlanTransition/ApplySkillTransition) or at an
// expert-job/dialogue quest, so no existing case joins the real Script.pvf
// profession resources with the finish handler's ACK and refresh order.
//
// This test joins both halves for exactly one class-change quest and one
// committed transaction. It never invents a quest ID: the candidate below is the
// real job-change family entry already pinned by
// internal/modules/dnf/quest/job_change_preflight_real_pvf_test.go, and every
// property this test relies on (profession reward, target branch, grant block,
// skill paths) is re-derived from the loaded Script.pvf at run time. When the
// fixed resource cannot supply one of them the test fails with the specific
// missing fact instead of skipping a gate.
const (
	round6RealChainQuestID = int64(7814)
	round6RealChainJob     = byte(0) // 鬼剑士
	round6RealChainLevel   = 90
)

// round6RealChainSettlement counts how many times the finish handler entered the
// character settlement unit of work and how many of those transactions committed.
type round6RealChainSettlement struct {
	inner     dnfrepo.CharacterSettlementUnitOfWork
	attempts  int
	committed int
}

func (s *round6RealChainSettlement) WithinCharacterSettlement(ctx context.Context, characterID string, fn func(dnfrepo.Group) error) error {
	s.attempts++
	err := s.inner.WithinCharacterSettlement(ctx, characterID, fn)
	if err == nil {
		s.committed++
	}
	return err
}

func TestRealPVFProfessionFinishRealChainRound6(t *testing.T) {
	pvfPath := strings.TrimSpace(os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE"))
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to run the real-PVF profession finish chain")
	}
	ctx := context.Background()
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	questIndex, err := dnfpvf.Build(ctx, archive, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := dnfquest.Load(ctx, questIndex)
	if err != nil {
		t.Fatal(err)
	}
	skillCatalog, err := buildSkillCatalogFromSource(ctx, archive)
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := dnfprofession.LoadProfiles(ctx, archive, skillCatalog)
	if err != nil {
		t.Fatal(err)
	}

	// ---- real definition and real transition plan ----
	definition, found := catalog.Find(round6RealChainQuestID)
	if !found {
		t.Fatalf("real PVF is missing the pinned job-change quest %d", round6RealChainQuestID)
	}
	if !currentQuestRewardIsProfession(definition.RewardType) {
		t.Fatalf("real PVF quest %d reward type=%q path=%q is not a grow/awakening type reward",
			round6RealChainQuestID, definition.RewardType, definition.Path)
	}
	eligibility := dnfquest.CharacterEligibility{Level: round6RealChainLevel, Job: int(round6RealChainJob), GrowType: 0}
	plan, err := catalog.PlanFinishReward(eligibility, round6RealChainQuestID, ^uint16(0), false)
	if err != nil {
		t.Fatalf("real PVF quest %d finish plan: %v", round6RealChainQuestID, err)
	}
	if !plan.HasProfession || plan.ProfessionRequest.ChainType != 1 {
		t.Fatalf("real PVF quest %d plan is not a class change: %+v", round6RealChainQuestID, plan)
	}
	newGrow := plan.ProfessionRequest.GrowNumber
	if newGrow == 0 || newGrow > 5 {
		t.Fatalf("real PVF quest %d class-change target=%d is outside the PVF branch domain", round6RealChainQuestID, newGrow)
	}
	transition, err := profiles.PlanTransition(round6RealChainJob, 0, plan.ProfessionRequest)
	if err != nil {
		t.Fatalf("real PVF class change job=%d grow %d->%d: %v", round6RealChainJob, 0, newGrow, err)
	}
	grants, ok := profiles.FreeGrants(round6RealChainJob, newGrow, 0)
	if !ok || len(grants) == 0 {
		t.Fatalf("real character .chr has no free-grant block for job=%d grow=%d (found=%t)", round6RealChainJob, newGrow, ok)
	}
	grantLevels := make(map[uint16]int, len(grants))
	for _, grant := range grants {
		grantLevels[grant.SkillID] = grant.Level
	}

	// A real skill from another branch that the class change must drop. It is
	// taken from the same real .chr grant blocks, so it is a genuine pre-change
	// skill of this job rather than a fabricated row.
	foreign := uint16(0)
	for branch := byte(0); branch <= 5 && foreign == 0; branch++ {
		if branch == newGrow {
			continue
		}
		other, ok := profiles.FreeGrants(round6RealChainJob, branch, 0)
		if !ok {
			continue
		}
		for _, grant := range other {
			if _, isTarget := grantLevels[grant.SkillID]; isTarget {
				continue
			}
			if _, resolves := skillCatalog.Find(round6RealChainJob, grant.SkillID); resolves {
				foreign = grant.SkillID
				break
			}
		}
	}
	if foreign == 0 {
		t.Fatalf("real PVF has no foreign-branch skill for job=%d to prove the class-change table replacement", round6RealChainJob)
	}

	// ---- repository seed: one character, one active real quest row, one real foreign skill ----
	repositories := dnfrepomemory.NewMemoryGroup()
	settlement := &round6RealChainSettlement{inner: repositories.CharacterSettlement}
	repositories.CharacterSettlement = settlement
	characterName := "round6prof"
	acceptedAt := time.Date(2026, 10, 16, 9, 30, 0, 0, time.UTC)
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "19", AccountID: defaultAccountPrefix + "1", Name: characterName,
		Job: strconv.Itoa(int(round6RealChainJob)), Level: round6RealChainLevel,
		Stats: map[string]int64{"exp": 0, "grow_type": 0},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Quest.Save(ctx, dnfrepo.QuestRecord{
		CharacterID: "19",
		States: map[int64]dnfrepo.QuestState{
			round6RealChainQuestID: {Status: "active", ProgressValue: 0, UpdatedAt: acceptedAt},
		},
		UpdatedAt: acceptedAt,
	}); err != nil {
		t.Fatal(err)
	}
	seedPoints := dnfrepo.SkillPointState{TotalSP: 120, RemainingSP: 40, TotalTP: 12, RemainingTP: 4, SyncedLevel: round6RealChainLevel}
	seedSkill := dnfrepo.SkillRecord{
		CharacterID: "19",
		Skills:      map[int64]dnfrepo.SkillState{int64(foreign): {Level: 3, Enabled: true}},
		Layouts:     map[int]dnfrepo.SkillLayout{0: {0: foreign}},
		Points:      seedPoints,
	}
	if err := repositories.Skill.Save(ctx, seedSkill); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Equipment.Save(ctx, dnfrepo.EquipmentRecord{CharacterID: "19"}); err != nil {
		t.Fatal(err)
	}

	// ---- the real op34 entry: real archive-backed catalog, progression, item
	// catalog and profession resources are all loaded by the handler itself ----
	connection := &bufferConn{}
	service := &Service{
		options: options{
			pvfPath:            pvfPath,
			accountPrefix:      defaultAccountPrefix,
			gameUpperBodyCodec: gameUpperBodyCodecPlain,
		},
		// Production preloads these from the same Script.pvf (preloadInitialEquipmentIndex,
		// preloadQuestCatalog, the dungeon monster catalog and preloadProfessionProfiles).
		// The handler's own load phase runs under createWriteTimeout (3s), so this test
		// warms the identical real resources first and only then drives one op34 request;
		// the request budget is spent on the settlement and the published order rather
		// than on re-parsing the whole archive.
		initialEquipmentArchive: archive,
		questCatalog:            catalog,
		repositoryProvider:      func() (dnfrepo.Group, bool) { return repositories, true },
	}
	monsters, err := newPVFDungeonMonsterCatalog(archive)
	if err != nil {
		t.Fatalf("real dungeon monster catalog: %v", err)
	}
	if _, err := monsters.DropCatalog(); err != nil {
		t.Fatalf("real dungeon drop catalog: %v", err)
	}
	service.dungeonMonsterTable = monsters
	if _, _, err := service.currentProfessionResources(ctx); err != nil {
		t.Fatalf("real profession resources: %v", err)
	}
	session := &gameSession{conn: connection, connID: "round6-real-profession-chain", selectedCharacterID: 19}
	// Ten-byte op34 request built from the already-verified finish-request codec
	// (opcode echo, quest id, no-reward-select marker, multiplier, observed tail).
	requestBody := make([]byte, dnfquest.CurrentFinishQuestRequestBodySize)
	binary.LittleEndian.PutUint16(requestBody[0:2], uint16(dnfenum.CmdPacketFinishQuest))
	binary.LittleEndian.PutUint16(requestBody[2:4], uint16(round6RealChainQuestID))
	binary.LittleEndian.PutUint16(requestBody[4:6], ^uint16(0))
	binary.LittleEndian.PutUint16(requestBody[6:8], 1)
	binary.LittleEndian.PutUint16(requestBody[8:10], dnfquest.CurrentFinishQuestObservedTailMarker)
	if err := service.handleCurrentFinishQuest(session, requestBody); err != nil {
		t.Fatalf("real op34 profession finish: %v", err)
	}

	// ---- exactly one successful transaction ----
	if settlement.attempts != 1 || settlement.committed != 1 {
		t.Fatalf("settlement attempts=%d committed=%d want exactly one committed transaction",
			settlement.attempts, settlement.committed)
	}
	committedCharacter, found, err := repositories.Character.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("reload character found=%t err=%v", found, err)
	}
	committedSkill, found, err := repositories.Skill.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("reload skill found=%t err=%v", found, err)
	}
	committedQuest, found, err := repositories.Quest.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("reload quest found=%t err=%v", found, err)
	}
	if got := committedCharacter.Stats["grow_type"]; got != int64(newGrow) {
		t.Fatalf("committed grow_type=%d want %d (quest %d)", got, newGrow, round6RealChainQuestID)
	}
	if state := committedQuest.States[round6RealChainQuestID]; state.Status != "completed" || state.Extra["reward_state"] != "granted" {
		t.Fatalf("committed quest state=%+v want completed/granted", state)
	}

	// ---- committed skills are the real .chr grant block, not a fixture table ----
	expectedSkill, err := profiles.ApplySkillTransition(skillCatalog, round6RealChainJob, committedCharacter.Level, transition, seedSkill, seedPoints)
	if err != nil {
		t.Fatalf("real ApplySkillTransition for the committed state: %v", err)
	}
	committedIDs := round6RealChainSkillIDs(committedSkill)
	expectedIDs := round6RealChainSkillIDs(expectedSkill)
	if !reflect.DeepEqual(committedIDs, expectedIDs) {
		t.Fatalf("committed skills=%v want the real PVF transition output=%v (job=%d grow=%d level=%d)",
			committedIDs, expectedIDs, round6RealChainJob, newGrow, committedCharacter.Level)
	}
	if _, survived := committedSkill.Skills[int64(foreign)]; survived {
		t.Fatalf("class change kept the foreign-branch skill %d: %v", foreign, committedIDs)
	}
	for _, skillID := range committedIDs {
		jobSkill, ok := skillCatalog.Find(round6RealChainJob, uint16(skillID))
		if !ok || strings.TrimSpace(jobSkill.Path) == "" || strings.TrimSpace(jobSkill.Name) == "" {
			t.Fatalf("committed skill %d is not backed by the real job-%d skill table: found=%t skill=%+v",
				skillID, round6RealChainJob, ok, jobSkill)
		}
		level := committedSkill.Skills[int64(skillID)].Level
		if want := expectedSkill.Skills[int64(skillID)].Level; want != level {
			t.Fatalf("committed skill %d level=%d want %d", skillID, level, want)
		}
		if grantLevel, isGrant := grantLevels[uint16(skillID)]; isGrant && level < grantLevel {
			t.Fatalf("committed granted skill %d level=%d below the real .chr grant level %d", skillID, level, grantLevel)
		}
	}
	for _, grant := range grants {
		if _, present := committedSkill.Skills[int64(grant.SkillID)]; !present {
			t.Fatalf("real .chr grant %d (level %d) is missing from the committed skill table: %v",
				grant.SkillID, grant.Level, committedIDs)
		}
	}
	if len(committedSkill.Layouts[dnfrepo.ActiveSkillTree(committedSkill)]) == 0 {
		t.Fatalf("class change left the active skill tree empty: layouts=%+v", committedSkill.Layouts)
	}

	// ---- published order: ACK -> mode0 identity -> op37 -> op19 -> op574 -> op356 -> op21 ----
	packets := splitAllFinishBridgePackets(t, connection.write.Bytes())
	round6RealChainAssertPublishedOrder(t, packets, committedCharacter, committedSkill, newGrow, characterName)

	// ---- ACK body: chainType 1 with the committed grow number and real grant rows ----
	ackBody := packets[0].Body
	if len(ackBody) < 11 || ackBody[0] != 1 ||
		binary.LittleEndian.Uint16(ackBody[1:3]) != uint16(round6RealChainQuestID) ||
		ackBody[9] != 1 || ackBody[10] != newGrow {
		t.Fatalf("profession ACK body=%x want success/quest=%d/chainType=1/grow=%d",
			ackBody, round6RealChainQuestID, newGrow)
	}
	treeGrants := round6RealChainAckGrants(t, ackBody)
	if len(treeGrants) != 2 || !reflect.DeepEqual(treeGrants[0], treeGrants[1]) {
		t.Fatalf("profession ACK grant trees=%v want both trees identical", treeGrants)
	}
	ackGrantIDs := make([]int, 0, len(treeGrants[0]))
	for _, grant := range treeGrants[0] {
		ackGrantIDs = append(ackGrantIDs, int(grant.SkillID))
		jobSkill, ok := skillCatalog.Find(round6RealChainJob, grant.SkillID)
		if !ok || strings.TrimSpace(jobSkill.Path) == "" {
			t.Fatalf("profession ACK grant %d is not a real job-%d skill: found=%t skill=%+v",
				grant.SkillID, round6RealChainJob, ok, jobSkill)
		}
	}
	if !reflect.DeepEqual(ackGrantIDs, committedIDs) {
		t.Fatalf("profession ACK grants=%v want the committed real skill table=%v", ackGrantIDs, committedIDs)
	}

	// ---- op19 skill body is the committed real skill table, first (active) tree ----
	skillIndex := round6RealChainIndex(packets, 0, currentSkillInfoMsgID)
	slots := currentSkillInfoFirstTreeSlots(t, packets[skillIndex].Body)
	published := make([]int, 0, len(slots))
	for _, skillID := range slots {
		published = append(published, int(skillID))
	}
	sort.Ints(published)
	for _, skillID := range published {
		if _, isCommitted := committedSkill.Skills[int64(skillID)]; !isCommitted {
			t.Fatalf("op19 published skill %d is not in the committed table: slots=%v committed=%v", skillID, slots, committedIDs)
		}
		jobSkill, ok := skillCatalog.Find(round6RealChainJob, uint16(skillID))
		if !ok || strings.TrimSpace(jobSkill.Path) == "" {
			t.Fatalf("op19 published skill %d has no real PVF source in the job-%d skill table: %+v", skillID, round6RealChainJob, jobSkill)
		}
	}
	for _, grant := range grants {
		foundGrant := false
		for _, skillID := range published {
			if skillID == int(grant.SkillID) {
				foundGrant = true
				break
			}
		}
		if !foundGrant {
			t.Fatalf("op19 first tree is missing the real .chr grant %d: slots=%v", grant.SkillID, slots)
		}
	}
	for _, skillID := range published {
		if skillID == int(foreign) {
			t.Fatalf("op19 still publishes the foreign-branch skill %d: slots=%v", foreign, slots)
		}
	}
}

// round6RealChainAssertPublishedOrder pins the current-EXE post-commit order and
// the committed identity payload for the real chain.
func round6RealChainAssertPublishedOrder(
	t *testing.T,
	packets []dnfproto.ChannelPacket,
	committedCharacter dnfrepo.CharacterRecord,
	committedSkill dnfrepo.SkillRecord,
	newGrow byte,
	characterName string,
) {
	t.Helper()
	type anchor struct {
		label       string
		class       byte
		msgID       uint16
		mode        int
		wantExactly int
	}
	ack := round6RealChainIndex(packets, dnfproto.DefaultChannelClassification, uint16(dnfenum.CmdPacketFinishQuest))
	if ack != 0 {
		t.Fatalf("profession finish ACK index=%d want 0 (packets=%v)", ack, finishBridgePacketIDs(packets))
	}
	if count := round6RealChainCount(packets, dnfproto.DefaultChannelClassification, uint16(dnfenum.CmdPacketFinishQuest)); count != 1 {
		t.Fatalf("profession finish ACK count=%d want 1 (packets=%v)", count, finishBridgePacketIDs(packets))
	}
	order := []anchor{
		{label: "op2 mode0 identity", class: 0, msgID: uint16(dnfenum.CmdPacketSetUDPIPPort), mode: 0, wantExactly: 1},
		{label: "op37 character state", class: 0, msgID: currentDungeonCharacterStateMsgID, mode: -1, wantExactly: 1},
		{label: "op19 skill info", class: 0, msgID: currentSkillInfoMsgID, mode: -1, wantExactly: 1},
		{label: "op574 active snapshot", class: 0, msgID: currentActiveQuestSnapshotMsgID, mode: -1, wantExactly: 1},
		{label: "op356 clear/complete list", class: 0, msgID: currentClearQuestListMsgID, mode: -1, wantExactly: 1},
		{label: "op21 acceptable list", class: 0, msgID: currentAcceptableQuestListMsgID, mode: -1, wantExactly: 1},
	}
	previous := ack
	for _, item := range order {
		index := -1
		for i, packet := range packets {
			if packet.Header.Classification != item.class || packet.Header.MsgID != item.msgID {
				continue
			}
			if item.mode >= 0 && (len(packet.Body) == 0 || int(packet.Body[0]) != item.mode) {
				continue
			}
			index = i
			break
		}
		if index < 0 {
			t.Fatalf("missing %s (%d/%d) after %d: packets=%v", item.label, item.class, item.msgID, previous, finishBridgePacketIDs(packets))
		}
		if count := round6RealChainCount(packets, item.class, item.msgID); count != item.wantExactly {
			t.Fatalf("%s count=%d want %d: packets=%v", item.label, count, item.wantExactly, finishBridgePacketIDs(packets))
		}
		if index <= previous {
			t.Fatalf("%s index=%d must follow index=%d: packets=%v", item.label, index, previous, finishBridgePacketIDs(packets))
		}
		previous = index
	}

	// Personal-info carriers stay forbidden on the profession path: mode3 opens
	// the panel and mode1 rebuilds the whole actor view.
	for _, packet := range packets {
		if packet.Header.Classification != 0 ||
			packet.Header.MsgID != uint16(dnfenum.CmdPacketSetUDPIPPort) ||
			len(packet.Body) == 0 {
			continue
		}
		if packet.Body[0] == 1 || packet.Body[0] == 3 {
			t.Fatalf("profession finish emitted forbidden personal-info mode=%d: packets=%v", packet.Body[0], finishBridgePacketIDs(packets))
		}
	}

	// Mode0 identity carries the committed grow_type and owner, and the buff-swap
	// reapply may follow it; neither may re-read or fabricate the profession.
	identity := packets[round6RealChainIndex(packets, 0, uint16(dnfenum.CmdPacketSetUDPIPPort))]
	if body := identity.Body; len(body) < actorGrowByteStart(characterName)+1 ||
		body[0] != 0 ||
		binary.LittleEndian.Uint16(body[5+0x47:5+0x47+2]) != 19 ||
		body[actorGrowByteStart(characterName)] != newGrow {
		t.Fatalf("profession identity body=%x want committed owner=19 grow=%d name=%q", identity.Body, newGrow, characterName)
	}

	// op37 is the committed numeric state, not a stale projection.
	characterState := packets[round6RealChainIndex(packets, 0, currentDungeonCharacterStateMsgID)]
	if len(characterState.Body) != currentFinishLoadingCharacterStateBodySize {
		t.Fatalf("op37 body len=%d want %d", len(characterState.Body), currentFinishLoadingCharacterStateBodySize)
	}

	// op574 must no longer list the finished quest.
	active := packets[round6RealChainIndex(packets, 0, currentActiveQuestSnapshotMsgID)]
	if len(active.Body) < 4 {
		t.Fatalf("op574 body too short: %x", active.Body)
	}
	rowCount := int(binary.LittleEndian.Uint32(active.Body[:4]))
	if len(active.Body) != 4+rowCount*6 {
		t.Fatalf("op574 body len=%d count=%d body=%x", len(active.Body), rowCount, active.Body)
	}
	for offset := 4; offset < len(active.Body); offset += 6 {
		if binary.LittleEndian.Uint16(active.Body[offset:offset+2]) == uint16(round6RealChainQuestID) {
			t.Fatalf("op574 still lists the completed quest %d: %x", round6RealChainQuestID, active.Body)
		}
	}

	// op356 rebuilds the completed lookup for exactly the committed quest.
	clearQuest := packets[round6RealChainIndex(packets, 0, currentClearQuestListMsgID)]
	assertFinishBridgeClearQuestProjection(t, clearQuest, uint16(round6RealChainQuestID))

	// op21 must not offer the completed quest again.
	acceptable := packets[round6RealChainIndex(packets, 0, currentAcceptableQuestListMsgID)]
	if len(acceptable.Body) < 4 || int(binary.LittleEndian.Uint32(acceptable.Body[:4])) != len(acceptable.Body)-4 {
		t.Fatalf("op21 protobuf length mismatch: %x", acceptable.Body)
	}
	_, messages := consumeCurrentSkillInfoFields(t, acceptable.Body[4:])
	if len(messages[4]) != 1 {
		t.Fatalf("op21 field4 rows=%d body=%x", len(messages[4]), acceptable.Body)
	}
	for _, questID := range consumePackedQuestIDs(t, messages[4][0]) {
		if questID == int32(round6RealChainQuestID) {
			t.Fatalf("op21 still offers the completed quest %d", round6RealChainQuestID)
		}
	}
}

func round6RealChainIndex(packets []dnfproto.ChannelPacket, class byte, msgID uint16) int {
	for i, packet := range packets {
		if packet.Header.Classification == class && packet.Header.MsgID == msgID {
			return i
		}
	}
	return -1
}

func round6RealChainCount(packets []dnfproto.ChannelPacket, class byte, msgID uint16) int {
	count := 0
	for _, packet := range packets {
		if packet.Header.Classification == class && packet.Header.MsgID == msgID {
			count++
		}
	}
	return count
}

func round6RealChainSkillIDs(record dnfrepo.SkillRecord) []int {
	out := make([]int, 0, len(record.Skills))
	for skillID, state := range record.Skills {
		if state.Level <= 0 {
			continue
		}
		out = append(out, int(skillID))
	}
	sort.Ints(out)
	return out
}

// round6RealChainAckGrants decodes the two inline grant trees of the profession
// finish ACK produced by buildCurrentFinishQuestSuccessBody.
func round6RealChainAckGrants(t *testing.T, body []byte) [][]dnfprofession.Grant {
	t.Helper()
	if len(body) < 8 {
		t.Fatalf("profession ACK body too short: %x", body)
	}
	// success u8 + questID u16 + completionType u8 + EXP u32 + consumedCount u8
	// + chainType u8 + growNumber u8, then the two inline grant trees.
	offset := 11
	if offset >= len(body) {
		t.Fatalf("profession ACK body=%x", body)
	}
	trees := make([][]dnfprofession.Grant, 0, currentSkillInfoTreeCount)
	for tree := 0; tree < currentSkillInfoTreeCount; tree++ {
		if offset >= len(body) {
			t.Fatalf("profession ACK tree=%d truncated: %x", tree, body)
		}
		count := int(body[offset])
		offset++
		if len(body) < offset+count*4 {
			t.Fatalf("profession ACK tree=%d count=%d truncated: %x", tree, count, body)
		}
		rows := make([]dnfprofession.Grant, 0, count)
		for row := 0; row < count; row++ {
			level := int(body[offset])
			skillID := binary.LittleEndian.Uint16(body[offset+1 : offset+3])
			learned := body[offset+3]
			if learned != 1 && learned != 0 {
				t.Fatalf("profession ACK tree=%d row=%d learned=%d body=%x", tree, row, learned, body)
			}
			rows = append(rows, dnfprofession.Grant{SkillID: skillID, Level: level})
			offset += 4
		}
		trees = append(trees, rows)
	}
	if offset != len(body) {
		t.Fatalf("profession ACK trailing bytes=%x", body[offset:])
	}
	return trees
}

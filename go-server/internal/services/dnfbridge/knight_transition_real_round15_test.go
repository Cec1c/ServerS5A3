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

// Round-15 offline closure for the remaining gap in 段68 (守护者 统御 : 魔灵战士).
//
// rest5 only proved the resource half: FreeGrants(12,2,*) carries 61@1, the
// class-change planner outputs 61@1, and skillcmd.Owner accepts 0->1 for a
// branch-2 Knight -- all against the domain layer plus an in-memory repository,
// never through the real bridge commit handler. The parent review on that
// report says exactly that: the "commit persists 61" sentence was a static
// chain inference, not an executed commit.
//
// This test closes that one step. It drives the real op34 finish handler
// (handleCurrentFinishQuest -> quest owner settlement -> profession
// ApplySkillTransition -> repository save) for the real Knight branch-2
// class-change quest, then re-reads the persisted character/skill/quest rows
// and the packets the handler published, asserting the real grant 61@1
// survives the whole assembly.
//
// It never invents a quest ID, opcode, grow number, grant row or skill row: the
// quest is located by its real PVF path, the target branch and grant set are
// re-derived from the loaded Script.pvf, and the expected committed skill table
// is the domain layer's own transition output.
const (
	knightRound15Job         = byte(12)   // 守护者/骑士
	knightRound15Branch      = byte(2)    // 混沌魔灵
	knightRound15Grant       = uint16(61) // 统御 : 魔灵战士
	knightRound15Level       = 90
	knightRound15CharacterNo = 19
	knightRound15CharacterID = "19"
	knightRound15QuestMarker = "k_chaos_knight_01"
)

// knightRound15Settlement counts entries into and commits of the character
// settlement unit of work, so "the commit ran exactly once" is an assertion
// rather than an assumption.
type knightRound15Settlement struct {
	inner     dnfrepo.CharacterSettlementUnitOfWork
	attempts  int
	committed int
}

func (s *knightRound15Settlement) WithinCharacterSettlement(ctx context.Context, characterID string, fn func(dnfrepo.Group) error) error {
	s.attempts++
	err := s.inner.WithinCharacterSettlement(ctx, characterID, fn)
	if err == nil {
		s.committed++
	}
	return err
}

func TestKnightTransitionRealRound15(t *testing.T) {
	pvfPath := strings.TrimSpace(os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE"))
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to run the real-PVF Knight class-change commit")
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

	// ---- real Knight branch-2 class-change quest, located by its real path ----
	definition, found := knightRound15FindQuest(t, catalog)
	questID := definition.ID
	if !found || questID <= 0 || questID > 0xffff {
		t.Fatalf("real Knight class-change quest id=%d found=%t cannot ride the op34 u16 body (path=%q)", questID, found, definition.Path)
	}
	if !currentQuestRewardIsProfession(definition.RewardType) {
		t.Fatalf("real quest %d reward type=%q path=%q is not a grow/awakening reward", questID, definition.RewardType, definition.Path)
	}
	eligibility := dnfquest.CharacterEligibility{Level: knightRound15Level, Job: int(knightRound15Job), GrowType: 0}
	plan, err := catalog.PlanFinishReward(eligibility, questID, ^uint16(0), false)
	if err != nil {
		t.Fatalf("real quest %d finish plan: %v", questID, err)
	}
	if !plan.HasProfession || plan.ProfessionRequest.ChainType != 1 || plan.ProfessionRequest.GrowNumber != knightRound15Branch {
		t.Fatalf("real quest %d plan=%+v want a class change to branch %d", questID, plan, knightRound15Branch)
	}
	transition, err := profiles.PlanTransition(knightRound15Job, 0, plan.ProfessionRequest)
	if err != nil {
		t.Fatalf("real class change job=%d branch=%d: %v", knightRound15Job, knightRound15Branch, err)
	}
	grants, ok := profiles.FreeGrants(knightRound15Job, knightRound15Branch, 0)
	if !ok || len(grants) == 0 {
		t.Fatalf("real Knight.chr has no free-grant block for branch %d", knightRound15Branch)
	}
	grantLevels := make(map[uint16]int, len(grants))
	for _, grant := range grants {
		grantLevels[grant.SkillID] = grant.Level
	}
	if level := grantLevels[knightRound15Grant]; level != 1 {
		t.Fatalf("real Knight branch %d free grants carry %d@%d, want %d@1 (grants=%v)",
			knightRound15Branch, knightRound15Grant, level, knightRound15Grant, grants)
	}
	t.Logf("real facts: quest=%d path=%q name=%q chainType=%d branch=%d grants=%v",
		questID, definition.Path, definition.Name, plan.ProfessionRequest.ChainType, plan.ProfessionRequest.GrowNumber, grants)

	// A real foreign-branch skill the class change must drop, taken from the
	// same real Knight.chr grant blocks rather than a fabricated row.
	foreign := uint16(0)
	for branch := byte(0); branch <= 5 && foreign == 0; branch++ {
		if branch == knightRound15Branch {
			continue
		}
		other, ok := profiles.FreeGrants(knightRound15Job, branch, 0)
		if !ok {
			continue
		}
		for _, grant := range other {
			if _, isTarget := grantLevels[grant.SkillID]; isTarget {
				continue
			}
			if _, resolves := skillCatalog.Find(knightRound15Job, grant.SkillID); resolves {
				foreign = grant.SkillID
				break
			}
		}
	}
	if foreign == 0 {
		t.Fatalf("real Knight.chr has no foreign-branch skill to prove the class-change replacement")
	}

	// ---- seed: one Knight character, one active real quest row, one real foreign skill ----
	repositories := dnfrepomemory.NewMemoryGroup()
	settlement := &knightRound15Settlement{inner: repositories.CharacterSettlement}
	repositories.CharacterSettlement = settlement
	characterName := "round15knight"
	acceptedAt := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: knightRound15CharacterID, AccountID: defaultAccountPrefix + "1", Name: characterName,
		Job: strconv.Itoa(int(knightRound15Job)), Level: knightRound15Level,
		Stats: map[string]int64{"exp": 0, "grow_type": 0},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Quest.Save(ctx, dnfrepo.QuestRecord{
		CharacterID: knightRound15CharacterID,
		States: map[int64]dnfrepo.QuestState{
			questID: {Status: "active", ProgressValue: 0, UpdatedAt: acceptedAt},
		},
		UpdatedAt: acceptedAt,
	}); err != nil {
		t.Fatal(err)
	}
	seedPoints := dnfrepo.SkillPointState{TotalSP: 11970, RemainingSP: 11970, TotalTP: 41, RemainingTP: 41, SyncedLevel: knightRound15Level}
	seedSkill := dnfrepo.SkillRecord{
		CharacterID: knightRound15CharacterID,
		Skills:      map[int64]dnfrepo.SkillState{int64(foreign): {Level: 3, Enabled: true}},
		Layouts:     map[int]dnfrepo.SkillLayout{0: {0: foreign}},
		Points:      seedPoints,
	}
	if err := repositories.Skill.Save(ctx, seedSkill); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: knightRound15CharacterID, Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Equipment.Save(ctx, dnfrepo.EquipmentRecord{CharacterID: knightRound15CharacterID}); err != nil {
		t.Fatal(err)
	}

	// ---- the real op34 entry; the handler loads its own real resources ----
	connection := &bufferConn{}
	service := &Service{
		options: options{
			pvfPath:            pvfPath,
			accountPrefix:      defaultAccountPrefix,
			gameUpperBodyCodec: gameUpperBodyCodecPlain,
		},
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
	session := &gameSession{conn: connection, connID: "round15-knight-transition", selectedCharacterID: knightRound15CharacterNo}
	requestBody := make([]byte, dnfquest.CurrentFinishQuestRequestBodySize)
	binary.LittleEndian.PutUint16(requestBody[0:2], uint16(dnfenum.CmdPacketFinishQuest))
	binary.LittleEndian.PutUint16(requestBody[2:4], uint16(questID))
	binary.LittleEndian.PutUint16(requestBody[4:6], ^uint16(0))
	binary.LittleEndian.PutUint16(requestBody[6:8], 1)
	binary.LittleEndian.PutUint16(requestBody[8:10], dnfquest.CurrentFinishQuestObservedTailMarker)
	if err := service.handleCurrentFinishQuest(session, requestBody); err != nil {
		t.Fatalf("real op34 Knight class-change finish: %v", err)
	}

	// ---- exactly one successful transaction ----
	if settlement.attempts != 1 || settlement.committed != 1 {
		t.Fatalf("settlement attempts=%d committed=%d want exactly one committed transaction", settlement.attempts, settlement.committed)
	}
	committedCharacter, found, err := repositories.Character.Load(ctx, knightRound15CharacterID)
	if err != nil || !found {
		t.Fatalf("reload character found=%t err=%v", found, err)
	}
	committedSkill, found, err := repositories.Skill.Load(ctx, knightRound15CharacterID)
	if err != nil || !found {
		t.Fatalf("reload skill found=%t err=%v", found, err)
	}
	committedQuest, found, err := repositories.Quest.Load(ctx, knightRound15CharacterID)
	if err != nil || !found {
		t.Fatalf("reload quest found=%t err=%v", found, err)
	}
	if got := committedCharacter.Stats["grow_type"]; got != int64(knightRound15Branch) {
		t.Fatalf("committed grow_type=%d want %d (quest %d)", got, knightRound15Branch, questID)
	}
	if state := committedQuest.States[questID]; state.Status != "completed" || state.Extra["reward_state"] != "granted" {
		t.Fatalf("committed quest state=%+v want completed/granted", state)
	}

	// ---- the real grant 61@1 must survive the handler assembly ----
	grantState, present := committedSkill.Skills[int64(knightRound15Grant)]
	if !present || grantState.Level < 1 {
		t.Fatalf("HANDLER LOST THE GRANT: committed skills=%v do not carry real Knight branch-%d grant %d@1",
			round6RealChainSkillIDs(committedSkill), knightRound15Branch, knightRound15Grant)
	}
	if grantState.Level != 1 {
		t.Fatalf("committed grant %d level=%d want 1 (real Knight.chr branch %d)", knightRound15Grant, grantState.Level, knightRound15Branch)
	}
	expectedSkill, err := profiles.ApplySkillTransition(skillCatalog, knightRound15Job, committedCharacter.Level, transition, seedSkill, seedPoints)
	if err != nil {
		t.Fatalf("real ApplySkillTransition for the committed state: %v", err)
	}
	committedIDs := round6RealChainSkillIDs(committedSkill)
	if expectedIDs := round6RealChainSkillIDs(expectedSkill); !reflect.DeepEqual(committedIDs, expectedIDs) {
		t.Fatalf("committed skills=%v want the real PVF transition output=%v", committedIDs, expectedIDs)
	}
	if _, survived := committedSkill.Skills[int64(foreign)]; survived {
		t.Fatalf("class change kept the foreign-branch skill %d: %v", foreign, committedIDs)
	}
	if len(committedSkill.Layouts[dnfrepo.ActiveSkillTree(committedSkill)]) == 0 {
		t.Fatalf("class change left the active skill tree empty: layouts=%+v", committedSkill.Layouts)
	}
	t.Logf("committed facts: grow_type=%d skills=%v points=%+v", committedCharacter.Stats["grow_type"], committedIDs, committedSkill.Points)

	// ---- published order + carriers ----
	packets := splitAllFinishBridgePackets(t, connection.write.Bytes())
	knightRound15AssertPublished(t, packets, questID, characterName)
}

// knightRound15FindQuest locates the real Knight branch-2 class-change quest by
// its PVF path fragment. No ID is written down anywhere.
func knightRound15FindQuest(t *testing.T, catalog *dnfquest.Catalog) (dnfquest.Definition, bool) {
	t.Helper()
	for _, candidate := range catalog.Search(knightRound15QuestMarker, 10) {
		if strings.Contains(strings.ToLower(candidate.Path), knightRound15QuestMarker) {
			return candidate, true
		}
	}
	return dnfquest.Definition{}, false
}

// knightRound15AssertPublished pins the current-EXE post-commit order and the
// committed carriers for this one quest, reusing the round-6 packet decoders.
func knightRound15AssertPublished(t *testing.T, packets []dnfproto.ChannelPacket, questID int64, characterName string) {
	t.Helper()
	ackIndex := round6RealChainIndex(packets, dnfproto.DefaultChannelClassification, uint16(dnfenum.CmdPacketFinishQuest))
	if ackIndex != 0 || round6RealChainCount(packets, dnfproto.DefaultChannelClassification, uint16(dnfenum.CmdPacketFinishQuest)) != 1 {
		t.Fatalf("class-change ACK index=%d count=%d want 0/1 (packets=%v)",
			ackIndex, round6RealChainCount(packets, dnfproto.DefaultChannelClassification, uint16(dnfenum.CmdPacketFinishQuest)), finishBridgePacketIDs(packets))
	}
	order := []struct {
		label string
		class byte
		msgID uint16
		mode  int
	}{
		{"op2 mode0 identity", 0, uint16(dnfenum.CmdPacketSetUDPIPPort), 0},
		{"op37 character state", 0, currentDungeonCharacterStateMsgID, -1},
		{"op19 skill info", 0, currentSkillInfoMsgID, -1},
		{"op574 active snapshot", 0, currentActiveQuestSnapshotMsgID, -1},
		{"op356 clear/complete list", 0, currentClearQuestListMsgID, -1},
		{"op21 acceptable list", 0, currentAcceptableQuestListMsgID, -1},
	}
	previous := ackIndex
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
			t.Fatalf("missing %s (%d/%d) after index %d: packets=%v", item.label, item.class, item.msgID, previous, finishBridgePacketIDs(packets))
		}
		if index <= previous {
			t.Fatalf("%s index=%d must follow index=%d: packets=%v", item.label, index, previous, finishBridgePacketIDs(packets))
		}
		previous = index
	}

	// ACK body: success, chainType 1, committed branch, and both inline grant
	// trees must carry the real 61@1 grant.
	ackBody := packets[ackIndex].Body
	if len(ackBody) < 11 || ackBody[0] != 1 || binary.LittleEndian.Uint16(ackBody[1:3]) != uint16(questID) ||
		ackBody[9] != 1 || ackBody[10] != knightRound15Branch {
		t.Fatalf("class-change ACK body=%x want success/quest=%d/chainType=1/branch=%d", ackBody, questID, knightRound15Branch)
	}
	trees := round6RealChainAckGrants(t, ackBody)
	if len(trees) != currentSkillInfoTreeCount {
		t.Fatalf("class-change ACK grant trees=%d want %d", len(trees), currentSkillInfoTreeCount)
	}
	for treeIndex, tree := range trees {
		level := 0
		for _, grant := range tree {
			if grant.SkillID == knightRound15Grant {
				level = grant.Level
			}
		}
		if level != 1 {
			t.Fatalf("class-change ACK tree=%d grant %d level=%d want 1 (tree=%v)", treeIndex, knightRound15Grant, level, tree)
		}
	}

	// op2 mode0 identity carries the committed owner and branch.
	identity := packets[round6RealChainIndex(packets, 0, uint16(dnfenum.CmdPacketSetUDPIPPort))]
	if body := identity.Body; len(body) < actorGrowByteStart(characterName)+1 ||
		body[0] != 0 ||
		binary.LittleEndian.Uint16(body[5+0x47:5+0x47+2]) != 19 ||
		body[actorGrowByteStart(characterName)] != knightRound15Branch {
		t.Fatalf("class-change identity body=%x want committed owner=19 branch=%d name=%q", identity.Body, knightRound15Branch, characterName)
	}

	// op37 is the committed numeric state.
	characterState := packets[round6RealChainIndex(packets, 0, currentDungeonCharacterStateMsgID)]
	if len(characterState.Body) != currentFinishLoadingCharacterStateBodySize {
		t.Fatalf("op37 body len=%d want %d", len(characterState.Body), currentFinishLoadingCharacterStateBodySize)
	}

	// op19 first tree must publish the real grant 61 in a committed slot.
	skillIndex := round6RealChainIndex(packets, 0, currentSkillInfoMsgID)
	slots := currentSkillInfoFirstTreeSlots(t, packets[skillIndex].Body)
	published := make([]int, 0, len(slots))
	found61 := false
	for _, skillID := range slots {
		published = append(published, int(skillID))
		if skillID == knightRound15Grant {
			found61 = true
		}
	}
	sort.Ints(published)
	if !found61 {
		t.Fatalf("op19 first tree is missing the real grant %d: slots=%v published=%v", knightRound15Grant, slots, published)
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
		if binary.LittleEndian.Uint16(active.Body[offset:offset+2]) == uint16(questID) {
			t.Fatalf("op574 still lists the completed quest %d: %x", questID, active.Body)
		}
	}

	// op356 rebuilds the completed lookup for exactly this quest.
	clearQuest := packets[round6RealChainIndex(packets, 0, currentClearQuestListMsgID)]
	assertFinishBridgeClearQuestProjection(t, clearQuest, uint16(questID))

	// op21 must not offer the completed quest again.
	acceptable := packets[round6RealChainIndex(packets, 0, currentAcceptableQuestListMsgID)]
	if len(acceptable.Body) < 4 || int(binary.LittleEndian.Uint32(acceptable.Body[:4])) != len(acceptable.Body)-4 {
		t.Fatalf("op21 protobuf length mismatch: %x", acceptable.Body)
	}
	_, messages := consumeCurrentSkillInfoFields(t, acceptable.Body[4:])
	if len(messages[4]) != 1 {
		t.Fatalf("op21 field4 rows=%d body=%x", len(messages[4]), acceptable.Body)
	}
	for _, candidate := range consumePackedQuestIDs(t, messages[4][0]) {
		if candidate == int32(questID) {
			t.Fatalf("op21 still offers the completed quest %d", questID)
		}
	}
}

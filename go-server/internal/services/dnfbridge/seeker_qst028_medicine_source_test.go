package dnfbridge

import (
	"context"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/progression"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

const (
	qst028MedicineAccountID   = defaultAccountPrefix + "1"
	qst028MedicineCharacterID = "19"
	qst028MedicineSourceID    = uint16(3346)
	qst028MedicineParentID    = uint16(3347)
	qst028MedicineMeetID      = uint16(3425)
	qst028MedicineDeliveryID  = uint16(3426)
	qst028MedicineItemID      = int64(10164764)
)

func TestQST028MedicineSourceRegularFixture(t *testing.T) {
	ctx := context.Background()
	catalog := qst028MedicineFixtureCatalog(t)
	items := qst028MedicineFixtureItemCatalog(t)
	progress := qst028MedicineProgression(t)

	t.Run("normal-source-finish-grants-medicine", func(t *testing.T) {
		repositories := qst028MedicineRepositories(t, ctx)
		qst028MedicineSeedMaterials(t, ctx, repositories, items)
		conn, service, session := qst028MedicineBridge(t, repositories, catalog, progress, items)

		if err := service.handleCurrentAcceptQuest(session, qst019BoxAcceptBody(qst028MedicineSourceID)); err != nil {
			t.Fatal(err)
		}
		boxCallTrigger(t, service, session, conn, qst028MedicineSourceID, 0)
		sourcePackets := boxCallFinish(t, service, session, conn, qst028MedicineSourceID)
		qst006AssertFinishSuccess(t, sourcePackets, qst028MedicineSourceID)
		if got := qst028MedicineCount(t, ctx, repositories); got != 1 {
			t.Fatalf("normal source medicine count=%d, want 1", got)
		}
		if got := qst028MedicineMaterialCount(t, ctx, repositories, 3033); got != 0 {
			t.Fatalf("black cube count=%d, want 0", got)
		}
		if got := qst028MedicineMaterialCount(t, ctx, repositories, 3166); got != 0 {
			t.Fatalf("low elemental crystal count=%d, want 0", got)
		}

		conn.write.Reset()
		if err := service.handleCurrentAcceptQuest(session, qst019BoxAcceptBody(qst028MedicineParentID)); err != nil {
			t.Fatal(err)
		}
		packets := finishBridgePacketIDs(splitAllFinishBridgePackets(t, conn.write.Bytes()))
		if !reflect.DeepEqual(packets, []string{"1/31", "0/574"}) {
			t.Fatalf("normal-source accept packets=%v", packets)
		}
		if qst028MedicineContainsPacketID(packets, "0/14") {
			t.Fatalf("normal source unexpectedly recovered medicine: %v", packets)
		}
		qst028MedicineFinishDeliveryAndParent(t, service, session, conn, repositories, ctx)
		qst028MedicineAssertSettled(t, ctx, repositories)
	})

	t.Run("skipped-source-accept-recovers-before-snapshot", func(t *testing.T) {
		repositories := qst028MedicineRepositories(t, ctx)
		qst028MedicineSeedSkippedSource(t, ctx, repositories, "epic_below_character_level_no_reward")
		conn, service, session := qst028MedicineBridge(t, repositories, catalog, progress, items)

		conn.write.Reset()
		if err := service.handleCurrentAcceptQuest(session, qst019BoxAcceptBody(qst028MedicineParentID)); err != nil {
			t.Fatal(err)
		}
		if got := finishBridgePacketIDs(splitAllFinishBridgePackets(t, conn.write.Bytes())); !reflect.DeepEqual(got, []string{"1/31", "0/14", "0/574"}) {
			t.Fatalf("skipped-source accept packets=%v", got)
		}
		if got := qst028MedicineCount(t, ctx, repositories); got != 1 {
			t.Fatalf("accept recovery medicine count=%d, want 1", got)
		}
		qst028MedicineAssertReceipt(t, ctx, repositories)
		qst028MedicineFinishDeliveryAndParent(t, service, session, conn, repositories, ctx)
		qst028MedicineAssertSettled(t, ctx, repositories)
	})

	t.Run("old-active-relogin-recovers-once", func(t *testing.T) {
		repositories := qst028MedicineRepositories(t, ctx)
		qst028MedicineSeedSkippedSource(t, ctx, repositories, "main_story_level_clear_no_reward")
		record, found, err := repositories.Quest.Load(ctx, qst028MedicineCharacterID)
		if err != nil || !found {
			t.Fatalf("load quest found=%t err=%v", found, err)
		}
		record.States[int64(qst028MedicineParentID)] = dnfrepo.QuestState{Status: "active", ProgressValue: 2}
		record.States[int64(qst028MedicineMeetID)] = dnfrepo.QuestState{Status: "completed"}
		record.States[int64(qst028MedicineDeliveryID)] = dnfrepo.QuestState{Status: "active", ProgressValue: 1}
		if err := repositories.Quest.Save(ctx, record); err != nil {
			t.Fatal(err)
		}
		conn, service, _ := qst028MedicineBridge(t, repositories, catalog, progress, items)
		session := &gameSession{
			conn: conn, connID: "qst028-old-active-relogin",
			selectedCharacterID: 19, channel: channelcatalog.Channel{ID: 19},
		}
		if err := service.sendCurrentActiveQuestSnapshotForSession(session, "qst028-old-active-relogin"); err != nil {
			t.Fatal(err)
		}
		if got := finishBridgePacketIDs(splitAllFinishBridgePackets(t, conn.write.Bytes())); !reflect.DeepEqual(got, []string{"0/14", "0/574"}) {
			t.Fatalf("old-active login packets=%v", got)
		}
		if got := qst028MedicineCount(t, ctx, repositories); got != 1 {
			t.Fatalf("old-active login medicine count=%d, want 1", got)
		}
		qst028MedicineAssertReceipt(t, ctx, repositories)

		conn.write.Reset()
		if err := service.sendCurrentActiveQuestSnapshotForSession(session, "qst028-old-active-relogin-repeat"); err != nil {
			t.Fatal(err)
		}
		if got := finishBridgePacketIDs(splitAllFinishBridgePackets(t, conn.write.Bytes())); !reflect.DeepEqual(got, []string{"0/574"}) {
			t.Fatalf("repeated old-active login packets=%v", got)
		}
		if got := qst028MedicineCount(t, ctx, repositories); got != 1 {
			t.Fatalf("repeated old-active login medicine count=%d, want 1", got)
		}
		qst028MedicineFinishDeliveryAndParent(t, service, session, conn, repositories, ctx)
		qst028MedicineAssertSettled(t, ctx, repositories)

		reconnect := &gameSession{
			conn: &bufferConn{}, connID: "qst028-after-finish-relogin",
			selectedCharacterID: 19, channel: channelcatalog.Channel{ID: 19},
		}
		if err := service.sendCurrentActiveQuestSnapshotForSession(reconnect, "qst028-after-finish-relogin"); err != nil {
			t.Fatal(err)
		}
		if got := qst028MedicineCount(t, ctx, repositories); got != 0 {
			t.Fatalf("after-finish relogin medicine count=%d, want 0", got)
		}
		qst028MedicineAssertReceipt(t, ctx, repositories)
	})
}

func TestSeekerQST028MedicineSourceRealPVFRecovery(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the matching Script.pvf")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatal(err)
	}
	if archive.Snapshot().Checksum != "9dcd31a3e6001fd5563d36dc65cbaa96bed8b782700cba30625197e27cf1e43c" {
		t.Fatal("unexpected PVF checksum")
	}
	ctx := context.Background()
	index, err := dnfpvf.Build(ctx, archive, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := dnfquest.Load(ctx, index)
	if err != nil {
		t.Fatal(err)
	}
	tables, err := progression.Load(ctx, archive)
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}

	repositories := qst028MedicineRepositories(t, ctx)
	character, found, err := repositories.Character.Load(ctx, qst028MedicineCharacterID)
	if err != nil || !found {
		t.Fatalf("load character found=%t err=%v", found, err)
	}
	character.Level = 49
	if err := repositories.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	skill, found, err := repositories.Skill.Load(ctx, qst028MedicineCharacterID)
	if err != nil || !found {
		t.Fatalf("load skill found=%t err=%v", found, err)
	}
	skill.Points.SyncedLevel = 49
	if err := repositories.Skill.Save(ctx, skill); err != nil {
		t.Fatal(err)
	}
	record, found, err := repositories.Quest.Load(ctx, qst028MedicineCharacterID)
	if err != nil || !found {
		t.Fatalf("load quest found=%t err=%v", found, err)
	}
	record.States[int64(qst028MedicineSourceID)] = dnfrepo.QuestState{Status: "active", ProgressValue: 1}
	plan, err := catalog.PlanAutoCompleteMainQuests(
		record,
		dnfquest.CharacterEligibility{Level: 49, Job: 2},
		48,
		int64(qst028MedicineSourceID),
		dnfquest.CouponClearScopeMainStorySweep,
		time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	if state := plan.Record.States[int64(qst028MedicineSourceID)]; state.Status != "completed" ||
		state.Extra["auto_complete_reason"] != "epic_below_character_level_no_reward" {
		t.Fatalf("auto-complete source state=%+v", state)
	}
	if err := repositories.Quest.Save(ctx, plan.Record); err != nil {
		t.Fatal(err)
	}

	conn, service, session := qst028MedicineBridge(t, repositories, catalog, tables, items)
	if err := service.handleCurrentAcceptQuest(session, qst019BoxAcceptBody(qst028MedicineParentID)); err != nil {
		t.Fatal(err)
	}
	if got := finishBridgePacketIDs(splitAllFinishBridgePackets(t, conn.write.Bytes())); !reflect.DeepEqual(got, []string{"1/31", "0/14", "0/574"}) {
		t.Fatalf("real PVF accept recovery packets=%v", got)
	}
	qst028MedicineFinishDeliveryAndParent(t, service, session, conn, repositories, ctx)
	qst028MedicineAssertSettled(t, ctx, repositories)
}

func qst028MedicineFinishDeliveryAndParent(
	t *testing.T,
	service *Service,
	session *gameSession,
	conn *bufferConn,
	repositories dnfrepo.Group,
	ctx context.Context,
) {
	t.Helper()
	qst028MedicineSetState(t, ctx, repositories, qst028MedicineMeetID,
		dnfrepo.QuestState{Status: "completed", Extra: map[string]string{"reward_state": "granted"}})
	boxCallTrigger(t, service, session, conn, qst028MedicineDeliveryID, 0)
	packets := boxCallFinish(t, service, session, conn, qst028MedicineDeliveryID)
	qst006AssertFinishSuccess(t, packets, qst028MedicineDeliveryID)
	if got := qst028MedicineCount(t, ctx, repositories); got != 0 {
		t.Fatalf("delivery left %d medicines, want 0", got)
	}
	record, found, err := repositories.Quest.Load(ctx, qst028MedicineCharacterID)
	if err != nil || !found {
		t.Fatalf("load delivery receipt found=%t err=%v", found, err)
	}
	receipt := qst006ReadReceipt(t, record.States[int64(qst028MedicineDeliveryID)])
	if len(receipt.ConsumedItems) != 1 {
		t.Fatalf("medicine delivery consumed rows=%+v", receipt.ConsumedItems)
	}
	consumed := receipt.ConsumedItems[0]
	if consumed.ItemID != qst028MedicineItemID || consumed.Delta != 1 || consumed.PostCount != 0 {
		t.Fatalf("medicine delivery consumed row=%+v", consumed)
	}
	conn.write.Reset()
	if duplicate := boxCallFinish(t, service, session, conn, qst028MedicineDeliveryID); len(duplicate) != 0 {
		t.Fatalf("duplicate medicine delivery packets=%v", finishBridgePacketIDs(duplicate))
	}
	qst006AssertFinishSuccess(t,
		boxCallFinish(t, service, session, conn, qst028MedicineParentID), qst028MedicineParentID)
}

func qst028MedicineAssertSettled(t *testing.T, ctx context.Context, repositories dnfrepo.Group) {
	t.Helper()
	qst006AssertState(t, ctx, repositories, map[int64]dnfrepo.QuestState{
		int64(qst028MedicineParentID):   {Status: "completed"},
		int64(qst028MedicineMeetID):     {Status: "completed"},
		int64(qst028MedicineDeliveryID): {Status: "completed"},
	})
}

func qst028MedicineRepositories(t *testing.T, ctx context.Context) dnfrepo.Group {
	t.Helper()
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: qst028MedicineCharacterID,
		AccountID:   qst028MedicineAccountID,
		Job:         "2",
		Level:       49,
		Stats:       map[string]int64{"grow_type": 0, "exp": 0, "fatigue": 156},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Quest.Save(ctx, dnfrepo.QuestRecord{
		CharacterID: qst028MedicineCharacterID,
		States: map[int64]dnfrepo.QuestState{
			3345: {Status: "completed"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Skill.Save(ctx, dnfrepo.SkillRecord{
		CharacterID: qst028MedicineCharacterID,
		Points: dnfrepo.SkillPointState{
			SyncedLevel: 49, TotalSP: 100, RemainingSP: 100, TotalTP: 20, RemainingTP: 20,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: qst028MedicineCharacterID,
		Slots:       map[string]dnfrepo.ItemStack{},
	}); err != nil {
		t.Fatal(err)
	}
	return repositories
}

func qst028MedicineSeedMaterials(
	t *testing.T,
	ctx context.Context,
	repositories dnfrepo.Group,
	items *pvfDungeonDropCatalog,
) {
	t.Helper()
	inventory, found, err := repositories.Inventory.Load(ctx, qst028MedicineCharacterID)
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	allocate := currentQuestFinishItemAllocator(items)
	for _, material := range []struct{ id, count int64 }{{3033, 30}, {3166, 10}} {
		if _, err := allocate(&inventory, dnfquest.FinishItemGrantRequest{
			ItemID: material.id, Count: material.count,
			QuestID: 3346, Source: "qst028-material-fixture",
			CompletionKey: "qst028-material-fixture",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := repositories.Inventory.Save(ctx, inventory); err != nil {
		t.Fatal(err)
	}
}

func qst028MedicineSeedSkippedSource(
	t *testing.T,
	ctx context.Context,
	repositories dnfrepo.Group,
	reason string,
) {
	t.Helper()
	record, found, err := repositories.Quest.Load(ctx, qst028MedicineCharacterID)
	if err != nil || !found {
		t.Fatalf("load quest found=%t err=%v", found, err)
	}
	record.States[int64(qst028MedicineSourceID)] = dnfrepo.QuestState{
		Status: "completed",
		Extra: map[string]string{
			"reward_state":         "granted",
			"auto_completed":       "true",
			"auto_complete_reason": reason,
		},
	}
	if err := repositories.Quest.Save(ctx, record); err != nil {
		t.Fatal(err)
	}
	if got := qst028MedicineCount(t, ctx, repositories); got != 0 {
		t.Fatalf("skipped source fixture medicine count=%d", got)
	}
}

func qst028MedicineSetState(
	t *testing.T,
	ctx context.Context,
	repositories dnfrepo.Group,
	questID uint16,
	state dnfrepo.QuestState,
) {
	t.Helper()
	record, found, err := repositories.Quest.Load(ctx, qst028MedicineCharacterID)
	if err != nil || !found {
		t.Fatalf("load quest found=%t err=%v", found, err)
	}
	record.States[int64(questID)] = state
	if err := repositories.Quest.Save(ctx, record); err != nil {
		t.Fatal(err)
	}
}

func qst028MedicineAssertReceipt(t *testing.T, ctx context.Context, repositories dnfrepo.Group) {
	t.Helper()
	record, found, err := repositories.Quest.Load(ctx, qst028MedicineCharacterID)
	if err != nil || !found {
		t.Fatalf("load quest found=%t err=%v", found, err)
	}
	if record.States[int64(qst028MedicineSourceID)].Extra[dnfquest.MedicineSourceRecoveryReceiptKey] == "" {
		t.Fatalf("medicine source recovery receipt missing: %+v", record.States[int64(qst028MedicineSourceID)])
	}
}

func qst028MedicineBridge(
	t *testing.T,
	repositories dnfrepo.Group,
	catalog *dnfquest.Catalog,
	progress *progression.Tables,
	items *pvfDungeonDropCatalog,
) (*bufferConn, *Service, *gameSession) {
	t.Helper()
	conn := &bufferConn{}
	service := &Service{
		options: options{
			accountID:            qst028MedicineAccountID,
			gameUpperBodyCodec:   gameUpperBodyCodecPlain,
			currentClientVersion: "official-24.9",
		},
		questCatalog:       catalog,
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
		questFinishResourceLoader: func(context.Context) (*progression.Tables, *pvfDungeonDropCatalog, error) {
			return progress, items, nil
		},
	}
	session := &gameSession{
		conn: conn, connID: "qst028-medicine-regular", selectedCharacterID: 19,
		channel: channelcatalog.Channel{ID: 19},
	}
	session.townSceneReadyCharacterID = session.selectedCharacterID
	return conn, service, session
}

func qst028MedicineCount(t *testing.T, ctx context.Context, repositories dnfrepo.Group) int64 {
	t.Helper()
	return qst028MedicineMaterialCount(t, ctx, repositories, qst028MedicineItemID)
}

func qst028MedicineMaterialCount(
	t *testing.T,
	ctx context.Context,
	repositories dnfrepo.Group,
	itemID int64,
) int64 {
	t.Helper()
	inventory, found, err := repositories.Inventory.Load(ctx, qst028MedicineCharacterID)
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	var count int64
	for _, stack := range inventory.Slots {
		if stack.ItemID == itemID {
			count += stack.Count
		}
	}
	return count
}

func qst028MedicineContainsPacketID(packets []string, want string) bool {
	for _, packet := range packets {
		if packet == want {
			return true
		}
	}
	return false
}

func qst028MedicineProgression(t *testing.T) *progression.Tables {
	t.Helper()
	var thresholds strings.Builder
	for level := 0; level < 100; level++ {
		base := (level*3 + 1) * 100
		thresholds.WriteString(strconv.Itoa(base))
		thresholds.WriteByte(' ')
		thresholds.WriteString(strconv.Itoa(base + 100))
		thresholds.WriteByte(' ')
		thresholds.WriteString(strconv.Itoa(base + 200))
		thresholds.WriteByte('\n')
	}
	var points strings.Builder
	points.WriteString("[sp table]\n")
	for level := 1; level <= 100; level++ {
		points.WriteString(strconv.Itoa(level))
		points.WriteString(" 30\n")
	}
	points.WriteString("[/sp table]\n[tp table]\n50 1\n[/tp table]\n")
	var questExperience strings.Builder
	for level := 0; level < 100; level++ {
		questExperience.WriteString("100 -1\n")
	}
	tables, err := progression.Load(context.Background(), finishBridgePVFSource{
		progression.ExperienceTablePath: thresholds.String(),
		progression.SkillPointTablePath: points.String(),
		progression.QuestParameterPath: "[difficulty]\n`G` 100\n`N` 100\n[/difficulty]\n[exp reward table]\n" +
			questExperience.String() + "[gold reward table]\n10\n[green level penalty]\n80\n[grey level penalty]\n30\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	return tables
}

func qst028MedicineFixtureCatalog(t *testing.T) *dnfquest.Catalog {
	t.Helper()
	source := finishBridgePVFSource{
		dnfquest.DefaultList:            "3346 `medicine_source.qst`\n3347 `medicine_parent.qst`\n3425 `medicine_meet.qst`\n3426 `medicine_delivery.qst`\n",
		"n_quest/medicine_source.qst":   "[grade]\n`[epic]`\n[type]\n`[seeking]`\n[difficulty]\n`G`\n[reward type]\n`[item]`\n[reward int data]\n10164764 1\n[level]\n48 99\n[job]\n`[all]`\n[pre required quest]\n3345\n[int data]\n3033 30 3166 10\n",
		"n_quest/medicine_parent.qst":   "[grade]\n`[epic]`\n[type]\n`[quest clear]`\n[difficulty]\n`G`\n[attribute]\n`not give exp quest`\n[/attribute]\n[reward type]\n`[item]`\n[reward int data]\n0 0\n[level]\n48 99\n[job]\n`[all]`\n[pre required quest]\n3346\n[int data]\n3425 3426\n",
		"n_quest/medicine_meet.qst":     "[grade]\n`[sub]`\n[type]\n`[hunt enemy]`\n[difficulty]\n`G`\n[attribute]\n`not give exp quest`\n[/attribute]\n[reward type]\n`[item]`\n[reward int data]\n0 0\n[level]\n48 99\n[job]\n`[all]`\n[main quest]\n3347\n[int data]\n61 -1 109001851 3 1\n",
		"n_quest/medicine_delivery.qst": "[grade]\n`[sub]`\n[type]\n`[seeking]`\n[difficulty]\n`G`\n[attribute]\n`not give exp quest`\n[/attribute]\n[reward type]\n`[item]`\n[reward int data]\n0 0\n[level]\n48 99\n[job]\n`[all]`\n[main quest]\n3347\n[int data]\n10164764 1\n",
	}
	index, err := dnfpvf.Build(context.Background(), source, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := dnfquest.Load(context.Background(), index)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func qst028MedicineFixtureItemCatalog(t *testing.T) *pvfDungeonDropCatalog {
	t.Helper()
	catalog, err := newPVFDungeonDropCatalog(finishBridgePVFSource{
		"monster/monster.lst":                         "",
		"equipment/equipment.lst":                     "",
		"stackable/stackable.lst":                     "10164764 `10164001/10164764.stk`\n3033 `material/cubepiece_black.stk`\n3166 `material/crystal_lowelemental.stk`\n",
		"stackable/10164001/10164764.stk":             "[name]\n`传染病治疗剂`\n[stackable type]\n`[quest]` 0\n[stack limit]\n1\n",
		"stackable/material/cubepiece_black.stk":      "[name]\n`黑色小晶块`\n[stackable type]\n`[material]`\n[stack limit]\n1000\n",
		"stackable/material/crystal_lowelemental.stk": "[name]\n`下级元素结晶`\n[stackable type]\n`[material]`\n[stack limit]\n1000\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

package dnfbridge

import (
	"context"
	"os"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	dnfhonor "longheng.io/server/internal/modules/dnf/honor"
	"longheng.io/server/internal/modules/dnf/progression"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

const (
	qst019BoxAccountID   = defaultAccountPrefix + "1"
	qst019BoxCharacterID = "19"
	qst019BoxSourceID    = uint16(3649)
	qst019BoxParentID    = uint16(3656)
	qst019BoxClearID     = uint16(3692)
	qst019BoxDeliveryID  = uint16(3693)
	qst019BoxItemID      = int64(10164767)
)

func TestQST019BoxSourceRegularFixture(t *testing.T) {
	ctx := context.Background()
	catalog := qst019BoxFixtureCatalog(t)
	items := qst019BoxFixtureItemCatalog(t)
	progress := finishBridgeProgression(t)

	t.Run("normal-source-finish-grants-box", func(t *testing.T) {
		repositories := qst019BoxRepositories(t, ctx)
		state := dnfrepo.QuestState{Status: "active", ProgressValue: 1}
		qst019BoxSetState(t, ctx, repositories, qst019BoxSourceID, state)
		conn, service, session := qst019BoxBridge(repositories, catalog, progress, items)

		boxCallTrigger(t, service, session, conn, qst019BoxSourceID, 0)
		packets := boxCallFinish(t, service, session, conn, qst019BoxSourceID)
		if len(packets) == 0 {
			owner, ownerErr := dnfquest.NewOwner(repositories)
			if ownerErr != nil {
				t.Fatal(ownerErr)
			}
			_, ownerErr = owner.ApplyFinishSettlement(ctx, catalog, dnfquest.FinishCommitInput{
				AccountID: qst019BoxAccountID, CharacterID: qst019BoxCharacterID,
				QuestID: int64(qst019BoxSourceID), Multiplier: 1,
				CommittedAt: time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC),
				Progression: progress, AllocateItem: currentQuestFinishItemAllocator(items),
			})
			t.Fatalf("source finish produced no ACK; owner diagnostic error=%v", ownerErr)
		}
		qst006AssertFinishSuccess(t, packets, qst019BoxSourceID)
		if got := qst019BoxCount(t, ctx, repositories); got != 1 {
			t.Fatalf("source finish box count=%d, want 1", got)
		}

		conn.write.Reset()
		if err := service.handleCurrentAcceptQuest(session, qst019BoxAcceptBody(qst019BoxParentID)); err != nil {
			t.Fatal(err)
		}
		if got := finishBridgePacketIDs(splitAllFinishBridgePackets(t, conn.write.Bytes())); !reflect.DeepEqual(got, []string{"1/31", "0/574"}) {
			t.Fatalf("normal-source accept packets=%v", got)
		}
		qst019BoxFinishMainChain(t, service, session, conn, repositories, ctx)
	})

	t.Run("skipped-source-accept-recovers", func(t *testing.T) {
		repositories := qst019BoxRepositories(t, ctx)
		qst019BoxSeedSkippedSource(t, ctx, repositories)
		conn, service, session := qst019BoxBridge(repositories, catalog, progress, items)

		conn.write.Reset()
		if err := service.handleCurrentAcceptQuest(session, qst019BoxAcceptBody(qst019BoxParentID)); err != nil {
			t.Fatal(err)
		}
		if got := finishBridgePacketIDs(splitAllFinishBridgePackets(t, conn.write.Bytes())); !reflect.DeepEqual(got, []string{"1/31", "0/14", "0/574"}) {
			t.Fatalf("skipped-source accept packets=%v", got)
		}
		if got := qst019BoxCount(t, ctx, repositories); got != 1 {
			t.Fatalf("accept recovery box count=%d, want 1", got)
		}
		qst019BoxAssertReceipt(t, ctx, repositories)
		qst019BoxFinishMainChain(t, service, session, conn, repositories, ctx)
	})

	t.Run("old-active-relogin-recovers-once", func(t *testing.T) {
		repositories := qst019BoxRepositories(t, ctx)
		qst019BoxSeedSkippedSource(t, ctx, repositories)
		record, found, err := repositories.Quest.Load(ctx, qst019BoxCharacterID)
		if err != nil || !found {
			t.Fatalf("load quest found=%t err=%v", found, err)
		}
		record.States[3656] = dnfrepo.QuestState{Status: "active", ProgressValue: 1}
		record.States[3692] = dnfrepo.QuestState{Status: "completed"}
		record.States[3693] = dnfrepo.QuestState{Status: "active", ProgressValue: 1}
		if err := repositories.Quest.Save(ctx, record); err != nil {
			t.Fatal(err)
		}
		conn, service, _ := qst019BoxBridge(repositories, catalog, progress, items)
		session := &gameSession{
			conn: conn, connID: "qst019-old-active-relogin",
			selectedCharacterID: 19, channel: channelcatalog.Channel{ID: 19},
		}
		if err := service.sendCurrentActiveQuestSnapshotForSession(session, "qst019-old-active-relogin"); err != nil {
			t.Fatal(err)
		}
		if got := finishBridgePacketIDs(splitAllFinishBridgePackets(t, conn.write.Bytes())); !reflect.DeepEqual(got, []string{"0/14", "0/574"}) {
			t.Fatalf("old-active login packets=%v", got)
		}
		if got := qst019BoxCount(t, ctx, repositories); got != 1 {
			t.Fatalf("old-active login box count=%d, want 1", got)
		}
		qst019BoxAssertReceipt(t, ctx, repositories)

		conn.write.Reset()
		if err := service.sendCurrentActiveQuestSnapshotForSession(session, "qst019-old-active-relogin-repeat"); err != nil {
			t.Fatal(err)
		}
		if got := finishBridgePacketIDs(splitAllFinishBridgePackets(t, conn.write.Bytes())); !reflect.DeepEqual(got, []string{"0/574"}) {
			t.Fatalf("repeated old-active login packets=%v", got)
		}
		if got := qst019BoxCount(t, ctx, repositories); got != 1 {
			t.Fatalf("repeated old-active login box count=%d, want 1", got)
		}
		qst019BoxFinishDeliveryAndParent(t, service, session, conn, repositories, ctx)
		reconnect := &gameSession{
			conn: &bufferConn{}, connID: "qst019-old-active-relogin-after-finish",
			selectedCharacterID: 19, channel: channelcatalog.Channel{ID: 19},
		}
		if err := service.sendCurrentActiveQuestSnapshotForSession(reconnect, "qst019-after-finish-relogin"); err != nil {
			t.Fatal(err)
		}
		if got := qst019BoxCount(t, ctx, repositories); got != 0 {
			t.Fatalf("after-finish relogin box count=%d, want 0", got)
		}
		qst019BoxAssertReceipt(t, ctx, repositories)
	})
}

func TestSeekerQST019BoxSourceRealPVFRecovery(t *testing.T) {
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
	honorTables, err := dnfhonor.LoadTables(archive)
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}

	repositories := qst019BoxRepositories(t, ctx)
	character, found, err := repositories.Character.Load(ctx, qst019BoxCharacterID)
	if err != nil || !found {
		t.Fatalf("load character found=%t err=%v", found, err)
	}
	character.Level = 90
	if err := repositories.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	skill, found, err := repositories.Skill.Load(ctx, qst019BoxCharacterID)
	if err != nil || !found {
		t.Fatalf("load skill found=%t err=%v", found, err)
	}
	skill.Points.SyncedLevel = 90
	if err := repositories.Skill.Save(ctx, skill); err != nil {
		t.Fatal(err)
	}
	plan, err := catalog.PlanAutoCompleteMainQuests(
		dnfrepo.QuestRecord{
			CharacterID: qst019BoxCharacterID,
			States: map[int64]dnfrepo.QuestState{
				3648: {Status: "completed"},
				3649: {Status: "active", ProgressValue: 1},
				3655: {Status: "completed"},
			},
		},
		dnfquest.CharacterEligibility{Level: 90, Job: 2},
		89,
		3649,
		dnfquest.CouponClearScopeMainStorySweep,
		time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	if state := plan.Record.States[3649]; state.Status != "completed" ||
		state.Extra["auto_complete_reason"] != "epic_below_character_level_no_reward" {
		t.Fatalf("auto-complete source state=%+v", state)
	}
	if err := repositories.Quest.Save(ctx, plan.Record); err != nil {
		t.Fatal(err)
	}

	conn, service, session := qst019BoxBridge(repositories, catalog, tables, items)
	service.honorTable = honorTables
	if err := service.handleCurrentAcceptQuest(session, qst019BoxAcceptBody(qst019BoxParentID)); err != nil {
		t.Fatal(err)
	}
	if got := finishBridgePacketIDs(splitAllFinishBridgePackets(t, conn.write.Bytes())); !reflect.DeepEqual(got, []string{"1/31", "0/14", "0/574"}) {
		t.Fatalf("real PVF accept recovery packets=%v", got)
	}
	qst019BoxFinishMainChain(t, service, session, conn, repositories, ctx)
	qst006AssertState(t, ctx, repositories, map[int64]dnfrepo.QuestState{
		3656: {Status: "completed"},
		3692: {Status: "completed"},
		3693: {Status: "completed"},
	})
}

func qst019BoxFinishMainChain(
	t *testing.T,
	service *Service,
	session *gameSession,
	conn *bufferConn,
	repositories dnfrepo.Group,
	ctx context.Context,
) {
	t.Helper()
	boxCallTrigger(t, service, session, conn, qst019BoxClearID, 0)
	qst006CallChildFinish(t, service, session, conn, qst019BoxClearID)
	qst019BoxFinishDeliveryAndParent(t, service, session, conn, repositories, ctx)
}

func qst019BoxFinishDeliveryAndParent(
	t *testing.T,
	service *Service,
	session *gameSession,
	conn *bufferConn,
	repositories dnfrepo.Group,
	ctx context.Context,
) {
	t.Helper()
	boxCallTrigger(t, service, session, conn, qst019BoxDeliveryID, 0)
	packets := boxCallFinish(t, service, session, conn, qst019BoxDeliveryID)
	qst006AssertFinishSuccess(t, packets, qst019BoxDeliveryID)
	if got := qst019BoxCount(t, ctx, repositories); got != 0 {
		t.Fatalf("delivery left %d boxes, want 0", got)
	}
	record, found, err := repositories.Quest.Load(ctx, qst019BoxCharacterID)
	if err != nil || !found {
		t.Fatalf("load delivery receipt found=%t err=%v", found, err)
	}
	receipt := qst006ReadReceipt(t, record.States[int64(qst019BoxDeliveryID)])
	if len(receipt.ConsumedItems) != 1 {
		t.Fatalf("box delivery consumed rows=%+v", receipt.ConsumedItems)
	}
	consumed := receipt.ConsumedItems[0]
	if consumed.ItemID != qst019BoxItemID || consumed.Delta != 1 || consumed.PostCount != 0 {
		t.Fatalf("box delivery consumed row=%+v", consumed)
	}
	conn.write.Reset()
	if packets := boxCallFinish(t, service, session, conn, qst019BoxDeliveryID); len(packets) != 0 {
		t.Fatalf("duplicate box delivery packets=%v", finishBridgePacketIDs(packets))
	}
	if got := qst019BoxCount(t, ctx, repositories); got != 0 {
		t.Fatalf("duplicate box delivery changed count=%d", got)
	}
	qst006AssertFinishSuccess(t,
		boxCallFinish(t, service, session, conn, qst019BoxParentID), qst019BoxParentID)
	qst006AssertState(t, ctx, repositories, map[int64]dnfrepo.QuestState{
		3656: {Status: "completed"},
		3692: {Status: "completed"},
		3693: {Status: "completed"},
	})
}

func qst019BoxRepositories(t *testing.T, ctx context.Context) dnfrepo.Group {
	t.Helper()
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: qst019BoxCharacterID,
		AccountID:   qst019BoxAccountID,
		Job:         "2",
		Level:       1,
		Stats:       map[string]int64{"grow_type": 0, "exp": 0, "fatigue": 156},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Quest.Save(ctx, dnfrepo.QuestRecord{
		CharacterID: qst019BoxCharacterID,
		States: map[int64]dnfrepo.QuestState{
			3648: {Status: "completed"},
			3655: {Status: "completed"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Skill.Save(ctx, dnfrepo.SkillRecord{
		CharacterID: qst019BoxCharacterID,
		Points: dnfrepo.SkillPointState{
			SyncedLevel: 1, TotalSP: 100, RemainingSP: 100, TotalTP: 20, RemainingTP: 20,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: qst019BoxCharacterID,
		Slots:       map[string]dnfrepo.ItemStack{},
	}); err != nil {
		t.Fatal(err)
	}
	return repositories
}

func qst019BoxSeedSkippedSource(t *testing.T, ctx context.Context, repositories dnfrepo.Group) {
	t.Helper()
	record, found, err := repositories.Quest.Load(ctx, qst019BoxCharacterID)
	if err != nil || !found {
		t.Fatalf("load quest found=%t err=%v", found, err)
	}
	record.States[3649] = dnfrepo.QuestState{
		Status: "completed",
		Extra: map[string]string{
			"reward_state":         "granted",
			"auto_completed":       "true",
			"auto_complete_reason": "epic_below_character_level_no_reward",
		},
	}
	if err := repositories.Quest.Save(ctx, record); err != nil {
		t.Fatal(err)
	}
	if got := qst019BoxCount(t, ctx, repositories); got != 0 {
		t.Fatalf("skipped source fixture box count=%d", got)
	}
}

func qst019BoxSetState(
	t *testing.T,
	ctx context.Context,
	repositories dnfrepo.Group,
	questID uint16,
	state dnfrepo.QuestState,
) {
	t.Helper()
	record, found, err := repositories.Quest.Load(ctx, qst019BoxCharacterID)
	if err != nil || !found {
		t.Fatalf("load quest found=%t err=%v", found, err)
	}
	record.States[int64(questID)] = state
	if err := repositories.Quest.Save(ctx, record); err != nil {
		t.Fatal(err)
	}
}

func qst019BoxAssertReceipt(t *testing.T, ctx context.Context, repositories dnfrepo.Group) {
	t.Helper()
	record, found, err := repositories.Quest.Load(ctx, qst019BoxCharacterID)
	if err != nil || !found {
		t.Fatalf("load quest found=%t err=%v", found, err)
	}
	if record.States[3649].Extra[dnfquest.BoxSourceRecoveryReceiptKey] == "" {
		t.Fatalf("box source recovery receipt missing: %+v", record.States[3649])
	}
}

func qst019BoxBridge(
	repositories dnfrepo.Group,
	catalog *dnfquest.Catalog,
	progress *progression.Tables,
	items *pvfDungeonDropCatalog,
) (*bufferConn, *Service, *gameSession) {
	conn := &bufferConn{}
	service := &Service{
		options: options{
			accountID:            qst019BoxAccountID,
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
		conn: conn, connID: "qst019-box-regular", selectedCharacterID: 19,
		channel: channelcatalog.Channel{ID: 19},
	}
	session.townSceneReadyCharacterID = session.selectedCharacterID
	return conn, service, session
}

func qst019BoxAcceptBody(questID uint16) []byte {
	return []byte{0x1f, 0x00, byte(questID), byte(questID >> 8)}
}

func boxCallTrigger(
	t *testing.T,
	service *Service,
	session *gameSession,
	conn *bufferConn,
	questID uint16,
	triggerType byte,
) {
	t.Helper()
	qst006CallTrigger(t, service, session, conn, questID, triggerType)
}

func boxCallFinish(
	t *testing.T,
	service *Service,
	session *gameSession,
	conn *bufferConn,
	questID uint16,
) []dnfproto.ChannelPacket {
	t.Helper()
	return qst006CallFinish(t, service, session, conn, questID)
}

func qst019BoxCount(t *testing.T, ctx context.Context, repositories dnfrepo.Group) int64 {
	t.Helper()
	inventory, found, err := repositories.Inventory.Load(ctx, qst019BoxCharacterID)
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	var count int64
	for _, stack := range inventory.Slots {
		if stack.ItemID == qst019BoxItemID {
			count += stack.Count
		}
	}
	return count
}

func qst019BoxFixtureCatalog(t *testing.T) *dnfquest.Catalog {
	t.Helper()
	source := finishBridgePVFSource{
		dnfquest.DefaultList:       "3649 `box_source.qst`\n3656 `box_parent.qst`\n3692 `box_clear.qst`\n3693 `box_delivery.qst`\n",
		"n_quest/box_source.qst":   "[grade]\n`[epic]`\n[type]\n`[clear map]`\n[difficulty]\n`N`\n[reward type]\n`[item]`\n[reward int data]\n10164767 1\n[level]\n1 99\n[job]\n`[all]`\n[pre required quest]\n3648\n[int data]\n91935\n",
		"n_quest/box_parent.qst":   "[grade]\n`[epic]`\n[type]\n`[quest clear]`\n[difficulty]\n`N`\n[attribute]\n`not give exp quest`\n[/attribute]\n[reward type]\n`[item]`\n[reward int data]\n0 0\n[level]\n1 99\n[job]\n`[all]`\n[pre required quest]\n3655\n[int data]\n3692 3693\n",
		"n_quest/box_clear.qst":    "[grade]\n`[sub]`\n[type]\n`[clear map]`\n[difficulty]\n`N`\n[attribute]\n`not give exp quest`\n[/attribute]\n[reward type]\n`[item]`\n[reward int data]\n0 0\n[level]\n1 99\n[job]\n`[all]`\n[main quest]\n3656\n[int data]\n91986\n",
		"n_quest/box_delivery.qst": "[grade]\n`[sub]`\n[type]\n`[seeking]`\n[difficulty]\n`N`\n[attribute]\n`not give exp quest`\n[/attribute]\n[reward type]\n`[item]`\n[reward int data]\n0 0\n[level]\n1 99\n[job]\n`[all]`\n[main quest]\n3656\n[int data]\n10164767 1\n",
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

func qst019BoxFixtureItemCatalog(t *testing.T) *pvfDungeonDropCatalog {
	t.Helper()
	catalog, err := newPVFDungeonDropCatalog(finishBridgePVFSource{
		"monster/monster.lst":             "",
		"equipment/equipment.lst":         "",
		"stackable/stackable.lst":         "10164767 `10164001/10164767.stk`\n",
		"stackable/10164001/10164767.stk": "[name]\n`小盒子`\n[stackable type]\n`[quest]` 0\n[stack limit]\n1\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

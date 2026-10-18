package dnfbridge

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/progression"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

const seekerQueenLetterID int64 = 10164786

// TestSeekerQueenLetterRealPVFSourceDiagnostic inventories the real quest
// definitions around 3249 and every quest-document section that mentions the
// letter. It is a discovery step for the normal acquisition path; it does not
// fabricate inventory or mutate production state.
func TestSeekerQueenLetterRealPVFSourceDiagnostic(t *testing.T) {
	archive, catalog := seekerQueenLetterRealPVF(t)
	ctx := context.Background()
	index, err := dnfpvf.Build(ctx, archive, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}

	for _, questID := range []int64{3248, 3249, 3609, 3610} {
		definition, ok := catalog.Find(questID)
		if !ok {
			t.Fatalf("quest %d is absent from the real catalog", questID)
		}
		t.Logf("chain quest=%d path=%q name=%q grade=%q type=%q int=%v pre=%v reward_type=%q reward_int=%v depend_give=%t/%v",
			definition.ID,
			definition.Path,
			definition.Name,
			definition.Grade,
			definition.Type,
			definition.IntData,
			definition.PreRequiredGroups,
			definition.RewardType,
			definition.RewardIntData,
			definition.HasDependGiveItem,
			definition.DependGiveItemData,
		)
	}

	referenceCount := 0
	for _, entry := range index.List(dnfquest.DefaultList) {
		document, ok := index.Document(entry.Path)
		if !ok {
			continue
		}
		for _, section := range document.Sections {
			for _, token := range document.Tokens[section.Start:section.End] {
				if token.Kind != dnfpvf.TokenInt || token.Int != seekerQueenLetterID {
					continue
				}
				referenceCount++
				definition, known := catalog.Find(entry.ID)
				if !known {
					t.Logf("letter-reference quest=%d path=%q section=%q token=%q known=false",
						entry.ID, entry.Path, section.Name, token.Raw)
					continue
				}
				t.Logf("letter-reference quest=%d path=%q name=%q grade=%q type=%q main=%d pre=%v section=%q int=%v reward_type=%q reward_int=%v depend_give=%t/%v",
					definition.ID,
					definition.Path,
					definition.Name,
					definition.Grade,
					definition.Type,
					definition.MainQuestID,
					definition.PreRequiredGroups,
					section.Name,
					definition.IntData,
					definition.RewardType,
					definition.RewardIntData,
					definition.HasDependGiveItem,
					definition.DependGiveItemData,
				)
			}
		}
	}
	if referenceCount == 0 {
		t.Fatal("real PVF quest catalog contains no document reference to 10164786")
	}

	source, ok := catalog.Find(3226)
	if !ok || source.RewardType != "[item]" ||
		!reflect.DeepEqual(source.RewardIntData, []int64{seekerQueenLetterID, 1}) ||
		!reflect.DeepEqual(source.PreRequiredGroups, [][]int64{{3225}}) {
		t.Fatalf("letter source quest 3226 definition=%+v found=%t", source, ok)
	}
}

// TestSeekerQueenLetterRealPVFFrom3226To3249 acquires the letter only through
// quest 3226's real finish reward, then exercises the unmodified 3249 parent
// and 3609/3610 children through the current bridge. The already-completed
// 3225/3248 rows are progress setup for isolated step boundaries; the letter
// itself is never inserted by the test.
func TestSeekerQueenLetterRealPVFFrom3226To3249(t *testing.T) {
	archive, catalog := seekerQueenLetterRealPVF(t)
	ctx := context.Background()
	tables, err := progression.Load(ctx, archive)
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	repositories := qst006Repositories(t, ctx)
	if err := repositories.Quest.Save(ctx, dnfrepo.QuestRecord{
		CharacterID: qst006Character,
		States: map[int64]dnfrepo.QuestState{
			3225: {Status: "completed"},
			3248: {Status: "completed"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	owner, err := dnfquest.NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := owner.ApplyAccept(ctx, catalog,
		dnfquest.CharacterEligibility{Level: 65, Job: 2, GrowType: 0},
		dnfquest.NewQuestIDCommand(alignedcmd.Request{
			AccountID: qst006AccountID, SelectedCharacterID: 19,
		}, "accept_quest", dnfquest.QuestIDRequest{QuestID: 3226}),
	)
	if err != nil {
		t.Fatalf("accept real letter source quest 3226: %v", err)
	}
	t.Logf("accepted source quest 3226 init_trigger=%d path=%q type=%q", accepted.InitTrigger, accepted.PVFPath, accepted.QuestType)

	conn, service, session := qst006Bridge(repositories, catalog, tables, items)
	qst006CallTrigger(t, service, session, conn, 3226, 0x00)
	packets := qst006CallFinish(t, service, session, conn, 3226)
	qst006AssertFinishSuccess(t, packets, 3226)
	if got := qst006LetterCount(t, ctx, repositories); got != 1 {
		t.Fatalf("real source quest 3226 finish granted letter count=%d, want 1", got)
	}
	sourceRecord, found, err := repositories.Quest.Load(ctx, qst006Character)
	if err != nil || !found {
		t.Fatalf("load source finish quest found=%t err=%v", found, err)
	}
	if state := sourceRecord.States[3226]; state.Status != "completed" || state.Extra["reward_state"] != "granted" {
		t.Fatalf("real source quest 3226 did not settle: %+v", state)
	}

	if _, err := owner.ApplyAccept(ctx, catalog,
		dnfquest.CharacterEligibility{Level: 65, Job: 2, GrowType: 0},
		dnfquest.NewQuestIDCommand(alignedcmd.Request{
			AccountID: qst006AccountID, SelectedCharacterID: 19,
		}, "accept_quest", dnfquest.QuestIDRequest{QuestID: qst006Parent}),
	); err != nil {
		t.Fatalf("accept 3249 after real source grant: %v", err)
	}
	if got := qst006LetterCount(t, ctx, repositories); got != 1 {
		t.Fatalf("accepting 3249 changed source-granted letter count=%d", got)
	}

	qst006CallTrigger(t, service, session, conn, qst006MeetNPC, 0x00)
	qst006CallChildFinish(t, service, session, conn, qst006MeetNPC)
	qst006CallTrigger(t, service, session, conn, qst006SeekMeet, 0x10)
	qst006CallTrigger(t, service, session, conn, qst006SeekMeet, 0x20)
	packets = qst006CallFinish(t, service, session, conn, qst006SeekMeet)
	qst006AssertFinishSuccess(t, packets, qst006SeekMeet)
	if got := qst006LetterCount(t, ctx, repositories); got != 0 {
		t.Fatalf("3610 settlement did not consume exactly the source-granted letter: remaining=%d", got)
	}
	packets = qst006CallFinish(t, service, session, conn, qst006Parent)
	qst006AssertFinishSuccess(t, packets, qst006Parent)
	qst006AssertState(t, ctx, repositories, map[int64]dnfrepo.QuestState{
		3249: {Status: "completed"},
		3609: {Status: "completed"},
		3610: {Status: "completed"},
	})
	settled, found, err := repositories.Quest.Load(ctx, qst006Character)
	if err != nil || !found {
		t.Fatalf("reload settled queen quest found=%t err=%v", found, err)
	}
	receipt := qst006ReadReceipt(t, settled.States[int64(qst006SeekMeet)])
	qst006AssertSingleLetterConsumption(t, receipt.ConsumedItems)
}

// TestSeekerQueenLetterRealPVFRecoveryFromAutoCompleted3226 covers the two
// live recovery boundaries for the one confirmed source relation: a fresh
// 3249 accept and an already-active 3249/3610 login. The source quest is closed
// by the real no-reward auto-complete path, so the test never injects the
// letter itself.
func TestSeekerQueenLetterRealPVFRecoveryFromAutoCompleted3226(t *testing.T) {
	archive, catalog := seekerQueenLetterRealPVF(t)
	ctx := context.Background()
	tables, err := progression.Load(ctx, archive)
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	completedAt := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	t.Run("accept-snapshot-recovers-then-dialogue-consumes-one", func(t *testing.T) {
		repositories := qst006Repositories(t, ctx)
		seekerQueenLetterAutoComplete3226(t, ctx, repositories, catalog, completedAt)
		conn, service, session := qst006Bridge(repositories, catalog, tables, items)
		session.townSceneReadyCharacterID = session.selectedCharacterID

		conn.write.Reset()
		parentID := uint16(qst006Parent)
		body := []byte{0x1f, 0x00, byte(parentID), byte(parentID >> 8)}
		if err := service.handleCurrentAcceptQuest(session, body); err != nil {
			t.Fatal(err)
		}
		packets := splitAllFinishBridgePackets(t, conn.write.Bytes())
		if got := finishBridgePacketIDs(packets); !reflect.DeepEqual(got, []string{"1/31", "0/14", "0/574"}) {
			t.Fatalf("accept recovery packet order=%v", got)
		}
		if got := qst006LetterCount(t, ctx, repositories); got != 1 {
			t.Fatalf("accept snapshot recovered letter count=%d, want 1", got)
		}
		record, found, err := repositories.Quest.Load(ctx, qst006Character)
		if err != nil || !found {
			t.Fatalf("load recovered quest found=%t err=%v", found, err)
		}
		if record.States[3226].Extra["queen_letter_recovery_receipt_v1"] == "" {
			t.Fatalf("source recovery receipt missing: %+v", record.States[3226])
		}

		qst006CallTrigger(t, service, session, conn, qst006MeetNPC, 0x00)
		qst006CallChildFinish(t, service, session, conn, qst006MeetNPC)
		qst006CallTrigger(t, service, session, conn, qst006SeekMeet, 0x10)
		qst006CallTrigger(t, service, session, conn, qst006SeekMeet, 0x20)
		packets = qst006CallFinish(t, service, session, conn, qst006SeekMeet)
		qst006AssertFinishSuccess(t, packets, qst006SeekMeet)
		if got := qst006LetterCount(t, ctx, repositories); got != 0 {
			t.Fatalf("3610 settlement did not consume exactly the recovered letter: remaining=%d", got)
		}
		settled, found, err := repositories.Quest.Load(ctx, qst006Character)
		if err != nil || !found {
			t.Fatalf("reload settled quest found=%t err=%v", found, err)
		}
		qst006AssertSingleLetterConsumption(t, qst006ReadReceipt(t, settled.States[int64(qst006SeekMeet)]).ConsumedItems)

		reconnect := &gameSession{
			conn: &bufferConn{}, connID: "qst006-recovery-relogin",
			selectedCharacterID: 19, channel: channelcatalog.Channel{ID: 19},
		}
		if err := service.sendCurrentActiveQuestSnapshotForSession(reconnect, "qst006-repeated-login"); err != nil {
			t.Fatal(err)
		}
		if got := qst006LetterCount(t, ctx, repositories); got != 0 {
			t.Fatalf("repeated snapshot regranted consumed letter: count=%d", got)
		}
	})

	t.Run("old-active-relogin-recovers-once", func(t *testing.T) {
		repositories := qst006Repositories(t, ctx)
		seekerQueenLetterAutoComplete3226(t, ctx, repositories, catalog, completedAt)
		owner, err := dnfquest.NewOwner(repositories)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := owner.ApplyAccept(ctx, catalog,
			dnfquest.CharacterEligibility{Level: 90, Job: 2, GrowType: 0},
			dnfquest.NewQuestIDCommand(alignedcmd.Request{
				AccountID: qst006AccountID, SelectedCharacterID: 19,
			}, "accept_quest", dnfquest.QuestIDRequest{QuestID: qst006Parent}),
		); err != nil {
			t.Fatalf("accept old active 3249: %v", err)
		}
		conn, service, _ := qst006Bridge(repositories, catalog, tables, items)
		session := &gameSession{
			conn: conn, connID: "qst006-old-active-relogin",
			selectedCharacterID: 19, channel: channelcatalog.Channel{ID: 19},
		}
		if err := service.sendCurrentActiveQuestSnapshotForSession(session, "qst006-old-active-relogin"); err != nil {
			t.Fatal(err)
		}
		packets := splitAllFinishBridgePackets(t, conn.write.Bytes())
		if got := finishBridgePacketIDs(packets); !reflect.DeepEqual(got, []string{"0/14", "0/574"}) {
			t.Fatalf("old-active login packet order=%v", got)
		}
		if got := qst006LetterCount(t, ctx, repositories); got != 1 {
			t.Fatalf("old-active login recovered count=%d, want 1", got)
		}
		conn.write.Reset()
		if err := service.sendCurrentActiveQuestSnapshotForSession(session, "qst006-old-active-relogin-repeat"); err != nil {
			t.Fatal(err)
		}
		packets = splitAllFinishBridgePackets(t, conn.write.Bytes())
		if got := finishBridgePacketIDs(packets); !reflect.DeepEqual(got, []string{"0/574"}) {
			t.Fatalf("repeated old-active login packet order=%v", got)
		}
		if got := qst006LetterCount(t, ctx, repositories); got != 1 {
			t.Fatalf("repeated old-active login changed count=%d", got)
		}
	})
}

func seekerQueenLetterAutoComplete3226(
	t *testing.T,
	ctx context.Context,
	repositories dnfrepo.Group,
	catalog *dnfquest.Catalog,
	completedAt time.Time,
) {
	t.Helper()
	character, found, err := repositories.Character.Load(ctx, qst006Character)
	if err != nil || !found {
		t.Fatalf("load character found=%t err=%v", found, err)
	}
	character.Level = 90
	if err := repositories.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	skill, found, err := repositories.Skill.Load(ctx, qst006Character)
	if err != nil || !found {
		t.Fatalf("load skill found=%t err=%v", found, err)
	}
	skill.Points.SyncedLevel = 90
	if err := repositories.Skill.Save(ctx, skill); err != nil {
		t.Fatal(err)
	}
	plan, err := catalog.PlanAutoCompleteMainQuests(
		dnfrepo.QuestRecord{
			CharacterID: qst006Character,
			States: map[int64]dnfrepo.QuestState{
				3226: {Status: "active", ProgressValue: 1},
				3248: {Status: "completed"},
			},
		},
		dnfquest.CharacterEligibility{Level: 90, Job: 2, GrowType: 0},
		89,
		3226,
		dnfquest.CouponClearScopeMainStorySweep,
		completedAt,
	)
	if err != nil {
		t.Fatalf("plan real source quest 3226 auto-complete: %v", err)
	}
	sourceState := plan.Record.States[3226]
	if sourceState.Status != "completed" ||
		sourceState.Extra["reward_state"] != "granted" ||
		sourceState.Extra["auto_complete_reason"] != "epic_below_character_level_no_reward" {
		t.Fatalf("source quest 3226 auto-complete state=%+v", sourceState)
	}
	if err := repositories.Quest.Save(ctx, plan.Record); err != nil {
		t.Fatal(err)
	}
	if got := qst006LetterCount(t, ctx, repositories); got != 0 {
		t.Fatalf("auto-complete unexpectedly granted letter count=%d", got)
	}
}

func seekerQueenLetterRealPVF(t *testing.T) (*platformpvf.Archive, *dnfquest.Catalog) {
	t.Helper()
	pvfPath := strings.TrimSpace(os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE"))
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the matching Script.pvf")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := archive.Snapshot()
	t.Logf("Script.pvf sha256=%s", snapshot.Checksum)
	ctx := context.Background()
	index, err := dnfpvf.Build(ctx, archive, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := dnfquest.Load(ctx, index)
	if err != nil {
		t.Fatal(err)
	}
	return archive, catalog
}

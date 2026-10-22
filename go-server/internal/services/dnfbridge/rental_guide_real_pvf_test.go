package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"reflect"
	"sort"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFRentalGuideAcceptRentRefreshAndFinishOnce(t *testing.T) {
	path := os.Getenv("DNF_RENTAL_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNF_RENTAL_REAL_PVF_SMOKE")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	monsters, err := newPVFDungeonMonsterCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	conn := &bufferConn{}
	s := &Service{options: options{accountID: "account-1", currentClientVersion: "official-24.9", gameUpperBodyCodec: gameUpperBodyCodecPlain}, initialEquipmentArchive: archive, dungeonMonsterTable: monsters, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
	catalog, err := s.loadQuestCatalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	guide, found := catalog.Find(8581)
	if !found || guide.Path != rentalGuideQuestTestPath || guide.Type != "[custom quest]" || !reflect.DeepEqual(guide.IntData, []int64{1, 0, 1}) {
		t.Fatalf("real rental guide definition changed: %+v", guide)
	}
	tables, items, err := s.currentQuestFinishResources(ctx)
	if err != nil {
		t.Fatal(err)
	}
	experience, err := tables.ThresholdToNext(19)
	if err != nil {
		t.Fatal(err)
	}
	for _, err := range []error{
		repos.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: "account-1", Metadata: map[string]string{currentRentalPointMetadataKey: "400"}}),
		repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "account-1", Level: 20, Job: "0", Stats: map[string]int64{"gold": 1000000, "exp": int64(experience), "grow_type": 0}}),
		repos.Skill.Save(ctx, dnfrepo.SkillRecord{CharacterID: "19", Points: dnfrepo.SkillPointState{SyncedLevel: 20}}),
		repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{}}),
		repos.Equipment.Save(ctx, dnfrepo.EquipmentRecord{CharacterID: "19", Entries: map[string]dnfrepo.EquipmentEntry{}}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	questOwner, err := dnfquest.NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := questOwner.ApplyAccept(ctx, catalog, dnfquest.CharacterEligibility{Level: 20, Job: 0}, dnfquest.NewQuestIDCommand(alignedcmd.Request{AccountID: "account-1", SelectedCharacterID: 19}, "accept_quest", dnfquest.QuestIDRequest{QuestID: 8581}))
	if err != nil || accepted.InitTrigger != 1 {
		t.Fatalf("accept guide: %+v %v", accepted, err)
	}
	rentalCatalog, source, _, err := s.currentRentalSources(ctx)
	if err != nil {
		t.Fatal(err)
	}
	job, err := currentRentalCharacterJobTag(source, 0)
	if err != nil {
		t.Fatal(err)
	}
	tier, found := rentalCatalog.tierForLevel(20)
	if !found {
		t.Fatal("missing level 20 rental tier")
	}
	var itemIDs []int
	for id := range rentalCatalog.Groups[currentRentalGroupKey{JobTag: job, Tier: tier}] {
		itemIDs = append(itemIDs, int(id))
	}
	sort.Ints(itemIDs)
	if len(itemIDs) == 0 {
		t.Fatal("missing rental weapons for current class/tier")
	}
	item := uint32(itemIDs[0])
	cost, found := rentalCatalog.itemCost(job, tier, item)
	if !found {
		t.Fatal("missing rental point cost")
	}
	session := &gameSession{conn: conn, selectedCharacterID: 19}
	trigger := make([]byte, currentSetQuestTriggerRequestBodySize)
	binary.LittleEndian.PutUint16(trigger, 33)
	binary.LittleEndian.PutUint16(trigger[2:], 8581)
	if err := s.handleCurrentSetQuestTrigger(session, trigger); err != nil {
		t.Fatal(err)
	}
	conn.write.Reset()
	body := make([]byte, currentRentalRequestWireSize)
	binary.LittleEndian.PutUint32(body[13:], item)
	binary.LittleEndian.PutUint32(body[17:], uint32(tier)<<16)
	if err := s.handleCurrentRentEquipment(session, body); err != nil {
		t.Fatal(err)
	}
	remaining := conn.write.Bytes()
	for _, id := range []uint16{999, 985, 14, currentActiveQuestSnapshotMsgID} {
		packet, rest := splitGameServerUpperPacket(t, remaining)
		if packet.Header.MsgID != id {
			t.Fatalf("rental packet op=%d, want %d", packet.Header.MsgID, id)
		}
		if id == 999 && (len(packet.Body) < 1 || packet.Body[0] != 1) {
			t.Fatalf("rental rejected: %x", packet.Body)
		}
		if id == currentActiveQuestSnapshotMsgID && (len(packet.Body) != 10 || binary.LittleEndian.Uint16(packet.Body[4:]) != 8581 || binary.LittleEndian.Uint32(packet.Body[6:]) != 0) {
			t.Fatalf("guide snapshot=%x", packet.Body)
		}
		remaining = rest
	}
	if len(remaining) != 0 {
		t.Fatal("unexpected rental packets")
	}
	account, _, err := repos.Account.Load(ctx, "account-1")
	if err != nil {
		t.Fatal(err)
	}
	points, err := currentRentalPoints(account)
	if err != nil || points != 400-cost {
		t.Fatalf("rental points=%d cost=%d error=%v", points, cost, err)
	}
	conn.write.Reset()
	if err := s.handleCurrentSetQuestTrigger(session, trigger); err != nil {
		t.Fatal(err)
	}
	if conn.write.Len() == 0 {
		t.Fatal("pre-rental echo replay cache suppressed the new committed zero")
	}
	echo, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if echo.Header.MsgID != 33 || len(echo.Body) != 7 || binary.LittleEndian.Uint32(echo.Body[3:]) != 0 || len(rest) != 0 {
		t.Fatalf("committed trigger echo=%x", echo.Body)
	}
	finishInput := dnfquest.FinishCommitInput{AccountID: "account-1", CharacterID: "19", QuestID: 8581, Multiplier: 1, CommittedAt: time.Now().UTC(), Progression: tables, MaximumCharacterLevel: 90, AllocateItem: currentQuestFinishItemAllocator(items)}
	finished, err := questOwner.ApplyFinishSettlement(ctx, catalog, finishInput)
	if err != nil || !finished.AtomicCommitted || finished.Replayed {
		t.Fatalf("finish guide result=%+v error=%v", finished, err)
	}
	beforeReplay := rentalGuideSQLSnapshot(t, repos)
	replayed, err := questOwner.ApplyFinishSettlement(ctx, catalog, finishInput)
	if err != nil || !replayed.Replayed || !reflect.DeepEqual(beforeReplay, rentalGuideSQLSnapshot(t, repos)) {
		t.Fatalf("finish replay changed assets: %+v %v", replayed, err)
	}
	quests, _, err := repos.Quest.Load(ctx, "19")
	if err != nil || quests.States[8581].Status != "completed" {
		t.Fatalf("guide completion state=%+v error=%v", quests.States[8581], err)
	}
	// Reset only this test's isolated quest fixture for a failed socket write.
	if err := repos.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: "19"}); err != nil {
		t.Fatal(err)
	}
	_, err = questOwner.ApplyAccept(ctx, catalog, dnfquest.CharacterEligibility{Level: 20, Job: 0}, dnfquest.NewQuestIDCommand(alignedcmd.Request{AccountID: "account-1", SelectedCharacterID: 19}, "accept_quest", dnfquest.QuestIDRequest{QuestID: 8581}))
	if err != nil {
		t.Fatal(err)
	}
	wantWriteErr := errors.New("rental guide snapshot write failed")
	failingConn := &rentalGuideSnapshotFailConn{err: wantWriteErr}
	failingSession := &gameSession{conn: failingConn, selectedCharacterID: 19}
	if err := s.handleCurrentRentEquipment(failingSession, body); !errors.Is(err, wantWriteErr) {
		t.Fatalf("snapshot failure error=%v, want %v", err, wantWriteErr)
	}
	afterFailure, _, err := repos.Quest.Load(ctx, "19")
	if err != nil || afterFailure.States[8581].ProgressValue != 0 {
		t.Fatal("socket failure undid committed quest progress")
	}
	afterAccount, _, err := repos.Account.Load(ctx, "account-1")
	if err != nil {
		t.Fatal(err)
	}
	afterPoints, err := currentRentalPoints(afterAccount)
	if err != nil || afterPoints != 400-2*cost {
		t.Fatal("socket failure undid or repeated rental debit")
	}
	conn.write.Reset()
	if err := s.sendCurrentActiveQuestSnapshotForSession(&gameSession{conn: conn, selectedCharacterID: 19}, "rental_guide_reconnect_test"); err != nil {
		t.Fatal(err)
	}
	restored, trailing := splitGameServerUpperPacket(t, conn.write.Bytes())
	if restored.Header.MsgID != currentActiveQuestSnapshotMsgID || !reflect.DeepEqual(restored.Body, buildCurrentActiveQuestSnapshotBody(afterFailure, true)) || len(trailing) != 0 {
		t.Fatal("reconnect did not restore committed guide snapshot")
	}
	t.Logf("quest=8581 item=%d cost=%d rental ACK/points/item/quest snapshot passed; finish/replay passed", item, cost)
}

type rentalGuideSnapshotFailConn struct {
	bufferConn
	writes int
	err    error
}

func (c *rentalGuideSnapshotFailConn) Write(data []byte) (int, error) {
	c.writes++
	if c.writes == 4 {
		return 0, c.err
	}
	return c.bufferConn.Write(data)
}

package dnfbridge

import (
	"context"
	"fmt"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"testing"
)

func TestQuestHellPartyProofRequiresEveryAuthoritativeWaveActorDead(t *testing.T) {
	service, session, _, _, _, runtime, _, _ := partyAbyssPillarFixture(t)
	if currentDungeonQuestHellPartyCleared(runtime) {
		t.Fatal("unplayed abyss counted as clear")
	}
	keys := waveHellPartyActorKeys(t, runtime)
	if len(keys) < 2 {
		t.Fatal("fixture lacks wave actors")
	}
	killDungeonActors(t, service, session, keys[:len(keys)-1]...)
	if currentDungeonQuestHellPartyCleared(runtime) {
		t.Fatal("remaining abyss actor ignored")
	}
	killDungeonActors(t, service, session, keys[len(keys)-1:]...)
	if !currentDungeonQuestHellPartyCleared(runtime) {
		t.Fatal("fully cleared authoritative abyss not recognized")
	}
	if runtime.HellPartyStarted {
		t.Fatal("fixture unexpectedly sent optional op235")
	}
	runtime.HellRoomSet = false
	if currentDungeonQuestHellPartyCleared(runtime) {
		t.Fatal("missing abyss room proof accepted")
	}
}

func TestAbyssFinalWavePersistsEP5AndNotifiesBeforeReturningWithoutDungeonSettlement(t *testing.T) {
	source := bridgeAbyssEpicFragmentPVF()
	source[worldmap.DefaultWorldMapList] = "39 `test.wdm`\n"
	source[dnfquest.DefaultList] = "10109 `ep5.qst`\n3145 `boss.qst`\n"
	source["n_quest/ep5.qst"] = "[grade] `[achievement]`\n[level] 90 99\n[type] `[condition under clear]`\n[sub type] 23\n[int data] -2 39 -1 1\n[/int data]\n"
	source["n_quest/boss.qst"] = "[grade] `[epic]`\n[level] 1 99\n[type] `[clear map]`\n[int data] " + fmt.Sprint(testHellPartyHellMapID) + "\n"
	service, session, conn, runtime, scene, repositories := prepareAbyssEpicFragmentRuntime(t, source)
	index, err := dnfpvf.Build(context.Background(), source, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	service.questCatalog, err = dnfquest.Load(context.Background(), index)
	if err != nil {
		t.Fatal(err)
	}
	if err := repositories.Quest.Save(context.Background(), dnfrepo.QuestRecord{CharacterID: "99", States: map[int64]dnfrepo.QuestState{10109: {Status: "active", ProgressValue: 1}, 3145: {Status: "active", ProgressValue: 1}}}); err != nil {
		t.Fatal(err)
	}
	keys := waveHellPartyActorKeys(t, runtime)
	killDungeonActors(t, service, session, keys[:len(keys)-1]...)
	before, _, err := repositories.Quest.Load(context.Background(), "99")
	if err != nil {
		t.Fatal(err)
	}
	if before.States[10109].ProgressValue != 1 {
		t.Fatal("quest completed before final wave death")
	}
	conn.write.Reset()
	killDungeonActors(t, service, session, keys[len(keys)-1:]...)
	after, _, err := repositories.Quest.Load(context.Background(), "99")
	if err != nil {
		t.Fatal(err)
	}
	if state := after.States[10109]; state.Status != "active" || state.ProgressValue != 0 || state.Extra["reward_state"] != "pending" {
		t.Fatalf("objective not rewardable: %+v", state)
	}
	if after.States[3145].ProgressValue != 1 || runtime.bossDieCheckAccepted || runtime.clearMapCompletionPhaseAPersisted || runtime.settlementEntrySent {
		t.Fatal("abyss objective completed whole dungeon or ordinary quest")
	}
	found := false
	for rest := conn.write.Bytes(); len(rest) > 0; {
		packet, next := splitGameServerUpperPacket(t, rest)
		rest = next
		if packet.Header.MsgID == currentActiveQuestSnapshotMsgID {
			assertClearMapCompletionNotification(t, packet, 10109)
			found = true
		}
	}
	if !found {
		t.Fatal("no native active quest snapshot after objective commit")
	}
	conn.write.Reset()
	if err := service.persistCurrentHellRegionQuestCompletionLocked(session, runtime, scene); err != nil {
		t.Fatal(err)
	}
	if conn.write.Len() != 0 {
		t.Fatal("final wave replay emitted duplicate objective snapshot")
	}
}

func TestRealPVFMetroCenterRegionForEP5(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF not configured")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	table, err := worldmap.LoadSource(context.Background(), archive, worldmap.Options{SkipMaps: true, SkipDungeons: true})
	if err != nil {
		t.Fatal(err)
	}
	area, found := table.FindAreaByDungeon(310)
	if !found || area.ID != 39 {
		t.Fatalf("real dungeon310 region=%d found=%t", area.ID, found)
	}
}

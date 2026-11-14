package dnfbridge

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	repo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestLuckyAllUnder90MonsterAndQuestActualAwards(t *testing.T) {
	ctx := context.Background()
	s, runtime, repos := prepareCurrentDungeonMonsterExperienceTest(t)
	enablePortedActivityTest(t, s, nil)
	ch, _, _ := repos.Character.Load(ctx, "99")
	if ch.Stats[currentLuckyNewCharacterStat] != 0 {
		t.Fatal("expected an existing character without a creation marker")
	}
	runtime.Character = ch
	s.luckyCharacterCatalog = &currentLuckyCatalog{Jobs: currentAllLuckyProfessions(), Level1To89Only: true, Monster: 30}
	q := &gameSession{selectedCharacterID: 99, dungeon: dungeonSessionState{runtime: runtime}}
	result, err := s.awardCurrentDungeonMonsterExperience(ctx, q, runtime, runtime.Room.Snapshot().Monsters[0])
	if err != nil {
		t.Fatal(err)
	}
	if result.LuckyCharacterBonus != 60 || result.Character.Stats["exp"] != 260 {
		t.Fatalf("monster award=%+v", result)
	}

	r := finishBridgeSeed(t)
	catalog := finishBridgeCatalog(t)
	ch, _, _ = r.Character.Load(ctx, "19")
	if ch.Stats[currentLuckyNewCharacterStat] != 0 {
		t.Fatal("expected an existing character without a creation marker")
	}
	s = &Service{options: options{gameUpperBodyCodec: gameUpperBodyCodecPlain}, questCatalog: catalog,
		repositoryProvider:    func() (repo.Group, bool) { return r, true },
		luckyCharacterCatalog: &currentLuckyCatalog{Jobs: currentAllLuckyProfessions(), Level1To89Only: true, Quest: 30}}
	q = &gameSession{conn: &bufferConn{}, selectedCharacterID: 19, initialTownRouteCharacterID: 19, initialTownRouteStage: currentInitialTownRoutePlayerStateSent}
	enablePortedActivityTest(t, s, q)
	request := dnfquest.FinishQuestRequest{QuestID: 3145, RewardSelectIndex: ^uint16(0), Multiplier: 1}
	for i := 0; i < 2; i++ {
		if err := s.handleCurrentFinishQuestWithResources(q, request, r, catalog, finishBridgeProgression(t), finishBridgeAllocator(nil)); err != nil {
			t.Fatal(err)
		}
		ch, _, _ = r.Character.Load(ctx, "19")
		if ch.Stats["exp"] != 143 {
			t.Fatalf("quest/replay%d=%v", i, ch.Stats)
		}
	}
}

func TestLuckyAllUnder90Qualification(t *testing.T) {
	c := &currentLuckyCatalog{Jobs: currentAllLuckyProfessions(), Level1To89Only: true}
	for job := 0; job <= 15; job++ {
		for _, level := range []int{0, 1, 20, 89, 90, 91} {
			for _, marker := range []int64{0, 1, 2} {
				ch := repo.CharacterRecord{CharacterID: "1", Job: fmt.Sprint(job), Level: level,
					Stats: map[string]int64{currentLuckyNewCharacterStat: marker}}
				want := level >= 1 && level < 90
				if got := currentLuckyCharacterMatches(c, ch); got != want {
					t.Fatalf("job=%d level=%d marker=%d match=%v want=%v", job, level, marker, got, want)
				}
				ch.Stats["is_hardcore_mode"] = 1
				if currentLuckyCharacterMatches(c, ch) {
					t.Fatal("hardcore qualified")
				}
				ch.Stats["is_hardcore_mode"] = 0
				ch.Stats["pvp_private_character"] = 1
				if currentLuckyCharacterMatches(c, ch) {
					t.Fatal("PVP-only character qualified outside a PVP channel")
				}
			}
		}
	}
}

func TestLuckyAllUnder90StateAndCommittedCapImmediate(t *testing.T) {
	s, q, r := luckyEffectsTestService(t, 10)
	s.luckyCharacterCatalog.Level1To89Only = true
	s.luckyCharacterCatalog.Monster, s.luckyCharacterCatalog.Clear = 30, 30
	s.luckyCharacterCatalog.Party, s.luckyCharacterCatalog.Quest = 30, 30
	ctx := context.Background()
	ch, _, _ := r.Character.Load(ctx, "99")
	ch.Level = 89
	if err := r.Character.Save(ctx, ch); err != nil {
		t.Fatal(err)
	}
	conn := q.conn.(*bufferConn)
	// An existing role without the old marker now receives the ordinary lucky
	// projection immediately; selection does not need to write a qualification.
	if ch.Stats[currentLuckyNewCharacterStat] != 0 {
		t.Fatal("expected an unmarked existing role")
	}
	if err := s.sendCurrentLuckyCharacterState(q); err != nil {
		t.Fatal(err)
	}
	list, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	state, rest := splitGameServerUpperPacket(t, rest)
	if list.Header.MsgID != 1054 || len(list.Body) <= 4 || !bytes.Equal(state.Body, []byte{100, 0, 0}) || len(rest) != 0 || !q.luckyPreparedActive.Load() || s.currentLuckyCharacterEffect(q, ch) == nil {
		t.Fatal("existing Lv89 role did not receive its lucky projection")
	}
	persisted, _, _ := r.Character.Load(ctx, "99")
	if persisted.Stats[currentLuckyNewCharacterStat] != 0 {
		t.Fatal("projection backfilled a creation marker")
	}
	conn.write.Reset()
	// The same command-tail owner used after a monster/quest/ticket commit
	// must revoke it without waiting for the minute clock or reward mail.
	ch.Level = 90
	if err := r.Character.Save(ctx, ch); err != nil {
		t.Fatal(err)
	}
	s.afterCurrentLevelUpRewardCommand(q)
	list, rest = splitGameServerUpperPacket(t, conn.write.Bytes())
	state, rest = splitGameServerUpperPacket(t, rest)
	if list.Header.MsgID != 1054 || !bytes.Equal(list.Body, make([]byte, 4)) || state.Header.MsgID != 490 || !bytes.Equal(state.Body, []byte{0, 0, 3}) || len(rest) != 0 || q.luckyPreparedActive.Load() {
		t.Fatal("Lv90 not revoked in command tail")
	}
	if s.currentLuckyCharacterEffect(q, ch) != nil || s.currentLuckyUniqueDropPercent(q, ch) != 0 {
		t.Fatal("Lv90 still receives actual effects")
	}
	conn.write.Reset()
	s.afterCurrentLevelUpRewardCommand(q)
	if conn.write.Len() != 0 {
		t.Fatal("duplicate cap projection")
	}
	// A fresh Lv90 login also starts empty, rather than briefly showing jobs.
	q.luckyPreparedCharacter = 0
	if err := s.sendCurrentLuckyCharacterState(q); err != nil {
		t.Fatal(err)
	}
	list, _ = splitGameServerUpperPacket(t, conn.write.Bytes())
	if !bytes.Equal(list.Body, make([]byte, 4)) {
		t.Fatal("Lv90 login got jobs")
	}
	q.loginRevoked.Store(true)
	conn.write.Reset()
	if err := s.sendCurrentLuckyCharacterState(q); err == nil || conn.write.Len() != 0 {
		t.Fatal("stale session emitted a projection")
	}
}

func TestLuckyNew007RealPVF(t *testing.T) {
	p := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if p == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE")
	}
	a, err := platformpvf.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	text, err := a.ReadText("etc/chn_activatecharacter.etc")
	if err != nil {
		t.Fatal(err)
	}
	c, err := parseCurrentLuckyCharacterCatalog(text)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Level1To89Only || c.Monster != 30 || c.Clear != 30 || c.Party != 30 || c.Quest != 30 || c.UniqueDrop != 50 || c.FatigueBurn != 100 || len(c.Jobs) != 178 {
		t.Fatalf("unexpected007 config: %+v", c)
	}
	for _, bad := range []string{"0", "2", "1 1", "`true`"} {
		// Actual text spacing is archive-defined; construct the malformed
		// document independently rather than relying on that presentation.
		base := strings.Split(text, "[private new characters only]")[0]
		_, err := parseCurrentLuckyCharacterCatalog(base + "\n[private new characters only]\n" + bad + "\n[/private new characters only]\n")
		if err == nil {
			t.Fatalf("bad eligibility policy accepted: %s", bad)
		}
	}
}

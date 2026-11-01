package dnfbridge

import (
	"context"
	"encoding/binary"
	"os"
	"testing"
	"time"

	dnfonlineevent "longheng.io/server/internal/modules/dnf/onlineevent"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestPortedReturneeEligibilityProgressAndClaims(t *testing.T) {
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	c := &dnfonlineevent.ReturneeCatalog{}
	for i := range c.Missions {
		c.Missions[i] = dnfonlineevent.ReturneeMission{ID: uint32(i + 1), Condition: "clear quest", Required: 2, Reward: dnfonlineevent.AttendancePVFItemReward{ItemID: 100, Count: 1}}
	}
	if err := repos.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: "a"}); err != nil {
		t.Fatal(err)
	}
	ch := dnfrepo.CharacterRecord{AccountID: "a", CharacterID: "7", Level: 50, Stats: map[string]int64{}}
	if err := repos.Character.Save(ctx, ch); err != nil {
		t.Fatal(err)
	}
	cmd := dnfonlineevent.ReturneeCommand{AccountID: "a", CharacterID: "7", Now: now, Login: true, Deliver: func(box dnfrepo.MailboxRepository, r dnfonlineevent.AttendancePVFItemReward, key string) (string, error) {
		return dnfrepo.AppendSystemMail(ctx, box, dnfrepo.SystemMailDelivery{RecipientCharacterID: "7", Title: "returnee", Body: "reward", Source: "test", CompletionKey: key, CreatedAt: now, Attachments: []dnfrepo.MailAttachment{{ItemID: r.ItemID, Count: r.Count}}})
	}}
	settle := func() dnfonlineevent.ReturneeState {
		t.Helper()
		s, _, err := dnfonlineevent.SettleReturnee(ctx, repos, c, cmd)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	if settle().ActiveAt(cmd.Now) {
		t.Fatal("first observation manufactured an offline history")
	}
	cmd.Now = now.Add(dnfonlineevent.ReturneeOfflinePeriod - time.Nanosecond)
	if settle().ActiveAt(cmd.Now) {
		t.Fatal("less than 24 hours qualified")
	}
	// The previous login reset the elapsed-offline anchor.
	cmd.Now = cmd.Now.Add(dnfonlineevent.ReturneeOfflinePeriod)
	s := settle()
	if !s.ActiveAt(cmd.Now) {
		t.Fatal("exactly 24 hours did not qualify")
	}
	cmd.Login = false
	cmd.Claim = 1
	if _, _, err := dnfonlineevent.SettleReturnee(ctx, repos, c, cmd); err == nil {
		t.Fatal("incomplete mission claimed")
	}
	cmd.Claim = 0
	ch, _, _ = repos.Character.Load(ctx, "7")
	ch.Stats[dnfonlineevent.ReturneeQuestCountKey] = 2
	if err := repos.Character.Save(ctx, ch); err != nil {
		t.Fatal(err)
	}
	s = settle()
	if s.Progress[0] != 2 {
		t.Fatalf("progress=%d", s.Progress[0])
	}
	if settle().Progress[0] != 2 {
		t.Fatal("same observation counted twice")
	}
	cmd.Claim = 1
	for i := 0; i < 2; i++ {
		s = settle()
		if s.Claimed != 1 {
			t.Fatal("claim not persisted")
		}
	}
	box, _, err := repos.Mailbox.Load(ctx, "7")
	if err != nil || len(box.Mails) != 1 {
		t.Fatalf("duplicate award: mails=%d err=%v", len(box.Mails), err)
	}
	b := buildCurrentReturneeBodyAt(c, s, cmd.Now)
	if len(b) != 352 || binary.LittleEndian.Uint16(b[:]) != 5 || binary.LittleEndian.Uint32(b[16:]) != 1 || binary.LittleEndian.Uint32(b[20:]) != 2 || b[28] != 1 || b[29] != 1 {
		t.Fatalf("native mission row=%x", b[:32])
	}
	for cmd.Claim = 2; cmd.Claim <= 10; cmd.Claim++ {
		s = settle()
	}
	if !s.ActiveAt(cmd.Now) || s.Claimed != 0x3ff {
		t.Fatal("last reward prematurely ended the returnee window")
	}
	cmd.Claim = 10
	settle() // Replayed last reward must keep the receipt and grant no new mail.
	box, _, err = repos.Mailbox.Load(ctx, "7")
	if err != nil || len(box.Mails) != 10 {
		t.Fatalf("last reward replay: mails=%d err=%v", len(box.Mails), err)
	}
	cmd.Now = s.ExpiresAt
	if _, _, err := dnfonlineevent.SettleReturnee(ctx, repos, c, cmd); err == nil {
		t.Fatal("expired returnee period accepted a reward request")
	}
	cmd.CharacterID = "99"
	if _, _, err := dnfonlineevent.SettleReturnee(ctx, repos, c, cmd); err == nil {
		t.Fatal("foreign/missing character accepted")
	}
}

func TestPortedReturneeLuckyMonsterAndQuestExperience(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name          string
		pvp, hardcore bool
		want          uint32
	}{{"ordinary", false, false, 20}, {"pvp", true, false, 0}, {"hardcore", false, true, 0}} {
		t.Run(tc.name, func(t *testing.T) {
			s, r, repos := prepareCurrentDungeonMonsterExperienceTest(t)
			ch, _, _ := repos.Character.Load(ctx, "99")
			ch.Job = "0"
			ch.Stats["grow_type"] = 0
			if tc.hardcore {
				ch.Stats["is_hardcore_mode"] = 1
			}
			if err := repos.Character.Save(ctx, ch); err != nil {
				t.Fatal(err)
			}
			enablePortedActivityTest(t, s, nil)
			s.luckyCharacterCatalog = &currentLuckyCatalog{Jobs: [][2]uint32{{0, 0}}, Monster: 10}
			r.Character = ch
			session := &gameSession{selectedCharacterID: 99, dungeon: dungeonSessionState{runtime: r}}
			if tc.pvp {
				session.channel.Type = 4
			}
			result, err := s.awardCurrentDungeonMonsterExperience(ctx, session, r, r.Room.Snapshot().Monsters[0])
			if err != nil {
				t.Fatal(err)
			}
			if result.LuckyCharacterBonus != tc.want || result.Character.Stats["exp"] != int64(200+tc.want) {
				t.Fatalf("bonus=%d exp=%d", result.LuckyCharacterBonus, result.Character.Stats["exp"])
			}
		})
	}
	repos := finishBridgeSeed(t)
	catalog := finishBridgeCatalog(t)
	ch, _, _ := repos.Character.Load(ctx, "19")
	ch.Stats["is_return_user"] = 1
	ch.Stats["seria_returnee_active_until"] = time.Now().Add(time.Hour).Unix()
	if err := repos.Character.Save(ctx, ch); err != nil {
		t.Fatal(err)
	}
	s := &Service{options: options{gameUpperBodyCodec: gameUpperBodyCodecPlain}, questCatalog: catalog, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }, luckyCharacterCatalog: &currentLuckyCatalog{Jobs: [][2]uint32{{2, 0}}, Quest: 10}}
	session := &gameSession{conn: &bufferConn{}, selectedCharacterID: 19, initialTownRouteCharacterID: 19, initialTownRouteStage: currentInitialTownRoutePlayerStateSent}
	enablePortedActivityTest(t, s, session)
	request := dnfquest.FinishQuestRequest{QuestID: 3145, RewardSelectIndex: ^uint16(0), Multiplier: 1}
	for i := 0; i < 2; i++ {
		if err := s.handleCurrentFinishQuestWithResources(session, request, repos, catalog, finishBridgeProgression(t), finishBridgeAllocator(nil)); err != nil {
			t.Fatal(err)
		}
		ch, _, _ := repos.Character.Load(ctx, "19")
		if ch.Stats["exp"] != 121 || ch.Stats[dnfonlineevent.ReturneeQuestCountKey] != 1 {
			t.Fatalf("quest replay %d exp=%d count=%d", i, ch.Stats["exp"], ch.Stats[dnfonlineevent.ReturneeQuestCountKey])
		}
	}
}

func TestPortedReturneeLuckyClearRewardAndReplay(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name             string
		party, returnee  bool
		unqualifiedLucky bool
		wantLucky        uint32
		wantWelcome      uint32
		newPolicy        bool
	}{
		{"solo", false, false, false, 30, 0, false},
		{"party", true, false, false, 50, 0, false},
		{"solo_returnee", false, true, false, 30, 40, false},
		{"party_returnee", true, true, false, 50, 40, false},
		{"returnee_without_lucky", false, true, true, 0, 40, false},
		{"existing_under90_solo", false, false, false, 60, 0, true},
		{"existing_under90_party", true, false, false, 120, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, r, session, _, _ := prepareCompletedSettlementRuntime(t)
			repos, _ := s.repositoryGroup()
			ch, _, _ := repos.Character.Load(ctx, "99")
			ch.Job = "0"
			if tc.unqualifiedLucky {
				ch.Job = "1"
			}
			ch.Level = 1
			ch.Stats["grow_type"] = 0
			ch.Stats["exp"] = 90
			if ch.Stats[currentLuckyNewCharacterStat] != 0 {
				t.Fatal("expected an existing role without a creation marker")
			}
			if err := repos.Character.Save(ctx, ch); err != nil {
				t.Fatal(err)
			}
			if err := repos.Skill.Save(ctx, dnfrepo.SkillRecord{CharacterID: "99", Points: dnfrepo.SkillPointState{TotalSP: 10, RemainingSP: 10, SyncedLevel: 1}}); err != nil {
				t.Fatal(err)
			}
			if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "99", Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
				t.Fatal(err)
			}
			enablePortedActivityTest(t, s, session)
			s.luckyCharacterCatalog = &currentLuckyCatalog{Jobs: [][2]uint32{{0, 0}}, Clear: 15, Party: 10}
			if tc.newPolicy {
				s.luckyCharacterCatalog.Level1To89Only = true
				s.luckyCharacterCatalog.Clear, s.luckyCharacterCatalog.Party = 30, 30
			}
			if tc.returnee {
				session.returneePreparedCharacter = session.selectedCharacterID
				session.returneePreparedGeneration = session.characterGeneration
				session.returneePreparedExpiresAt = s.gameplayNow().Add(time.Hour).Unix()
			}
			r.Character = ch
			r.Dungeon.Metadata.BasisLevel.Set = true
			r.Dungeon.Metadata.BasisLevel.Value = 1
			r.Dungeon.Metadata.ExperienceIncreasingPoint.Set = true
			r.Dungeon.Metadata.ExperienceIncreasingPoint.Value = 1
			r.Request.Difficulty = 1
			r.DropOwner = newRuntimeDungeonDropOwner()
			r.settlementPlayResultReceived = true
			r.settlementPlayResultBody = make([]byte, currentDungeonPlayResultBaseSize)
			r.settlementPlayResultBody[currentDungeonPlayResultClientRankPointOffset] = 73
			r.startedAt = time.Unix(1700000000, 0)
			r.clearMapCompletionAt = r.startedAt.Add(12 * time.Second)
			want := int64(90 + 200 + 40 + tc.wantLucky + tc.wantWelcome)
			if tc.party {
				r.partyInstance = &dungeonPartyInstance{members: map[uint16]time.Time{99: time.Now(), 98: time.Now()}}
			}
			// Card allocation is covered by the card suite; freeze its state so this
			// check isolates the real progression transaction and party EXP roster.
			r.settlementCardRewardState = &dungeonCardState{}
			resources := currentDungeonSettlementTestResources(t)
			for i := 0; i < 2; i++ {
				r.settlementResultPlan = nil
				if i == 1 {
					// Eligibility can change while retrying the committed packet plan.
					s.luckyCharacterCatalog = nil
					session.returneePreparedExpiresAt = 0
				}
				if err := s.produceCurrentDungeonSettlementPlanWithResourcesLocked(ctx, session, r, resources); err != nil {
					t.Fatal(err)
				}
				got, _, _ := repos.Character.Load(ctx, "99")
				if got.Stats["exp"] != want {
					t.Fatalf("replay %d exp=%d want=%d", i, got.Stats["exp"], want)
				}
				if len(r.settlementResultPlan.ClearRewardBody) < currentDungeonClearRewardMinimumBodySize {
					t.Fatal("通关奖励报文缺失")
				}
				body := r.settlementResultPlan.ClearRewardBody
				// 固定线上的10号字段，而不是只用常量自证：幸运与回归独立，
				// 重放资格变化不挪栏、不多发经验；本夹具另有40评分经验，
				// 没有成长契约，不能把评分经验误当成契约栏。
				if binary.LittleEndian.Uint32(body[:4]) != 200 || binary.LittleEndian.Uint32(body[4:8]) != 40 {
					t.Fatal("基础或评分通关经验被显示修正改变")
				}
				for index, expected := range map[int]uint32{0: tc.wantWelcome, 8: 0, 9: 0, 10: tc.wantLucky} {
					if value := binary.LittleEndian.Uint32(body[17+4*index:]); value != expected {
						t.Fatalf("重放%d，通关经验栏[%d]=%d，期望%d", i, index, value, expected)
					}
				}
			}
		})
	}
}

func TestPortedReturneeLuckyRealPVF(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := dnfonlineevent.LoadReturneeCatalog(context.Background(), archive)
	if err != nil {
		t.Fatal(err)
	}
	gifts, err := dnfonlineevent.LoadReturneeGiftRules(archive, 2)
	if err != nil {
		t.Fatal(err)
	}
	if gifts.Level != 20 || gifts.Count != 100 || gifts.Limit != 5 || gifts.MaxPoints != 40000 || len(gifts.Points) != 6 {
		t.Fatalf("gift rules=%+v", gifts)
	}
	items, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range c.Missions {
		d, err := items.ResolveItem(uint32(m.Reward.ItemID))
		if err != nil {
			t.Fatal(err)
		}
		d, err = currentPVFItemDefinitionForGrantAt(d, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("reward %d kind=%s deadline=%v", d.ItemID, d.Kind, d.ExpirationDate)
	}
	want := []uint32{47, 48, 49, 50, 51, 52, 53, 20, 21, 22}
	for i, id := range want {
		if c.Missions[i].ID != id {
			t.Fatalf("mission[%d]=%d", i, c.Missions[i].ID)
		}
	}
	text, err := archive.ReadText("etc/chn_activatecharacter.etc")
	if err != nil {
		t.Fatal(err)
	}
	lucky, err := parseCurrentLuckyCharacterCatalog(text)
	if err != nil {
		t.Fatal(err)
	}
	wantMonster, wantClear, wantParty, wantQuest := int64(10), int64(15), int64(10), int64(10)
	if lucky.Level1To89Only {
		wantMonster, wantClear, wantParty, wantQuest = 30, 30, 30, 30
	}
	if len(lucky.Jobs) != 178 || lucky.Monster != wantMonster || lucky.Clear != wantClear || lucky.Party != wantParty || lucky.Quest != wantQuest || lucky.UniqueDrop != 50 || lucky.FatigueBurn != 100 {
		t.Fatalf("unexpected lucky catalog: %+v", lucky)
	}
	blockedText, err := archive.ReadText("etc/chn_fatigueaccelchkdungeon.etc")
	if err != nil {
		t.Fatal(err)
	}
	blocked, err := parseCurrentLuckyFatigueBlacklist(blockedText)
	if err != nil || len(blocked) != 1 || !blocked[3557] {
		t.Fatalf("真实PVF疲劳加速禁用配置不符: %v %v", blocked, err)
	}
	body := buildCurrentLuckyCharacterBody(lucky)
	if len(body) != 4+178*8 || binary.LittleEndian.Uint32(body) != 178 {
		t.Fatal("lucky list wire layout")
	}
	ch := dnfrepo.CharacterRecord{CharacterID: "7", Job: "0", Level: 90, Stats: map[string]int64{"grow_type": 33}}
	if currentLuckyCharacterMatches(lucky, ch) == lucky.Level1To89Only {
		t.Fatal("90-level eligibility does not match the level-limited policy")
	}
	if lucky.Level1To89Only {
		ch.Level = 89
		if !currentLuckyCharacterMatches(lucky, ch) {
			t.Fatal("existing Lv89 second awakening should qualify without a marker")
		}
	}
	ch.Stats["is_hardcore_mode"] = 1
	if currentLuckyCharacterMatches(lucky, ch) {
		t.Fatal("hardcore qualified")
	}
	if len(buildCurrentLuckyCharacterBody(nil)) != 4 {
		t.Fatal("empty list must clear previous state")
	}
}

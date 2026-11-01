package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/fatigue"
	"longheng.io/server/internal/modules/dnf/onlineevent"
	"longheng.io/server/internal/modules/dnf/premium"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func luckyEffectsTestService(t *testing.T, balance int64) (*Service, *gameSession, repo.Group) {
	t.Helper()
	r := memory.NewMemoryGroup()
	ctx := context.Background()
	if err := r.Account.Save(ctx, repo.AccountRecord{AccountID: "lucky-test"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Character.Save(ctx, repo.CharacterRecord{CharacterID: "99", AccountID: "lucky-test", Job: "0", Stats: map[string]int64{"grow_type": 0, "fatigue": balance, "fatigue_limit": 156, fatigue.DayKey: premium.ServiceDayID(time.Now())}}); err != nil {
		t.Fatal(err)
	}
	s := &Service{options: options{accountID: "lucky-test", gameUpperBodyCodec: gameUpperBodyCodecPlain}, repositoryProvider: func() (repo.Group, bool) { return r, true }}
	q := &gameSession{selectedCharacterID: 99, accountID: "lucky-test", conn: &bufferConn{}}
	enablePortedActivityTest(t, s, q)
	s.luckyCharacterCatalog = &currentLuckyCatalog{Jobs: [][2]uint32{{0, 0}}, UniqueDrop: 50, FatigueBurn: 100, NoFatigueBurnDungeons: map[int64]bool{3557: true}}
	return s, q, r
}

func TestLuckyFatigueTogglePersistsAndUsesNativePacketShapes(t *testing.T) {
	s, q, r := luckyEffectsTestService(t, 10)
	conn := q.conn.(*bufferConn)
	for _, enabled := range []byte{1, 0, 1} {
		conn.write.Reset()
		if err := s.handleCurrentLuckyFatigueToggle(q, []byte{enabled}); err != nil {
			t.Fatal(err)
		}
		ch, _, _ := r.Character.Load(context.Background(), "99")
		if ch.Stats["fatigue_acceleration"] != int64(enabled) || ch.Stats["fatigue"] != 10 {
			t.Fatalf("开关不应立即扣费: %v", ch.Stats)
		}
		ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
		state, rest := splitGameServerUpperPacket(t, rest)
		want := byte(0)
		if enabled == 1 {
			want = 1
		}
		if ack.Header.MsgID != 562 || ack.Header.Classification != 1 || !bytes.Equal(ack.Body, []byte{1}) || state.Header.MsgID != 490 || state.Header.Classification != 0 || !bytes.Equal(state.Body, []byte{100, 0, want}) || len(rest) != 0 {
			t.Fatalf("原生开关报文不符: ack=%+v state=%+v rest=%x", ack, state, rest)
		}
	}
	// 畸形、无资格及过期会话都不能改变已保存的选择。
	for _, body := range [][]byte{nil, {2}, {0, 0}} {
		conn.write.Reset()
		if err := s.handleCurrentLuckyFatigueToggle(q, body); err != nil {
			t.Fatal(err)
		}
		ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
		if !bytes.Equal(ack.Body, []byte{0, 0x16}) || len(rest) != 0 {
			t.Fatalf("应拒绝畸形开关: %x", ack.Body)
		}
	}
	q.loginRevoked.Store(true)
	conn.write.Reset()
	if err := s.handleCurrentLuckyFatigueToggle(q, []byte{0}); err != nil {
		t.Fatal(err)
	}
	ch, _, _ := r.Character.Load(context.Background(), "99")
	if ch.Stats["fatigue_acceleration"] != 1 {
		t.Fatal("已失效会话写入了角色")
	}
}

func TestLuckyFatigueStateClearsOnSpecialChannelAndDoesNotSpam(t *testing.T) {
	s, q, _ := luckyEffectsTestService(t, 10)
	conn := q.conn.(*bufferConn)
	if err := s.sendCurrentLuckyCharacterState(q); err != nil {
		t.Fatal(err)
	}
	first := conn.write.Len()
	if err := s.sendCurrentLuckyCharacterState(q); err != nil || conn.write.Len() != first {
		t.Fatalf("状态未变化时重复发送: %v", err)
	}
	conn.write.Reset()
	q.channel.Type = 4
	if err := s.sendCurrentLuckyCharacterState(q); err != nil {
		t.Fatal(err)
	}
	list, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	state, rest := splitGameServerUpperPacket(t, rest)
	if list.Header.MsgID != 1054 || !bytes.Equal(list.Body, make([]byte, 4)) || state.Header.MsgID != 490 || !bytes.Equal(state.Body, []byte{0, 0, 3}) || len(rest) != 0 {
		t.Fatal("特殊频道没有清除幸运角色及燃烧状态")
	}
	q.channel.Type = 0
	if err := s.sendCurrentLuckyCharacterState(q); err != nil {
		t.Fatal(err)
	}
	conn.write.Reset()
	if _, err := s.activityControls.Save(context.Background(), s.activityControls.Snapshot().Revision, []onlineevent.ControlChange{{ID: 1054, Enabled: false}}); err != nil {
		t.Fatal(err)
	}
	if err := s.initializeActivityControls(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.sendCurrentLuckyCharacterState(q); err != nil {
		t.Fatal(err)
	}
	list, rest = splitGameServerUpperPacket(t, conn.write.Bytes())
	state, rest = splitGameServerUpperPacket(t, rest)
	if !bytes.Equal(list.Body, make([]byte, 4)) || !bytes.Equal(state.Body, []byte{0, 0, 3}) || len(rest) != 0 {
		t.Fatal("活动关闭后没有清除原生加成状态")
	}
}

func TestLuckyFatigueRoomDebitIsPaidPersonalAndIdempotent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		balance int64
		enabled bool
		dungeon int64
		want    int64
		bonus   int64
	}{
		{"默认关闭", 10, false, 310, 9, 0}, {"开启扣两点", 10, true, 310, 8, 100}, {"最后一点", 1, true, 310, 0, 0}, {"零点", 0, true, 310, 0, 0}, {"PVF禁用", 10, true, 3557, 9, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, q, r := luckyEffectsTestService(t, tc.balance)
			ch, _, _ := r.Character.Load(context.Background(), "99")
			if tc.enabled {
				ch.Stats["fatigue_acceleration"] = 1
			}
			if err := r.Character.Save(context.Background(), ch); err != nil {
				t.Fatal(err)
			}
			runtime := &runtimeDungeonState{Dungeon: worldmap.Dungeon{ID: tc.dungeon}}
			scene := worldmap.DungeonRoomScene{Coordinate: worldmap.RoomCoordinate{X: 1}}
			for i := 0; i < 2; i++ {
				if err := s.debitDungeonRoomFatigue(q, runtime, scene); err != nil {
					t.Fatal(err)
				}
			}
			ch, _, _ = r.Character.Load(context.Background(), "99")
			if ch.Stats["fatigue"] != tc.want || runtime.luckyFatigueRooms[scene.Coordinate] != tc.bonus {
				t.Fatalf("扣费与经验资格不符: %v %v", ch.Stats, runtime.luckyFatigueRooms)
			}
			if runtime.fatigueSpent != tc.balance-tc.want {
				t.Fatalf("实际扣费统计不符: %d", runtime.fatigueSpent)
			}
		})
	}
}

func TestLuckyDailyEntryFatigueUsesSamePaidRule(t *testing.T) {
	s, q, r := luckyEffectsTestService(t, 10)
	ch, _, _ := r.Character.Load(context.Background(), "99")
	ch.Stats["fatigue_acceleration"] = 1
	assets, err := s.currentDailyCountEntryAssets(q, worldmap.Dungeon{ID: 310}, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := assets.inTransaction(&ch); err != nil {
		t.Fatal(err)
	}
	if ch.Stats["fatigue"] != 8 || assets.luckyBurn.BonusPercent != 100 || assets.luckyBurn.Cost != 2 {
		t.Fatal("周期入场与普通房间的燃烧规则不一致")
	}
	assets, err = s.currentDailyCountEntryAssets(q, guildDungeonFatigueTestDungeon(), true)
	if err != nil {
		t.Fatal(err)
	}
	ch.Stats["fatigue"] = 20
	if err := assets.inTransaction(&ch); err != nil {
		t.Fatal(err)
	}
	if ch.Stats["fatigue"] != 12 || assets.luckyBurn.BonusPercent != 0 {
		t.Fatal("公会定额扣费被加倍")
	}
}

func TestLuckyUniqueProbabilityAddsOnlyUniqueWeightAndUsesRecipient(t *testing.T) {
	base := [7]int64{200000, 500000, 900000, 950000, 980000, 990000, 1000000}
	got := currentLuckyRarityThresholds(base, false, 50)
	if got != ([7]int64{175000, 475000, 875000, 950000, 980000, 990000, 1000000}) {
		t.Fatalf("神器概率不符: %v", got)
	}
	if got := currentLuckyRarityThresholds(base, true, 50); got[2] != 815000 || got[3] != base[3] || got[6] != base[6] {
		t.Fatalf("与大富翁叠加不符: %v", got)
	}
	if currentLuckyRarityThresholds(base, false, 0) != base {
		t.Fatal("无资格改变了原概率")
	}
	s, q, r := luckyEffectsTestService(t, 10)
	ch, _, _ := r.Character.Load(context.Background(), "99")
	if s.currentLuckyUniqueDropPercent(q, ch) != 50 {
		t.Fatal("本人没有获得神器加成")
	}
	peer := repo.CloneCharacter(ch)
	peer.CharacterID = "100"
	if s.currentLuckyUniqueDropPercent(q, peer) != 0 {
		t.Fatal("队友借用了击杀者资格")
	}
	peerSession := &gameSession{selectedCharacterID: 100, accountID: ch.AccountID}
	peer.Job = "1"
	if s.currentLuckyUniqueDropPercent(peerSession, peer) != 0 {
		t.Fatal("未列入幸运职业的队友获得加成")
	}
	peer.Job = "0"
	peer.Stats["is_hardcore_mode"] = 1
	if s.currentLuckyUniqueDropPercent(peerSession, peer) != 0 {
		t.Fatal("极限角色获得加成")
	}
}

func TestLuckyFatigueMonsterExperienceUsesPaidRoomSnapshot(t *testing.T) {
	s, r, repos := prepareCurrentDungeonMonsterExperienceTest(t)
	ch, _, _ := repos.Character.Load(context.Background(), "99")
	enablePortedActivityTest(t, s, nil)
	s.luckyCharacterCatalog = &currentLuckyCatalog{Jobs: [][2]uint32{{0, 0}}, Monster: 10, FatigueBurn: 100}
	r.Character = ch
	scene, ok := r.Session.Scene()
	if !ok {
		t.Fatal("缺少房间")
	}
	recordCurrentLuckyFatigueRoom(r, scene.Coordinate, 100)
	q := &gameSession{selectedCharacterID: 99, dungeon: dungeonSessionState{runtime: r}}
	result, err := s.awardCurrentDungeonMonsterExperience(context.Background(), q, r, r.Room.Snapshot().Monsters[0])
	if err != nil {
		t.Fatal(err)
	}
	if result.FatigueBurnBonus != 200 || result.LuckyCharacterBonus != 20 || result.Character.Stats["exp"] != 420 {
		t.Fatalf("燃烧与普通幸运加成应独立: %+v", result)
	}
	recordCurrentDungeonMonsterExperience(r, r.Room.Snapshot().Monsters[0], result)
	if r.settlementMonsterFatigueBurnBonus != 200 {
		t.Fatal("未投影燃烧经验统计")
	}
	body := buildCurrentFinishLoadingCharacterStateBodyWithPresentation(result.Character, result.Skill.Points, &currentFinishLoadingExperiencePresentation{FatigueBurnBonus: &result.FatigueBurnBonus})
	if binary.LittleEndian.Uint32(body[38:42]) != 200 {
		t.Fatal("逐次击杀经验通知缺少燃烧展示")
	}
}

func TestLuckyEffectConfigOptionalRatiosAndBlacklist(t *testing.T) {
	base := "[activate character schedule]\n0 0 0 0\n[/activate character schedule]\n[monster kill exp buff ratio]\n10\n[dungeon clear exp buff ratio]\n15\n[party play buff ratio]\n10\n[quest clear buff ratio]\n10\n"
	for _, v := range []string{"50", "-1", "1001", "`bad`", "50 60"} {
		c, err := parseCurrentLuckyCharacterCatalog(base + "[unique drop ratio]\n" + v + "\n[fatigue accel percent]\n100\n")
		if v == "50" {
			if err != nil || c.UniqueDrop != 50 || c.FatigueBurn != 100 {
				t.Fatalf("配置不符: %+v %v", c, err)
			}
		} else if err == nil {
			t.Fatalf("接受了无效配置: %s", v)
		}
	}
	c, err := parseCurrentLuckyCharacterCatalog(base)
	if err != nil || c.UniqueDrop != 0 || c.FatigueBurn != 0 {
		t.Fatal("旧配置默认值错误")
	}
	for _, text := range []string{"[unable dungeon]\n3557\n[/unable dungeon]", "[unable dungeon]\n`bad`\n[/unable dungeon]", "[other]\n1"} {
		blocked, err := parseCurrentLuckyFatigueBlacklist(text)
		if strings.Contains(text, "3557") {
			if err != nil || !blocked[3557] {
				t.Fatal("未读取禁用副本")
			}
		} else if err == nil {
			t.Fatal("接受了无效禁用配置")
		}
	}
	if got := buildCurrentLuckyFatigueBody(100, true, false); binary.LittleEndian.Uint16(got[:]) != 100 || got[2] != 0 {
		t.Fatal("490字段宽度不符")
	}
}

// 现用EXE原生0x21341f0实际控件调用：0可操作未勾选、1可操作已勾选、3禁用。
// 该契约来自隔离执行原生UI函数，不能用全局场景状态3推导为开启。
func TestLuckyFatigueStateMatchesNativeCheckboxContract(t *testing.T) {
	for _, tc := range []struct {
		percent            int64
		available, enabled bool
		want               [3]byte
	}{
		{100, true, false, [3]byte{100, 0, 0}},
		{100, true, true, [3]byte{100, 0, 1}},
		{100, false, true, [3]byte{0, 0, 3}},
		{0, true, true, [3]byte{0, 0, 3}},
		{1001, true, true, [3]byte{0, 0, 3}},
	} {
		if got := buildCurrentLuckyFatigueBody(tc.percent, tc.available, tc.enabled); got != tc.want {
			t.Fatalf("原生复选框契约不符：%+v 得到%x", tc, got)
		}
	}
}

func TestLuckyFatigueClearRewardReplayKeepsPaidBonus(t *testing.T) {
	ctx := context.Background()
	s, r, q, _, _ := prepareCompletedSettlementRuntime(t)
	repos, _ := s.repositoryGroup()
	ch, _, _ := repos.Character.Load(ctx, "99")
	ch.Job = "0"
	ch.Level = 1
	ch.Stats["grow_type"] = 0
	ch.Stats["exp"] = 90
	if err := repos.Character.Save(ctx, ch); err != nil {
		t.Fatal(err)
	}
	if err := repos.Skill.Save(ctx, repo.SkillRecord{CharacterID: "99", Points: repo.SkillPointState{TotalSP: 10, RemainingSP: 10, SyncedLevel: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "99", Slots: map[string]repo.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	enablePortedActivityTest(t, s, q)
	s.luckyCharacterCatalog = &currentLuckyCatalog{Jobs: [][2]uint32{{0, 0}}, Clear: 15, FatigueBurn: 100}
	r.Character = ch
	r.Dungeon.Metadata.BasisLevel = worldmap.OptionalInt{Set: true, Value: 1}
	r.Dungeon.Metadata.ExperienceIncreasingPoint = worldmap.OptionalNumber{Set: true, Value: 1}
	r.Request.Difficulty = 1
	r.DropOwner = newRuntimeDungeonDropOwner()
	r.settlementPlayResultReceived = true
	r.settlementPlayResultBody = make([]byte, currentDungeonPlayResultBaseSize)
	r.settlementPlayResultBody[currentDungeonPlayResultClientRankPointOffset] = 73
	r.startedAt = time.Unix(1700000000, 0)
	r.clearMapCompletionAt = r.startedAt.Add(12 * time.Second)
	r.settlementCardRewardState = &dungeonCardState{}
	scene, ok := r.Session.Scene()
	if !ok {
		t.Fatal("缺少结算房间")
	}
	recordCurrentLuckyFatigueRoom(r, scene.Coordinate, 100)
	r.settlementMonsterFatigueBurnBonus = 123
	resources := currentDungeonSettlementTestResources(t)
	for i := 0; i < 2; i++ {
		r.settlementResultPlan = nil
		if i == 1 {
			r.luckyFatigueRooms = nil
			s.luckyCharacterCatalog = nil
		}
		if err := s.produceCurrentDungeonSettlementPlanWithResourcesLocked(ctx, q, r, resources); err != nil {
			t.Fatal(err)
		}
		got, _, _ := repos.Character.Load(ctx, "99")
		if got.Stats["exp"] != 560 {
			t.Fatalf("燃烧经验提交/重放不符: %d", got.Stats["exp"])
		}
		body := r.settlementResultPlan.ClearRewardBody
		for idx, want := range map[int]uint32{0: 0, 10: 30, currentDungeonClearRewardBonusClearFatigueBurnIndex: 200, currentDungeonClearRewardBonusMonsterFatigueBurnIndex: 123} {
			if got := binary.LittleEndian.Uint32(body[17+idx*4:]); got != want {
				t.Fatalf("结算燃烧栏[%d]=%d,期望%d", idx, got, want)
			}
		}
	}
}

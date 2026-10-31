package dnfbridge

import (
	"context"
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/adventuregroup"
	dnfhonor "longheng.io/server/internal/modules/dnf/honor"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 使用现用真实PVF的怪物、经验和名誉表，房间及角色资产保持隔离。
func TestRealPVFDungeonDeathBurstExperienceAndHonor(t *testing.T) {
	t.Run("满级经验及名誉", func(t *testing.T) { testRealPVFDungeonDeathBurstExperience(t, true) })
	t.Run("未满级经验", func(t *testing.T) { testRealPVFDungeonDeathBurstExperience(t, false) })
}

func TestRealPVFDungeonDeathQuestNegativeKeepsExperienceAndHonor(t *testing.T) {
	t.Run("满级", func(t *testing.T) { testRealPVFDungeonDeathBurstExperience(t, true, true) })
	t.Run("未满级", func(t *testing.T) { testRealPVFDungeonDeathBurstExperience(t, false, true) })
}

func TestRealPVFDungeonDeathUnrelatedActiveTasksKeepExperienceAndTargets(t *testing.T) {
	archive := real90DropArchive(t)
	ctx := context.Background()
	index, err := dnfpvf.Build(ctx, archive, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := dnfquest.Load(ctx, index)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{2683, 4874, 8643} {
		if _, found := catalog.Find(id); !found {
			t.Fatalf("真实活动任务%d缺失", id)
		}
	}
	if work := catalog.DungeonKillWork([]int64{2683, 4874, 8643}, dnfquest.HuntEnemyKillInput{
		DungeonID: 310, Difficulty: 2, EnemyCode: 107005202, EnemyType: 1,
	}); work != (dnfquest.DungeonKillWork{}) || len(currentQuestDropMatchEnemy(catalog, []int64{2683, 4874, 8643}, 310, 2, 1, 107005202)) != 0 {
		t.Fatalf("现场三个无关任务仍触发普通怪任务：%+v", work)
	}
	if drops := currentQuestDropMatchEnemy(catalog, []int64{2683}, 243, 0, 1, 63413); len(drops) != 1 || drops[0].ItemID != 10093922 {
		t.Fatalf("安徒恩真实任务物品被过滤：%+v", drops)
	}
	if drops := currentQuestDropMatchEnemy(catalog, []int64{4874}, 52, 1, 3, 109006893); len(drops) != 1 || drops[0].ItemID != 10164919 {
		t.Fatalf("毒草真实任务物品被过滤：%+v", drops)
	}
	for _, capped := range []bool{false, true} {
		t.Run(map[bool]string{false: "未满级", true: "满级"}[capped], func(t *testing.T) {
			testRealPVFDungeonDeathBurstExperience(t, capped, true, true)
		})
	}
}

func testRealPVFDungeonDeathBurstExperience(t *testing.T, capped bool, narrow ...bool) {
	a := real90DropArchive(t)
	s, p, repos, packets := prepareDungeonDeathBurstTest(t, capped)
	var taskProbe *deathQuestStatusProbe
	if len(narrow) > 0 && narrow[0] {
		taskProbe = &deathQuestStatusProbe{deathQuestFullProbe: &deathQuestFullProbe{QuestRepository: repos.Quest}}
		repos.Quest = taskProbe
	}
	if len(narrow) > 1 && narrow[1] {
		saveUnrelatedDeathQuests(t, repos.Quest)
		s.initialEquipmentArchive = a
		s.questCatalog, s.questCatalogLoadErr = nil, nil
		var err error
		s.questCatalog, err = s.loadQuestCatalog(context.Background())
		if err != nil {
			t.Fatal(err)
		}
	}
	var err error
	s.dungeonMonsterTable, err = newPVFDungeonMonsterCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	s.honorTable, err = dnfhonor.LoadTables(a)
	if err != nil {
		t.Fatal(err)
	}
	s.initialEquipmentArchive = a
	s.adventureGroupTable, err = adventuregroup.LoadComplete(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	account, _, err := repos.Account.Load(context.Background(), "account-1")
	if err != nil {
		t.Fatal(err)
	}
	account.AccountID = "account-1"
	if err := repos.Account.Save(context.Background(), account); err != nil {
		t.Fatal(err)
	}
	if !capped {
		character, _, _ := repos.Character.Load(context.Background(), "99")
		character.Level = 85
		if err := repos.Character.Save(context.Background(), character); err != nil {
			t.Fatal(err)
		}
		skill, _, _ := repos.Skill.Load(context.Background(), "99")
		skill.Points.SyncedLevel = 85
		if err := repos.Skill.Save(context.Background(), skill); err != nil {
			t.Fatal(err)
		}
		p.dungeon.runtime.Character = character
	}
	const monsterID = 107005202 // 本机现场80496房间的普通怪。
	definition, found, err := s.dungeonMonsterTable.Find(monsterID)
	if err != nil || !found {
		t.Fatalf("真实怪物不存在：found=%t err=%v", found, err)
	}
	r := p.dungeon.runtime
	for i := range r.Room.monsters {
		m := &r.Room.monsters[i]
		m.Spawn.MonsterID, m.Spawn.Level, m.Spawn.AutoLevel = monsterID, 0, 85
		m.Definition = definition
	}
	resources, err := currentDungeonMonsterExperienceResourcesForCatalog(s.dungeonMonsterTable)
	if err != nil {
		t.Fatal(err)
	}
	award, err := currentDungeonMonsterExperienceAwardFor(r, r.Room.monsters[0], resources.Sources)
	if err != nil || award.Gain == 0 {
		t.Fatalf("真实经验计算失败：award=%+v err=%v", award, err)
	}
	probe := &deathNotifyProgressionProbe{base: repos.CharacterProgression}
	accountProbe := &deathBurstSettlementProbe{base: repos.CharacterSettlement}
	probe.before = func() {
		deaths := 0
		for _, frame := range splitAllCurrentGameServerUpperPackets(t, p.conn.(*bufferConn).write.Bytes()) {
			if frame.Header.MsgID == 38 {
				deaths++
			}
		}
		if deaths != 3 {
			t.Fatalf("真实PVF经验事务挡住后续死亡：%d/3", deaths)
		}
	}
	accountProbe.before = probe.before
	repos.CharacterProgression = probe
	repos.CharacterSettlement = accountProbe
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	if err := s.handleCurrentDungeonDeathBurstPackets(p, packets[:3], false); err != nil {
		t.Fatal(err)
	}
	if len(narrow) > 1 && narrow[1] && (taskProbe.queries != 3 || taskProbe.loads != 0) {
		t.Fatalf("真实无关任务仍完整读取：queries=%d loads=%d", taskProbe.queries, taskProbe.loads)
	}
	character, found, err := repos.Character.Load(context.Background(), "99")
	wantOrdinaryEXP := int64(award.Gain) * 3
	if capped {
		wantOrdinaryEXP = 0 // 与击杀前相同，op37不产生普通EXP正差值。
	}
	if err != nil || !found || character.Stats["exp"] != wantOrdinaryEXP {
		t.Fatalf("真实经验汇总错误：%+v err=%v", character, err)
	}
	deaths, states := 0, 0
	for _, frame := range splitAllCurrentGameServerUpperPackets(t, p.conn.(*bufferConn).write.Bytes()) {
		if frame.Header.MsgID == 38 {
			deaths++
		}
		if frame.Header.MsgID == 37 {
			states++
			if len(frame.Body) != currentFinishLoadingCharacterStateBodySize ||
				binary.LittleEndian.Uint32(frame.Body[1:5]) != uint32(character.Stats["exp"]) ||
				binary.LittleEndian.Uint32(frame.Body[55:59]) != uint32(character.Stats[currentHonorExpertLevelStatKey]) ||
				binary.LittleEndian.Uint64(frame.Body[59:67]) != uint64(character.Stats[currentHonorExpertProgressExperienceStatKey]) {
				t.Fatalf("真实经验/名誉通知与持久化不一致：%x", frame.Body)
			}
		}
	}
	if deaths != 3 || states != 1 || probe.calls+accountProbe.calls != 1 {
		t.Fatalf("真实PVF连续击杀：死亡=%d 经验提示=%d 事务=%d", deaths, states, probe.calls+accountProbe.calls)
	}
	account, _, _ = repos.Account.Load(context.Background(), "account-1")
	state, err := adventuregroup.ParseRuntimeState(account, s.adventureGroupTable.Runtime(), s.gameplayNow())
	wantCapsule := uint64(0)
	if capped {
		wantCapsule = min(uint64(award.Gain)*3, s.adventureGroupTable.Runtime().Capsule.MaximumExperience)
	}
	if err != nil || state.GrowthExperience != wantCapsule {
		t.Fatalf("真实PVF胶囊累计=%d，期望%d：%v", state.GrowthExperience, wantCapsule, err)
	}
	t.Logf("真实怪物%d Lv85：三次死亡确认、一次经验事务、一次经验及熟练度通知通过；每只经验%d，胶囊经验%d", monsterID, award.Gain, state.GrowthExperience)
}

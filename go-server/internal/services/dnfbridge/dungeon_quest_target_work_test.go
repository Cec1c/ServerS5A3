package dnfbridge

import (
	"context"
	"reflect"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func unrelatedDeathQuestCatalog(t *testing.T) *dnfquest.Catalog {
	t.Helper()
	source := bridgePVFSource{
		dnfquest.DefaultList: "2683 `anton.qst`\n4874 `plant.qst`\n8643 `raid.qst`\n",
		"n_quest/anton.qst":  "[type]\n`[seeking]`\n[int data]\n10093922 1\n[monster reward item]\n63413 243 -1 10093922 1 100 1\n[/monster reward item]\n",
		"n_quest/plant.qst":  "[type]\n`[seeking]`\n[int data]\n10164919 10\n[enemy reward item]\n109006893 3 52 1 10164919 1 20 10\n[/enemy reward item]\n",
		"n_quest/raid.qst":   "[type]\n`[raid phase clear]`\n[int data]\n10\n",
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

func saveUnrelatedDeathQuests(t *testing.T, repo dnfrepo.QuestRepository) {
	t.Helper()
	record := dnfrepo.QuestRecord{CharacterID: "99", States: make(map[int64]dnfrepo.QuestState)}
	for id := int64(1); id <= 734; id++ {
		record.States[id] = dnfrepo.QuestState{Status: "completed", Extra: map[string]string{"reward_state": "granted", "pvf_path": "history.qst"}}
	}
	for _, id := range []int64{2683, 4874, 8643} {
		record.States[id] = dnfrepo.QuestState{Status: "active", ProgressValue: 1}
	}
	if err := repo.Save(context.Background(), record); err != nil {
		t.Fatal(err)
	}
}

func TestDungeonDeathUnrelatedActiveTasksKeepDropsExperienceAndTaskState(t *testing.T) {
	for _, capped := range []bool{false, true} {
		t.Run(map[bool]string{false: "未满级", true: "满级"}[capped], func(t *testing.T) {
			var originalDrops [][]byte
			var originalStats map[string]int64
			var originalTasks dnfrepo.QuestRecord
			originalLoads := 0
			for _, narrow := range []bool{false, true} {
				s, p, repos, packets := prepareDungeonDeathBurstTest(t, capped)
				s.questCatalog = unrelatedDeathQuestCatalog(t)
				saveUnrelatedDeathQuests(t, repos.Quest)
				full := &deathQuestFullProbe{QuestRepository: repos.Quest}
				probe := &deathQuestStatusProbe{deathQuestFullProbe: full}
				repos.Quest = full
				if narrow {
					repos.Quest = probe
				}
				s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
				if err := s.handleCurrentDungeonDeathBurstPackets(p, packets, false); err != nil {
					t.Fatal(err)
				}
				var drops [][]byte
				for _, frame := range splitAllCurrentGameServerUpperPackets(t, p.conn.(*bufferConn).write.Bytes()) {
					if frame.Header.MsgID == 38 {
						drops = append(drops, frame.Body)
					}
				}
				character, _, _ := repos.Character.Load(context.Background(), "99")
				tasks, _, _ := full.QuestRepository.Load(context.Background(), "99")
				if !narrow {
					originalDrops, originalStats, originalTasks, originalLoads = drops, character.Stats, tasks, full.loads
				} else {
					if len(drops) != 5 || probe.queries != 5 || full.loads >= originalLoads ||
						!reflect.DeepEqual(drops, originalDrops) || !reflect.DeepEqual(character.Stats, originalStats) || !reflect.DeepEqual(tasks, originalTasks) {
						t.Fatalf("无关任务筛选改变掉落/经验/任务：reads=%d→%d queries=%d", originalLoads, full.loads, probe.queries)
					}
					t.Logf("734条历史、3个无关活动任务、5次击杀：完整读取%d→%d，实时窄查%d次，死亡/掉落/经验/任务一致", originalLoads, full.loads, probe.queries)
				}
			}
		})
	}
}

func TestDungeonDeathTargetEligibilityIsRealtimeAndKeepsFallback(t *testing.T) {
	s, p, repos, _ := qst017Fixture(t, 3)
	full := &deathQuestFullProbe{QuestRepository: repos.Quest}
	probe := &deathQuestStatusProbe{deathQuestFullProbe: full}
	repos.Quest = probe
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	runtime := p.dungeon.runtime
	monster := runtime.Room.monsters[0]
	for _, active := range []bool{false, true, false, true} {
		status := "completed"
		if active {
			status = "\u2003ACCEPTED\t"
		}
		if err := full.QuestRepository.Save(context.Background(), dnfrepo.QuestRecord{CharacterID: "19", Progress: map[int64]dnfrepo.QuestState{3687: {Status: status, ProgressValue: 3}}}); err != nil {
			t.Fatal(err)
		}
		work := s.currentDungeonDeathQuestWorkFor(p, runtime, monster)
		if work.huntMonster != active || work.huntEnemy || work.questDrops {
			t.Fatalf("实时接取/完成或投影匹配错误：active=%t work=%+v", active, work)
		}
	}
	probe.missing = true
	if got := s.currentDungeonDeathQuestWorkFor(p, runtime, monster); got != (currentDungeonDeathQuestWork{true, true, true}) {
		t.Fatalf("读取失败没有保留原路径：%+v", got)
	}
}

func TestDungeonDeathTargetKeepsHuntEnemyAndLinkedChildActivation(t *testing.T) {
	for _, linked := range []bool{false, true} {
		t.Run(map[bool]string{false: "正常杀敌", true: "主任务同次补建子任务"}[linked], func(t *testing.T) {
			s, p, repos, key := qst017Fixture(t, 3)
			grade := "[epic]"
			extra, list := "", "3687 `enemy.qst`\n"
			if linked {
				grade, extra = "[sub]", "[main quest]\n9000\n"
				list += "9000 `parent.qst`\n"
			}
			source := bridgePVFSource{
				dnfquest.DefaultList: list,
				"n_quest/enemy.qst":  "[grade]\n`" + grade + "`\n[type]\n`[hunt enemy]`\n[int data]\n72 -1 56158 1 3\n" + extra,
				"n_quest/parent.qst": "[grade]\n`[epic]`\n[type]\n`[quest clear]`\n[int data]\n3687\n",
			}
			index, err := dnfpvf.Build(context.Background(), source, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
			if err != nil {
				t.Fatal(err)
			}
			s.questCatalog, err = dnfquest.Load(context.Background(), index)
			if err != nil {
				t.Fatal(err)
			}
			if linked {
				if err := repos.Quest.Save(context.Background(), dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{9000: {Status: "active", ProgressValue: 1}}}); err != nil {
					t.Fatal(err)
				}
			}
			probe := &deathQuestStatusProbe{deathQuestFullProbe: &deathQuestFullProbe{QuestRepository: repos.Quest}}
			repos.Quest = probe
			s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
			if err := qst017Death(s, p, key); err != nil {
				t.Fatal(err)
			}
			if state := qst017State(t, repos); state.ProgressValue != 2 || probe.queries != 1 {
				t.Fatalf("杀敌/同次关联任务没有推进：state=%+v query=%d", state, probe.queries)
			}
		})
	}
}

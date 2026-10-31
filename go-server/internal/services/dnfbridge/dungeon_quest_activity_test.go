package dnfbridge

import (
	"context"
	"errors"
	"reflect"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

type deathQuestFullProbe struct {
	dnfrepo.QuestRepository
	loads int
}

// The personal-drop journal reads inside its own transaction. Observe that
// actual read rather than requiring the retired outer-repository read path.
type deathQuestPreparationProbe struct {
	dnfrepo.QuestMaterialPreparationUnitOfWork
	loads int
}

func (p *deathQuestPreparationProbe) WithinQuestMaterialPreparation(ctx context.Context, ids []string, apply func(dnfrepo.Group) error) error {
	return p.QuestMaterialPreparationUnitOfWork.WithinQuestMaterialPreparation(ctx, ids, func(tx dnfrepo.Group) error {
		probe := &deathQuestFullProbe{QuestRepository: tx.Quest}
		tx.Quest = probe
		err := apply(tx)
		p.loads += probe.loads
		return err
	})
}

func TestDungeonDeathQuestActiveMonsterItemDropIsNotSkipped(t *testing.T) {
	s, p, repos, key := qst017Fixture(t, 2)
	source := bridgePVFSource{
		dnfquest.DefaultList:        "3687 `ordinary_drop.qst`\n",
		"n_quest/ordinary_drop.qst": "[grade]\n`[normal]`\n[type]\n`[seeking]`\n[int data]\n10164883 1\n[monster reward item]\n56158 72 -1 10164883 1 100 1\n[/monster reward item]\n",
	}
	index, err := dnfpvf.Build(context.Background(), source, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	s.questCatalog, err = dnfquest.Load(context.Background(), index)
	if err != nil {
		t.Fatal(err)
	}
	s.pvfItemCatalog = seekerQST022ItemCatalog()
	full := &deathQuestFullProbe{QuestRepository: repos.Quest}
	probe := &deathQuestStatusProbe{deathQuestFullProbe: full}
	repos.Quest = probe
	preparation := &deathQuestPreparationProbe{QuestMaterialPreparationUnitOfWork: repos.QuestMaterialPlan}
	repos.QuestMaterialPlan = preparation
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	if err := qst017Death(s, p, key); err != nil {
		t.Fatal(err)
	}
	inv, _, err := repos.Inventory.Load(context.Background(), "19")
	if err != nil || currentQuestDropHeldCount(inv, int64(seekerQST022ItemID)) != 1 || preparation.loads == 0 || probe.queries != 1 {
		t.Fatalf("正常怪任务物品未发放：transactionReads=%d query=%d err=%v", preparation.loads, probe.queries, err)
	}
	if err := qst017Death(s, p, key); err != nil {
		t.Fatal(err)
	}
	inv, _, _ = repos.Inventory.Load(context.Background(), "19")
	if currentQuestDropHeldCount(inv, int64(seekerQST022ItemID)) != 1 {
		t.Fatal("重复死亡重复掉落任务物品")
	}
}

func (p *deathQuestFullProbe) Load(ctx context.Context, id string) (dnfrepo.QuestRecord, bool, error) {
	p.loads++
	return p.QuestRepository.Load(ctx, id)
}

type deathQuestStatusProbe struct {
	*deathQuestFullProbe
	queries int
	failure error
	missing bool
}

func (p *deathQuestStatusProbe) LoadCharacterQuestActivities(ctx context.Context, id string) ([]dnfrepo.QuestActivity, bool, error) {
	p.queries++
	if p.failure != nil || p.missing {
		return nil, false, p.failure
	}
	record, _, err := p.QuestRepository.Load(ctx, id)
	var activities []dnfrepo.QuestActivity
	for _, states := range []map[int64]dnfrepo.QuestState{record.States, record.Progress} {
		for id, state := range states {
			activities = append(activities, dnfrepo.QuestActivity{QuestID: id, Status: state.Status})
		}
	}
	return activities, true, err
}

func (p *deathQuestStatusProbe) LoadCharacterQuestStatuses(ctx context.Context, id string) ([]string, bool, error) {
	p.queries++
	if p.failure != nil || p.missing {
		return nil, false, p.failure
	}
	record, _, err := p.QuestRepository.Load(ctx, id)
	var statuses []string
	for _, states := range []map[int64]dnfrepo.QuestState{record.States, record.Progress} {
		for _, state := range states {
			statuses = append(statuses, state.Status)
		}
	}
	return statuses, true, err
}

func TestDungeonDeathQuestNegativeReadPreservesDropsAndExperience(t *testing.T) {
	for _, capped := range []bool{false, true} {
		t.Run(map[bool]string{false: "未满级", true: "满级"}[capped], func(t *testing.T) {
			var referenceDrops [][]byte
			var referenceStats map[string]int64
			var referenceLoads int
			for _, narrow := range []bool{false, true} {
				s, p, repos, packets := prepareDungeonDeathBurstTest(t, capped)
				full := &deathQuestFullProbe{QuestRepository: repos.Quest}
				status := &deathQuestStatusProbe{deathQuestFullProbe: full}
				repos.Quest = full
				if narrow {
					repos.Quest = status
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
				character, _, err := repos.Character.Load(context.Background(), "99")
				if err != nil || len(drops) != len(packets) {
					t.Fatalf("deaths=%d err=%v", len(drops), err)
				}
				if !narrow {
					referenceDrops, referenceStats = drops, character.Stats
					referenceLoads = full.loads
					if full.loads == 0 {
						t.Fatal("完整对照没有读取任务")
					}
				} else {
					if full.loads >= referenceLoads || status.queries != len(packets) {
						t.Fatalf("full=%d realtime=%d", full.loads, status.queries)
					}
					t.Logf("%d只击杀的完整任务读取%d→%d；实时状态查询%d次", len(packets), referenceLoads, full.loads, status.queries)
					if !reflect.DeepEqual(referenceDrops, drops) || !reflect.DeepEqual(referenceStats, character.Stats) {
						t.Fatalf("普通掉落、死亡报文或经验变化：stats=%v want=%v", character.Stats, referenceStats)
					}
				}
			}
		})
	}
}

func TestDungeonDeathQuestStatusesKeepActiveAndRealtimeTransitions(t *testing.T) {
	s, p, repos, _ := prepareDungeonDeathBurstTest(t, false)
	probe := &deathQuestStatusProbe{deathQuestFullProbe: &deathQuestFullProbe{QuestRepository: repos.Quest}}
	repos.Quest = probe
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	for _, tc := range []struct {
		status string
		active bool
	}{{"completed", false}, {"active", true}, {" ACCEPTED\t", true}, {"InProgress", true}, {"progress", true}, {"title_book_active", false}, {"áctive", false}, {"completed", false}} {
		status := tc.status
		if err := probe.QuestRepository.Save(context.Background(), dnfrepo.QuestRecord{CharacterID: "99", Progress: map[int64]dnfrepo.QuestState{7: {Status: status}}}); err != nil {
			t.Fatal(err)
		}
		want := tc.active
		if got := s.currentDungeonDeathNeedsQuestWork(p); got != want {
			t.Fatalf("status=%q got=%t want=%t", status, got, want)
		}
	}
	if probe.queries != 8 {
		t.Fatal("任务资格被跨击杀缓存")
	}
	for _, fail := range []bool{false, true} {
		probe.missing = !fail
		if fail {
			probe.failure = errors.New("只读查询失败")
		}
		if !s.currentDungeonDeathNeedsQuestWork(p) {
			t.Fatal("缺角色/查询失败必须恢复完整路径")
		}
	}
}

func TestDungeonDeathQuestActiveHuntAndReadFailureKeepOriginalPath(t *testing.T) {
	for _, failure := range []bool{false, true} {
		s, p, repos, key := qst017Fixture(t, 3)
		full := &deathQuestFullProbe{QuestRepository: repos.Quest}
		probe := &deathQuestStatusProbe{deathQuestFullProbe: full}
		if failure {
			probe.failure = errors.New("只读查询失败")
		}
		repos.Quest = probe
		s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
		if err := qst017Death(s, p, key); err != nil {
			t.Fatal(err)
		}
		if qst017State(t, repos).ProgressValue != 2 || full.loads == 0 || probe.queries != 1 {
			t.Fatalf("active/fallback loads=%d queries=%d", full.loads, probe.queries)
		}
		if err := qst017Death(s, p, key); err != nil {
			t.Fatal(err)
		}
		if qst017State(t, repos).ProgressValue != 2 {
			t.Fatal("重放重复推进正常任务")
		}
	}
}

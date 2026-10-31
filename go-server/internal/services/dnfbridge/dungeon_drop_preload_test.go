package dnfbridge

import (
	"context"
	"errors"
	"testing"
	"time"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

type dungeonGenericPoolReadProbe struct {
	source dnfpvf.Source
	reads  int
}

func (p *dungeonGenericPoolReadProbe) ReadText(name string) (string, error) {
	p.reads++
	return p.source.ReadText(name)
}

func genericDropPreloadTestSource() bridgePVFSource {
	return bridgePVFSource{
		"monster/monster.lst":              "2 `monster.mob`\n",
		"stackable/stackable.lst":          "6004 `consumable/potion.stk`\n",
		"equipment/equipment.lst":          "9001 `weapon/test.equ`\n",
		"stackable/consumable/potion.stk":  "[creation rate]\n1\n[grade]\n5\n[rarity]\n0\n",
		dungeonDropEquipmentDictionaryPath: "9001 0 5 0 10000 1 `test`\n",
	}
}

func TestDungeonGenericDropPreloadUsesMonsterCacheAndKeepsDrawSequence(t *testing.T) {
	probe := &dungeonGenericPoolReadProbe{source: genericDropPreloadTestSource()}
	monsters, err := newPVFDungeonMonsterCatalog(probe)
	if err != nil {
		t.Fatal(err)
	}
	decoy, err := newPVFDungeonDropCatalog(probe)
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{dungeonMonsterTable: monsters, pvfItemCatalog: decoy}
	if err := s.preloadDungeonGenericDropPools(context.Background()); err != nil {
		t.Fatal(err)
	}
	drops, err := monsters.DropCatalog()
	if err != nil || !drops.genericLoaded || decoy.genericLoaded {
		t.Fatal("预载未进入怪物实际掉落缓存，或预载了无关背包目录")
	}
	reads := probe.reads
	for i := 0; i < 3; i++ {
		if err := s.preloadDungeonGenericDropPools(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	rng := newCurrentDungeonDropLCG(42)
	item, selected, err := drops.SelectGenericStackable(rng, 5, 0, 1, 0)
	if err != nil || !selected || item != 6004 || probe.reads != reads {
		t.Fatalf("已预载的抽取仍扫描PVF：item=%d selected=%t reads=%d/%d %v", item, selected, probe.reads, reads, err)
	}
	cold, err := newPVFDungeonDropCatalog(genericDropPreloadTestSource())
	if err != nil {
		t.Fatal(err)
	}
	baseline := newCurrentDungeonDropLCG(42)
	other, selected, err := cold.SelectGenericStackable(baseline, 5, 0, 1, 0)
	if err != nil || !selected || item != other || rng.Seed() != baseline.Seed() {
		t.Fatal("预载改变了选物或随机数消耗")
	}
}

func TestDungeonGenericDropPreloadPreservesCancellationAndMissingTableFallback(t *testing.T) {
	source := genericDropPreloadTestSource()
	delete(source, dungeonDropEquipmentDictionaryPath)
	probe := &dungeonGenericPoolReadProbe{source: source}
	monsters, err := newPVFDungeonMonsterCatalog(probe)
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{dungeonMonsterTable: monsters}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reads := probe.reads
	if err := s.preloadDungeonGenericDropPools(ctx); !errors.Is(err, context.Canceled) || probe.reads != reads {
		t.Fatal("启动已取消仍进行了整表扫描")
	}
	if err := s.preloadDungeonGenericDropPools(context.Background()); err != nil {
		t.Fatal("预载改变了既有缺表降级策略")
	}
	drops, _ := monsters.DropCatalog()
	reads = probe.reads
	if _, _, err := drops.SelectGenericStackable(newCurrentDungeonDropLCG(42), 5, 0, 1, 0); err == nil || probe.reads != reads {
		t.Fatal("缺表被伪造为正常奖励，或失败后重复扫描整表")
	}
}

func TestRealPVFDungeonGenericDropPreloadAvoidsColdRollScan(t *testing.T) {
	a := real90DropArchive(t)
	probe := &dungeonGenericPoolReadProbe{source: a}
	cold, err := newPVFDungeonDropCatalog(probe)
	if err != nil {
		t.Fatal(err)
	}
	monsters, err := newPVFDungeonMonsterCatalog(probe)
	if err != nil {
		t.Fatal(err)
	}
	rules, err := currentDungeonMonsterDropRules(monsters)
	if err != nil {
		t.Fatal(err)
	}
	window, found := rules.ItemReference(5)
	if !found {
		t.Fatal("现用PVF缺少现场5级怪物掉落范围")
	}
	potion, err := cold.ResolveItem(6004)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("现场6004：grade=%d rarity=%d；5级怪物候选范围下移%d、上移%d", potion.Grade, potion.Rarity, window.ValueA, window.ValueB)
	a.ReleaseReadCaches()
	started := time.Now()
	if _, _, err := cold.SelectGenericStackable(newCurrentDungeonDropLCG(42), 5, window.ValueA, window.ValueB, potion.Rarity); err != nil {
		t.Fatal(err)
	}
	coldElapsed := time.Since(started)
	s := &Service{dungeonMonsterTable: monsters}
	a.ReleaseReadCaches()
	if err := s.preloadDungeonGenericDropPools(context.Background()); err != nil {
		t.Fatal(err)
	}
	warm, err := monsters.DropCatalog()
	if err != nil || !warm.genericLoaded || warm.genericErr != nil {
		t.Fatalf("真实PVF预载失败：%v", err)
	}
	hasPotion := false
	for _, id := range warm.genericStackableCandidates(5, window.ValueA, window.ValueB, potion.Rarity) {
		hasPotion = hasPotion || id == 6004
	}
	if !hasPotion {
		t.Fatal("现场6004不属于对应真实PVF通用掉落候选，需要重新核对归因")
	}
	// 生产启动会释放解码缓存，已经发布的结构化候选仍必须有效。
	a.ReleaseReadCaches()
	reads := probe.reads
	started = time.Now()
	for seed := uint32(1); seed <= 64; seed++ {
		before, after := newCurrentDungeonDropLCG(seed), newCurrentDungeonDropLCG(seed)
		want, wantOK, err := cold.SelectGenericStackable(before, 5, window.ValueA, window.ValueB, potion.Rarity)
		if err != nil {
			t.Fatal(err)
		}
		got, gotOK, err := warm.SelectGenericStackable(after, 5, window.ValueA, window.ValueB, potion.Rarity)
		if err != nil || want != got || wantOK != gotOK || before.Seed() != after.Seed() {
			t.Fatalf("真实PVF预载改变掉落：seed=%d want=%d got=%d %v", seed, want, got, err)
		}
	}
	if probe.reads != reads {
		t.Fatalf("生产释放缓存后抽取再次读取整表：%d→%d", reads, probe.reads)
	}
	t.Logf("现用PVF首次通用抽取耗时%s；预载并释放解码缓存后64种子抽取耗时%s，新增脚本读取0次；现场6004在真实候选内", coldElapsed, time.Since(started))
}

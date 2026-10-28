package dnfbridge

import (
	"context"
	"encoding/binary"
	"math"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/adventuregroup"
	"longheng.io/server/internal/modules/dnf/progression"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

type deathBurstSettlementProbe struct {
	base   dnfrepo.CharacterSettlementUnitOfWork
	calls  int
	before func()
}

func (p *deathBurstSettlementProbe) WithinCharacterSettlement(ctx context.Context, id string, apply func(dnfrepo.Group) error) error {
	p.calls++
	if p.before != nil {
		p.before()
	}
	return p.base.WithinCharacterSettlement(ctx, id, apply)
}

func prepareDeathBurstCapsule(t *testing.T, s *Service, repos dnfrepo.Group) {
	t.Helper()
	source := adventureShopExpirationSource("")
	source[adventuregroup.AdventureSystem2018Path] = strings.ReplaceAll(source[adventuregroup.AdventureSystem2018Path], "[capsule max exp]\n10\n", "[capsule max exp]\n1000\n")
	source[adventuregroup.AdventureSystem2018Path] = strings.ReplaceAll(source[adventuregroup.AdventureSystem2018Path], "[capsule need minimum exp]\n1\n", "[capsule need minimum exp]\n20\n")
	var err error
	s.adventureGroupTable, err = adventuregroup.LoadComplete(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if err := repos.Account.Save(context.Background(), dnfrepo.AccountRecord{AccountID: "account-1", Metadata: map[string]string{"保留": "原值"}}); err != nil {
		t.Fatal(err)
	}
}

func TestDungeonDeathBurstCapsuleAndHonorShareOneCommit(t *testing.T) {
	s, p, repos, packets := prepareDungeonDeathBurstTest(t, true)
	prepareDeathBurstCapsule(t, s, repos)
	probe := &deathBurstSettlementProbe{base: repos.CharacterSettlement}
	probe.before = func() {
		deaths := 0
		for _, frame := range splitAllCurrentGameServerUpperPackets(t, p.conn.(*bufferConn).write.Bytes()) {
			if frame.Header.MsgID == 38 {
				deaths++
			}
		}
		if deaths != 3 {
			t.Fatalf("账号经验事务挡住死亡确认：%d/3", deaths)
		}
	}
	repos.CharacterSettlement = probe
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	if err := s.handleCurrentDungeonDeathBurstPackets(p, packets[:3], false); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	check := func(wantCapsule uint64, wantExp int64) {
		t.Helper()
		a, _, _ := repos.Account.Load(ctx, "account-1")
		c, _, _ := repos.Character.Load(ctx, "99")
		state, err := adventuregroup.ParseRuntimeState(a, s.adventureGroupTable.Runtime(), s.gameplayNow())
		if err != nil || state.GrowthExperience != wantCapsule || c.Stats["exp"] != wantExp || a.Metadata["保留"] != "原值" {
			t.Fatalf("胶囊/经验不一致：account=%+v char=%+v state=%+v %v", a, c, state, err)
		}
	}
	check(30, 0)
	pushes := 0
	states := 0
	for _, frame := range splitAllCurrentGameServerUpperPackets(t, p.conn.(*bufferConn).write.Bytes()) {
		if frame.Header.MsgID == 37 {
			states++
			if binary.LittleEndian.Uint32(frame.Body[1:5]) != 0 || binary.LittleEndian.Uint64(frame.Body[59:67]) != 30 {
				t.Fatalf("满级普通EXP产生提示或熟练度未刷新：%x", frame.Body)
			}
		}
		if frame.Header.MsgID == currentAdventureInfoPushMsgID {
			pushes++
			if got := binary.LittleEndian.Uint64(frame.Body[18+currentAdventureInfoGrowthCapsuleOffset:]); got != 30 {
				t.Fatalf("胶囊通知回退为%d", got)
			}
		}
	}
	if pushes != 1 || states != 1 {
		t.Fatalf("同批胶囊通知=%d 熟练度刷新=%d", pushes, states)
	}
	if probe.calls != 1 {
		t.Fatalf("三只怪使用%d次经验事务", probe.calls)
	}
	probe.before = nil
	if err := s.handleCurrentDungeonDeathBurstPackets(p, packets[:3], false); err != nil {
		t.Fatal(err)
	}
	check(30, 0)
}

func TestRealPVFDungeonDeathBurstCapsuleClaimDoesNotRefillAndContinuesKills(t *testing.T) {
	archive := real90DropArchive(t)
	s, p, repos, packets := prepareDungeonDeathBurstTest(t, true)
	prepareDeathBurstCapsule(t, s, repos)
	s.initialEquipmentArchive = archive
	check := func(wantCapsule uint64, wantExp int64) {
		t.Helper()
		a, _, _ := repos.Account.Load(context.Background(), "account-1")
		c, _, _ := repos.Character.Load(context.Background(), "99")
		state, err := adventuregroup.ParseRuntimeState(a, s.adventureGroupTable.Runtime(), s.gameplayNow())
		if err != nil || state.GrowthExperience != wantCapsule || c.Stats["exp"] != wantExp {
			t.Fatalf("领取后经验不一致：capsule=%d exp=%d %v", state.GrowthExperience, c.Stats["exp"], err)
		}
	}
	if err := s.handleCurrentDungeonDeathBurstPackets(p, packets[:3], false); err != nil {
		t.Fatal(err)
	}
	check(30, 0)
	if err := s.handleCurrentAdventureGrowthCapsule(p, nil); err != nil {
		t.Fatal(err)
	}
	check(10, 1)
	if err := s.handleCurrentAdventureGrowthCapsule(p, nil); err != nil {
		t.Fatal(err)
	}
	check(10, 1)
	if err := s.handleCurrentDungeonDeathBurstPackets(p, packets[3:], false); err != nil {
		t.Fatal(err)
	}
	check(30, 1)
	if err := s.handleCurrentDungeonDeathBurstPackets(p, packets[:3], false); err != nil {
		t.Fatal(err)
	}
	check(30, 1)
}

func TestDungeonDeathBurstAccountFailureRollsBackExperienceAndCapsule(t *testing.T) {
	s, p, repos, packets := prepareDungeonDeathBurstTest(t, true)
	prepareDeathBurstCapsule(t, s, repos)
	repos.CharacterSettlement = failingCapsuleSettlement{repos.CharacterSettlement}
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	if err := s.handleCurrentDungeonDeathBurstPackets(p, packets[:3], false); err != nil {
		t.Fatal(err)
	}
	c, _, _ := repos.Character.Load(context.Background(), "99")
	a, _, _ := repos.Account.Load(context.Background(), "account-1")
	state, err := adventuregroup.ParseRuntimeState(a, s.adventureGroupTable.Runtime(), s.gameplayNow())
	if err != nil || c.Stats["exp"] != 0 || c.Stats[currentHonorExpertProgressExperienceStatKey] != 0 || state.GrowthExperience != 0 || a.HonorExp != 0 {
		t.Fatalf("失败后出现半笔经验：char=%+v account=%+v %v", c, a, err)
	}
	for _, frame := range splitAllCurrentGameServerUpperPackets(t, p.conn.(*bufferConn).write.Bytes()) {
		if frame.Header.MsgID == 37 {
			t.Fatal("账号写失败仍发布角色经验")
		}
	}
}

func TestDungeonDeathBurstCrosses90AndCreditsOnlyMaximumLevelOverflow(t *testing.T) {
	s, p, repos, packets := prepareDungeonDeathBurstTest(t, true)
	prepareDeathBurstCapsule(t, s, repos)
	catalog, err := s.dungeonMonsterCatalog()
	if err != nil {
		t.Fatal(err)
	}
	source := catalog.source.(bridgePVFSource)
	source[progression.ExperienceTablePath] = strings.Repeat("0 ", 88) + "15"
	source[progression.SkillPointTablePath] = "[sp table]\n89 20\n90 10\n[/sp table]\n[tp table]\n90 0\n[/tp table]\n"
	ctx := context.Background()
	c, _, _ := repos.Character.Load(ctx, "99")
	c.Level = 89
	if err := repos.Character.Save(ctx, c); err != nil {
		t.Fatal(err)
	}
	skill, _, _ := repos.Skill.Load(ctx, "99")
	skill.Points.SyncedLevel = 89
	if err := repos.Skill.Save(ctx, skill); err != nil {
		t.Fatal(err)
	}
	p.dungeon.runtime.Character = c
	probe := &deathBurstSettlementProbe{base: repos.CharacterSettlement}
	repos.CharacterSettlement = probe
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	if err := s.handleCurrentDungeonDeathBurstPackets(p, packets[:3], false); err != nil {
		t.Fatal(err)
	}
	c, _, _ = repos.Character.Load(ctx, "99")
	a, _, _ := repos.Account.Load(ctx, "account-1")
	state, err := adventuregroup.ParseRuntimeState(a, s.adventureGroupTable.Runtime(), s.gameplayNow())
	if err != nil || c.Level != 90 || c.Stats["exp"] != 15 || state.GrowthExperience != 15 || a.HonorExp != 15 || probe.calls != 2 {
		t.Fatalf("跨满级分流错误：char=%+v account=%+v state=%+v commits=%d %v", c, a, state, probe.calls, err)
	}
	p.conn.(*bufferConn).write.Reset()
	if err := s.handleCurrentDungeonDeathBurstPackets(p, packets[3:4], false); err != nil {
		t.Fatal(err)
	}
	c, _, _ = repos.Character.Load(ctx, "99")
	a, _, _ = repos.Account.Load(ctx, "account-1")
	state, err = adventuregroup.ParseRuntimeState(a, s.adventureGroupTable.Runtime(), s.gameplayNow())
	if err != nil || c.Stats["exp"] != 15 || state.GrowthExperience != 25 || a.HonorExp != 25 {
		t.Fatalf("升90后继续击杀分流错误：char=%+v capsule=%d account_exp=%d %v", c, state.GrowthExperience, a.HonorExp, err)
	}
	states := 0
	for _, frame := range splitAllCurrentGameServerUpperPackets(t, p.conn.(*bufferConn).write.Bytes()) {
		if frame.Header.MsgID == 37 {
			states++
			if binary.LittleEndian.Uint32(frame.Body[1:5]) != 15 || binary.LittleEndian.Uint64(frame.Body[59:67]) != 25 {
				t.Fatalf("升90后产生普通EXP差值或漏刷熟练度：%x", frame.Body)
			}
		}
	}
	if states != 1 {
		t.Fatalf("升90后熟练度状态包=%d", states)
	}
}

func TestDungeonDeathBurstCapsuleWriteFailureKeepsAcceptedDeathCredit(t *testing.T) {
	s, p, repos, packets := prepareDungeonDeathBurstTest(t, true)
	prepareDeathBurstCapsule(t, s, repos)
	p.conn = &deathNotifyFailedConn{}
	if err := s.handleCurrentDungeonDeathBurstPackets(p, packets[:3], false); err == nil {
		t.Fatal("未返回通知写失败")
	}
	c, _, _ := repos.Character.Load(context.Background(), "99")
	a, _, _ := repos.Account.Load(context.Background(), "account-1")
	state, err := adventuregroup.ParseRuntimeState(a, s.adventureGroupTable.Runtime(), s.gameplayNow())
	if err != nil || c.Stats["exp"] != 0 || state.GrowthExperience != 10 || a.HonorExp != 10 {
		t.Fatalf("写失败丢失已接受击杀的经验或胶囊：exp=%d capsule=%d account_exp=%d %v", c.Stats["exp"], state.GrowthExperience, a.HonorExp, err)
	}
}

func TestDungeonDeathBurstSaturatedExperienceStillCreditsHonorAndCapsule(t *testing.T) {
	s, p, repos, packets := prepareDungeonDeathBurstTest(t, true)
	prepareDeathBurstCapsule(t, s, repos)
	ctx := context.Background()
	c, _, _ := repos.Character.Load(ctx, "99")
	c.Stats["exp"] = math.MaxUint32
	if err := repos.Character.Save(ctx, c); err != nil {
		t.Fatal(err)
	}
	p.dungeon.runtime.Character = c
	if err := s.handleCurrentDungeonDeathBurstPackets(p, packets[:3], false); err != nil {
		t.Fatal(err)
	}
	c, _, _ = repos.Character.Load(ctx, "99")
	a, _, _ := repos.Account.Load(ctx, "account-1")
	state, err := adventuregroup.ParseRuntimeState(a, s.adventureGroupTable.Runtime(), s.gameplayNow())
	if err != nil || c.Stats["exp"] != math.MaxUint32 || c.Stats[currentHonorExpertProgressExperienceStatKey] != 30 || state.GrowthExperience != 30 || c.Stats[currentDungeonCapAwardRevisionStatKey] != 3 {
		t.Fatalf("角色EXP饱和后经验停止：exp=%d honor=%d capsule=%d revision=%d %v", c.Stats["exp"], c.Stats[currentHonorExpertProgressExperienceStatKey], state.GrowthExperience, c.Stats[currentDungeonCapAwardRevisionStatKey], err)
	}
	if err := s.handleCurrentDungeonDeathBurstPackets(p, packets[:3], false); err != nil {
		t.Fatal(err)
	}
	a, _, _ = repos.Account.Load(ctx, "account-1")
	state, _ = adventuregroup.ParseRuntimeState(a, s.adventureGroupTable.Runtime(), s.gameplayNow())
	if state.GrowthExperience != 30 {
		t.Fatalf("饱和经验重复死亡再次增加胶囊：%d", state.GrowthExperience)
	}
}

package dnfbridge

import (
	"context"
	"encoding/binary"
	"longheng.io/server/internal/modules/dnf/progression"
	"strconv"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/onlineevent"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestPortedProductionGateAndSessionGeneration(t *testing.T) {
	r := memory.NewMemoryGroup()
	s := &Service{options: options{currentClientVersion: "official-24.9"}, repositoryProvider: func() (repo.Group, bool) { return r, true }}
	if err := s.initializeActivityControls(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.initializePortedActivityPolicy(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []uint16{1370, 1054, 222, 116, 288, 2348} {
		if s.currentPortedActivityConfigured(id) || s.currentPortedActivityEnabled(id) {
			t.Fatalf("未匹配当前EXE的参考协议%d启用", id)
		}
	}
	configurePortedActivityTestControls(t, s)
	for _, id := range []uint16{222, 1054, 1370, 2348} {
		if !s.currentPortedActivityEnabled(id) {
			t.Fatalf("已核对原生结构的活动%d显式开启后仍被拦截", id)
		}
	}
	for _, id := range []uint16{116} {
		if s.currentPortedActivityEnabled(id) {
			t.Fatalf("未适配活动%d的兼容门禁被绕过", id)
		}
	}
	session := &gameSession{conn: &bufferConn{}, accountID: "a", selectedCharacterID: 1, characterGeneration: 1, loginPacketSeen: true, accountFromToken: true}
	s.gameSessions = map[uint16]*gameSession{1: session}
	check := s.currentPortedRequestValidator(session, 1, false)
	if !check() {
		t.Fatal("当前会话未通过验证")
	}
	session.characterGeneration++
	if check() {
		t.Fatal("事务验证重新绑定到了后来的会话代次")
	}
}

func TestPortedLuckyProfessionTupleAndAllJobs(t *testing.T) {
	c := &currentLuckyCatalog{Jobs: [][2]uint32{{0, 17}, {1, 34}}}
	ch := repo.CharacterRecord{CharacterID: "1", Job: "0", Stats: map[string]int64{"grow_type": 34}}
	if currentLuckyCharacterMatches(c, ch) {
		t.Fatal("不同职业的转职和觉醒被交叉组合")
	}
	ch.Job = "1"
	if !currentLuckyCharacterMatches(c, ch) {
		t.Fatal("完整职业记录未匹配")
	}
	c.Jobs = currentAllLuckyProfessions()
	for job := 0; job <= 15; job++ {
		ch.Job = string(rune('0' + job))
		if job >= 10 {
			ch.Job = "1" + string(rune('0'+job-10))
		}
		ch.Stats["grow_type"] = 0
		if !currentLuckyCharacterMatches(c, ch) {
			t.Fatalf("职业%d未开放", job)
		}
	}
}

func TestPortedLaborCatalogHasExactlyThreeNativeItems(t *testing.T) {
	c := &onlineevent.LaborDayCatalog{Policy: onlineevent.LaborDayPolicy{Starts: time.Unix(0, 0)}}
	for i := range c.Login {
		c.Login[i] = onlineevent.AttendancePVFItemReward{ItemID: int64(100 + i), Count: 1}
	}
	body, err := buildCurrentActivityEventInfoBodyWithPorted([]uint32{1}, 60, false, false, false, false, false, 0, false, false, false, false, false, false, false, false, false, currentPortedCatalog{LevelAiming: true, LaborDay: c})
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != 83 || binary.LittleEndian.Uint16(body) != 2 || binary.LittleEndian.Uint16(body[2:]) != 222 || binary.LittleEndian.Uint16(body[17:]) != 2348 || body[32] != 1 || binary.LittleEndian.Uint16(body[33:]) != 2348 {
		t.Fatalf("活动目录数量或长度错误%x", body)
	}
	for i := 0; i < 12; i++ {
		value := binary.LittleEndian.Uint32(body[35+i*4:])
		want := uint32(0xffffffff)
		if i < 3 {
			want = uint32(100 + i)
		}
		if i == 10 {
			want = 3
		}
		if i == 11 {
			want = 0
		}
		if value != want {
			t.Fatalf("参数%d=%d，预期%d", i, value, want)
		}
	}
}

func TestPortedLuckyExperienceCrossesLevelBoundary(t *testing.T) {
	ctx := context.Background()
	s, runtime, r := prepareCurrentDungeonMonsterExperienceTest(t)
	session := &gameSession{conn: &bufferConn{}, selectedCharacterID: 99, accountID: runtime.Character.AccountID, dungeon: dungeonSessionState{runtime: runtime}}
	enablePortedActivityTest(t, s, session)
	s.luckyCharacterCatalog = &currentLuckyCatalog{Jobs: [][2]uint32{{0, 0}}, Monster: 10}
	catalog, err := s.dungeonMonsterCatalog()
	if err != nil {
		t.Fatal(err)
	}
	source := catalog.source.(bridgePVFSource)
	var entries []string
	for i := 1; i <= 90; i++ {
		entries = append(entries, strconv.Itoa(i*1000))
	}
	source[progression.ExperienceTablePath] = strings.Join(entries, " ")
	var skillRows []string
	for i := 1; i <= 90; i++ {
		skillRows = append(skillRows, strconv.Itoa(i)+" 20")
	}
	source[progression.SkillPointTablePath] = "[sp table]\n" + strings.Join(skillRows, "\n") + "\n[/sp table]\n[tp table]\n50 1\n[/tp table]\n"
	resources, err := s.currentDungeonExperienceCommitResources()
	if err != nil {
		t.Fatal(err)
	}
	threshold, err := resources.Progression.ThresholdToNext(runtime.Character.Level)
	if err != nil {
		t.Fatal(err)
	}
	ch, _, err := r.Character.Load(ctx, "99")
	if err != nil {
		t.Fatal(err)
	}
	level := ch.Level
	ch.Stats["exp"] = int64(threshold - 160)
	if err := r.Character.Save(ctx, ch); err != nil {
		t.Fatal(err)
	}
	runtime.Character = ch
	result, err := s.commitCurrentDungeonMonsterExperience(ctx, session, runtime, resources.Progression, currentDungeonMonsterExperienceCommitResult{Award: currentDungeonMonsterExperienceAward{Gain: 150}})
	if err != nil {
		t.Fatal(err)
	}
	if result.LuckyCharacterBonus != 15 || !result.Applied {
		t.Fatalf("活动加成未提交：%+v", result)
	}
	ch, _, _ = r.Character.Load(ctx, "99")
	if ch.Level != level+1 || ch.Stats["exp"] != int64(threshold)+5 {
		t.Fatalf("等级%d经验%d", ch.Level, ch.Stats["exp"])
	}
}

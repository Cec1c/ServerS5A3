package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfhonor "longheng.io/server/internal/modules/dnf/honor"
	"longheng.io/server/internal/modules/dnf/premium"
	"longheng.io/server/internal/modules/dnf/progression"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func prepareDungeonDeathBurstTest(t *testing.T, capped bool) (*Service, *gameSession, dnfrepo.Group, []dnfproto.LatestGameStreamPacket) {
	t.Helper()
	s, _, repos := prepareCurrentDungeonMonsterExperienceTest(t)
	catalog, err := s.dungeonMonsterCatalog()
	if err != nil {
		t.Fatal(err)
	}
	source := catalog.source.(bridgePVFSource)
	source["map/dungeon/test/start.map"] = "[map name]\n`start`\n[dungeon]\n700\n[type]\n`[start]`\n[monster]\n" +
		strings.Repeat("3001 10 0 100 200 0 0 0 `[fixed]` `[normal]`\n", 5)
	s.worldMapTable, s.worldMapResolver, s.dungeonMonsterTable = loadBridgeDungeonStaticData(t, source)
	s.options.currentClientVersion = "official-24.9"
	if capped {
		s.honorTable, err = dnfhonor.LoadTables(honorServiceTestSource{dnfhonor.TablePath: honorServiceTestDocument()})
		if err != nil {
			t.Fatal(err)
		}
		character, _, _ := repos.Character.Load(context.Background(), "99")
		character.Level = currentAdventureCharacterLevelCap
		if err := repos.Character.Save(context.Background(), character); err != nil {
			t.Fatal(err)
		}
		skill, _, _ := repos.Skill.Load(context.Background(), "99")
		skill.Points.SyncedLevel = currentAdventureCharacterLevelCap
		if err := repos.Skill.Save(context.Background(), skill); err != nil {
			t.Fatal(err)
		}
	}
	p := &gameSession{conn: &bufferConn{}, connID: "连续击杀测试", selectedCharacterID: 99}
	r, _, err := s.prepareDungeonRuntime(context.Background(), p, dungeoncmd.SelectDungeonRequest{DungeonID: 700, Difficulty: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Room.AnnounceAllActors(r.Session); err != nil {
		t.Fatal(err)
	}
	p.dungeon.runtime = r
	var packets []dnfproto.LatestGameStreamPacket
	for _, m := range r.Room.Snapshot().Monsters {
		body := make([]byte, dungeoncmd.Official249DieMonsterBaseSize+dungeoncmd.Official249DieMonsterEntrySize)
		binary.LittleEndian.PutUint32(body, m.ObjectKey)
		binary.LittleEndian.PutUint16(body[4:], currentSceneActorObjectKey(99))
		body[30] = 1
		packets = append(packets, nativeResultTestPacket(39, body))
	}
	if len(packets) != 5 {
		t.Fatalf("连续击杀夹具怪物数=%d", len(packets))
	}
	return s, p, repos, packets
}

func TestDungeonDeathBurstCappedNotifiesAllDeathsBeforeFirstExperienceCommit(t *testing.T) {
	s, p, repos, packets := prepareDungeonDeathBurstTest(t, true)
	c := p.conn.(*bufferConn)
	probe := &deathNotifyProgressionProbe{base: repos.CharacterProgression}
	probe.before = func() {
		wire := splitAllCurrentGameServerUpperPackets(t, c.write.Bytes())
		deaths := 0
		for _, frame := range wire {
			if frame.Header.MsgID == 38 {
				deaths++
			}
			if frame.Header.MsgID == 37 {
				t.Fatal("连续经验事务未完成就发出了经验提示")
			}
		}
		if deaths != 5 {
			t.Fatalf("经验事务仍挡住后续怪物死亡：已发送%d/5", deaths)
		}
	}
	repos.CharacterProgression = probe
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	if err := s.handleCurrentDungeonDeathBurstPackets(p, packets, false); err != nil {
		t.Fatal(err)
	}
	character, _, err := repos.Character.Load(context.Background(), "99")
	if err != nil || character.Stats["exp"] != 0 || character.Stats[currentHonorExpertProgressExperienceStatKey] != 50 || probe.calls != 1 || p.dungeonDeathBurst != nil {
		t.Fatalf("经验或事务不匹配：exp=%d calls=%d err=%v", character.Stats["exp"], probe.calls, err)
	}
	states := 0
	for _, frame := range splitAllCurrentGameServerUpperPackets(t, c.write.Bytes()) {
		if frame.Header.Classification == 0 && frame.Header.MsgID == 37 {
			states++
			if binary.LittleEndian.Uint32(frame.Body[1:5]) != 0 || binary.LittleEndian.Uint64(frame.Body[59:67]) != 50 {
				t.Fatalf("汇总经验=%x", frame.Body)
			}
		}
	}
	if states != 1 {
		t.Fatalf("一次连续击杀发出%d次经验提示", states)
	}
	c.write.Reset()
	if err := s.handleCurrentDungeonDeathBurstPackets(p, packets, false); err != nil || probe.calls != 1 {
		t.Fatalf("重复死亡重复提交经验：calls=%d err=%v", probe.calls, err)
	}
}

func TestDungeonDeathBurstBelowCapNotifiesDeathsBeforeExperienceCommit(t *testing.T) {
	s, p, repos, packets := prepareDungeonDeathBurstTest(t, false)
	probe := &deathNotifyProgressionProbe{base: repos.CharacterProgression}
	probe.before = func() {
		deaths := 0
		for _, frame := range splitAllCurrentGameServerUpperPackets(t, p.conn.(*bufferConn).write.Bytes()) {
			if frame.Header.MsgID == 38 {
				deaths++
			}
		}
		if deaths != 3 {
			t.Fatalf("未满级经验事务挡住后续死亡：死亡=%d/3 提交=%d", deaths, probe.calls)
		}
	}
	repos.CharacterProgression = probe
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	if err := s.handleCurrentDungeonDeathBurstPackets(p, packets[:3], false); err != nil {
		t.Fatal(err)
	}
	character, _, _ := repos.Character.Load(context.Background(), "99")
	if character.Stats["exp"] != 600 || probe.calls != 1 {
		t.Fatalf("低等级累计经验错误：%+v", character)
	}
	states := 0
	for _, frame := range splitAllCurrentGameServerUpperPackets(t, p.conn.(*bufferConn).write.Bytes()) {
		if frame.Header.MsgID == 37 {
			states++
		}
	}
	if states != 1 {
		t.Fatalf("未清房连续经验提示=%d", states)
	}
}

func TestDungeonDeathBurstFailureDoesNotPublishUncommittedExperience(t *testing.T) {
	s, p, repos, packets := prepareDungeonDeathBurstTest(t, true)
	probe := &deathNotifyProgressionProbe{base: repos.CharacterProgression, reject: errors.New("隔离经验事务拒绝")}
	repos.CharacterProgression = probe
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	if err := s.handleCurrentDungeonDeathBurstPackets(p, packets[:3], false); err != nil {
		t.Fatal(err)
	}
	for _, frame := range splitAllCurrentGameServerUpperPackets(t, p.conn.(*bufferConn).write.Bytes()) {
		if frame.Header.MsgID == 37 {
			t.Fatal("事务失败后仍发布经验")
		}
	}
	character, _, _ := repos.Character.Load(context.Background(), "99")
	if character.Stats["exp"] != 0 || probe.calls != 1 || p.dungeonDeathBurst != nil {
		t.Fatalf("失败后状态错误：%+v calls=%d", character, probe.calls)
	}
}

func TestDungeonDeathBurstWriteFailureStillCommitsAcceptedDeath(t *testing.T) {
	s, p, repos, packets := prepareDungeonDeathBurstTest(t, true)
	p.conn = &deathNotifyFailedConn{}
	if err := s.handleCurrentDungeonDeathBurstPackets(p, packets[:3], false); err == nil {
		t.Fatal("未返回写失败")
	}
	character, _, _ := repos.Character.Load(context.Background(), "99")
	if character.Stats["exp"] != 0 || character.Stats[currentHonorExpertProgressExperienceStatKey] != 10 || p.dungeonDeathBurst != nil {
		t.Fatalf("写失败丢失已确认死亡的经验：%+v", character)
	}
}

func TestDungeonDeathBurstSpansStopAtOtherPacketAndBoundCount(t *testing.T) {
	death := nativeResultTestPacket(39, nil)
	other := nativeResultTestPacket(45, nil)
	if currentDungeonDeathBurstSpan([]dnfproto.LatestGameStreamPacket{death, death, other, death}) != 2 ||
		currentDungeonDeathBurstSpan([]dnfproto.LatestGameStreamPacket{other, death}) != 1 {
		t.Fatal("连续死亡跨过了其他协议")
	}
	packets := make([]dnfproto.LatestGameStreamPacket, 64)
	for i := range packets {
		packets[i] = death
	}
	if currentDungeonDeathBurstSpan(packets) != currentDungeonDeathBurstLimit {
		t.Fatal("连续击杀未保持有界")
	}
}

func TestDungeonDeathBurstGrowthBonusesFreezeBeforeSingleCommit(t *testing.T) {
	s, p, repos, packets := prepareDungeonDeathBurstTest(t, true)
	s.premiumCatalog = &currentPremiumCatalog{effectsByType: map[int64]currentPremiumEffectInfo{
		premium.TypeBonusExp: {BonusExperiencePercent: 20},
	}}
	if err := repos.Account.Save(context.Background(), dnfrepo.AccountRecord{AccountID: "account-1",
		Metadata: map[string]string{"premium_expire_84": strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)}}); err != nil {
		t.Fatal(err)
	}
	probe := &deathNotifyProgressionProbe{base: repos.CharacterProgression}
	probe.before = func() {
		probe.reject = nil
		if probe.calls == 1 {
			// 提交期间到期也不能改写已接受击杀的契约资格。
			account, _, _ := repos.Account.Load(context.Background(), "account-1")
			account.Metadata["premium_expire_84"] = strconv.FormatInt(time.Now().Add(-time.Hour).Unix(), 10)
			if err := repos.Account.Save(context.Background(), account); err != nil {
				t.Fatal(err)
			}
		}
	}
	repos.CharacterProgression = probe
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	if err := s.handleCurrentDungeonDeathBurstPackets(p, packets, false); err != nil {
		t.Fatal(err)
	}
	states := 0
	for _, frame := range splitAllCurrentGameServerUpperPackets(t, p.conn.(*bufferConn).write.Bytes()) {
		if frame.Header.MsgID != 37 {
			continue
		}
		states++
		if binary.LittleEndian.Uint32(frame.Body[1:5]) != 0 || binary.LittleEndian.Uint32(frame.Body[30:34]) != 10 || binary.LittleEndian.Uint64(frame.Body[59:67]) != 60 {
			t.Fatalf("汇总包含未提交经验或契约加成错误：%x", frame.Body)
		}
	}
	if states != 1 || probe.calls != 1 {
		t.Fatalf("通知或事务次数错误：states=%d calls=%d", states, probe.calls)
	}
}

func TestDungeonDeathBurstLevelUpKeepsSPAndLatestCharacterState(t *testing.T) {
	s, p, repos, packets := prepareDungeonDeathBurstTest(t, false)
	catalog, err := s.dungeonMonsterCatalog()
	if err != nil {
		t.Fatal(err)
	}
	source := catalog.source.(bridgePVFSource)
	source[progression.ExperienceTablePath] = strings.TrimSpace(strings.Repeat("0 ", 19) + "100 1000")
	source[progression.SkillPointTablePath] = "[sp table]\n20 20\n21 10\n[/sp table]\n[tp table]\n50 1\n[/tp table]\n"
	if err := s.handleCurrentDungeonDeathBurstPackets(p, packets[:3], false); err != nil {
		t.Fatal(err)
	}
	character, _, _ := repos.Character.Load(context.Background(), "99")
	skill, _, _ := repos.Skill.Load(context.Background(), "99")
	if character.Level != 21 || character.Stats["exp"] != 600 || skill.Points.RemainingSP != 30 || skill.Points.SyncedLevel != 21 {
		t.Fatalf("连续升级经验/SP错误：character=%+v points=%+v", character, skill.Points)
	}
	states := 0
	for _, frame := range splitAllCurrentGameServerUpperPackets(t, p.conn.(*bufferConn).write.Bytes()) {
		if frame.Header.MsgID == 37 {
			states++
			if frame.Body[0] != 21 || binary.LittleEndian.Uint32(frame.Body[1:5]) != 600 || binary.LittleEndian.Uint16(frame.Body[9:11]) != 30 {
				t.Fatalf("升级后汇总状态错误：%x", frame.Body)
			}
		}
	}
	if states != 1 {
		t.Fatalf("连续升级经验提示=%d", states)
	}
}

func prepareDungeonDeathBurstLevelBoundary(t *testing.T, threshold string) (*Service, *gameSession, dnfrepo.Group, []dnfproto.LatestGameStreamPacket) {
	t.Helper()
	s, p, repos, packets := prepareDungeonDeathBurstTest(t, false)
	catalog, err := s.dungeonMonsterCatalog()
	if err != nil {
		t.Fatal(err)
	}
	source := catalog.source.(bridgePVFSource)
	source[progression.ExperienceTablePath] = strings.TrimSpace(strings.Repeat("0 ", 19) + threshold + " 1000")
	source[progression.SkillPointTablePath] = "[sp table]\n20 20\n21 10\n[/sp table]\n[tp table]\n50 1\n[/tp table]\n"
	source[progression.MonsterExperienceTablePath] += " 21 100 22 100 23 100 24 100 25 100 26 100"
	for i := range p.dungeon.runtime.Room.monsters {
		m := &p.dungeon.runtime.Room.monsters[i]
		m.Spawn.Level, m.Spawn.AutoLevel = 0, 26
	}
	return s, p, repos, packets
}

func TestDungeonDeathBurstLevelBoundaryUsesCommittedLevelForNextKill(t *testing.T) {
	s, p, repos, packets := prepareDungeonDeathBurstLevelBoundary(t, "100")
	probe := &deathNotifyProgressionProbe{base: repos.CharacterProgression}
	probe.before = func() {
		deaths := 0
		for _, frame := range splitAllCurrentGameServerUpperPackets(t, p.conn.(*bufferConn).write.Bytes()) {
			if frame.Header.MsgID == 38 {
				deaths++
			}
			if frame.Header.MsgID == 37 {
				t.Fatal("升级边界提前拆开发送经验提示")
			}
		}
		want := 3
		if probe.calls == 1 {
			want = 1
		}
		if deaths != want {
			t.Fatalf("升级边界死亡通知=%d，期望%d，提交%d", deaths, want, probe.calls)
		}
	}
	repos.CharacterProgression = probe
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	if err := s.handleCurrentDungeonDeathBurstPackets(p, packets[:3], false); err != nil {
		t.Fatal(err)
	}
	character, _, _ := repos.Character.Load(context.Background(), "99")
	skill, _, _ := repos.Skill.Load(context.Background(), "99")
	// Lv20对Lv26先得150并升级；随后两只按Lv21的倍率各得200。
	if character.Level != 21 || character.Stats["exp"] != 550 || skill.Points.RemainingSP != 30 || probe.calls != 2 {
		t.Fatalf("后续击杀未使用真实新等级：character=%+v skill=%+v calls=%d", character, skill.Points, probe.calls)
	}
	states := 0
	for _, frame := range splitAllCurrentGameServerUpperPackets(t, p.conn.(*bufferConn).write.Bytes()) {
		if frame.Header.MsgID == 37 {
			states++
			if frame.Body[0] != 21 || binary.LittleEndian.Uint32(frame.Body[1:5]) != 550 {
				t.Fatalf("升级汇总经验错误：%x", frame.Body)
			}
		}
	}
	if states != 1 {
		t.Fatalf("升级分段出现%d次经验提示", states)
	}
}

func TestDungeonDeathBurstFailedPrefixResetsLevelBoundaryFromCommittedExperience(t *testing.T) {
	s, p, repos, packets := prepareDungeonDeathBurstLevelBoundary(t, "250")
	probe := &deathNotifyProgressionProbe{base: repos.CharacterProgression}
	probe.before = func() {
		probe.reject = nil
		if probe.calls == 1 {
			probe.reject = errors.New("隔离首只经验事务失败")
		}
		deaths := 0
		for _, frame := range splitAllCurrentGameServerUpperPackets(t, p.conn.(*bufferConn).write.Bytes()) {
			if frame.Header.MsgID == 38 {
				deaths++
			}
		}
		want := probe.calls * 2
		if deaths != want {
			t.Fatalf("局部失败后升级边界=%d，期望%d，提交%d", deaths, want, probe.calls)
		}
	}
	repos.CharacterProgression = probe
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	if err := s.handleCurrentDungeonDeathBurstPackets(p, packets[:4], false); err != nil {
		t.Fatal(err)
	}
	character, _, _ := repos.Character.Load(context.Background(), "99")
	skill, _, _ := repos.Skill.Load(context.Background(), "99")
	// 首段两只整体回滚，后两只仍按Lv20各得150，再真实升级。
	if character.Level != 21 || character.Stats["exp"] != 300 || skill.Points.RemainingSP != 30 || probe.calls != 2 {
		t.Fatalf("失败经验造成虚假升级：character=%+v skill=%+v calls=%d", character, skill.Points, probe.calls)
	}
}

type dungeonDeathBurstInputConn struct {
	net.Conn
	wire []byte
}

func (c *dungeonDeathBurstInputConn) Write(p []byte) (int, error) {
	c.wire = append(c.wire, p...)
	return len(p), nil
}

func TestDungeonDeathBurstRealConnectionFragmentationAndEOF(t *testing.T) {
	s, p, _, packets := prepareDungeonDeathBurstTest(t, true)
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	c := &dungeonDeathBurstInputConn{Conn: server}
	p.conn = c
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan struct{})
	go func() { defer close(finished); s.handleGameStreamInput(ctx, p) }()
	var input []byte
	for _, packet := range packets {
		input = append(input, packet.Data...)
	}
	for _, fragment := range [][]byte{input[:3], input[3:9], input[9:]} {
		if _, err := client.Write(fragment); err != nil {
			t.Fatal(err)
		}
	}
	_ = client.Close()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("分片或EOF导致读包循环未结束")
	}
	deaths, states := 0, 0
	for _, frame := range splitAllCurrentGameServerUpperPackets(t, c.wire) {
		if frame.Header.MsgID == 38 {
			deaths++
		}
		if frame.Header.MsgID == 37 {
			states++
			if binary.LittleEndian.Uint32(frame.Body[1:5]) != 0 || binary.LittleEndian.Uint64(frame.Body[59:67]) != 50 {
				t.Fatalf("EOF前未提交完整经验：%x", frame.Body)
			}
		}
	}
	if deaths != 5 || states != 1 {
		t.Fatalf("真实连接连续击杀：死亡=%d 经验提示=%d", deaths, states)
	}
}

type dungeonDeathBurstRevokingAccount struct {
	dnfrepo.AccountRepository
	session *gameSession
}

func (r dungeonDeathBurstRevokingAccount) Load(ctx context.Context, id string) (dnfrepo.AccountRecord, bool, error) {
	r.session.loginRevoked.Store(true)
	return r.AccountRepository.Load(ctx, id)
}

func TestDungeonDeathBurstRechecksReplacedLoginBetweenRequests(t *testing.T) {
	s, p, repos, packets := prepareDungeonDeathBurstTest(t, true)
	repos.Account = dungeonDeathBurstRevokingAccount{AccountRepository: repos.Account, session: p}
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	if err := s.handleCurrentDungeonDeathBurstPackets(p, packets, false); !errors.Is(err, errLoginReplaced) {
		t.Fatalf("替换登录未阻止后续请求：%v", err)
	}
	character, _, _ := repos.Character.Load(context.Background(), "99")
	if character.Stats["exp"] != 0 || character.Stats[currentHonorExpertProgressExperienceStatKey] != 10 || p.dungeonDeathBurst != nil {
		t.Fatalf("替换登录后接受了额外死亡或漏记之前奖励：%+v", character)
	}
}

// 公会练习场沿上游独立生命周期处理，不被普通怪经验批次接管。
func TestDungeonDeathBurstExcludesPublicGuildPractice(t *testing.T) {
	service, session, _, _ := prepareDungeonDeathBurstTest(t, false)
	if !service.currentDungeonDeathBurstEnabled(session) {
		t.Fatal("普通副本批次未启用")
	}
	session.dungeon.runtime.guildPractice = &currentGuildPracticeRun{}
	if service.currentDungeonDeathBurstEnabled(session) {
		t.Fatal("公会练习场进入普通击杀批次")
	}
}

func TestDungeonDeathBurstCollectorPreservesPartialBadFrameAndEOF(t *testing.T) {
	s, p, _, packets := prepareDungeonDeathBurstTest(t, true)
	ahead := &nativeResultReadAhead{ready: make(chan nativeResultReadChunk, 3)}
	ahead.ready <- nativeResultReadChunk{data: packets[1].Data[:4]}
	ahead.ready <- nativeResultReadChunk{data: packets[1].Data[4:]}
	ahead.ready <- nativeResultReadChunk{err: io.EOF}
	collected, tail, held := s.collectCurrentDungeonDeathReadyPackets(p, ahead, packets[:1], nil)
	if len(collected) != 2 || len(tail) != 0 || held == nil || !errors.Is(held.err, io.EOF) {
		t.Fatalf("死亡拆包或EOF丢失：%d %d %+v", len(collected), len(tail), held)
	}
	broken := &nativeResultReadAhead{ready: make(chan nativeResultReadChunk, 1)}
	broken.ready <- nativeResultReadChunk{data: []byte{0xff, 0xff, 0, 0}}
	collected, _, held = s.collectCurrentDungeonDeathReadyPackets(p, broken, packets[:1], nil)
	if len(collected) != 1 || held == nil {
		t.Fatal("坏帧丢弃此前完整死亡请求")
	}
	other := &nativeResultReadAhead{ready: make(chan nativeResultReadChunk, 1)}
	other.ready <- nativeResultReadChunk{data: packets[1].Data}
	collected, _, held = s.collectCurrentDungeonDeathReadyPackets(p, other, []dnfproto.LatestGameStreamPacket{nativeResultTestPacket(45, nil)}, nil)
	if len(collected) != 1 || held != nil || len(other.ready) != 1 {
		t.Fatal("死亡收集器消费了其他协议后续字节")
	}
}

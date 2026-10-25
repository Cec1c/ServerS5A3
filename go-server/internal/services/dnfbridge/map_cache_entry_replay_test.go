package dnfbridge

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnffatigue "longheng.io/server/internal/modules/dnf/fatigue"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

var replayClockStart = time.Now()

func replayCounter() int64              { return time.Since(replayClockStart).Nanoseconds() }
func replayElapsed(start int64) float64 { return float64(replayCounter()-start) / 1e6 }
func replayDigest(v any) string {
	b, e := json.Marshal(v)
	if e != nil {
		panic(e)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

type replayCase struct {
	Name    string
	Dungeon uint32
	Quest   int64
	Fatigue int64
}
type replayStep struct {
	Name      string
	Packets   []replayPacket
	Scene     string
	Character string
	Inventory string
}
type replayPacket struct {
	ID    uint16
	Class byte
	Body  string
}
type replayTrial struct {
	Case, Mode string
	Repeat     int
	Steps      []replayStep
	Times      map[string]float64
	Maps       []int64
	Moved      int
	Rejected   bool
}

func replayFlow(t *testing.T, base *Service, table *worldmap.Table, resolver *worldmap.Resolver, a *platformpvf.Archive, monsters *pvfDungeonMonsterCatalog, ai *pvfDungeonAICharacterCatalog, tutorial *pvfDungeonTutorialScriptCatalog, hell *pvfDungeonHellPartyCatalog, tc replayCase, mode string, repeat int, evict func()) replayTrial {
	t.Helper()
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	queue := newFakeCurrentDungeonDeathTimerQueue()
	character := dnfrepo.CharacterRecord{CharacterID: "99", AccountID: "account-1", Name: "缓存验证", Job: "11", Level: 90, Stats: map[string]int64{"fatigue": tc.Fatigue, "town_id": 38, "area_id": 1, "pos_x": 450, "pos_y": 234, "direction": 5, "area_state": 3}}
	dnffatigue.Refresh(&character, dnfrepo.AccountRecord{}, queue.Now())
	character.Stats["fatigue"] = tc.Fatigue
	if e := repos.Character.Save(ctx, character); e != nil {
		t.Fatal(e)
	}
	if e := repos.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: "account-1", Metadata: map[string]string{currentRentalPointMetadataKey: "0"}}); e != nil {
		t.Fatal(e)
	}
	if e := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "99", Slots: map[string]dnfrepo.ItemStack{}}); e != nil {
		t.Fatal(e)
	}
	states := map[int64]dnfrepo.QuestState{}
	if tc.Quest != 0 {
		states[tc.Quest] = dnfrepo.QuestState{Status: "active", ProgressValue: 1}
	}
	if e := repos.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: "99", States: states}); e != nil {
		t.Fatal(e)
	}
	s := base
	s.worldMapTable = table
	s.worldMapResolver = resolver
	s.dungeonMonsterTable = monsters
	s.dungeonAICharacterTable = ai
	s.dungeonTutorialScripts = tutorial
	s.dungeonHellPartyTable = hell
	s.dungeonChoice = func(int) (int, error) { return 0, nil }
	s.dungeonSeed = func() (uint32, error) { return 1, nil }
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	s.gameplayTimers = queue
	if _, e := s.ensureCharacterInitializationSnapshot(ctx, repos, character); e != nil {
		t.Fatalf("准备真实登录角色记录失败：%v", e)
	}
	skill, _, e := repos.Skill.Load(ctx, "99")
	if e != nil {
		t.Fatal(e)
	}
	skill.UpdatedAt = queue.Now()
	if e = repos.Skill.Save(ctx, skill); e != nil {
		t.Fatal(e)
	}
	equipment, _, e := repos.Equipment.Load(ctx, "99")
	if e != nil {
		t.Fatal(e)
	}
	equipment.UpdatedAt = queue.Now()
	if e = repos.Equipment.Save(ctx, equipment); e != nil {
		t.Fatal(e)
	}
	logger, logPath := newPeerEndpointTestLogger(t)
	s.packetLog = logger
	defer func() {
		logger.Close()
		if t.Failed() {
			if b, e := os.ReadFile(logPath); e == nil {
				if len(b) > 10000 {
					b = b[len(b)-10000:]
				}
				t.Logf("回放诊断：%s", b)
			}
		}
	}()
	conn := &bufferConn{}
	ch := channelcatalog.Channel{ServerID: 1, ID: 16, Type: 1, Name: "ch.16", Port: 10016}
	session := &gameSession{conn: conn, connID: "local-map-cache-replay", selectedCharacterID: 99, accountID: "account-1", channel: ch, residentChannel: ch}
	result := replayTrial{Case: tc.Name, Mode: mode, Repeat: repeat, Times: map[string]float64{}}
	capture := func(name string) {
		step := replayStep{Name: name}
		stream := conn.write.Bytes()
		for len(stream) > 0 {
			packet, rest := splitCurrentGameServerUpperPacketAuto(t, stream)
			step.Packets = append(step.Packets, replayPacket{packet.Header.MsgID, packet.Header.Classification, hex.EncodeToString(packet.Body)})
			stream = rest
		}
		conn.write.Reset()
		if r := session.dungeon.runtime; r != nil {
			step.Scene = replayDigest(r.Session.Snapshot())
			result.Maps = append(result.Maps, r.Session.Snapshot().Scene.Map.Map.ID)
		}
		c, _, err := repos.Character.Load(ctx, "99")
		if err != nil {
			t.Fatal(err)
		}
		i, _, err := repos.Inventory.Load(ctx, "99")
		if err != nil {
			t.Fatal(err)
		}
		step.Character = replayDigest(c)
		step.Inventory = replayDigest(i)
		result.Steps = append(result.Steps, step)
	}
	perform := func(name string, fn func() error) {
		start := replayCounter()
		err := fn()
		result.Times[name] = replayElapsed(start)
		if err != nil {
			t.Fatalf("%s/%s: %v", tc.Name, name, err)
		}
		capture(name)
	}
	enter := func() error {
		bindDungeonSelectorOriginForTestAt(t, s, session, 38, 1, 450, 234)
		body := make([]byte, 21)
		binary.LittleEndian.PutUint32(body, tc.Dungeon)
		if tc.Dungeon == 5000 {
			body[8] = 1
		}
		binary.LittleEndian.PutUint16(body[9:11], 0xffff)
		return s.handleDungeonSelectUpper(session, body)
	}
	perform("首次进图", enter)
	if session.dungeon.runtime == nil {
		result.Rejected = true
		if tc.Fatigue != 0 {
			t.Fatalf("%s 基线不应拒绝进图：%+v", tc.Name, result.Steps)
		}
		return result
	}
	perform("首次加载完成", func() error { return s.sendFinishLoadingStatus(session, make([]byte, 16)) })
	perform("重复加载完成", func() error { return s.sendFinishLoadingStatus(session, make([]byte, 16)) })
	if mode == "候选淘汰后" {
		evict()
	}
	r := session.dungeon.runtime
	scene := r.Session.Snapshot().Scene
	var target *worldmap.RoomCoordinate
	for _, room := range r.Session.Rooms() {
		if room.Coordinate == scene.Coordinate && len(room.Neighbors) > 0 {
			c := room.Neighbors[0].Coordinate
			target = &c
			break
		}
	}
	if target != nil {
		clearCurrentRealPVFSmokeRoom(t, r)
		body := make([]byte, dungeoncmd.MoveMapRequestSize)
		body[0], body[1] = byte(target.X), byte(target.Y)
		perform("过门", func() error { return s.handleDungeonMoveMap(session, body) })
		if after := r.Session.Snapshot().Scene; after.Coordinate != *target {
			t.Fatalf("%s 过门未提交：%v => %v", tc.Name, scene.Coordinate, after.Coordinate)
		}
		result.Moved++
		perform("过门加载完成", func() error { return s.sendFinishLoadingStatus(session, make([]byte, 16)) })
	}
	perform("申请回城", func() error { return s.handleDungeonBackToVillage(session, nil) })
	perform("确认回城", func() error { return s.commitPendingDungeonReturnForSceneRequest(session, "local_map_cache_replay") })
	if session.dungeon.runtime != nil {
		t.Fatalf("%s 回城没有释放运行时", tc.Name)
	}
	perform("退出后重进", enter)
	if session.dungeon.runtime == nil {
		t.Fatalf("%s 重进被拒绝", tc.Name)
	}
	perform("重进加载完成", func() error { return s.sendFinishLoadingStatus(session, make([]byte, 16)) })
	s.resetGameSessionForCharacterSelect(session)
	return result
}

func TestRealPVFMapCacheFullEntryReplay(t *testing.T) {
	if os.Getenv("DNF_WORLDMAP_REAL_PVF_SMOKE") == "" {
		t.Skip("未指定真实PVF")
	}
	t.Setenv("DNF_MAP_CACHE_REPLAY_OUT", t.TempDir())
	a, err := platformpvf.Open(os.Getenv("DNF_WORLDMAP_REAL_PVF_SMOKE"))
	if err != nil {
		t.Fatal(err)
	}
	base := New(nil)
	base.options.mapDefinitionCache = true
	base.initialEquipmentArchive = a
	base.options.pvfPath = os.Getenv("DNF_WORLDMAP_REAL_PVF_SMOKE")
	base.options.currentClientVersion = "official-24.9"
	base.options.gameUpperBodyCodec = gameUpperBodyCodecPlain
	base.options.accountID = "account-1"
	base.options.joustEventDisabled = true
	ctx := context.Background()
	for _, preload := range []struct {
		name string
		run  func(context.Context) error
	}{
		{"初始装备", base.preloadInitialEquipmentIndex}, {"物品目录", base.preloadPVFItemCatalog}, {"副职业", base.preloadExpertJobCatalog}, {"冒险团", base.preloadAdventureGroupTable}, {"荣誉", base.preloadHonorTable}, {"城镇", base.preloadTownCatalog}, {"技能", base.preloadSkillCatalog}, {"职业", base.preloadProfessionProfiles}, {"任务", base.preloadQuestCatalog}, {"角色属性", base.preloadCharacterStatTable}, {"装备属性", base.preloadEquipmentStatIndex}, {"战斗力", base.preloadCombatPowerAffixCatalog}, {"装备合成", base.preloadEquipmentRegenerationCatalog},
	} {
		if e := preload.run(ctx); e != nil {
			t.Fatalf("回放预载%s失败：%v", preload.name, e)
		}
	}
	t.Log("已按正式启动路径预载角色、技能及其他依赖；不打开监听、不连接数据库")
	table, err := worldmap.LoadSource(context.Background(), a, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := worldmap.NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}
	monsters, err := newPVFDungeonMonsterCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	ai, err := newPVFDungeonAICharacterCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	tutorial, err := newPVFDungeonTutorialScriptCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	tutorial.indexBasicActionDestroyTargets(a, table)
	hell, err := newPVFDungeonHellPartyCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	text, err := a.ReadText(worldmap.DefaultMapList)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := dnfpvf.Parse(worldmap.DefaultMapList, text)
	if err != nil {
		t.Fatal(err)
	}
	entries := dnfpvf.ParseList(doc)
	cases := []replayCase{{"普通幽暗密林", 3, 0, 156}, {"任务3145", 3, 3145, 156}, {"任务3146", 3, 3146, 156}, {"格蓝迪门", 104, 0, 156}, {"克雷门", 101, 0, 156}, {"修炼场", 5000, 0, 156}, {"时间广场", 310, 0, 156}, {"疲劳不足拒绝", 3, 0, 0}}
	repeats := 8
	if v, e := strconv.Atoi(os.Getenv("DNF_MAP_CACHE_REPLAY_REPEATS")); e == nil && v > 0 {
		repeats = v
	}
	evict := func() {
		for i := 0; i < 256; i++ {
			if _, ok := table.FindMap(entries[i*len(entries)/256].ID); !ok {
				t.Fatal("淘汰预备读取失败")
			}
		}
		a.ReleaseReadCaches()
	}
	// 对应真实角色已完成登录投影后的依赖缓存；预热流程不计入性能样本。
	for _, tc := range cases {
		replayFlow(t, base, table, resolver, a, monsters, ai, tutorial, hell, tc, "预热", 0, evict)
	}
	a.ReleaseReadCaches()
	runtime.GC()
	var initialMemory runtime.MemStats
	runtime.ReadMemStats(&initialMemory)
	memory := []map[string]any{{"阶段": "现用完整预载及登录依赖已热", "堆存活MiB": float64(initialMemory.HeapAlloc) / 1048576}}
	expected := map[string][]replayStep{}
	var trials []replayTrial
	for _, mode := range []string{"现用全量常驻", "候选首次", "候选命中", "候选淘汰后"} {
		if mode == "候选首次" {
			if err = table.EnableMapCache(a, worldmap.DefaultMapCacheOptions()); err != nil {
				t.Fatal(err)
			}
			a.ReleaseReadCaches()
			runtime.GC()
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			memory = append(memory, map[string]any{"阶段": "同一完整预载启用按需缓存", "堆存活MiB": float64(m.HeapAlloc) / 1048576})
			t.Logf("候选接入：%+v", table.MapCacheSnapshot())
		}
		for _, tc := range cases {
			for repeat := 0; repeat < repeats; repeat++ {
				if mode == "候选淘汰后" || mode == "候选首次" {
					evict()
				}
				trial := replayFlow(t, base, table, resolver, a, monsters, ai, tutorial, hell, tc, mode, repeat, evict)
				if want, ok := expected[tc.Name]; ok {
					if !reflect.DeepEqual(want, trial.Steps) {
						old, _ := json.MarshalIndent(want, "", "  ")
						new, _ := json.MarshalIndent(trial.Steps, "", "  ")
						os.WriteFile(filepath.Join(os.Getenv("DNF_MAP_CACHE_REPLAY_OUT"), "不一致_"+tc.Name+"_原.json"), old, 0600)
						os.WriteFile(filepath.Join(os.Getenv("DNF_MAP_CACHE_REPLAY_OUT"), "不一致_"+tc.Name+"_候选.json"), new, 0600)
						t.Fatalf("%s / %s / %d 发包或角色、房间状态不一致", mode, tc.Name, repeat)
					}
				} else {
					expected[tc.Name] = trial.Steps
				}
				trials = append(trials, trial)
			}
			t.Logf("已回放：%s / %s", mode, tc.Name)
		}
	}
	summaries := map[string]any{}
	for _, mode := range []string{"现用全量常驻", "候选首次", "候选命中", "候选淘汰后"} {
		var entryTimes, moveTimes []float64
		for _, trial := range trials {
			if trial.Mode == mode && !trial.Rejected {
				entryTimes = append(entryTimes, trial.Times["首次进图"])
				if trial.Moved > 0 {
					moveTimes = append(moveTimes, trial.Times["过门"])
				}
			}
		}
		summaries[mode] = map[string]any{"进图": replayLatency(entryTimes), "过门": replayLatency(moveTimes)}
	}
	result := map[string]any{"条件": "同一实际PVF、Resolver、固定随机种子和时间、隔离内存仓库；逐包消息号、分类、正文及角色/库存/房间摘要完全比较", "回放次数": len(trials), "完整预载内存": memory, "统计": summaries, "缓存": table.MapCacheSnapshot(), "回放": trials}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(os.Getenv("DNF_MAP_CACHE_REPLAY_OUT"), "完整进图回放.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("完整回放一致：%d组；%v", len(trials), summaries)
}
func replayLatency(v []float64) any {
	if len(v) == 0 {
		return nil
	}
	sort.Float64s(v)
	at := func(n int) float64 { return v[(len(v)-1)*n/100] }
	return map[string]any{"次数": len(v), "中位毫秒": at(50), "P95毫秒": at(95), "最大毫秒": at(100)}
}

// 明确证明底层地图读取失败时不扣疲劳、不创建运行时，也不发起始地图。
type replayFailSource struct{}

func (replayFailSource) ReadText(string) (string, error) { return "", fmt.Errorf("隔离读取故障") }
func TestMapCacheEntryReadFailurePreservesAssets(t *testing.T) {
	s, session := newDungeonSelectRefusalFixture(t, dnfrepo.CharacterRecord{CharacterID: "99", AccountID: "account-1", Level: 20, Stats: map[string]int64{"fatigue": 100, "town_id": 38, "area_id": 1, "pos_x": 450, "pos_y": 234, "direction": 5, "area_state": 3}}, nil)
	s.options.mapDefinitionCache = true
	if err := s.worldMapTable.EnableMapCache(replayFailSource{}, worldmap.DefaultMapCacheOptions()); err != nil {
		t.Fatal(err)
	}
	repos, _ := s.repositoryGroup()
	s.gameplayTimers = newFakeCurrentDungeonDeathTimerQueue()
	if _, err := s.updateCurrentCharacterFatigue(context.Background(), session, false); err != nil {
		t.Fatal(err)
	}
	before, _, err := repos.Character.Load(context.Background(), "99")
	if err != nil {
		t.Fatal(err)
	}
	bindDungeonSelectorOriginForTestAt(t, s, session, 38, 1, 450, 234)
	body := make([]byte, 21)
	binary.LittleEndian.PutUint32(body, 700)
	if err = s.handleDungeonSelectUpper(session, body); err != nil {
		t.Fatal(err)
	}
	after, _, err := repos.Character.Load(context.Background(), "99")
	if err != nil {
		t.Fatal(err)
	}
	if session.dungeon.runtime != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("地图读取失败仍提交角色或运行时：active=%t before=%+v after=%+v", session.dungeon.runtime != nil, before.Stats, after.Stats)
	}
	conn := session.conn.(*bufferConn)
	for raw := conn.write.Bytes(); len(raw) > 0; {
		p, rest := splitCurrentGameServerUpperPacketAuto(t, raw)
		if p.Header.MsgID == currentDungeonStartNotification {
			t.Fatal("读取失败发出了起始地图")
		}
		raw = rest
	}
}

func TestMapCacheTicketConsumptionFollowsSuccessfulPreparation(t *testing.T) {
	t.Setenv(dungeonTournamentEnvKey, dungeonTournamentModeVendor)
	for _, fail := range []bool{true, false} {
		t.Run(fmt.Sprintf("故障=%t", fail), func(t *testing.T) {
			s, session := newDungeonSelectRefusalFixture(t, dnfrepo.CharacterRecord{CharacterID: "99", AccountID: "account-1", Level: 20, Stats: map[string]int64{"fatigue": 100, "town_id": 38, "area_id": 1, "pos_x": 450, "pos_y": 234, "direction": 5, "area_state": 3}}, nil)
			s.options.mapDefinitionCache = true
			source := bridgeDungeonPVF(false)
			source["dungeon/test.dgn"] += "[required item]\n4726 1 1\n[/required item]\n"
			table, resolver, monsters := loadBridgeDungeonStaticData(t, source)
			s.worldMapTable = table
			s.worldMapResolver = resolver
			s.dungeonMonsterTable = monsters
			var readSource dnfpvf.Source = source
			if fail {
				readSource = replayFailSource{}
			}
			if e := table.EnableMapCache(readSource, worldmap.DefaultMapCacheOptions()); e != nil {
				t.Fatal(e)
			}
			repos, _ := s.repositoryGroup()
			s.gameplayTimers = newFakeCurrentDungeonDeathTimerQueue()
			if _, e := s.updateCurrentCharacterFatigue(context.Background(), session, false); e != nil {
				t.Fatal(e)
			}
			inventory := dnfrepo.InventoryRecord{CharacterID: "99", Slots: map[string]dnfrepo.ItemStack{"0:0": {ItemID: 4726, Count: 2}}}
			if e := repos.Inventory.Save(context.Background(), inventory); e != nil {
				t.Fatal(e)
			}
			before, _, _ := repos.Character.Load(context.Background(), "99")
			bindDungeonSelectorOriginForTestAt(t, s, session, 38, 1, 450, 234)
			body := make([]byte, 21)
			binary.LittleEndian.PutUint32(body, 700)
			if e := s.handleDungeonSelectUpper(session, body); e != nil {
				t.Fatal(e)
			}
			after, _, _ := repos.Character.Load(context.Background(), "99")
			got, _, _ := repos.Inventory.Load(context.Background(), "99")
			if fail {
				if session.dungeon.runtime != nil || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(inventory, got) {
					t.Fatalf("地图错误后资产改变：active=%t fp=%d->%d ticket=%+v", session.dungeon.runtime != nil, before.Stats["fatigue"], after.Stats["fatigue"], got)
				}
			} else {
				if session.dungeon.runtime == nil || got.Slots["0:0"].Count != 1 || after.Stats["fatigue"] != 99 {
					t.Fatalf("正常入场未按PVF扣一次：active=%t fp=%d tickets=%+v", session.dungeon.runtime != nil, after.Stats["fatigue"], got)
				}
			}
		})
	}
}

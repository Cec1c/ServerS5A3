package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	dnfexpertjob "longheng.io/server/internal/modules/dnf/expertjob"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

type nativeExtractionTrace struct {
	dnfrepo.CharacterSettlementUnitOfWork
	transactions, characters, inventories, accounts, experienceSaves, inventorySaves, accountSaves int
	failure                                                                                        string
}
type nativeExtractionCharacterTrace struct {
	dnfrepo.CharacterRepository
	trace *nativeExtractionTrace
}

func (r nativeExtractionCharacterTrace) Load(ctx context.Context, id string) (dnfrepo.CharacterRecord, bool, error) {
	r.trace.characters++
	return r.CharacterRepository.Load(ctx, id)
}
func (r nativeExtractionCharacterTrace) SaveExpertJobExperience(ctx context.Context, c dnfrepo.CharacterRecord) error {
	r.trace.experienceSaves++
	if r.trace.failure == "经验" {
		return errors.New("注入经验保存失败")
	}
	return dnfrepo.SaveCharacterExpertExperience(ctx, r.CharacterRepository, c)
}

type nativeExtractionInventoryTrace struct {
	dnfrepo.InventoryRepository
	trace *nativeExtractionTrace
}

func (r nativeExtractionInventoryTrace) Load(ctx context.Context, id string) (dnfrepo.InventoryRecord, bool, error) {
	r.trace.inventories++
	return r.InventoryRepository.Load(ctx, id)
}
func (r nativeExtractionInventoryTrace) Save(ctx context.Context, c dnfrepo.InventoryRecord) error {
	r.trace.inventorySaves++
	if r.trace.failure == "背包" {
		return errors.New("注入背包保存失败")
	}
	return r.InventoryRepository.Save(ctx, c)
}

type nativeExtractionAccountTrace struct {
	dnfrepo.AccountInventoryRepository
	trace *nativeExtractionTrace
}

func (r nativeExtractionAccountTrace) Load(ctx context.Context, id string) (dnfrepo.AccountInventoryRecord, bool, error) {
	r.trace.accounts++
	return r.AccountInventoryRepository.Load(ctx, id)
}
func (r nativeExtractionAccountTrace) Save(ctx context.Context, c dnfrepo.AccountInventoryRecord) error {
	r.trace.accountSaves++
	if r.trace.failure == "共享仓库" {
		return errors.New("注入共享仓库保存失败")
	}
	return r.AccountInventoryRepository.Save(ctx, c)
}
func (r *nativeExtractionTrace) WithinCharacterSettlement(ctx context.Context, id string, apply func(dnfrepo.Group) error) error {
	r.transactions++
	return r.CharacterSettlementUnitOfWork.WithinCharacterSettlement(ctx, id, func(g dnfrepo.Group) error {
		g.Character = nativeExtractionCharacterTrace{g.Character, r}
		g.Inventory = nativeExtractionInventoryTrace{g.Inventory, r}
		g.AccountInventory = nativeExtractionAccountTrace{g.AccountInventory, r}
		if err := apply(g); err != nil {
			return err
		}
		if r.failure == "提交" {
			return errors.New("注入最终提交前失败")
		}
		return nil
	})
}

func nativeExtractionRealCatalogs(t *testing.T) (*dnfexpertjob.Catalog, *pvfDungeonDropCatalog) {
	t.Helper()
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("需要匹配的真实PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := dnfexpertjob.Load(context.Background(), archive)
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	return jobs, items
}
func nativeExtractionChooseTool(t *testing.T, jobs *dnfexpertjob.Catalog, kind byte) int64 {
	t.Helper()
	config, ok := jobs.Config(kind)
	if !ok {
		t.Fatal("缺少副职业配置")
	}
	metadata, err := jobs.Equipment(100050285)
	if err != nil {
		t.Fatal(err)
	}
	metadata.State = currentExpertJobEquipmentState(dnfrepo.ItemStack{ItemID: 100050285, Count: 1})
	ids := make([]int64, 0, len(config.Extractors))
	for id := range config.Extractors {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		if plan, e := jobs.PlanExtraction(kind, 100, id, metadata, func(int) int { return 0 }); e == nil && len(plan.Materials) > 0 {
			return id
		}
	}
	t.Fatal("没有真实可用提取器")
	return 0
}
func nativeExtractionRealFixture(t *testing.T, jobs *dnfexpertjob.Catalog, items *pvfDungeonDropCatalog, kind byte, count int, initial, tool int64) (*Service, *gameSession, dnfrepo.Group, *nativeExtractionTrace, []dnfproto.LatestGameStreamPacket) {
	t.Helper()
	t.Setenv(currentExpertJobExpNotifyEnv, "on")
	t.Setenv("DNFBRIDGE_NATIVE_RESULT_DELIVERY", "on")
	ctx := context.Background()
	repos := mustCurrentNPCShopRepositories(t, 1000, nil)
	c, _, _ := repos.Character.Load(ctx, "19")
	c.Name = "extract_batch"
	c.Stats["expert_job_type"] = int64(kind)
	c.Stats["expert_job_exp"] = initial
	if err := repos.Character.Save(ctx, c); err != nil {
		t.Fatal(err)
	}
	bag, _, _ := repos.Inventory.Load(ctx, "19")
	bag.Slots = map[string]dnfrepo.ItemStack{"0:200": {ItemID: tool, Count: 1}}
	packets := make([]dnfproto.LatestGameStreamPacket, count)
	for i := range count {
		slot := int16(i + 10)
		bag.Slots[fmt.Sprintf("0:%d", slot)] = dnfrepo.ItemStack{ItemID: 100050285, Count: 1}
		body := []byte{kind, 200, 0, 0, byte(slot), byte(slot >> 8)}
		if i%2 == 1 {
			body = append(body, 1, 2, 3, 4)
		}
		packets[i] = nativeResultTestPacket(416, body)
	}
	if err := repos.Inventory.Save(ctx, bag); err != nil {
		t.Fatal(err)
	}
	if err := repos.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{AccountID: "account-1", Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	trace := &nativeExtractionTrace{CharacterSettlementUnitOfWork: repos.CharacterSettlement}
	repos.CharacterSettlement = trace
	s := currentDisjointTestService(repos, items)
	s.expertJobCatalog = jobs
	s.options.currentClientVersion = "official-24.9"
	s.options.maxPacketBytes = 65536
	p := &gameSession{conn: &channelSceneWriteConn{}, selectedCharacterID: 19, characterGeneration: 1, accountID: "account-1", accountFromToken: true, loginPacketSeen: true, itemListMigrationsDone: true}
	p.channel.ID = 10
	s.gameSessions = map[uint16]*gameSession{19: p}
	return s, p, repos, trace, packets
}

func TestNativeExtractionPrefixAndCollectorKeepBoundaries(t *testing.T) {
	s, p, _, _, _ := nativeNPCBatchFixture(t, 2)
	first := nativeResultTestPacket(416, []byte{2, 200, 0, 0, 10, 0})
	second := nativeResultTestPacket(416, []byte{2, 200, 0, 0, 11, 0, 1, 2, 3, 4})
	if nativeExtractionPrefix([]dnfproto.LatestGameStreamPacket{first, second}) != 2 {
		t.Fatal("传输尾误拆同入口")
	}
	for _, body := range [][]byte{{4, 200, 0, 0, 11, 0}, {2, 201, 0, 0, 11, 0}, {2, 200, 0, 1, 11, 0}, {2, 200, 0, 0, 200, 0}} {
		if nativeExtractionPrefix([]dnfproto.LatestGameStreamPacket{first, nativeResultTestPacket(416, body)}) != 1 {
			t.Fatal("职业/工具/容器或非法请求越界合并")
		}
	}
	many := make([]dnfproto.LatestGameStreamPacket, 128)
	for i := range many {
		many[i] = first
	}
	if nativeExtractionPrefix(many) != 127 {
		t.Fatal("件数上限失效")
	}
	upper := first
	upper.Kind = dnfproto.LatestGameStreamUpper
	if nativeExtractionPrefix([]dnfproto.LatestGameStreamPacket{upper}) != 0 || nativeExtractionPrefix([]dnfproto.LatestGameStreamPacket{nativeResultTestPacket(26, currentDisjointItemTestBody(10, 0, -1, 317))}) != 0 {
		t.Fatal("改变其它协议入口")
	}
	ahead := &nativeResultReadAhead{ready: make(chan nativeResultReadChunk, 3)}
	ahead.ready <- nativeResultReadChunk{data: second.Data[:3]}
	ahead.ready <- nativeResultReadChunk{data: second.Data[3:]}
	ahead.ready <- nativeResultReadChunk{err: io.EOF}
	packets, tail, held := s.collectNativeExtractionReadyPackets(p, ahead, []dnfproto.LatestGameStreamPacket{first}, nil)
	if len(packets) != 2 || len(tail) != 0 || held == nil || !errors.Is(held.err, io.EOF) {
		t.Fatal("碎片或EOF顺序改变")
	}
	broken := &nativeResultReadAhead{ready: make(chan nativeResultReadChunk, 1)}
	broken.ready <- nativeResultReadChunk{data: []byte{255, 255, 0, 0}}
	packets, _, held = s.collectNativeExtractionReadyPackets(p, broken, []dnfproto.LatestGameStreamPacket{first}, nil)
	if len(packets) != 1 || held == nil {
		t.Fatal("坏帧影响此前完整请求")
	}
	empty := &nativeResultReadAhead{ready: make(chan nativeResultReadChunk)}
	done := make(chan struct{})
	go func() {
		s.collectNativeExtractionReadyPackets(p, empty, []dnfproto.LatestGameStreamPacket{first}, nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("队列为空却等待合批")
	}
}

func TestNativeExtractionBatchKeepsSessionGuards(t *testing.T) {
	for _, mode := range []string{"未选角色", "未绑定", "无频道", "换线投影", "登录重建", "NPC重放", "提取重放", "非配套客户端"} {
		t.Run(mode, func(t *testing.T) {
			s, p, _, _, _ := nativeNPCBatchFixture(t, 2)
			s.options.currentClientVersion = "official-24.9"
			t.Setenv(currentExpertJobExpNotifyEnv, "on")
			t.Setenv("DNFBRIDGE_NATIVE_RESULT_DELIVERY", "on")
			if !s.nativeExtractionBatchAllowed(p) {
				t.Fatal("正常城镇会话被拒绝")
			}
			switch mode {
			case "未选角色":
				p.selectedCharacterID = 0
			case "未绑定":
				s.gameSessions = nil
			case "无频道":
				p.channel.ID = 0
			case "换线投影":
				p.channelSceneBatch = &currentChannelSceneBatch{}
			case "登录重建":
				p.loginActorGenerationRebuildArmed = true
			case "NPC重放":
				p.nativeNPCDisjointReplay = &nativeNPCDisjointReplay{}
			case "提取重放":
				p.nativeExtractionReplay = &nativeExtractionReplay{}
			case "非配套客户端":
				s.options.currentClientVersion = "other"
			}
			requests := []dnfproto.LatestGameStreamPacket{
				nativeResultTestPacket(416, []byte{2, 200, 0, 0, 10, 0}),
				nativeResultTestPacket(416, []byte{2, 200, 0, 0, 11, 0}),
			}
			if handled, err := s.handleNativeExtractionBatch(p, requests); handled || err != nil {
				t.Fatal("原有会话边界被绕过", handled, err)
			}
		})
	}
}

func TestRealPVFNativeExtractionBatchProgressAndAssets(t *testing.T) {
	jobs, items := nativeExtractionRealCatalogs(t)
	for _, kind := range []byte{1, 2, 4} {
		config, _ := jobs.Config(kind)
		tool := nativeExtractionChooseTool(t, jobs, kind)
		original := config.Extractors[tool]
		for _, count := range []int{24, 127} {
			for _, mode := range []string{"正常经验", "跨等级", "零经验"} {
				t.Run(fmt.Sprintf("职业%d/%d/%s", kind, count, mode), func(t *testing.T) {
					extractor := original
					if mode == "零经验" {
						if extractor.MinimumExperienceGain > 0 {
							t.Fatal("真实提取器不包含零经验端点")
						}
						extractor.MaximumExperienceGain = 0
					} else {
						if extractor.MinimumExperienceGain > 1 || extractor.MaximumExperienceGain < 1 {
							t.Fatal("真实提取器不包含1点经验端点")
						}
						extractor.MinimumExperienceGain = 1
						extractor.MaximumExperienceGain = 1
					}
					config.Extractors[tool] = extractor
					defer func() { config.Extractors[tool] = original }()
					initial := int64(100)
					if mode == "跨等级" {
						for _, threshold := range config.ExperienceThresholds {
							if threshold > initial+1 {
								initial = threshold - 1
								break
							}
						}
						if config.Level(initial) == config.Level(initial+1) {
							t.Fatal("没有真实跨级边界")
						}
					}
					s, p, repos, trace, requests := nativeExtractionRealFixture(t, jobs, items, kind, count, initial, tool)
					before, _, _ := repos.Character.Load(context.Background(), "19")
					handled, err := s.handleNativeExtractionBatch(p, requests)
					if !handled || err != nil || p.nativeExtractionReplay != nil {
						t.Fatal(handled, err)
					}
					if trace.transactions != 1 || trace.characters != 1 || trace.inventories != 1 || trace.accounts != 1 || trace.inventorySaves != 1 {
						t.Fatalf("仍逐件资产事务：%+v", trace)
					}
					expectedSaves := 1
					if mode == "零经验" {
						expectedSaves = 0
					}
					if trace.experienceSaves != expectedSaves {
						t.Fatal("经验保存次数不符", trace.experienceSaves)
					}
					conn := p.conn.(*channelSceneWriteConn)
					if len(conn.writes) != 0 {
						t.Fatal("事务结果提前投影")
					}
					if err := s.finishNativeResultDelivery(p, "真实提取批次"); err != nil {
						t.Fatal(err)
					}
					if len(conn.writes) != 1 {
						t.Fatal("连续结果被拆开")
					}
					wire := splitAllGameServerUpperPackets(t, conn.write.Bytes())
					ack, updates, actors, infos := 0, 0, 0, 0
					gains := map[int64]int64{}
					slotCounts := map[int16]uint32{}
					for _, packet := range wire {
						switch packet.Header.MsgID {
						case 416:
							body := packet.Body
							if packet.Header.Classification != 1 || body[0] != 1 || int(binary.LittleEndian.Uint16(body[2:4])) != ack+10 {
								t.Fatal("应答来源或顺序改变", ack)
							}
							for row := 0; row < int(body[4]); row++ {
								at := 5 + row*10
								slot := int16(binary.LittleEndian.Uint16(body[at:]))
								id := int64(binary.LittleEndian.Uint32(body[at+2:]))
								n := binary.LittleEndian.Uint32(body[at+6:])
								gains[id] += int64(n)
								slotCounts[slot] += n
							}
							ack++
						case 14:
							updates++
							body := packet.Body
							if packet.Header.Classification != 0 || body[0] != 0 {
								t.Fatal("局部刷新分类或容器改变")
							}
							for at := 3; at < len(body); at += currentItemListEntryWireSize {
								slot := int16(binary.LittleEndian.Uint16(body[at:]))
								id := binary.LittleEndian.Uint32(body[at+2:])
								n := binary.LittleEndian.Uint32(body[at+6:])
								if slot == int16(ack+9) {
									if id != ^uint32(0) || n != 0 {
										t.Fatal("来源未清空")
									}
								} else if n != slotCounts[slot] {
									t.Fatal("局部刷新不是对应请求的递进状态", slot, n, slotCounts[slot])
								}
							}
						case 2:
							actors++
							_, offset := expertJobExpNotifySubtype0BodyOffsets(before.Name)
							want := initial + int64(ack)
							if mode == "零经验" {
								t.Fatal("零经验仍发USERINFO")
							}
							if len(packet.Body) < offset+4 || int64(binary.LittleEndian.Uint32(packet.Body[offset:])) != want {
								t.Fatal("经验不是逐件累加的已提交结果", ack)
							}
						case 205:
							infos++
							want := initial + int64(ack)
							if config.Level(want-1) == config.Level(want) {
								t.Fatal("未升级却重发配方")
							}
						case 13:
							t.Fatal("出现整容器替换")
						}
					}
					wantActors, wantInfos := count, 0
					if mode == "零经验" {
						wantActors = 0
					} else {
						for i := 1; i <= count; i++ {
							if config.Level(initial+int64(i-1)) != config.Level(initial+int64(i)) {
								wantInfos++
							}
						}
					}
					if ack != count || updates != count || actors != wantActors || infos != wantInfos {
						t.Fatal("应答/刷新/经验/升级数量改变", ack, updates, actors, infos, wantActors, wantInfos)
					}
					after, _, _ := repos.Character.Load(context.Background(), "19")
					wantExp := initial
					if mode != "零经验" {
						wantExp += int64(count)
					}
					if after.Stats["expert_job_exp"] != wantExp {
						t.Fatal("最终经验落库不符")
					}
					for key, value := range before.Stats {
						if key != "expert_job_exp" && after.Stats[key] != value {
							t.Fatal("其它角色属性被改变", key)
						}
					}
					bag, _, _ := repos.Inventory.Load(context.Background(), "19")
					account, _, _ := repos.AccountInventory.Load(context.Background(), "account-1")
					actual := map[int64]int64{}
					for key, stack := range bag.Slots {
						if key == "0:200" {
							if stack.ItemID != tool || stack.Count != 1 {
								t.Fatal("提取器被消耗")
							}
							continue
						}
						actual[stack.ItemID] += stack.Count
					}
					for _, stack := range account.Slots {
						actual[stack.ItemID] += stack.Count
					}
					if !reflect.DeepEqual(actual, gains) {
						t.Fatal("产物与ACK总量不符", actual, gains)
					}
					t.Logf("%d件：单事务，经验与局部槽逐件递进，产物/最终经验一致", count)
				})
			}
		}
	}
}

func TestRealPVFNativeExtractionBatchRejectionRollbackAndShortWrite(t *testing.T) {
	jobs, items := nativeExtractionRealCatalogs(t)
	config, _ := jobs.Config(2)
	tool := nativeExtractionChooseTool(t, jobs, 2)
	original := config.Extractors[tool]
	fixed := original
	fixed.MinimumExperienceGain = 1
	fixed.MaximumExperienceGain = 1
	config.Extractors[tool] = fixed
	defer func() { config.Extractors[tool] = original }()
	rules := maps.Clone(config.ExtractionRules)
	defer func() { config.ExtractionRules = rules }()
	// 失败注入将真实规则的主要产物指向实际存在的无色固定槽，以覆盖共享仓库保存。
	for key, rule := range config.ExtractionRules {
		rule.ResultItemID = 3037
		rule.Multiplier = 1
		rule.BigWinChancePercent = 0
		rule.AdditionalTable = 777
		config.ExtractionRules[key] = rule
	}
	additional := config.AdditionalResults[777]
	defer func() { config.AdditionalResults[777] = additional }()
	for _, mode := range []string{"无效与重复", "账号越权", "经验", "背包", "共享仓库", "提交", "部分产物失败", "短写", "单件", "legacy"} {
		t.Run(mode, func(t *testing.T) {
			config.AdditionalResults[777] = nil
			if mode == "部分产物失败" {
				config.AdditionalResults[777] = []dnfexpertjob.SelectionRule{{MinimumLevel: 1, MaximumLevel: 999, ItemID: 88888888, Weight: 1, QuantityMultiplier: 1}}
			}
			s, p, repos, trace, packets := nativeExtractionRealFixture(t, jobs, items, 2, 4, 100, tool)
			beforeChar, _, _ := repos.Character.Load(context.Background(), "19")
			beforeBag, _, _ := repos.Inventory.Load(context.Background(), "19")
			beforeAccount, _, _ := repos.AccountInventory.Load(context.Background(), "account-1")
			switch mode {
			case "账号越权":
				p.accountID = "other-account"
			case "无效与重复":
				packets = []dnfproto.LatestGameStreamPacket{packets[0], nativeResultTestPacket(416, []byte{2, 200, 0, 0, 250, 0}), packets[0], packets[3]}
			case "经验", "背包", "共享仓库", "提交":
				trace.failure = mode
			case "短写":
				p.conn = &channelSceneWriteConn{short: true}
			case "单件":
				packets = packets[:1]
			case "legacy":
				t.Setenv(currentExpertJobExpNotifyEnv, "legacy")
			}
			handled, err := s.handleNativeExtractionBatch(p, packets)
			if mode == "单件" || mode == "legacy" {
				if handled || err != nil || trace.transactions != 0 {
					t.Fatal("单件或兼容档进入合批")
				}
				if err := s.handleGameStreamPacket(p, packets[0]); err != nil {
					t.Fatal(err)
				}
				if trace.transactions != 1 {
					t.Fatal("单件没有立即提交")
				}
				return
			}
			if !handled || err != nil {
				t.Fatal(handled, err)
			}
			err = s.finishNativeResultDelivery(p, "提取失败验证")
			if mode == "短写" {
				if !errors.Is(err, io.ErrShortWrite) || !p.conn.(*channelSceneWriteConn).closed || p.nativeExtractionReplay != nil {
					t.Fatal("短写未清理")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			afterChar, _, _ := repos.Character.Load(context.Background(), "19")
			afterBag, _, _ := repos.Inventory.Load(context.Background(), "19")
			afterAccount, _, _ := repos.AccountInventory.Load(context.Background(), "account-1")
			if mode == "账号越权" || mode == "经验" || mode == "背包" || mode == "共享仓库" || mode == "提交" || mode == "部分产物失败" {
				if !reflect.DeepEqual(beforeChar, afterChar) || !reflect.DeepEqual(beforeBag, afterBag) || !reflect.DeepEqual(beforeAccount, afterAccount) {
					t.Fatal("失败未回滚来源、经验或部分产物")
				}
				wire := splitAllGameServerUpperPackets(t, p.conn.(*channelSceneWriteConn).write.Bytes())
				if len(wire) != 4 {
					t.Fatal("失败仍发槽或经验刷新", len(wire))
				}
				for _, packet := range wire {
					if packet.Header.MsgID != 416 || packet.Body[0] != 0 {
						t.Fatal("失败仍发成功")
					}
				}
			} else if mode == "无效与重复" {
				var success []byte
				for _, packet := range splitAllGameServerUpperPackets(t, p.conn.(*channelSceneWriteConn).write.Bytes()) {
					if packet.Header.MsgID == 416 {
						success = append(success, packet.Body[0])
					}
				}
				if !reflect.DeepEqual(success, []byte{1, 0, 0, 1}) || afterChar.Stats["expert_job_exp"] != 102 {
					t.Fatal("逐件拒绝或经验顺序改变", success)
				}
			} else if mode == "短写" {
				if afterChar.Stats["expert_job_exp"] != 104 || len(afterBag.Slots) != 1 || len(afterAccount.Slots) != 1 {
					t.Fatal("提交后短写丢失资产")
				}
				if err := s.finishNativeResultDelivery(p, "重复清理"); err != nil {
					t.Fatal(err)
				}
			}
			if trace.transactions != 1 {
				t.Fatal("重复提交资产", trace.transactions)
			}
		})
	}
}

type nativeExtractionBlockingSettlement struct {
	dnfrepo.CharacterSettlementUnitOfWork
	once             sync.Once
	entered, release chan struct{}
}

func (b *nativeExtractionBlockingSettlement) WithinCharacterSettlement(ctx context.Context, id string, apply func(dnfrepo.Group) error) error {
	b.once.Do(func() { close(b.entered); <-b.release })
	return b.CharacterSettlementUnitOfWork.WithinCharacterSettlement(ctx, id, apply)
}
func TestRealPVFNativeExtractionInputLoopBatch(t *testing.T) {
	jobs, items := nativeExtractionRealCatalogs(t)
	tool := nativeExtractionChooseTool(t, jobs, 2)
	s, p, repos, trace, packets := nativeExtractionRealFixture(t, jobs, items, 2, 24, 100, tool)
	blocked := &nativeExtractionBlockingSettlement{CharacterSettlementUnitOfWork: repos.CharacterSettlement, entered: make(chan struct{}), release: make(chan struct{})}
	repos.CharacterSettlement = blocked
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	var once sync.Once
	release := func() { once.Do(func() { close(blocked.release) }) }
	defer release()
	var rest []byte
	for _, packet := range packets[1:] {
		rest = append(rest, packet.Data...)
	}
	conn := &nativeNPCScriptedConn{chunks: [][]byte{packets[0].Data, rest[:3], rest[3:19], rest[19:]}, beforeRest: blocked.entered, queued: make(chan struct{}), stop: make(chan struct{}), writeReady: make(chan struct{}, 4)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer conn.Close()
	p.conn, p.ctx = conn, ctx
	done := make(chan struct{})
	go func() { defer close(done); s.handleGameStreamInput(ctx, p) }()
	select {
	case <-blocked.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("首件未进入事务")
	}
	select {
	case <-conn.queued:
	case <-time.After(5 * time.Second):
		t.Fatal("后续碎片未排队")
	}
	release()
	select {
	case <-conn.writeReady:
	case <-time.After(10 * time.Second):
		t.Fatal("结果未写出")
	}
	conn.mu.Lock()
	wire := append([]byte(nil), conn.wire...)
	writes := conn.writes
	conn.mu.Unlock()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("读取未结束")
	}
	acks := 0
	for _, packet := range splitAllGameServerUpperPackets(t, wire) {
		if packet.Header.MsgID == 416 {
			if packet.Body[0] != 1 {
				t.Fatal("提取失败")
			}
			acks++
		}
	}
	if writes != 1 || acks != 24 || trace.transactions != 2 {
		t.Fatal("真实输入仍逐件事务或分开写回", writes, acks, trace.transactions)
	}
}

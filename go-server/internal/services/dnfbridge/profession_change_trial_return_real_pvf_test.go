package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/inventory"
	"longheng.io/server/internal/modules/dnf/profession"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// 故障只在本次测试明确布防的回城投影阶段触发；首次入图、op24和重入均走正常写入。
// 本专项固定server16明文配套，因此mode1判据读取当前16字节包头后的首字节。
type professionTrialReturnFailConn struct {
	*bufferConn
	stage  string
	failed bool
}

// 仅模拟已核对的原生对象身份契约：op13/list0清除原对象并从0x77行创建新对象，
// E7D7F0/25ECE10按指针相等查找槽位。这里不模拟真实分配器的地址复用，
// 所以重建后旧指针查不到；现场复用为其它槽位也是同一生命周期缺陷。
type professionTrialBookObject struct{ itemID uint32 }

func applyProfessionTrialMainContainer(t *testing.T, objects map[uint16]*professionTrialBookObject, stream []byte) {
	t.Helper()
	for _, packet := range splitProfessionTrialServer16Packets(t, stream) {
		if packet.Header.Classification != 0 || packet.Header.MsgID != 13 || len(packet.Body) < 5 || packet.Body[0] != 0 {
			continue
		}
		clear(objects)
		count := int(binary.LittleEndian.Uint16(packet.Body[3:5]))
		if len(packet.Body) != 5+count*currentItemListEntryWireSize {
			t.Fatal("主背包真实封包行宽不符")
		}
		for i := 0; i < count; i++ {
			row := packet.Body[5+i*currentItemListEntryWireSize:]
			objects[binary.LittleEndian.Uint16(row)] = &professionTrialBookObject{itemID: binary.LittleEndian.Uint32(row[2:])}
		}
	}
}

func (c *professionTrialReturnFailConn) Write(data []byte) (int, error) {
	if c.stage != "" && !c.failed && len(data) >= 16 {
		op := binary.LittleEndian.Uint16(data[1:3])
		match := c.stage == "回城ACK42" && data[0] == 1 && op == 42
		match = match || (c.stage == "回城mode0" && data[0] == 0 && op == 2 && len(data) > 16 && data[16] == 0)
		match = match || (c.stage == "回城mode1" && data[0] == 0 && op == 2 && len(data) > 16 && data[16] == 1)
		match = match || (c.stage == "回城op19" && data[0] == 0 && op == currentSkillInfoMsgID)
		if match {
			c.failed = true
			return 0, io.ErrClosedPipe
		}
	}
	return c.bufferConn.Write(data)
}

func TestProfessionChangeTrialReturnAndReentryRealPVF(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("需要现用真实PVF，验证7142体验返回、正式提交和再次入场")
	}
	ctx := context.Background()
	archive := levelUpTicketRealArchive(t)
	table, err := worldmap.LoadSource(ctx, archive, worldmap.Options{})
	if err != nil {
		t.Fatal("加载真实地图目录失败", err)
	}
	resolver, err := worldmap.NewResolver(table)
	if err != nil {
		t.Fatal("建立真实地图解析器失败", err)
	}
	monsters, err := newPVFDungeonMonsterCatalog(archive)
	if err != nil {
		t.Fatal("加载真实怪物目录失败", err)
	}
	aiCharacters, err := newPVFDungeonAICharacterCatalog(archive)
	if err != nil {
		t.Fatal("加载真实AI角色目录失败", err)
	}
	capturedBody, err := hex.DecodeString("e61b00000000000001ffff00000000000000000000")
	if err != nil {
		t.Fatal("读取现场7142请求失败", err)
	}
	for _, tc := range []struct {
		name          string
		job           int
		grow          byte
		target        byte
		gold          int64
		failure       string
		paid          bool
		selectorFirst bool
		book          bool
		bookItemID    int64
		invalidFirst  bool
	}{
		{name: "体验返回后缺少1金币", job: 2, grow: 1, target: 2, gold: 100112},
		{name: "体验返回后合法收费", job: 2, grow: 1, target: 2, gold: 3000000, paid: true},
		{name: "回城ACK42写失败后重试金币不足", job: 2, grow: 1, target: 2, gold: 100112, failure: "回城ACK42"},
		{name: "回城mode0写失败后重试金币不足", job: 2, grow: 1, target: 2, gold: 100112, failure: "回城mode0"},
		{name: "回城mode1写失败后重试金币不足", job: 2, grow: 1, target: 2, gold: 100112, failure: "回城mode1"},
		{name: "回城op19写失败后重试金币不足", job: 2, grow: 1, target: 2, gold: 100112, failure: "回城op19"},
		{name: "现场守护者3转1体验返回后缺少1金币", job: 12, grow: 3, target: 1, gold: 100112},
		{name: "42退出后op15先确认再提交金币不足", job: 2, grow: 1, target: 2, gold: 100112, selectorFirst: true},
		{name: "68级一觉守护者高级书体验返回正式提交", job: 12, grow: 0x13, target: 2, gold: 3000000, book: true},
		{name: "68级一觉守护者高级特别书体验返回正式提交", job: 12, grow: 0x13, target: 2, gold: 3000000, book: true, bookItemID: 2683470},
		{name: "高级书回城ACK42写失败重试", job: 12, grow: 0x13, target: 2, gold: 3000000, book: true, failure: "回城ACK42"},
		{name: "高级书回城mode1写失败重试", job: 12, grow: 0x13, target: 2, gold: 3000000, book: true, failure: "回城mode1"},
		{name: "高级书回城op19写失败重试", job: 12, grow: 0x13, target: 2, gold: 3000000, book: true, failure: "回城op19"},
		{name: "高级书错误来源365拒绝后合法槽位重试", job: 12, grow: 0x13, target: 2, gold: 3000000, book: true, invalidFirst: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			level, itemID := 25, int64(2683467)
			if tc.book {
				level, itemID = 68, 2683468
			}
			if tc.bookItemID != 0 {
				itemID = tc.bookItemID
			}
			s, session, repositories, _ := levelUpTicketBridgeFixture(t, itemID, level)
			prepareProfessionRealCharacter(t, s, repositories, tc.job, tc.grow, level, itemID)
			s.options.pvfPath = pvfPath
			s.options.gameUpperHeader = gameUpperHeaderServer16
			s.options.gameUpperBodyCodec = gameUpperBodyCodecPlain
			s.worldMapTable, s.worldMapResolver = table, resolver
			s.dungeonMonsterTable, s.dungeonAICharacterTable = monsters, aiCharacters
			s.dungeonChoice = func(int) (int, error) { return 0, nil }
			s.dungeonSeed = func() (uint32, error) { return 1, nil }
			character, found, err := repositories.Character.Load(ctx, "29")
			if err != nil || !found {
				t.Fatalf("读取正式角色失败：found=%t error=%v", found, err)
			}
			character.Stats[inventory.ProfessionChangeCountKey] = 1
			character.Stats["gold"] = tc.gold
			character.Stats["town_id"], character.Stats["area_id"] = 38, 1
			character.Stats["pos_x"], character.Stats["pos_y"] = 450, 234
			character.Stats["direction"], character.Stats["area_state"] = 5, 3
			if err := repositories.Character.Save(ctx, character); err != nil {
				t.Fatal("保存正式角色夹具失败", err)
			}
			if tc.job == 12 {
				// 去掉男枪夹具的技能、布局和另一技能页，按守护者正常登录先建基础账本。
				points, err := s.currentPVFSkillPointTarget(ctx, character)
				if err != nil {
					t.Fatal("读取守护者正式技能点基线失败", err)
				}
				if err := repositories.Skill.Save(ctx, repo.SkillRecord{CharacterID: character.CharacterID, Points: points}); err != nil {
					t.Fatal("清理异职业技能夹具失败", err)
				}
				seed, _, err := s.loadOrBackfillCurrentSceneSkillRecord(ctx, repositories, character, byte(tc.job))
				if err != nil {
					t.Fatal("初始化守护者基础技能失败", err)
				}
				profiles, catalog, err := s.currentProfessionResources(ctx)
				if err != nil {
					t.Fatal("读取守护者正式职业配置失败", err)
				}
				// 只在夹具中按生产规划补成现场已转职分支，不调用收费事务或修改角色次数。
				first, stage := profession.Decode(tc.grow)
				transition := profession.Transition{Kind: profession.KindClassChange, ChainType: 1,
					NewGrowType: tc.grow, GrowNumber: first, FirstGrowType: first, AwakeningStage: stage}
				seed, err = profiles.ApplySkillTransition(catalog, byte(tc.job), character.Level, transition, seed, points)
				if err != nil {
					t.Fatal("重建守护者正式分支技能失败", err)
				}
				if err := repositories.Skill.Save(ctx, seed); err != nil {
					t.Fatal("保存守护者正式技能夹具失败", err)
				}
			}
			// 与正常登录一致，先完成技能点/双页账本初始化，之后才冻结正式资产。
			if _, ready, err := s.buildCurrentSceneSkillInfoProjection(session, ctx, character, "体验返回夹具登录初始化"); err != nil || !ready {
				t.Fatalf("正式技能账本未就绪：ready=%t error=%v", ready, err)
			}
			// 正常登录的物品列表也会按PVF补全期限，不能把故意缺字段的道具当作冻结基线。
			if summary, err := s.reconcileCurrentPVFItemExpirations(ctx, repositories, character.CharacterID, s.accountIDForSession(session)); err != nil {
				t.Fatal("体验夹具登录道具期限补全失败", err)
			} else {
				t.Logf("体验夹具登录道具期限补全：%+v", summary)
			}
			// 选角登录会迁移当日疲劳字段；先走同一非扣除初始化，再验证体验不消耗疲劳。
			if _, err := s.updateCurrentCharacterFatigue(ctx, session, false); err != nil {
				t.Fatal("体验夹具登录疲劳初始化失败", err)
			}
			conn := session.conn.(*bufferConn)
			// 原生转职窗口保存的是物品对象指针，正式提交再按指针反查槽位。
			// 必须先交付真实登录背包，再用实际入图包驱动物品对象生命周期；
			// 不能始终手写槽位4，否则检测不到体验入图op13使来源指针失效。
			if err := s.sendCurrentSelectInventoryBootstrap(session, "转职书体验前真实登录背包"); err != nil {
				t.Fatal("体验前背包引导失败", err)
			}
			bookObjects := make(map[uint16]*professionTrialBookObject)
			applyProfessionTrialMainContainer(t, bookObjects, conn.write.Bytes())
			savedBook := bookObjects[4]
			if tc.book && savedBook == nil {
				t.Fatal("体验前没有建立转职书对象")
			}
			bindDungeonSelectorOriginForTestAt(t, s, session, 38, 1, 450, 234)
			beforeEntry := levelUpTicketBridgeSnapshot(t, repositories)
			conn.write.Reset()
			if err := s.handleDungeonSelectUpper(session, capturedBody); err != nil {
				t.Fatal("首次体验入场失败", err)
			}
			firstRuntime := session.dungeon.runtime
			assertProfessionTrialRealRuntime(t, firstRuntime)
			assertProfessionTrialRealEntryPackets(t, conn.write.Bytes())
			applyProfessionTrialMainContainer(t, bookObjects, conn.write.Bytes())
			bookSlot := -1
			for slot, object := range bookObjects {
				if object == savedBook {
					bookSlot = int(slot)
				}
			}
			if tc.book && bookSlot != 4 {
				t.Fatalf("体验入图重建主背包使原生转职书来源失效：反查槽位=%d，应为4", bookSlot)
			}
			if err := s.handleGameCommand(session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.GameTypeFinishLoading), make([]byte, 8)); err != nil {
				t.Fatal("首次体验完成读条失败", err)
			}
			if !session.currentFinishLoadingCompletionSent {
				t.Fatal("首次体验没有完成加载生命周期")
			}
			assertProfessionTrialAssetsUnchanged(t, beforeEntry, levelUpTicketBridgeSnapshot(t, repositories), "首次体验入场或读条")
			conn.write.Reset()
			if err := s.handleDungeonGiveupGame(session, nil); err != nil {
				t.Fatal("体验内正式提交前的42返回失败", err)
			}
			if session.dungeon.runtime != firstRuntime || !firstRuntime.townReturnPending || !firstRuntime.townReturnOp24Sent || firstRuntime.Session.Snapshot().Run.Status != worldmap.DungeonRunActive {
				t.Fatal("发送op24后没有保留待确认的原体验运行时")
			}
			returnPackets := splitProfessionTrialServer16Packets(t, conn.write.Bytes())
			if len(returnPackets) != 1 || returnPackets[0].Header.MsgID != currentSceneTransitionMsgID || returnPackets[0].Header.Classification != 0 {
				t.Fatal("体验42应先仅发送真实op24，等待客户端确认", returnPackets)
			}
			beforeSubmit := levelUpTicketBridgeSnapshot(t, repositories)
			conn.write.Reset()
			if tc.selectorFirst {
				if session.backToVillageEnterSelectPending {
					session.backToVillageEnterSelectArmedAt = time.Now().Add(-11 * time.Second)
				}
				if err := s.sendEnterSelectDungeonState(session, "7142退出后由op15首次确认返回", false, true); err != nil {
					t.Fatal("7142退出后的首条op15确认失败", err)
				}
				assertProfessionTrialNoSceneActivation(t, conn.write.Bytes(), "42退出后的首条op15确认")
			}
			failing := &professionTrialReturnFailConn{bufferConn: conn, stage: tc.failure}
			session.conn = failing
			request := make([]byte, 14)
			request[13] = tc.target
			op := uint16(785)
			if tc.book {
				op, request = 502, []byte{byte(bookSlot), byte(bookSlot >> 8), tc.target}
			}
			if tc.invalidFirst {
				if err := s.handleCurrentProfessionChange(session, op, []byte{0x6d, 1, tc.target}); err != nil {
					t.Fatal("拒绝现场错误来源槽位失败", err)
				}
				assertProfessionTrial502Ack(t, conn.write.Bytes(), []byte{0, 1})
				assertProfessionTrialNoSceneActivation(t, conn.write.Bytes(), "错误来源请求确认体验返回")
				assertProfessionTrialAssetsUnchanged(t, beforeSubmit, levelUpTicketBridgeSnapshot(t, repositories), "错误来源转职书")
				conn.write.Reset()
			}
			err = s.handleCurrentProfessionChange(session, op, request)
			var acceptedPrefix []byte
			if tc.failure != "" {
				if !errors.Is(err, io.ErrClosedPipe) || !failing.failed {
					t.Fatalf("未命中指定回城写失败：stage=%s error=%v failed=%t", tc.failure, err, failing.failed)
				}
				if !reflect.DeepEqual(beforeSubmit, levelUpTicketBridgeSnapshot(t, repositories)) {
					t.Fatal("回城投影中断前错误扣金币、增加次数或改动正式资产")
				}
				if tc.failure == "回城ACK42" {
					if session.dungeon.runtime != firstRuntime || !firstRuntime.townReturnPending || firstRuntime.Session.Snapshot().Run.Status != worldmap.DungeonRunActive {
						t.Fatal("ACK42写失败提前退休了待确认运行时")
					}
				} else if session.dungeon.runtime != nil || !session.confirmedDungeonReturnStatePending || firstRuntime.Session.Snapshot().Run.Status != worldmap.DungeonRunAbandoned {
					t.Fatal("回城mode1写失败没有保留已退休运行时的资料续传状态")
				}
				acceptedPrefix = append([]byte(nil), conn.write.Bytes()...)
				prefix := professionTrialReturnPacketSignatures(t, splitProfessionTrialServer16Packets(t, acceptedPrefix))
				wantPrefix := map[string][]string{
					"回城ACK42": {},
					"回城mode0": {"ack42"},
					"回城mode1": {"ack42", "mode0"},
					"回城op19":  {"ack42", "mode0", "mode1", "op105", "op37"},
				}[tc.failure]
				if !reflect.DeepEqual(prefix, wantPrefix) {
					t.Fatalf("写失败前已完成回城阶段不符：stage=%s got=%v want=%v", tc.failure, prefix, wantPrefix)
				}
				if tc.failure != "回城ACK42" && session.currentFinishLoadingCompletionSent {
					t.Fatal("省略op30的回城被伪标为已经发送激活通知")
				}
				if tc.failure == "回城op19" && session.townPostTransition.stage != currentTownPostTransitionFinishCompletionOmitted {
					t.Fatalf("op19写失败未保留省略op30的独立游标：stage=%d", session.townPostTransition.stage)
				}
				conn.write.Reset()
				err = s.handleCurrentProfessionChange(session, op, request)
			}
			if err != nil {
				t.Fatal("确认体验返回后的正式提交失败", err)
			}
			if session.dungeon.runtime != nil || firstRuntime.Session.Snapshot().Run.Status != worldmap.DungeonRunAbandoned || session.confirmedDungeonReturnStatePending {
				t.Fatal("转职提交未及时退休体验运行时或完成城镇资料续传")
			}
			allReturnPackets := append(acceptedPrefix, conn.write.Bytes()...)
			assertProfessionTrialNoSceneActivation(t, allReturnPackets, "7142退出后正式提交及写失败续传")
			if session.currentFinishLoadingCompletionSent || !session.townPostTransition.omitSceneActivation {
				t.Fatal("7142返回代没有保留省略策略，或把未发送的op30伪标为已发送")
			}
			if !tc.paid && !tc.book {
				want := []string{"ack42", "mode0", "mode1", "op105", "op37", "op19", "op120"}
				if got := professionTrialReturnPacketSignatures(t, splitProfessionTrialServer16Packets(t, allReturnPackets)); !reflect.DeepEqual(got, want) {
					t.Fatalf("7142返回链丢阶段或重放已发送阶段：got=%v want=%v", got, want)
				}
			}
			if ready, reason := s.currentTownEnterSelectReady(session); !ready {
				t.Fatal("转职提交确认后城镇未就绪", reason)
			}
			position := session.townPositionSnapshot
			if position.CharacterID != session.selectedCharacterID || !position.PositionValid || position.TownID != 38 || position.AreaID != 1 || position.PositionX != 450 || position.PositionY != 234 {
				t.Fatalf("回城确认或资料重试丢失冻结来源坐标：%+v", position)
			}
			wantCode, wantCount := uint32(22), byte(1)
			if tc.paid {
				wantCode, wantCount = 0, 2
			}
			if tc.book {
				assertProfessionTrial502Ack(t, conn.write.Bytes(), []byte{1})
			} else {
				assertProfessionTrial785Ack(t, conn.write.Bytes(), wantCode, wantCount)
			}
			afterSubmit := levelUpTicketBridgeSnapshot(t, repositories)
			if tc.book {
				after := afterSubmit[0].(repo.CharacterRecord)
				bag := afterSubmit[1].(repo.InventoryRecord)
				if after.Level != 68 || after.Stats["grow_type"] != int64(tc.grow&0xf0|tc.target) || after.Stats["gold"] != tc.gold || after.Stats[inventory.ProfessionChangeCountKey] != 1 {
					t.Fatal("高级书没有保留觉醒、金币或免费次数", after)
				}
				if bag.Slots["0:4"].ItemID == itemID || bag.Slots["0:5"].Count != 8 {
					t.Fatal("高级书未恰好扣除一本或误改其它资产", bag)
				}
				if itemID == 2683470 {
					var bonusCount int64
					for _, stack := range bag.Slots {
						if stack.ItemID == 2683472 {
							bonusCount += stack.Count
							if hours := stack.ExpireAt.Sub(s.gameplayNow()).Hours(); hours < 359 || hours > 361 {
								t.Fatal("高级特别书赠书期限不是15天", stack.ExpireAt)
							}
						}
					}
					if bonusCount != 1 {
						t.Fatal("高级特别书没有按原规则赠送一本对应转回书", bonusCount)
					}
				}
			} else if !tc.paid {
				if !reflect.DeepEqual(beforeSubmit, afterSubmit) {
					t.Fatal("金币不足的正式提交改动了职业、技能、金币、次数、背包或任务")
				}
			} else {
				after := afterSubmit[0].(repo.CharacterRecord)
				skill := afterSubmit[2].(repo.SkillRecord)
				if after.Job != strconv.Itoa(tc.job) || after.Level != 25 || after.Stats["grow_type"] != int64(tc.target) || after.Stats["gold"] != tc.gold-100113 || after.Stats[inventory.ProfessionChangeCountKey] != 2 {
					t.Fatal("正式转职结果、PVF整数费用或持久次数不符", after)
				}
				if skill.Points.RemainingSP != skill.Points.TotalSP || !reflect.DeepEqual(beforeSubmit[1], afterSubmit[1]) {
					t.Fatal("正式转职未退款技能点或误消耗转职书/其它背包资产")
				}
			}
			// 重复同一提交：缺钱仍返回22；已扣书或已变更到目标则拒绝，不再消费。
			conn.write.Reset()
			if err := s.handleCurrentProfessionChange(session, op, request); err != nil {
				t.Fatal("重复正式提交处理失败", err)
			}
			assertProfessionTrialNoSceneActivation(t, conn.write.Bytes(), "返回后重复转职提交")
			if tc.book {
				assertProfessionTrial502Ack(t, conn.write.Bytes(), []byte{0, 1})
			} else if tc.paid {
				assertProfessionTrial785Ack(t, conn.write.Bytes(), 99, 2)
			} else {
				assertProfessionTrial785Ack(t, conn.write.Bytes(), 22, 1)
			}
			if !reflect.DeepEqual(afterSubmit, levelUpTicketBridgeSnapshot(t, repositories)) {
				t.Fatal("重复正式提交再次改动了资产或收费次数")
			}
			// 现场再次打开体验相隔约11秒；只推进此收尾守卫，不等待真实时间。
			if session.backToVillageEnterSelectPending {
				session.backToVillageEnterSelectArmedAt = time.Now().Add(-11 * time.Second)
			}
			conn.write.Reset()
			if err := s.sendEnterSelectDungeonState(session, "体验返回后再次选图回归", false, true); err != nil {
				t.Fatal("体验返回后再次选图失败", err)
			}
			assertProfessionTrialNoSceneActivation(t, conn.write.Bytes(), "7142退出后的op15")
			for _, packet := range splitProfessionTrialServer16Packets(t, conn.write.Bytes()) {
				if packet.Header.Classification == 0 && (packet.Header.MsgID == 19 || packet.Header.MsgID == 37 || (packet.Header.MsgID == 2 && len(packet.Body) > 0 && packet.Body[0] <= 1)) {
					t.Fatalf("再次op15重复发送城镇角色/技能重建：opcode=%d body=%x", packet.Header.MsgID, packet.Body)
				}
			}
			beforeReentry := levelUpTicketBridgeSnapshot(t, repositories)
			conn.write.Reset()
			if err := s.handleDungeonSelectUpper(session, capturedBody); err != nil {
				t.Fatal("再次进入体验地图失败", err)
			}
			assertProfessionTrialRealRuntime(t, session.dungeon.runtime)
			if session.dungeon.runtime == firstRuntime {
				t.Fatal("再次入图复用了已放弃的旧体验运行时")
			}
			assertProfessionTrialRealEntryPackets(t, conn.write.Bytes())
			conn.write.Reset()
			if err := s.handleGameCommand(session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.GameTypeFinishLoading), make([]byte, 8)); err != nil {
				t.Fatal("再次体验完成读条失败", err)
			}
			if !session.currentFinishLoadingCompletionSent {
				t.Fatal("再次体验读条未完成")
			}
			if session.townPostTransition.omitSceneActivation {
				t.Fatal("上一代7142退出省略策略泄漏到下一次真实入图")
			}
			activationCount := 0
			for _, packet := range splitProfessionTrialServer16Packets(t, conn.write.Bytes()) {
				if packet.Header.Classification == 0 && packet.Header.MsgID == currentIncreaseStatusResultMsgID {
					activationCount++
				}
			}
			if activationCount != 1 {
				t.Fatalf("下一次真实7142入图37应正常激活一次：op30数量=%d", activationCount)
			}
			assertProfessionTrialAssetsUnchanged(t, beforeReentry, levelUpTicketBridgeSnapshot(t, repositories), "再次体验入场或读条")
		})
	}
}

func assertProfessionTrial502Ack(t *testing.T, stream, want []byte) {
	t.Helper()
	matched := 0
	for _, packet := range splitProfessionTrialServer16Packets(t, stream) {
		if packet.Header.MsgID == 502 && packet.Header.Classification == 1 {
			matched++
			if !bytes.Equal(packet.Body, want) {
				t.Fatalf("502应答错误：body=%x want=%x", packet.Body, want)
			}
		}
	}
	if matched != 1 {
		t.Fatalf("502应答数量错误：got=%d want=1", matched)
	}
}

func assertProfessionTrialRealRuntime(t *testing.T, runtime *runtimeDungeonState) {
	t.Helper()
	if runtime == nil || runtime.Session == nil || runtime.Dungeon.ID != 7142 || runtime.Request.RuntimeState != 1 || runtime.Request.RuntimeToken != 0xffff {
		t.Fatal("真实7142入场运行时或现场请求形状不符", runtime)
	}
	snapshot := runtime.Session.Snapshot()
	if snapshot.Run.Status != worldmap.DungeonRunActive || snapshot.Run.DungeonID != 7142 || snapshot.Scene.Map.Map.ID != 76032 {
		t.Fatal("真实7142没有进入活动76032地图", snapshot.Run, snapshot.Scene.Map.Map.ID)
	}
}

func assertProfessionTrialRealEntryPackets(t *testing.T, stream []byte) {
	t.Helper()
	ack, info, start := false, false, false
	for _, packet := range splitProfessionTrialServer16Packets(t, stream) {
		if packet.Header.Classification == 1 && packet.Header.MsgID == 16 {
			ack = len(packet.Body) > 0 && packet.Body[0] == 1
		}
		if packet.Header.Classification == 0 && packet.Header.MsgID == currentDungeonInfoNotification {
			info = len(packet.Body) >= 4 && binary.LittleEndian.Uint32(packet.Body) == 7142
		}
		if packet.Header.Classification == 0 && packet.Header.MsgID == currentDungeonStartNotification {
			start = len(packet.Body) > 0
		}
	}
	if !ack || !info || !start {
		t.Fatalf("真实7142入场包缺失：op16=%t op28=%t op29=%t", ack, info, start)
	}
}

func assertProfessionTrial785Ack(t *testing.T, stream []byte, code uint32, count byte) {
	t.Helper()
	want := []byte{1, 0, 0, 0, 0, count}
	binary.LittleEndian.PutUint32(want[1:5], code)
	matched := 0
	for _, packet := range splitProfessionTrialServer16Packets(t, stream) {
		if packet.Header.MsgID == 785 && packet.Header.Classification == 1 {
			matched++
			if !bytes.Equal(packet.Body, want) {
				t.Fatalf("785应答未使用实际结果码/次数：body=%x want=%x", packet.Body, want)
			}
		}
	}
	if matched != 1 {
		t.Fatalf("785应答数量不符：got=%d want=1", matched)
	}
}

func assertProfessionTrialNoSceneActivation(t *testing.T, stream []byte, stage string) {
	t.Helper()
	for _, packet := range splitProfessionTrialServer16Packets(t, stream) {
		if packet.Header.Classification == 0 && packet.Header.MsgID == currentIncreaseStatusResultMsgID {
			t.Fatalf("%s向已销毁体验对象发送了op30：body=%x", stage, packet.Body)
		}
	}
}

// 只筛选回城主链；职业成功后的专属快照、502/785和选择器通知由各自断言验证。
func professionTrialReturnPacketSignatures(t *testing.T, packets []dnfproto.ChannelPacket) []string {
	t.Helper()
	signatures := make([]string, 0)
	for _, packet := range packets {
		if packet.Header.Classification == 1 && packet.Header.MsgID == 42 {
			if !bytes.Equal(packet.Body, []byte{1}) {
				t.Fatalf("42返回确认正文不符：body=%x", packet.Body)
			}
			signatures = append(signatures, "ack42")
			continue
		}
		if packet.Header.Classification != 0 {
			continue
		}
		switch packet.Header.MsgID {
		case 2:
			if len(packet.Body) > 0 && packet.Body[0] == 0 {
				signatures = append(signatures, "mode0")
			} else if len(packet.Body) > 0 && packet.Body[0] == 1 {
				signatures = append(signatures, "mode1")
			}
		case currentCreatureStateTableMsgID:
			signatures = append(signatures, "op105")
		case currentDungeonCharacterStateMsgID:
			signatures = append(signatures, "op37")
		case currentIncreaseStatusResultMsgID:
			signatures = append(signatures, "op30")
		case currentSkillInfoMsgID:
			signatures = append(signatures, "op19")
		case uint16(dnfenum.CmdPacketRequestBlacklist):
			signatures = append(signatures, "op120")
		}
	}
	return signatures
}

// 本专项明确选用server16；通用自动拆包器只为部分固定16字节通知切换，不能用于整流。
func splitProfessionTrialServer16Packets(t *testing.T, stream []byte) []dnfproto.ChannelPacket {
	t.Helper()
	var packets []dnfproto.ChannelPacket
	for len(stream) > 0 {
		packet, rest := splitLongHengGameServerUpperPacket(t, stream)
		packets = append(packets, packet)
		stream = rest
	}
	return packets
}

// 比较完整正式记录，不过滤差异；失败时指出具体记录，便于区分正常初始化遗漏和资产错误。
func assertProfessionTrialAssetsUnchanged(t *testing.T, before, after []any, stage string) {
	t.Helper()
	names := []string{"角色", "背包", "技能", "任务"}
	for i := range before {
		if !reflect.DeepEqual(before[i], after[i]) {
			t.Errorf("%s改动%s记录：之前=%+v；之后=%+v", stage, names[i], before[i], after[i])
		}
	}
	if t.Failed() {
		t.FailNow()
	}
}

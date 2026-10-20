package dnfbridge

// 选角 ACK town:u8 = 初始城镇描述符（不是角色列表 slot）的回归测试。
//
// 移植自 2026-09-15 已验收修复（归档提交 6be4c236）的
// go-server/internal/services/dnfbridge/select_character_town_descriptor_test.go，
// 该修复在当前在役树中已失落（在役 ACK 又把角色列表 slot 写进这一个字节）。
// 证据：docs/evidence/cold-login-town-descriptor-20260914.md:32,36-46,50-53。
//
// RED 口径（写回 slot 必须失败）：角色 6 在线上是账号里唯一的角色、record_slot=0
// （packet_log.txt: game-upper-select-character-resolved char_id=6 record_slot=0 count=1），
// 所以线上这一个字节一直是 0 —— 正是当前 EXE 解析不到 descriptor 的那个值。
// GREEN 口径：恒为 newCharacterInitialTownID(38)。

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// selectAckTownGoldenHex 是冻结的整包字节（fixture 见
// TestSelectAckTownDescriptorGoldenPacketLiteral）。**只有在拿到新的 EXE 证据时才允许更新**：
// 证据文档 cold-login-town-descriptor-20260914.md:53 要求"只改变ACK这个BYTE。不改包大小、
// 四u32、相邻教程字段"。town:u8 位于第 222 字节 = 0x26 = 38。
//
// 2026-09-29 更新尾部 12 字节 → 13 字节：`26 00 01 1f`（38 / 忽略标志 / count=1 / [31]）
// 改为 `26 00 02 0d 1f`（38 / 忽略标志 / count=2 / [13,31]）。这一次的"新 EXE 证据"是
// 同一血统 2026-09-24 归档里**已实现且已实机复测**的教程索引列表
// （归档 select_character_ack.go 的 currentSelectAckTutorialIndexesForCharacter，
// 归档 PROTOCOL_INDEX.md:55 "class1/op4 select ACK: initial town, not roster slot
// (2026-09-15)"：below Lv20 keep [31]; Lv20+ keep [13,31]；Live retest confirms
// [13,31] is actually received by the Lv64 client）。town:u8 本身仍是 0x26 = 38 不变，
// 包变长的那 1 字节就是补回的索引 13。
const selectAckTownGoldenHex = "010000000000000000010000009c00000000393000006400030000002c0101000000ffff00000000ffff00000000ffff00000000ffff00000000ffff00000000ffff00000000ffff00000000ffff00000000ffff00000000ffff00000000ffff00000000ffff00000000ffff00000000ffff00000000ffff00000000ffff00000000ffff00000000ffff00000000ffff00000000ffff00000000ffff00000000ffff00000000ffff00000000ffff00000000ffff00000000ffff00000000ffff00000000ffff0000000000000000000000000000000000000000000000002600020d1f0000000000000000"

// selectAckTownGoldenFixture 是 golden 用的确定性输入（不碰仓储，逐字节可复现）。
func selectAckTownGoldenFixture() (dnfrepo.CharacterRecord, dnfrepo.QuestRecord) {
	character := dnfrepo.CharacterRecord{Level: 86, Stats: map[string]int64{
		currentDungeonTutorialCompletedKey: currentDungeonTutorialCompleteFlag,
		"fatigue":                          156,
		"fatigue_limit":                    156,
	}}
	quests := dnfrepo.QuestRecord{States: map[int64]dnfrepo.QuestState{
		300: {Status: "active", ProgressValue: 1},
		100: {Status: "active", ProgressValue: 3},
		200: {Status: "complete", ProgressValue: 7},
	}}
	return character, quests
}

// TestSelectAckTownDescriptorGoldenPacketLiteral 把这一档 ACK 的完整字节钉成字面量：
// 包长、四个 u32、相邻教程字段、疲劳尾部任何一个字节漂移都会失败；
// 同时用回滚字面量（修复前线上角色 6 的真实值 = 它的 roster slot 0）证明只差 town:u8 一个字节。
func TestSelectAckTownDescriptorGoldenPacketLiteral(t *testing.T) {
	character, quests := selectAckTownGoldenFixture()
	body := buildCurrentSelectCharacterAckBody(character, true, quests, true, 1, newCharacterInitialTownID, 12345, []byte{0})
	if got := hex.EncodeToString(body); got != selectAckTownGoldenHex {
		t.Fatalf("select ACK drifted from the frozen golden packet:\n got=%s\nwant=%s", got, selectAckTownGoldenHex)
	}
	townOffset := selectAckTownOffset(body)
	if body[townOffset] != newCharacterInitialTownID {
		t.Fatalf("golden town byte=%d at %d, want %d", body[townOffset], townOffset, newCharacterInitialTownID)
	}
	rollback := buildCurrentSelectCharacterAckBody(character, true, quests, true, 1, 0, 12345, []byte{0})
	if len(rollback) != len(body) {
		t.Fatalf("rollback literal changed ACK size: %d vs %d", len(rollback), len(body))
	}
	rollback[townOffset] = newCharacterInitialTownID
	if !bytes.Equal(rollback, body) {
		t.Fatal("the pre-fix roster-slot literal differs from the fixed packet beyond the town byte")
	}
}

// selectAckTownOffset 按当前 EXE 的布局独立复算 town:u8 的下标，不借道被测 parser：
// 18 字节前缀 → premium rows → 30 固定任务行 → overflow 行 → 四个 u32 → town:u8。
func selectAckTownOffset(body []byte) int {
	questOffset := 18 + int(body[currentSelectAckPremiumCountOffset])*9 + 4
	overflowOffset := questOffset + currentSelectAckFixedQuestCount*currentSelectAckQuestRowSize
	overflowCount := int(binary.LittleEndian.Uint32(body[overflowOffset : overflowOffset+4]))
	return overflowOffset + 4 + overflowCount*currentSelectAckQuestRowSize + currentSelectAckStateByteSize
}

// selectAckFixtureFor 建一组最小仓储 + 服务 + 会话，用来跑真实选角入口。
func selectAckFixtureFor(t *testing.T, slots []int, tutorialCompleted int64, savedTown int64) (dnfrepo.Group, *Service, func() *gameSession) {
	t.Helper()
	repos := testRepositoryGroup()
	records := repos.Character.(*fakeCharacterStore).records
	for _, slot := range slots {
		id := fmt.Sprint(slot + 1)
		records[id] = dnfrepo.CharacterRecord{
			CharacterID: id, AccountID: defaultAccountPrefix + "1",
			Slot: slot, Name: "hero" + id, Job: "0", Level: 86,
			Stats: map[string]int64{
				"town_id": savedTown, "area_id": 2, "pos_x": 612, "pos_y": 240,
				"grow_type": 19, currentDungeonTutorialCompletedKey: tutorialCompleted,
			},
		}
	}
	service := &Service{
		options:            options{accountPrefix: defaultAccountPrefix},
		repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true },
	}
	freshSession := func() *gameSession {
		channel := channelcatalog.Channel{ID: 251, Type: 40, Name: "ch.251", Port: 10251}
		return &gameSession{conn: &bufferConn{}, channel: channel, residentChannel: channel, rosterRequested: true}
	}
	return repos, service, freshSession
}

// selectAckBodyForSlot 走真实 upper 选角入口，返回第一包（选角 ACK）的 body。
func selectAckBodyForSlot(t *testing.T, service *Service, session *gameSession, slot int) []byte {
	t.Helper()
	conn, ok := session.conn.(*bufferConn)
	if !ok {
		t.Fatalf("fixture session has no buffer conn")
	}
	conn.write.Reset()
	request := make([]byte, 16)
	binary.LittleEndian.PutUint32(request, uint32(slot))
	frame, err := dnfproto.BuildChannelPacket(uint16(dnfenum.UpperMsgSelectCharacter), request, 0, dnfproto.DefaultChannelClassification)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.handleGameUpper(session, frame); err != nil {
		t.Fatal(err)
	}
	packet, _ := splitCurrentGameServerUpperPacketAuto(t, conn.write.Bytes())
	if packet.Header.MsgID != uint16(dnfenum.UpperMsgSelectCharacter) || packet.Header.Classification != dnfproto.DefaultChannelClassification {
		t.Fatalf("first reply class=%d op=%d is not the select ACK", packet.Header.Classification, packet.Header.MsgID)
	}
	return packet.Body
}

// TestSelectCharacterAckTownDescriptorIsIndependentOfRosterSlot 覆盖三种保存城镇、
// 两种教程标记、冷第一/冷第六/同会话切回第一：ACK 的 town:u8 必须恒为登录房间城镇 38，
// 与角色所在 slot 无关；角色 ID、相邻教程字段与持久化位置都不许动。
func TestSelectCharacterAckTownDescriptorIsIndependentOfRosterSlot(t *testing.T) {
	t.Setenv(currentSelectAckTownEnv, "")
	// slot5 = 操作者的角色 6（test78）所在档位；slot0 是线上真实值（唯一角色）。
	slots := []int{0, 1, 5}
	for _, tutorialPending := range []bool{false, true} {
		for _, savedTown := range []int64{38, 39, 40} {
			t.Run(fmt.Sprintf("tutorialPending=%t/savedTown=%d", tutorialPending, savedTown), func(t *testing.T) {
				completed := currentDungeonTutorialCompleteFlag
				if tutorialPending {
					completed = 0
				}
				repos, service, freshSession := selectAckFixtureFor(t, slots, completed, savedTown)
				coldFirst := freshSession()
				coldSixthThenFirst := freshSession()
				for _, selection := range []struct {
					name    string
					session *gameSession
					slot    int
				}{
					{"cold-first", coldFirst, 0},
					{"cold-sixth", coldSixthThenFirst, 5},
					{"warm-first", coldSixthThenFirst, 0},
				} {
					t.Run(selection.name, func(t *testing.T) {
						before, found, err := repos.Character.Load(context.Background(), fmt.Sprint(selection.slot+1))
						if err != nil || !found {
							t.Fatalf("fixture character missing: found=%t err=%v", found, err)
						}
						beforeStats := make(map[string]int64, len(before.Stats))
						for key, value := range before.Stats {
							beforeStats[key] = value
						}
						body := selectAckBodyForSlot(t, service, selection.session, selection.slot)
						townOffset := selectAckTownOffset(body)
						if got := body[townOffset]; got != newCharacterInitialTownID {
							t.Fatalf("native ACK town descriptor=%d, want login room town %d (roster slot=%d, saved town=%d)",
								got, newCharacterInitialTownID, selection.slot, savedTown)
						}
						if got := binary.LittleEndian.Uint16(body[currentSelectAckCharacterIDOffset : currentSelectAckCharacterIDOffset+2]); got != uint16(selection.slot+1) {
							t.Fatalf("ACK selected character=%d, want %d", got, selection.slot+1)
						}
						// fixture 角色 Level 86 ⇒ Lv20+ 既有角色必须拿到
						// 归档口径的 [13,31]（账号循环副本教程 13 + page-1 路由 31）。
						wantTutorial := []byte{
							0,
							2,
							currentSelectAckAccountLoopDungeonTutorialIndex,
							currentSelectAckPage1RouteIndex,
						}
						if tutorialPending {
							wantTutorial = []byte{0, 0}
						}
						if !bytes.Equal(body[townOffset+1:townOffset+1+len(wantTutorial)], wantTutorial) {
							t.Fatal("ACK tutorial route changed")
						}
						if selection.session.selectedCharacterID != uint16(selection.slot+1) {
							t.Fatalf("selected session character=%d", selection.session.selectedCharacterID)
						}
						// ACK 不得提前搬动/持久化角色位置，也不得替换既有的归属城镇/房间出口上下文。
						loaded, found, err := repos.Character.Load(context.Background(), fmt.Sprint(selection.slot+1))
						// Daily fatigue migration is independent of the saved town.
						beforeStats = dnfrepo.CloneCharacter(dnfrepo.CharacterRecord{Stats: beforeStats}).Stats
						for _, key := range []string{"fatigue", "fatigue_limit", "fatigue_used", "fatigue_display_update", "fatigue_service_day_id"} {
							delete(loaded.Stats, key)
							delete(beforeStats, key)
						}
						if err != nil || !found || !reflect.DeepEqual(loaded.Stats, beforeStats) ||
							loaded.Stats["town_id"] != savedTown || loaded.Stats["pos_x"] != 612 {
							t.Fatalf("ACK altered durable location: found=%t err=%v stats=%v", found, err, loaded.Stats)
						}
					})
				}
			})
		}
	}
}

// TestSelectAckTownDescriptorChangeOnlyTouchesItsNativeByte 是包级 golden 对照：
// 除 town:u8 外整包逐字节不变、长度不变、所有截断都被 parser 拒绝。
// town 参数 0 = 修复前的回滚字面量（线上角色 6 的真实旧值）。
func TestSelectAckTownDescriptorChangeOnlyTouchesItsNativeByte(t *testing.T) {
	for _, completed := range []int64{0, currentDungeonTutorialCompleteFlag} {
		for _, questCount := range []int{0, 30, 32} {
			for _, premiumCount := range []int{0, 2} {
				t.Run(fmt.Sprintf("completed=%d/quests=%d/premium=%d", completed, questCount, premiumCount), func(t *testing.T) {
					character := dnfrepo.CharacterRecord{Level: 86, Stats: map[string]int64{currentDungeonTutorialCompletedKey: completed, "fatigue": 156, "fatigue_limit": 156}}
					quests := dnfrepo.QuestRecord{States: make(map[int64]dnfrepo.QuestState)}
					for i := 0; i < questCount; i++ {
						quests.States[int64(100+i)] = dnfrepo.QuestState{Status: "active", ProgressValue: int64(i)}
					}
					premium := make([]byte, 1+premiumCount*9)
					premium[0] = byte(premiumCount)
					oldBody := buildCurrentSelectCharacterAckBody(character, true, quests, true, 1, 0, 12345, premium)
					body := buildCurrentSelectCharacterAckBody(character, true, quests, true, 1, newCharacterInitialTownID, 12345, premium)
					if len(oldBody) != len(body) {
						t.Fatal("town correction changed ACK size")
					}
					townOffset := selectAckTownOffset(body)
					oldBody[townOffset] = newCharacterInitialTownID
					if !bytes.Equal(oldBody, body) {
						t.Fatal("town correction altered an unrelated ACK field")
					}
					if got, ok := currentSelectAckInitialTownIDFromBody(body); !ok || got != newCharacterInitialTownID {
						t.Fatalf("initial-town parser = %d/%t", got, ok)
					}
					for cut := 0; cut < len(body); cut++ {
						if _, ok := currentSelectAckInitialTownIDFromBody(body[:cut]); ok {
							t.Fatalf("truncated ACK accepted at %d/%d bytes", cut, len(body))
						}
					}
				})
			}
		}
	}
}

// TestSelectCharacterAckTownEndToEndMatchesRollbackLiteral 在真实选角入口上做逐字节对照：
// 默认档（38）与回滚档 DNFBRIDGE_SELECT_ACK_TOWN=roster-slot（写回 slot 0）产出的包
// 长度相同、且恰好只差 town:u8 这一个字节。
func TestSelectCharacterAckTownEndToEndMatchesRollbackLiteral(t *testing.T) {
	t.Setenv(currentSelectAckTownEnv, "")
	reposFixed := testRepositoryGroup()
	records := reposFixed.Character.(*fakeCharacterStore).records
	records["6"] = dnfrepo.CharacterRecord{
		CharacterID: "6", AccountID: defaultAccountPrefix + "1", Slot: 0, Name: "test78", Job: "0", Level: 90,
		Stats: map[string]int64{"town_id": 11, "area_id": 2, "pos_x": 612, "pos_y": 240, currentDungeonTutorialCompletedKey: currentDungeonTutorialCompleteFlag},
	}
	run := func(env string) []byte {
		var body []byte
		t.Run("env="+env, func(t *testing.T) {
			t.Setenv(currentSelectAckTownEnv, env)
			repos := testRepositoryGroup()
			repos.Character.(*fakeCharacterStore).records["6"] = records["6"]
			service := &Service{
				options:            options{accountPrefix: defaultAccountPrefix},
				repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true },
			}
			channel := channelcatalog.Channel{ID: 251, Type: 40, Name: "ch.251", Port: 10251}
			session := &gameSession{conn: &bufferConn{}, channel: channel, residentChannel: channel, rosterRequested: true}
			body = selectAckBodyForSlot(t, service, session, 0)
		})
		return body
	}
	fixed := run("")
	rollback := run(currentSelectAckTownRollbackValue)
	if len(fixed) != len(rollback) {
		t.Fatalf("rollback changed ACK size: fixed=%d rollback=%d", len(fixed), len(rollback))
	}
	townOffset := selectAckTownOffset(fixed)
	if fixed[townOffset] != newCharacterInitialTownID {
		t.Fatalf("fixed ACK town=%d, want %d", fixed[townOffset], newCharacterInitialTownID)
	}
	if rollback[townOffset] != 0 {
		t.Fatalf("rollback ACK town=%d, want the pre-fix roster slot 0", rollback[townOffset])
	}
	diff := 0
	for idx := range fixed {
		if fixed[idx] != rollback[idx] {
			diff++
			if idx != townOffset {
				t.Fatalf("rollback differs at byte %d beyond the town byte", idx)
			}
		}
	}
	if diff != 1 {
		t.Fatalf("expected exactly one differing byte, got %d", diff)
	}
}

// TestSelectCharacterAckTownSwitchDefaultsToInitialTown 钉死开关的两档取值：
// 默认（未设置/任何其它值）= 初始城镇；只有显式回滚字面量才回到 slot 口径。
func TestSelectCharacterAckTownSwitchDefaultsToInitialTown(t *testing.T) {
	for _, env := range []string{"", "town", "initial-town", "38"} {
		t.Setenv(currentSelectAckTownEnv, env)
		got, source, ok := currentSelectAckTownID(5)
		if !ok || got != newCharacterInitialTownID || source != currentSelectAckTownSourceInitialTown {
			t.Fatalf("env=%q town=%d source=%q ok=%t, want %d/%s/true", env, got, source, ok, newCharacterInitialTownID, currentSelectAckTownSourceInitialTown)
		}
	}
	for _, env := range []string{currentSelectAckTownRollbackValue, "slot", "legacy", "old"} {
		t.Setenv(currentSelectAckTownEnv, env)
		got, source, ok := currentSelectAckTownID(5)
		if !ok || got != 5 || source != currentSelectAckTownSourceRosterRollback {
			t.Fatalf("env=%q town=%d source=%q ok=%t, want 5/%s/true", env, got, source, ok, currentSelectAckTownSourceRosterRollback)
		}
	}
	// 回滚档保留修复前的 u8 越界阻断，默认档不再有这个阻断（该值已不再进包）。
	t.Setenv(currentSelectAckTownEnv, currentSelectAckTownRollbackValue)
	if _, _, ok := currentSelectAckTownID(256); ok {
		t.Fatal("rollback mode must keep the pre-fix out-of-u8 block")
	}
	t.Setenv(currentSelectAckTownEnv, "")
	if got, _, ok := currentSelectAckTownID(256); !ok || got != newCharacterInitialTownID {
		t.Fatalf("default mode must not block on the unused roster slot: got=%d ok=%t", got, ok)
	}
}

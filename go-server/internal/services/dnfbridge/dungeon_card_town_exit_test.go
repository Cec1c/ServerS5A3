package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func cardTownExitFixture(t *testing.T, bonus int64) (*Service, *runtimeDungeonState, *gameSession, *bufferConn) {
	t.Helper()
	s, runtime, session, conn := prepareOrdinaryCompletedSettlementRuntimeForExitTest(t)
	completeSettlementExitBarriersForTest(t, s, runtime, session)
	plan, err := newDungeonCardRewardPlan(dungeonCardPlanIdentity{CharacterID: "99", DungeonID: runtime.Dungeon.ID, RunSeed: runtime.Seed},
		"翻牌回城隔离回归", dungeonCardRewardBundle{Gold: 33}, dungeonCardRewardBundle{Gold: 77}, dungeonCardRewardBundle{Gold: bonus})
	if err != nil {
		t.Fatal(err)
	}
	runtime.settlementCardRewardState, err = newDungeonCardState(plan)
	if err != nil {
		t.Fatal(err)
	}
	repos, ok := s.repositoryGroup()
	if !ok {
		t.Fatal("缺少隔离仓储")
	}
	character, found, err := repos.Character.Load(context.Background(), "99")
	if err != nil || !found {
		t.Fatalf("隔离角色不可用：%t %v", found, err)
	}
	if character.Stats == nil {
		character.Stats = map[string]int64{}
	}
	character.Stats["gold"] = 1000
	if err := repos.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}
	conn.write.Reset()
	return s, runtime, session, conn
}

func cardTownExitRequest(t *testing.T, s *Service, session *gameSession, opcode uint16) error {
	t.Helper()
	packet, err := dnfproto.BuildChannelPacket(opcode, nil, 0, dnfproto.DefaultChannelClassification)
	if err != nil {
		t.Fatal(err)
	}
	return s.handleGameUpper(session, packet)
}

func cardTownExitGold(t *testing.T, s *Service) int64 {
	t.Helper()
	repos, _ := s.repositoryGroup()
	character, found, err := repos.Character.Load(context.Background(), "99")
	if err != nil || !found {
		t.Fatalf("隔离余额读取失败：%t %v", found, err)
	}
	return character.Stats["gold"]
}

func cardTownExitPacketCounts(t *testing.T, raw []byte) map[uint16]int {
	t.Helper()
	counts := map[uint16]int{}
	for len(raw) > 0 {
		packet, tail := splitGameServerUpperPacket(t, raw)
		counts[packet.Header.MsgID]++
		raw = tail
	}
	return counts
}

func assertCardTownExitCommitted(t *testing.T, s *Service, runtime *runtimeDungeonState, session *gameSession, gold int64) {
	t.Helper()
	if session.dungeon.runtime != nil || !runtime.townReturnOp24Sent || runtime.Session.Snapshot().Run.Status != worldmap.DungeonRunCompleted {
		t.Fatal("翻牌期间的退出意图未完成回城")
	}
	if !runtime.settlementCardSideRewardCommitted[dungeonCardSideFree] || runtime.settlementCardSideRewardCommitted[dungeonCardSidePaid] {
		t.Fatal("免费奖励未结清，或未经用户选择发放了付费牌")
	}
	if got := cardTownExitGold(t, s); got != gold {
		t.Fatalf("免费奖励或黑钻加成落账错误：实际%d，预期%d", got, gold)
	}
	if runtime.settlementCardAutoFlipTimerName != "" {
		t.Fatal("回城后仍保留自动翻牌定时器")
	}
}

func TestCardTownExitWaitsForLayoutAndReturns(t *testing.T) {
	for _, opcode := range []uint16{uint16(dnfenum.CmdPacketGiveupGame), uint16(dnfenum.CmdPacketBack2Village)} {
		for phase := 0; phase < 3; phase++ {
			for _, bonus := range []int64{0, 7} {
				t.Run(fmt.Sprintf("请求%d/阶段%d/黑钻%d", opcode, phase, bonus), func(t *testing.T) {
					s, runtime, session, conn := cardTownExitFixture(t, bonus)
					if phase > 0 {
						if err := s.handleDungeonScoreScrollState(session, nil); err != nil {
							t.Fatal(err)
						}
					}
					if phase > 1 {
						if err := s.handleDungeonCardSelectRightState(session, nil); err != nil {
							t.Fatal(err)
						}
					}
					conn.write.Reset()
					if err := cardTownExitRequest(t, s, session, opcode); err != nil {
						t.Fatal(err)
					}
					if phase < 2 {
						if conn.write.Len() != 0 || runtime.townReturnPending || cardTownExitGold(t, s) != 1000 {
							t.Fatal("牌面未就绪时伪造翻牌、回城或变更资产")
						}
						if err := cardTownExitRequest(t, s, session, opcode); err != nil {
							t.Fatal(err)
						}
						if phase == 0 {
							if err := s.handleDungeonScoreScrollState(session, nil); err != nil {
								t.Fatal(err)
							}
							counts := cardTownExitPacketCounts(t, conn.write.Bytes())
							if counts[69] != 1 || counts[70] != 0 || counts[24] != 0 {
								t.Fatal("69越过了真实牌面屏障")
							}
							conn.write.Reset()
						}
						if err := s.handleDungeonCardSelectRightState(session, nil); err != nil {
							t.Fatal(err)
						}
					}
					assertCardTownExitCommitted(t, s, runtime, session, 1033+bonus)
					counts := cardTownExitPacketCounts(t, conn.write.Bytes())
					if counts[24] != 1 || counts[71] != 1 {
						t.Fatalf("回城或自动选免费牌数量错误：%v", counts)
					}
					conn.write.Reset()
					if err := cardTownExitRequest(t, s, session, opcode); err != nil {
						t.Fatal(err)
					}
					if err := s.handleDungeonSelectCard(session, []byte{0}); err != nil {
						t.Fatal(err)
					}
					if err := s.handleDungeonCardSelectRightState(session, nil); err != nil {
						t.Fatal(err)
					}
					if cardTownExitGold(t, s) != 1033+bonus || conn.write.Len() != 0 {
						t.Fatal("退出后重放再次发奖或发回城包")
					}
				})
			}
		}
	}
}

type cardTownExitFailedAssets struct{ err error }

func (f cardTownExitFailedAssets) WithinCharacterAssets(context.Context, string, func(dnfrepo.CharacterRepository, dnfrepo.InventoryRepository, dnfrepo.EquipmentRepository) error) error {
	return f.err
}
func (f cardTownExitFailedAssets) WithinMailboxAssets(context.Context, string, string, func(dnfrepo.CharacterRepository, dnfrepo.InventoryRepository, dnfrepo.MailboxRepository) error) error {
	return f.err
}

func TestCardTownExitRewardFailureRetainsIntentAndRetriesOnce(t *testing.T) {
	for _, opcode := range []uint16{42, 132} {
		t.Run(fmt.Sprint(opcode), func(t *testing.T) {
			s, runtime, session, conn := cardTownExitFixture(t, 0)
			if err := cardTownExitRequest(t, s, session, opcode); err != nil {
				t.Fatal(err)
			}
			provider := s.repositoryProvider
			repos, _ := provider()
			failure := cardTownExitFailedAssets{errors.New("隔离奖励事务失败")}
			repos.CharacterAssets = failure
			repos.MailboxAssets = failure
			s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
			if err := s.handleDungeonScoreScrollState(session, nil); err != nil {
				t.Fatal(err)
			}
			if err := s.handleDungeonCardSelectRightState(session, nil); err != nil {
				t.Fatal(err)
			}
			if session.dungeon.runtime != runtime || runtime.townReturnOp24Sent || runtime.settlementCardRewardCommitted || cardTownExitGold(t, s) != 1000 {
				t.Fatal("奖励失败仍回城、错误标记完成或吞掉待办")
			}
			s.repositoryProvider = provider
			conn.write.Reset()
			if err := s.handleDungeonSelectCard(session, []byte{0}); err != nil {
				t.Fatal(err)
			}
			assertCardTownExitCommitted(t, s, runtime, session, 1033)
		})
	}
}

func TestCardTownExitTownWriteFailureKeepsRewardAndRetry(t *testing.T) {
	for _, opcode := range []uint16{42, 132} {
		t.Run(fmt.Sprint(opcode), func(t *testing.T) {
			s, runtime, session, _ := cardTownExitFixture(t, 0)
			if err := cardTownExitRequest(t, s, session, opcode); err != nil {
				t.Fatal(err)
			}
			if err := s.handleDungeonScoreScrollState(session, nil); err != nil {
				t.Fatal(err)
			}
			want := errors.New("隔离回城24写入失败")
			conn := &failCompletedTownOp24WriteConn{t: t, err: want}
			session.conn = conn
			if err := s.handleDungeonCardSelectRightState(session, nil); !errors.Is(err, want) {
				t.Fatalf("未命中预期回城写失败：%v", err)
			}
			if cardTownExitGold(t, s) != 1033 || session.dungeon.runtime != runtime || runtime.townReturnOp24Sent {
				t.Fatal("写失败丢失原局或重复奖励")
			}
			if err := cardTownExitRequest(t, s, session, opcode); err != nil {
				t.Fatal(err)
			}
			assertCardTownExitCommitted(t, s, runtime, session, 1033)
			if conn.failedOp24Attempts != 1 || conn.successfulOp24Writes != 1 || cardTownExitPacketCounts(t, conn.write.Bytes())[71] != 1 {
				t.Fatal("回城重试重复翻牌或重复成功24")
			}
		})
	}
}

func TestCardTownExitRejectsPreStatisticAndMalformedRequests(t *testing.T) {
	for _, opcode := range []uint16{42, 132} {
		for _, kind := range []string{"统计未就绪", "非空请求", "错误分类", "角色不符"} {
			t.Run(fmt.Sprintf("%d/%s", opcode, kind), func(t *testing.T) {
				s, runtime, session, conn := cardTownExitFixture(t, 0)
				body, class := []byte(nil), byte(dnfproto.DefaultChannelClassification)
				if kind == "统计未就绪" {
					runtime.settlementStatisticReceived = false
				}
				if kind == "非空请求" {
					body = []byte{1}
				}
				if kind == "错误分类" {
					class = 2
				}
				if kind == "角色不符" {
					session.selectedCharacterID = 100
				}
				packet, err := dnfproto.BuildChannelPacket(opcode, body, 0, class)
				if err != nil {
					t.Fatal(err)
				}
				if err := s.handleGameUpper(session, packet); err != nil {
					t.Fatal(err)
				}
				session.selectedCharacterID = 99
				runtime.settlementStatisticReceived = true
				conn.write.Reset()
				if err := s.handleDungeonScoreScrollState(session, nil); err != nil {
					t.Fatal(err)
				}
				if err := s.handleDungeonCardSelectRightState(session, nil); err != nil {
					t.Fatal(err)
				}
				if runtime.townReturnOp24Sent || cardTownExitGold(t, s) != 1000 || cardTownExitPacketCounts(t, conn.write.Bytes())[24] != 0 {
					t.Fatal("非法或未授权退出被保留并越过屏障")
				}
			})
		}
	}
}

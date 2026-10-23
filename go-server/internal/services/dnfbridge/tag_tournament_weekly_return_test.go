package dnfbridge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/tagtournament"
)

// 走完整七关与结果页72/24，不补35、36、15或队伍设置请求。
func tagWeeklyCompletedReturnFixture(t *testing.T) (*Service, *gameSession, repo.Group) {
	t.Helper()
	s, p, g := tagPlayFixture(t)
	weekly, _, _ := tagWeeklyClaimBridgeFixture(t)
	s.tagTournamentCatalog = weekly.tagTournamentCatalog
	p.accountFromToken = true
	enableTownMoveSkillProjection(t, s, g, "29")
	ch, _, err := g.Character.Load(context.Background(), "29")
	if err != nil {
		t.Fatal(err)
	}
	ch.Stats["direction"], ch.Stats["area_state"] = 0, 0
	if err = g.Character.Save(context.Background(), ch); err != nil {
		t.Fatal(err)
	}
	tagPlayChallenge(t, s, p, tagtournament.Hard)
	for range p.tagTournamentBattle.plan.Encounters {
		tagPlayMoveNext(t, s, p)
		tagPlayKillBoss(t, s, p)
	}
	if err = s.handleDungeonEplpCommand(p, []byte{1, 2}); err != nil {
		t.Fatal(err)
	}
	if p.dungeon.runtime != nil || p.tagTournamentBattle != nil || p.confirmedDungeonReturnStatePending || !p.sceneBootstrapTailDeferred || p.sceneBootstrapTailSent || currentTagTournamentLobbyReady(p) {
		t.Fatal("没有复现结果页返回已提交而旧界面尾包仍挂起的现场")
	}
	p.conn.(*bufferConn).write.Reset()
	return s, p, g
}

func TestTagWeeklyClaimAfterCompletedReturnWithoutTownCallback(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprint(legacy), func(t *testing.T) {
			s, p, g := tagWeeklyCompletedReturnFixture(t)
			beforeCount, beforeRewards := tagBattleMailTotals(t, g)
			for attempt := 0; attempt < 3; attempt++ {
				p.conn.(*bufferConn).write.Reset()
				var handled bool
				var err error
				if legacy {
					handled, err = currentGameplayModules.DispatchLegacy(s, p, 1554, nil)
				} else {
					handled, err = currentGameplayModules.DispatchUpper(s, p, 1554, 1, nil)
				}
				if err != nil || !handled {
					t.Fatal("真实领取入口未处理", handled, err)
				}
				counts := map[uint16]int{}
				for rest := p.conn.(*bufferConn).write.Bytes(); len(rest) > 0; {
					packet, tail := splitCurrentGameServerUpperPacketAuto(t, rest)
					counts[packet.Header.MsgID]++
					if packet.Header.MsgID == 1554 && (packet.Header.Classification != 1 || !bytes.Equal(packet.Body, []byte{1})) {
						t.Fatal("已达标、已回休息室却拒绝领取", packet)
					}
					if packet.Header.MsgID == 1530 && (len(packet.Body) != 13 || packet.Body[12] != 0) {
						t.Fatal("领取后仍开放按钮", packet)
					}
					rest = tail
				}
				if !currentTagTournamentWeeklyClaimLocationReady(p) || !p.sceneBootstrapTailDeferred || p.sceneBootstrapTailSent || counts[1554] != 1 || counts[1530] != 1 || counts[99] != 1 || counts[24] != 0 || counts[72] != 0 || counts[2] != 0 || counts[37] != 0 || counts[19] != 0 {
					t.Fatal("领取未成功或改动了独立场景尾包流程", counts)
				}
			}
			afterCount, afterRewards := tagBattleMailTotals(t, g)
			if afterCount != beforeCount+1 || afterRewards[777] != 1 {
				t.Fatal("回城后多次领取未只增加一封周奖", afterCount, afterRewards)
			}
			delete(afterRewards, 777)
			if !reflect.DeepEqual(beforeRewards, afterRewards) {
				t.Fatal("回城领奖重复发放阶段材料", beforeRewards, afterRewards)
			}
		})
	}
}

func TestTagWeeklyClaimInvalidRequestCannotConsumeDeferredReturnTail(t *testing.T) {
	for _, kind := range []string{"正文", "未认证", "未登录", "选角失效", "非帝国频道", "错误分类", "未写完回城", "位置属于别的角色", "落点不是休息室", "位置未确认", "回城仍未提交"} {
		t.Run(kind, func(t *testing.T) {
			s, p, g := tagWeeklyCompletedReturnFixture(t)
			beforeCount, beforeRewards := tagBattleMailTotals(t, g)
			var body []byte
			class := byte(1)
			switch kind {
			case "正文":
				body = []byte{1}
			case "未认证":
				p.accountFromToken = false
			case "未登录":
				p.loginPacketSeen = false
			case "选角失效":
				delete(s.gameSessions, p.selectedCharacterID)
			case "非帝国频道":
				p.channel.Type = 0
			case "错误分类":
				class = 0
			case "未写完回城":
				p.initialTownRouteStage = currentInitialTownRouteActorBound
			case "位置属于别的角色":
				p.townPositionSnapshot.CharacterID++
			case "落点不是休息室":
				p.townPositionSnapshot.TownID = 1
			case "位置未确认":
				p.townPositionSnapshot.PositionValid = false
			case "回城仍未提交":
				p.confirmedDungeonReturnStatePending = true
			}
			pending, stage, position := p.confirmedDungeonReturnStatePending, p.initialTownRouteStage, p.townPositionSnapshot
			if _, err := currentGameplayModules.DispatchUpper(s, p, 1554, class, body); err != nil {
				t.Fatal(err)
			}
			afterCount, afterRewards := tagBattleMailTotals(t, g)
			if p.confirmedDungeonReturnStatePending != pending || p.initialTownRouteStage != stage || p.townPositionSnapshot != position || !p.sceneBootstrapTailDeferred || p.sceneBootstrapTailSent || afterCount != beforeCount || !reflect.DeepEqual(beforeRewards, afterRewards) {
				t.Fatal("非法领奖改动了场景状态或发奖", afterCount, afterRewards)
			}
		})
	}
}

func TestTagWeeklyClaimDeferredReturnWriteFailureCanRetry(t *testing.T) {
	for failAt := 1; failAt <= 3; failAt++ {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			s, p, g := tagWeeklyCompletedReturnFixture(t)
			beforeCount, _ := tagBattleMailTotals(t, g)
			failure := errors.New("模拟回城后周奖应答写失败")
			p.conn = &failNthDungeonWriteConn{failAt: failAt, err: failure}
			if err := s.handleCurrentTagTournamentWeeklyClaim(p, nil); !errors.Is(err, failure) {
				t.Fatal("没有返回周奖通知写失败", err)
			}
			failedCount, failedRewards := tagBattleMailTotals(t, g)
			if failedCount != beforeCount+1 || failedRewards[777] != 1 {
				t.Fatal("通知失败丢失已提交周奖", failedCount, failedRewards)
			}
			p.conn = &bufferConn{}
			if err := s.handleCurrentTagTournamentWeeklyClaim(p, nil); err != nil {
				t.Fatal(err)
			}
			afterCount, afterRewards := tagBattleMailTotals(t, g)
			if !p.sceneBootstrapTailDeferred || p.sceneBootstrapTailSent || afterCount != beforeCount+1 || afterRewards[777] != 1 {
				t.Fatal("重试改变场景尾包或重复发奖", afterCount, afterRewards)
			}
		})
	}
}

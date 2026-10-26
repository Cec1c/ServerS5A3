package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/premium"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

type premiumGoldCardRestoreFailureConn struct {
	bufferConn
	attempt []byte
	err     error
}

func (c *premiumGoldCardRestoreFailureConn) Write(p []byte) (int, error) {
	if len(p) >= 3 && p[0] == 1 && binary.LittleEndian.Uint16(p[1:3]) == uint16(dnfenum.CmdPacketPremiumService) {
		c.attempt = append([]byte(nil), p...)
		return 0, c.err
	}
	return c.bufferConn.Write(p)
}

func TestCurrentPremiumGoldCardTownRestoreFailureWithholdsTransition(t *testing.T) {
	service, session, connection, _ := newSceneReadyReplayFixture(t)
	repos, _ := service.repositoryGroup()
	now := time.Now().UTC()
	account := dnfrepo.AccountRecord{AccountID: service.accountIDForSession(session), Metadata: map[string]string{}}
	premium.Upsert(&account, premium.DevilSlotType(premium.DevilSlotGoldCard), 86400, 1, now)
	if err := repos.Account.Save(context.Background(), account); err != nil {
		t.Fatal(err)
	}
	character, _, _ := repos.Character.Load(context.Background(), "6")
	for range 9 {
		premium.TryConsumeDaily(&character, premium.DevilSlotGoldCard, now)
	}
	if err := repos.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}
	armLoginRouteUpToOp24(session)
	wantErr := errors.New("测试登录契约明细写失败")
	failed := &premiumGoldCardRestoreFailureConn{err: wantErr}
	session.conn = failed
	if err := service.sendCurrentInitialTownPlayerState(session, "测试选角契约恢复"); !errors.Is(err, wantErr) {
		t.Fatalf("未到达契约同步入口：%v", err)
	}
	// 本夹具明确使用16字节服务端头，不能用只识别少数场景通知的自动切包器。
	packet, trailing := splitGameServerUpperPacketWithHeader(t, failed.attempt, 16)
	want := buildCurrentPremiumServiceDataBody(account, character, now)
	if len(trailing) != 0 || !bytes.Equal(packet.Body, want) {
		t.Fatalf("登录契约明细未恢复期限/次数：%x want=%x", packet.Body, want)
	}
	for _, packet := range splitAllCurrentUpperPackets(t, failed.write.Bytes()) {
		if packet.Header.MsgID == currentSceneTransitionMsgID {
			t.Fatal("契约同步失败仍提交城镇过场")
		}
	}
	if session.initialTownRouteStage != currentInitialTownRouteActorBound || connection.write.Len() != 0 {
		t.Fatal("契约同步失败推进了初始化完成状态")
	}
}

func TestCurrentPremiumGoldCardRestoreUsesSelectedRoleAndServiceDay(t *testing.T) {
	service, session, repos, connection := newPremiumServiceTestRuntime(t, premium.DevilSlotGoldCard)
	now := time.Now().UTC()
	character, _, _ := repos.Character.Load(context.Background(), "19")
	for range 10 {
		premium.TryConsumeDaily(&character, premium.DevilSlotGoldCard, now)
	}
	if err := repos.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}
	if err := service.sendCurrentPremiumServiceState(session, "测试次数用尽恢复"); err != nil {
		t.Fatal(err)
	}
	if premiumServicePacketUsage(t, connection.write.Bytes(), 0) != 10 {
		t.Fatal("用尽次数未恢复")
	}
	character.CharacterID = "20"
	character.Stats = map[string]int64{}
	if err := repos.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}
	session.selectedCharacterID = 20
	connection.write.Reset()
	if err := service.sendCurrentPremiumServiceState(session, "测试切换角色恢复"); err != nil {
		t.Fatal(err)
	}
	if premiumServicePacketUsage(t, connection.write.Bytes(), 0) != 0 {
		t.Fatal("继承了上一角色次数")
	}
	for range 10 {
		premium.TryConsumeDaily(&character, 0, now.Add(-48*time.Hour))
	}
	if err := repos.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}
	connection.write.Reset()
	if err := service.sendCurrentPremiumServiceState(session, "测试服务日重置"); err != nil {
		t.Fatal(err)
	}
	if premiumServicePacketUsage(t, connection.write.Bytes(), 0) != 0 {
		t.Fatal("旧服务日次数未清除")
	}
	account := dnfrepo.AccountRecord{AccountID: character.AccountID, Metadata: map[string]string{premium.MetadataKey(premium.DevilSlotType(0)): strconv.FormatInt(now.Add(-time.Hour).Unix(), 10)}}
	if err := repos.Account.Save(context.Background(), account); err != nil {
		t.Fatal(err)
	}
	connection.write.Reset()
	if err := service.sendCurrentPremiumServiceState(session, "测试过期状态清除"); err != nil {
		t.Fatal(err)
	}
	packet, _ := splitGameServerUpperPacket(t, connection.write.Bytes())
	if binary.LittleEndian.Uint32(packet.Body[9:13]) != 0 {
		t.Fatal("过期契约仍投影专属牌资格")
	}
}

func TestCurrentPremiumGoldCardSettlementSyncResumesBeforeOp35(t *testing.T) {
	for _, failAt := range []int{0, 3, 4} {
		t.Run(strconv.Itoa(failAt), func(t *testing.T) {
			service, runtime, session, _, _ := prepareCompletedSettlementRuntime(t)
			plan := currentDungeonSettlementPacketPlanForTest(t, runtime, 73)
			account := dnfrepo.AccountRecord{Metadata: map[string]string{premium.MetadataKey(premium.DevilSlotType(0)): "2000000000"}}
			plan.PremiumServiceBody = buildCurrentPremiumServiceDataBody(account, dnfrepo.CharacterRecord{}, time.Unix(1900000000, 0))
			runtime.settlementResultPlan = &plan
			wantErr := errors.New("测试契约或翻牌写失败")
			connection := &failNthDungeonWriteConn{failAt: failAt, err: wantErr}
			session.conn = connection
			request := make([]byte, currentDungeonPlayResultBaseSize)
			request[currentDungeonPlayResultClientRankPointOffset] = 73
			err := service.handleDungeonSetPlayResult(session, request)
			if failAt > 0 {
				if !errors.Is(err, wantErr) || runtime.settlementClearRewardSent || runtime.settlementPremiumServiceSent != (failAt == 4) {
					t.Fatalf("失败游标错误：%v", err)
				}
				if err = service.handleDungeonSetPlayResult(session, request); err != nil {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			packets := splitAllCurrentUpperPackets(t, connection.write.Bytes())
			wantIDs := []uint16{34, 37, 903, 35}
			if len(packets) != len(wantIDs) {
				t.Fatalf("重复/缺少结算包：%d", len(packets))
			}
			for i, packet := range packets {
				if packet.Header.MsgID != wantIDs[i] {
					t.Fatalf("结算顺序[%d]=%d want=%d", i, packet.Header.MsgID, wantIDs[i])
				}
			}
			if packets[2].Header.Classification != 1 || !bytes.Equal(packets[2].Body, plan.PremiumServiceBody) {
				t.Fatal("契约明细未使用冻结正文")
			}
			before := connection.write.Len()
			if err := service.handleDungeonSetPlayResult(session, request); err != nil || connection.write.Len() != before {
				t.Fatalf("成功重放重复发包：%v", err)
			}
		})
	}
}

package dnfbridge

import (
	"bytes"
	"context"
	"os"
	"reflect"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/inventory"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func TestProfessionChangeFailurePromptsRealPVF(t *testing.T) {
	if os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE") == "" {
		t.Skip("需要现用真实PVF")
	}
	for _, activeTrial := range []bool{false, true} {
		name := "城镇金币不足"
		if activeTrial {
			name = "体验地图尚未发起回城"
		}
		t.Run(name, func(t *testing.T) {
			s, p, g, _ := levelUpTicketBridgeFixture(t, 2683467, 25)
			prepareProfessionRealCharacter(t, s, g, 2, 1, 25, 2683467)
			ctx := context.Background()
			c, _, err := g.Character.Load(ctx, "29")
			if err != nil {
				t.Fatal(err)
			}
			c.Stats[inventory.ProfessionChangeCountKey] = 1
			c.Stats["gold"] = 100112 // 当前PVF第二次变更费用100113，恰好不足1金币。
			if err := g.Character.Save(ctx, c); err != nil {
				t.Fatal(err)
			}
			var runtime *runtimeDungeonState
			var trialBefore worldmap.DungeonSessionSnapshot
			if activeTrial {
				// 使用已有地图源夹具建立真实active会话；这里只验证7142未回城保护，
				// 真实7142地图内容和完整入场另由专用PVF回归覆盖。
				source := bridgeDungeonPVF(true)
				for path, text := range source {
					source[path] = strings.ReplaceAll(text, "700", "7142")
				}
				_, resolver, _ := loadBridgeDungeonStaticData(t, source)
				topology, err := worldmap.BuildDungeonTopology(resolver, 7142, 0)
				if err != nil {
					t.Fatal(err)
				}
				run, err := worldmap.NewDungeonRun(topology)
				if err != nil {
					t.Fatal(err)
				}
				trial, err := worldmap.NewDungeonSession(run)
				if err != nil {
					t.Fatal(err)
				}
				runtime = &runtimeDungeonState{
					Dungeon: worldmap.Dungeon{ID: 7142}, Character: c, AccountID: c.AccountID, Session: trial,
				}
				p.dungeon.runtime = runtime
				p.townSceneReadyCharacterID = 0
				p.townPositionSnapshot.PositionValid = false
				trialBefore = trial.Snapshot()
				if trialBefore.Run.Status != worldmap.DungeonRunActive || trialBefore.Run.DungeonID != 7142 {
					t.Fatal("体验夹具没有活动7142运行时", trialBefore.Run)
				}
			}
			before := levelUpTicketBridgeSnapshot(t, g)
			conn := p.conn.(*bufferConn)
			conn.write.Reset()
			body := make([]byte, 14)
			body[13] = 2
			if err := s.handleCurrentProfessionChange(p, 785, body); err != nil {
				t.Fatal(err)
			}
			ack, rest := splitCurrentGameServerUpperPacketAuto(t, conn.write.Bytes())
			wantCode := byte(22)
			if activeTrial {
				wantCode = 99
			}
			want := []byte{1, wantCode, 0, 0, 0, 1}
			if ack.Header.MsgID != 785 || ack.Header.Classification != 1 || !bytes.Equal(ack.Body, want) || len(rest) != 0 {
				t.Fatalf("转职拒绝应答不符：header=%+v body=%x rest=%x，期望正文%x", ack.Header, ack.Body, rest, want)
			}
			if !reflect.DeepEqual(before, levelUpTicketBridgeSnapshot(t, g)) {
				t.Fatal("转职失败改动了金币、次数、正式职业、技能页、背包或任务")
			}
			if activeTrial && (p.dungeon.runtime != runtime || runtime.townReturnPending || runtime.townReturnOp24Sent || !reflect.DeepEqual(trialBefore, runtime.Session.Snapshot())) {
				t.Fatal("未发起回城的体验运行时被转职请求改变")
			}
		})
	}
}

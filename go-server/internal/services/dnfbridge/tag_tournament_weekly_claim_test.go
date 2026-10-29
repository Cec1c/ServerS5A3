package dnfbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/tagtournament"
)

func tagWeeklyClaimBridgeFixture(t *testing.T) (*Service, *gameSession, repo.Group) {
	t.Helper()
	s, p, g := newTagRosterBridgeTest(t)
	source := tagRosterTestSource(t)
	source[tagtournament.ConfigPath] += "\n[tag tournament hard mode time attack standard rank]\n20000 90000\n[tag tournament hard mode time attack reward]\n777 1668 1669 15\n"
	source["stackable/stackable.lst"] += "777 `777.pvf`\n"
	source["stackable/777.pvf"] = "[name] `合成周挑战字体`\n[attach type] `[account]`\n[usable period] 3\n[action type] `[add damage font skin]`\n[/action type]\n[damage font info]\n[index] 10\n[expiration info] `[period]` 9\n[/damage font info]\n"
	var err error
	s.tagTournamentCatalog, err = tagtournament.LoadCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	p.accountFromToken = true
	ctx := context.Background()
	a, _, _ := g.Account.Load(ctx, "a")
	// 隔离桥接夹具只种入明确的持久成绩；完整Run通关凭据由领域与原有终局回归覆盖。
	at := time.Now().UTC()
	record := tagtournament.ClearRecord{Mode: tagtournament.Hard, ElapsedMS: 80123, FinishedAt: at, Characters: [2]tagtournament.RecordCharacter{{ID: "29", Name: "当前角色", Job: "2", Grow: 33}, {ID: "60", Name: "队员二", Job: "0", Grow: 34}}}
	raw, err := json.Marshal(map[string]any{"best": [2]*tagtournament.ClearRecord{nil, &record}, "weeks": map[int64]tagtournament.ClearRecord{tagtournament.RecordWeek(at): record}})
	if err != nil {
		t.Fatal(err)
	}
	a.Metadata["tag_tournament_records_v1"] = string(raw)
	if err = g.Account.Save(ctx, a); err != nil {
		t.Fatal(err)
	}
	return s, p, g
}

func TestTagWeeklyClaimRoutesNativeAckStateAndMailWithoutRepeat(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprint(legacy), func(t *testing.T) {
			s, p, g := tagWeeklyClaimBridgeFixture(t)
			if err := s.sendCurrentTagTournamentWeeklyInfo(p); err != nil {
				t.Fatal(err)
			}
			state, rest := splitCurrentGameServerUpperPacketAuto(t, p.conn.(*bufferConn).write.Bytes())
			if state.Header.MsgID != 1530 || len(rest) != 0 || state.Body[12] != 1 {
				t.Fatal("达标未亮领取按钮", state)
			}
			for i := 0; i < 2; i++ {
				p.conn.(*bufferConn).write.Reset()
				var handled bool
				var err error
				if legacy {
					handled, err = currentGameplayModules.DispatchLegacy(s, p, 1554, nil)
				} else {
					handled, err = currentGameplayModules.DispatchUpper(s, p, 1554, 1, nil)
				}
				if !handled || err != nil {
					t.Fatal(handled, err)
				}
				ack, tail := splitCurrentGameServerUpperPacketAuto(t, p.conn.(*bufferConn).write.Bytes())
				if ack.Header.MsgID != 1554 || ack.Header.Classification != 1 || !bytes.Equal(ack.Body, []byte{1}) {
					t.Fatal("原生领取成功标记错误", ack)
				}
				info, tail := splitCurrentGameServerUpperPacketAuto(t, tail)
				if info.Header.MsgID != 1530 || info.Body[12] != 0 {
					t.Fatal("领取后按钮未关闭", info)
				}
				alarm, tail := splitCurrentGameServerUpperPacketAuto(t, tail)
				if alarm.Header.MsgID != 99 || len(tail) != 0 {
					t.Fatal("没有已提交邮件提醒", alarm)
				}
			}
			box, _, _ := g.Mailbox.Load(context.Background(), "29")
			if len(box.Mails) != 1 {
				t.Fatal("重复点击多发邮件", box)
			}
			for _, m := range box.Mails {
				if len(m.Attachments) != 1 || m.Attachments[0].ItemID != 777 || m.Attachments[0].Count != 1 {
					t.Fatal(m)
				}
			}
		})
	}
}

func TestTagWeeklyClaimProjectionWriteFailureKeepsOneDurableReward(t *testing.T) {
	for failAt := 1; failAt <= 3; failAt++ {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			s, p, g := tagWeeklyClaimBridgeFixture(t)
			failure := errors.New("模拟周奖通知写失败")
			p.conn = &failNthDungeonWriteConn{failAt: failAt, err: failure}
			if err := s.handleCurrentTagTournamentWeeklyClaim(p, nil); !errors.Is(err, failure) {
				t.Fatal(err)
			}
			p.conn = &bufferConn{}
			if err := s.handleCurrentTagTournamentWeeklyClaim(p, nil); err != nil {
				t.Fatal(err)
			}
			box, _, _ := g.Mailbox.Load(context.Background(), "29")
			if len(box.Mails) != 1 {
				t.Fatal("写失败重试多发周奖", box)
			}
		})
	}
}

func TestTagWeeklyClaimRejectsInvalidSceneSessionAndRequest(t *testing.T) {
	for _, kind := range []string{"正文", "未认证", "选角失效", "未就绪", "比赛中", "非帝国频道", "错误分类"} {
		t.Run(kind, func(t *testing.T) {
			s, p, g := tagWeeklyClaimBridgeFixture(t)
			var body []byte
			cls := byte(1)
			switch kind {
			case "正文":
				body = []byte{1}
			case "未认证":
				p.accountFromToken = false
			case "选角失效":
				delete(s.gameSessions, p.selectedCharacterID)
			case "未就绪":
				p.sceneBootstrapTailSent = false
			case "比赛中":
				p.tagTournamentBattle = &currentTagTournamentBattle{}
			case "非帝国频道":
				p.channel.Type = 0
			case "错误分类":
				cls = 0
			}
			if _, err := currentGameplayModules.DispatchUpper(s, p, 1554, cls, body); err != nil {
				t.Fatal(err)
			}
			box, _, _ := g.Mailbox.Load(context.Background(), "29")
			if len(box.Mails) != 0 {
				t.Fatal("非法入口发了周奖", kind)
			}
		})
	}
}

func TestTagWeeklyWireRefusesClaimWithoutQualifyingTime(t *testing.T) {
	if _, err := buildCurrentTagTournamentWeeklyInfo(90000, nil, true); err == nil {
		t.Fatal("无成绩可领")
	}
	r := &tagtournament.ClearRecord{Mode: tagtournament.Hard, ElapsedMS: 90001, Characters: [2]tagtournament.RecordCharacter{{Job: "0"}, {Job: "2"}}}
	if _, err := buildCurrentTagTournamentWeeklyInfo(90000, r, true); err == nil {
		t.Fatal("超时可领")
	}
}

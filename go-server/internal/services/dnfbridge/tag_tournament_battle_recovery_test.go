package dnfbridge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/tagtournament"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

type tagBattleFailTransaction struct {
	repo.MailboxAssetUnitOfWork
	inner repo.AccountTradeMailboxUnitOfWork
	fail  bool
}

func (u *tagBattleFailTransaction) WithinAccountTradeMailbox(ctx context.Context, account string, ids []string, recipient string, fn func(repo.AccountRepository, repo.CharacterRepository, repo.InventoryRepository, repo.MailboxRepository) error) error {
	return u.inner.WithinAccountTradeMailbox(ctx, account, ids, recipient, func(a repo.AccountRepository, c repo.CharacterRepository, i repo.InventoryRepository, m repo.MailboxRepository) error {
		if err := fn(a, c, i, m); err != nil {
			return err
		}
		if u.fail {
			u.fail = false
			return errors.New("隔离注入邮件事务提交失败")
		}
		return nil
	})
}

func TestTagBattleTagWriteFailureOnlyReprojectsCommittedRole(t *testing.T) {
	s, p, _, now := tagBattleBridgeFixture(t, tagtournament.Normal)
	failure := errors.New("隔离切人写失败")
	c := &failNthDungeonWriteConn{failAt: 1, err: failure}
	p.conn = c
	if err := s.handleCurrentTagTournamentTag(p, []byte{1}); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	first := p.tagTournamentBattle.run.Snapshot()
	if first.Active != 1 || !p.tagTournamentBattle.tagProjectionPending {
		t.Fatal("丢失已提交切人或未保留投影")
	}
	if err := s.handleCurrentTagTournamentTag(p, []byte{0}); err != nil {
		t.Fatal(err)
	}
	if after := p.tagTournamentBattle.run.Snapshot(); !reflect.DeepEqual(first, after) {
		t.Fatal("重发又切一次或重置冷却")
	}
	packet, rest := splitCurrentGameServerUpperPacketAuto(t, c.write.Bytes())
	if packet.Header.MsgID != 1402 || !bytes.Equal(packet.Body, []byte{1}) || len(rest) != 0 {
		t.Fatal(packet, rest)
	}
	*now = now.Add(5 * time.Second)
	if err := s.handleCurrentTagTournamentTag(p, []byte{0}); err != nil || p.tagTournamentBattle.run.Snapshot().Active != 0 {
		t.Fatal(err)
	}
}

func TestTagBattleSettlementSocketFailuresRetryWithoutDuplicateMail(t *testing.T) {
	for failAt := 1; failAt <= 3; failAt++ {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			s, p, g, now := tagBattleBridgeFixture(t, tagtournament.Normal)
			e, _, err := s.beginCurrentTagTournamentRound(p, false)
			if err != nil {
				t.Fatal(err)
			}
			failure := errors.New("隔离奖励通知写失败")
			c := &failNthDungeonWriteConn{failAt: failAt, err: failure}
			p.conn = c
			if err = s.confirmCurrentTagTournamentRound(p, e.MapID, []uint32{e.MonsterID}, true); !errors.Is(err, failure) {
				t.Fatal("没有命中通知失败", err)
			}
			if count, _ := tagBattleMailTotals(t, g); count != 1 {
				t.Fatal("网络失败导致邮件丢失")
			}
			if _, _, err = s.beginCurrentTagTournamentRound(p, false); err == nil {
				t.Fatal("上轮投影没完成仍开始下一关")
			}
			// 共用时间队列可重试；不要求客户端再次伪造Boss死亡。
			p.tagTournamentLobbyDay = p.tagTournamentBattle.plan.ServiceDay
			if err = s.refreshCurrentTagTournamentLobby(p, *now); err != nil {
				t.Fatal(err)
			}
			if p.tagTournamentBattle.settlementProjectionPending || p.tagTournamentBattle.mailAlarmPending {
				t.Fatal("重试后仍挂起")
			}
			if count, _ := tagBattleMailTotals(t, g); count != 1 {
				t.Fatal("网络补偿重复生成邮件")
			}
		})
	}
}

func TestTagBattleMailCommitFailurePreservesRunAndRollsBackClaims(t *testing.T) {
	s, p, g, now := tagBattleBridgeFixture(t, tagtournament.Normal)
	u := &tagBattleFailTransaction{MailboxAssetUnitOfWork: g.MailboxAssets, inner: g.MailboxAssets.(repo.AccountTradeMailboxUnitOfWork), fail: true}
	g.MailboxAssets = u
	s.repositoryProvider = func() (repo.Group, bool) { return g, true }
	o, err := tagtournament.NewOwnerWithClock(s.tagTournamentCatalog, g, func() time.Time { return *now })
	if err != nil {
		t.Fatal(err)
	}
	p.tagTournamentBattle.owner = o
	e, _, err := s.beginCurrentTagTournamentRound(p, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.confirmCurrentTagTournamentRound(p, e.MapID, []uint32{e.MonsterID}, true); err == nil {
		t.Fatal("未命中邮件事务失败")
	}
	if p.tagTournamentBattle.run.Snapshot().Cleared != 1 || !p.tagTournamentBattle.settlementPending {
		t.Fatal("入库失败丢失已确认进度")
	}
	if count, _ := tagBattleMailTotals(t, g); count != 0 {
		t.Fatal("失败事务仍留下邮件")
	}
	view, err := o.Lobby(context.Background(), "a")
	if err != nil || len(view.Day.NormalClaims) != 0 {
		t.Fatal("邮件失败却消耗每日领取额度", view, err)
	}
	p.tagTournamentLobbyDay = p.tagTournamentBattle.plan.ServiceDay
	if err = s.refreshCurrentTagTournamentLobby(p, *now); err != nil {
		t.Fatal(err)
	}
	if count, _ := tagBattleMailTotals(t, g); count != 1 {
		t.Fatal("邮件事务失败后不能恢复")
	}
}

func TestTagBattleNewAdmissionInvalidatesOldLiveCommands(t *testing.T) {
	s, p, _, _ := tagBattleBridgeFixture(t, tagtournament.Normal)
	b := p.tagTournamentBattle
	if _, err := b.owner.Admit(context.Background(), "a", "29", [2]string{"29", "60"}, tagtournament.Normal); err != nil {
		t.Fatal(err)
	}
	before := b.run.Snapshot()
	if err := s.handleCurrentTagTournamentTag(p, []byte{1}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, b.run.Snapshot()) {
		t.Fatal("新挑战后旧连接还能切人")
	}
	packet, _ := splitCurrentGameServerUpperPacketAuto(t, p.conn.(*bufferConn).write.Bytes())
	if packet.Header.MsgID != 1465 || packet.Body[0] != 0 {
		t.Fatal(packet)
	}
}

func TestTagBattleMutationGuardCoversFallbackAndBetweenRounds(t *testing.T) {
	for _, transport := range []string{"legacy", "upper"} {
		for _, opcode := range []dnfenum.CmdPacket{dnfenum.CmdPacketMoveItemspace, dnfenum.CmdPacketChangeSkillslot, dnfenum.CmdPacketBuySkill, dnfenum.CmdPacketSkillInit, dnfenum.CmdPacketChangeAnotherSkillTree, dnfenum.CmdPacketSkillSwitchInventory} {
			t.Run(transport+fmt.Sprint(opcode), func(t *testing.T) {
				s, p, g, now := tagBattleBridgeFixture(t, tagtournament.Normal)
				tagBattleBridgeClear(t, s, p, now, false)
				p.conn.(*bufferConn).write.Reset()
				before, _, _ := g.Character.Load(context.Background(), "29")
				var handled bool
				var err error
				if transport == "legacy" {
					handled, err = currentGameplayModules.DispatchLegacy(s, p, uint16(opcode), nil)
				} else {
					handled, err = currentGameplayModules.DispatchUpper(s, p, uint16(opcode), 1, nil)
				}
				if !handled || err != nil {
					t.Fatal(handled, err)
				}
				packet, rest := splitCurrentGameServerUpperPacketAuto(t, p.conn.(*bufferConn).write.Bytes())
				if packet.Header.MsgID != uint16(opcode) || packet.Body[0] != 0 || len(rest) != 0 {
					t.Fatal(packet)
				}
				after, _, _ := g.Character.Load(context.Background(), "29")
				if !reflect.DeepEqual(before, after) {
					t.Fatal("比赛中修改持久角色")
				}
			})
		}
	}
}

func tagBattleAuthoritativeDungeon(t *testing.T, s *Service, p *gameSession) (*runtimeDungeonState, uint32) {
	t.Helper()
	b := p.tagTournamentBattle
	e := b.plan.Encounters[b.run.Snapshot().Round]
	source := bridgeDungeonPVF(true)
	source[worldmap.DefaultMapList] = fmt.Sprintf("%d `dungeon/test/start.map`\n", e.MapID)
	source[worldmap.DefaultDungeonList] = fmt.Sprintf("%d `test.dgn`\n", b.plan.DungeonID)
	source["dungeon/test.dgn"] = strings.ReplaceAll(source["dungeon/test.dgn"], "`map` 0 0 100", fmt.Sprintf("`map` 0 0 %d", e.MapID))
	source["map/dungeon/test/start.map"] = fmt.Sprintf("[map name]\n`隔离竞技场地图`\n[dungeon]\n%d\n[type]\n`[start]`\n[monster]\n%d 90 0 100 200 0 0 0 `[fixed]` `[normal]`\n", b.plan.DungeonID, e.MonsterID)
	table, err := worldmap.LoadSource(context.Background(), source, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := worldmap.NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}
	topology, err := worldmap.BuildDungeonTopology(resolver, int64(b.plan.DungeonID), 0)
	if err != nil {
		t.Fatal(err)
	}
	run, err := worldmap.NewDungeonRun(topology)
	if err != nil {
		t.Fatal(err)
	}
	dungeon, _ := table.FindDungeon(int64(b.plan.DungeonID))
	world, err := worldmap.NewDungeonSession(run)
	if err != nil {
		t.Fatal(err)
	}
	const key uint32 = 302
	if err = world.BindHostileObject(worldmap.HostileReference{Kind: worldmap.HostileMonster, Index: 0}, key); err != nil {
		t.Fatal(err)
	}
	runtime := &runtimeDungeonState{Dungeon: dungeon, Session: world, AccountID: p.accountID, Character: b.characters[0]}
	p.dungeon.runtime = runtime
	return runtime, key
}

func TestTagBattleAuthoritativeMapHookRequiresCommittedDeathAndCurrentRuntime(t *testing.T) {
	s, p, g, _ := tagBattleBridgeFixture(t, tagtournament.Normal)
	if _, _, err := s.beginCurrentTagTournamentRound(p, false); err != nil {
		t.Fatal(err)
	}
	runtime, key := tagBattleAuthoritativeDungeon(t, s, p)
	if handled, err := s.syncCurrentTagTournamentRoundLocked(p, runtime); !handled || err != nil {
		t.Fatal(handled, err)
	}
	if count, _ := tagBattleMailTotals(t, g); count != 0 {
		t.Fatal("只绑定对象、未死亡就发奖")
	}
	copyRuntime := *runtime
	if _, err := s.syncCurrentTagTournamentRoundLocked(p, &copyRuntime); err == nil {
		t.Fatal("接受另一份地图指针")
	}
	if _, err := runtime.Session.MarkHostileDefeated(key); err != nil {
		t.Fatal(err)
	}
	for repeat := 0; repeat < 2; repeat++ {
		if handled, err := s.syncCurrentTagTournamentRoundLocked(p, runtime); !handled || err != nil {
			t.Fatal(handled, err)
		}
	}
	if count, _ := tagBattleMailTotals(t, g); count != 1 {
		t.Fatal("权威Boss确认没有一次结算", count)
	}
}

func TestTagBattleIndependentBossChecksCannotEnterOrdinaryCards(t *testing.T) {
	s, p, g, _ := tagBattleBridgeFixture(t, tagtournament.Normal)
	if _, _, err := s.beginCurrentTagTournamentRound(p, false); err != nil {
		t.Fatal(err)
	}
	runtime, key := tagBattleAuthoritativeDungeon(t, s, p)
	runtime.Room = &runtimeDungeonRoom{}
	request := dungeoncmd.BossDieCheckRequest{RelatedActorObjectKey: currentSceneActorObjectKey(p.selectedCharacterID), TargetObjectKey: uint16(key)}
	if err := s.handleDungeonBossDieCheckLocked(p, runtime, request, false); err != nil {
		t.Fatal(err)
	}
	if count, _ := tagBattleMailTotals(t, g); count != 0 {
		t.Fatal("提前boss-check发了奖励")
	}
	if _, err := runtime.Session.MarkHostileDefeated(key); err != nil {
		t.Fatal(err)
	}
	if err := s.handleDungeonBossDieCheckLocked(p, runtime, request, true); err != nil {
		t.Fatal(err)
	}
	scene, _ := runtime.Session.Scene()
	if err := s.completeCurrentDungeonOrdinaryFinalRoomAfterDeathLocked(p, runtime, scene, key); err != nil {
		t.Fatal(err)
	}
	if runtime.bossDieCheckAccepted || runtime.ordinaryFinalRoomClearAccepted || runtime.settlementEntrySent || runtime.Session.Snapshot().Run.Status != worldmap.DungeonRunActive {
		t.Fatal("帝国回合误进普通副本翻牌完成链")
	}
	if count, _ := tagBattleMailTotals(t, g); count != 1 {
		t.Fatal("不同Boss入口重复结算", count)
	}
}

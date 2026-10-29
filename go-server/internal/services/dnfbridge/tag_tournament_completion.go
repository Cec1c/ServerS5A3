package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"time"

	"longheng.io/server/internal/modules/dnf/tagtournament"
)

const currentTagTournamentCompletionNotification uint16 = 1396

// 1461750依次读取四个u32，146BE30以第三项作为本场毫秒数，第一项<=0时
// 也采用本场时间。发送前第一项接入当前模式历史最佳，第二、四项保留0。
// 此通知关闭原生计时窗并进入通关窗，不写入未知名次或触发字体奖励。
func buildCurrentTagTournamentCompletion(start, end time.Time) ([]byte, error) {
	if start.IsZero() || end.Before(start) {
		return nil, fmt.Errorf("帝国竞技场完成时间缺少有效首回合起点")
	}
	elapsed := end.Sub(start).Milliseconds()
	if elapsed > math.MaxInt32 {
		return nil, fmt.Errorf("帝国竞技场完成时间超出现用有符号毫秒范围")
	}
	var w packetWriter
	w.writeUint32(0)
	w.writeUint32(0)
	w.writeUint32(uint32(elapsed))
	w.writeUint32(0)
	return w.bytes(), nil
}

func (s *Service) sendCurrentTagTournamentCompletion(p *gameSession, b *currentTagTournamentBattle) error {
	if b.completionBody == nil || b.completionSent {
		return nil
	}
	if b.run.Snapshot().Phase != tagtournament.Finished || b.roundProjectionPending || b.settlementPending || b.settlementProjectionPending || b.mailAlarmPending {
		return fmt.Errorf("帝国竞技场尚未完成邮件与回合投影，不能结束计时")
	}
	if b.completionRecord == nil {
		ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
		record, err := b.owner.RecordCompletion(ctx, b.run)
		cancel()
		if err != nil {
			return err
		}
		b.completionRecord = &record
		binary.LittleEndian.PutUint32(b.completionBody, record.Best.ElapsedMS)
		binary.LittleEndian.PutUint32(b.completionBody[8:], record.Current.ElapsedMS)
	}
	if !b.weeklyRecordSent && b.plan.Mode == tagtournament.Hard && s.tagTournamentCatalog.WeeklyChallengeTimeMS() != 0 {
		ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
		state, err := b.owner.Weekly(ctx, p.accountID)
		cancel()
		if err != nil {
			return err
		}
		body, err := buildCurrentTagTournamentWeeklyInfo(state.TargetTimeMS, state.Best, state.CanClaim)
		if err != nil {
			return err
		}
		if err = s.sendGameUpperRawClass(p, currentTagTournamentWeeklyNotification, body, 0); err != nil {
			return err
		}
		b.weeklyRecordSent = true
	}
	if err := s.sendGameUpperRawClass(p, currentTagTournamentCompletionNotification, b.completionBody, 0); err != nil {
		return err
	}
	b.completionSent = true
	s.logGameEvent(p, "帝国竞技场通关收尾已发送", "mode", b.plan.Mode, "cleared", b.run.Snapshot().Cleared)
	return nil
}

// 1397只展示本阶段已提交的奖励；已领奖的重复挑战由投影入口跳过，
// 不能通过发送空数组或零数量的图标来表示本次没有发奖。
func (s *Service) currentTagTournamentRoundDisplay(p *gameSession, b *currentTagTournamentBattle) ([]byte, error) {
	if len(b.pendingRoundRewards) > 0 {
		return buildCurrentTagTournamentRoundReward(b.pendingRoundRewards)
	}
	snapshot := b.run.Snapshot()
	if snapshot.Round < 0 || snapshot.Round >= len(b.plan.Encounters) {
		return nil, fmt.Errorf("帝国竞技场奖励显示缺少已确认回合")
	}
	rows, err := s.tagTournamentCatalog.RewardDisplayIcons(b.plan.Mode, b.plan.Encounters[snapshot.Round].Optional)
	if err != nil {
		return nil, err
	}
	return buildCurrentTagTournamentRoundReward(rows)
}

// 14618B0无条件创建94号K.O奖励窗口。没有新奖励时不发送1397，
// 仍确认本回合投影已完成，让45过图、王房选择和1396通关收尾继续。
func (s *Service) projectCurrentTagTournamentRoundReward(p *gameSession, b *currentTagTournamentBattle) error {
	if !b.roundProjectionPending {
		return nil
	}
	if b.roundRewardGranted {
		body, err := s.currentTagTournamentRoundDisplay(p, b)
		if err != nil {
			return err
		}
		if err := s.sendGameUpperRawClass(p, currentTagTournamentRoundRewardNotification, body, 0); err != nil {
			return err
		}
	}
	b.pendingRoundRewards = nil
	b.roundRewardGranted = false
	b.roundProjectionPending = false
	b.projectedRounds = b.run.Snapshot().Cleared
	return nil
}

// 仅在原地图已经提交回城、无挂起邮件或通知时释放连接挑战。当天付款资格
// 保留在账号账本，下次16重新准备；不以终局本身提前释放仍在战场的实例。
func (s *Service) releaseCurrentTagTournamentAfterTownCommit(p *gameSession, runtime *runtimeDungeonState) {
	if p == nil || p.tagTournamentBattle == nil || p.dungeon.runtime != nil {
		return
	}
	b := p.tagTournamentBattle
	if b.prepared == nil || b.prepared.runtime != runtime || b.settlementPending || b.settlementProjectionPending || b.roundProjectionPending || b.mailAlarmPending || b.tagProjectionPending {
		return
	}
	phase := b.run.Snapshot().Phase
	if phase != tagtournament.Finished && phase != tagtournament.Defeated && phase != tagtournament.Abandoned {
		return
	}
	p.tagTournamentBattle = nil
	s.logGameEvent(p, "帝国竞技场回城后旧挑战已释放", "mode", b.plan.Mode, "cleared", b.run.Snapshot().Cleared)
}

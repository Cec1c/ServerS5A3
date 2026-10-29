package dnfbridge

import (
	"context"
	"fmt"
	"math"
	"strconv"

	"longheng.io/server/internal/modules/dnf/tagtournament"
)

const currentTagTournamentWeeklyNotification uint16 = 1530
const currentTagTournamentWeeklyClaimOpcode uint16 = 1554

// 14621E0连续读取13字节：本周个人最佳毫秒、两个基础职业、两个转职字节、
// 挑战目标毫秒、领取状态。37C5960把bestTime/curTime绑定至1DC/1E4，
// 37C6010分别以字段+8/+0填值；XUI的上栏是目标、下栏是个人成绩。
// 两个职业缺记录时用原生默认16，不画假鬼剑士。
func buildCurrentTagTournamentWeeklyInfo(target uint32, best *tagtournament.ClearRecord, canClaim bool) ([]byte, error) {
	if target == 0 || target > math.MaxInt32 {
		return nil, fmt.Errorf("帝国竞技场周挑战时间不能由原生界面表达")
	}
	jobs, grows := [2]byte{16, 16}, [2]byte{}
	var elapsed uint32
	if best != nil {
		if best.Mode != tagtournament.Hard || best.ElapsedMS == 0 || best.ElapsedMS > math.MaxInt32 {
			return nil, fmt.Errorf("帝国竞技场本周最佳模式或时间无效")
		}
		elapsed = best.ElapsedMS
		for i, ch := range best.Characters {
			job, err := strconv.ParseUint(ch.Job, 10, 8)
			if err != nil || job > 15 || ch.Grow < 0 || ch.Grow > 127 {
				return nil, fmt.Errorf("帝国竞技场最快队伍职业不能由原生界面表达")
			}
			jobs[i], grows[i] = byte(job), byte(ch.Grow)
		}
	}
	if canClaim && (best == nil || best.ElapsedMS > target) {
		return nil, fmt.Errorf("帝国竞技场周挑战缺少达标成绩，不能开放领取")
	}
	var w packetWriter
	w.writeUint32(elapsed)
	w.writeByte(jobs[0])
	w.writeByte(jobs[1])
	w.writeByte(grows[0])
	w.writeByte(grows[1])
	w.writeUint32(target)
	if canClaim {
		w.writeByte(1)
	} else {
		w.writeByte(0)
	}
	return w.bytes(), nil
}

func (s *Service) sendCurrentTagTournamentWeeklyInfo(p *gameSession) error {
	if !s.isCurrentTagTournamentChannel(p) || s.tagTournamentCatalog.WeeklyChallengeTimeMS() == 0 {
		return nil
	}
	o, err := s.currentTagTournamentOwner()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	state, err := o.Weekly(ctx, p.accountID)
	if err != nil {
		return err
	}
	body, err := buildCurrentTagTournamentWeeklyInfo(state.TargetTimeMS, state.Best, state.CanClaim)
	if err != nil {
		return err
	}
	return s.sendGameUpperRawClass(p, currentTagTournamentWeeklyNotification, body, 0)
}

// 37C5570仅在1530末字节非零时发送1554空正文；1456460只使用共用成功标记。
// 现用分类接收包装已允许class1/1554，不能沿用旧编号门的静态结论。
// 通关72回城已写出24并提交休息室位置时，旧界面尾包可以仍在等1345/36。
// 周奖只需确认当前角色已离开战场并落在休息室，不消费或伪造这些场景回调。
func currentTagTournamentWeeklyClaimLocationReady(p *gameSession) bool {
	return currentTagTournamentLobbyInteractionReady(p)
}

func (s *Service) handleCurrentTagTournamentWeeklyClaim(p *gameSession, body []byte) error {
	ack := func(ok bool) error {
		value := byte(0)
		if ok {
			value = 1
		}
		return s.sendGameUpperRawClass(p, currentTagTournamentWeeklyClaimOpcode, []byte{value}, 1)
	}
	if len(body) != 0 || !p.accountFromToken || !p.loginPacketSeen || !currentTagTournamentWeeklyClaimLocationReady(p) || p.tagTournamentBattle != nil {
		return ack(false)
	}
	if _, ok := s.boundGameSessionCharacterSnapshot(p); !ok {
		return ack(false)
	}
	o, err := s.currentTagTournamentOwner()
	if err != nil {
		return ack(false)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	_, err = o.ClaimWeekly(ctx, p.accountID, strconv.Itoa(int(p.selectedCharacterID)))
	cancel()
	if err != nil {
		s.logGameEvent(p, "帝国竞技场周挑战领奖未提交", "error", err)
		if err = ack(false); err != nil {
			return err
		}
		return s.sendCurrentTagTournamentWeeklyInfo(p)
	}
	// 资产先提交，写应答失败后重试也只刷新已有邮件与状态，不再发一份。
	if err = ack(true); err != nil {
		return err
	}
	if err = s.sendCurrentTagTournamentWeeklyInfo(p); err != nil {
		return err
	}
	return s.sendMailboxAlarmForSession(p, p.selectedCharacterID, "帝国竞技场周挑战奖励邮件已提交")
}

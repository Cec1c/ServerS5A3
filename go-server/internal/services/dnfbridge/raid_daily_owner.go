package dnfbridge

import (
	"context"
	"fmt"
	"time"

	"longheng.io/server/internal/modules/dnf/raid/fiendwar"
)

type currentRaidDailyRule struct {
	Mode  byte
	Scope string
	Limit int
	Now   time.Time
}

// 三行协议分别读取各模式既有日账本；不能用超时空次数或固定零填其他模式。
// 本入口仅投影，不新增安图恩/卢克开团扣次规则。
func (s *Service) currentRaidDailyRule(mode byte) (*currentRaidDailyRule, error) {
	path, scope := "", ""
	switch mode {
	case 1:
		path, scope = "etc/raid/anton.etc", "raid:anton:daily_entries"
	case 2:
		path, scope = "etc/raid/luke/luke.etc", "raid:luke:daily_entries"
	case 3:
		path, scope = fiendwar.FiendWarPVFPath, fiendwar.FiendWarDailyScope
	default:
		return nil, fmt.Errorf("团本日次数模式无效")
	}
	s.initialEquipmentMu.Lock()
	a, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err != nil {
		return nil, err
	}
	doc, err := parseDungeonCardPVFDocument(a, path)
	if err != nil {
		return nil, err
	}
	limit, present := doc.Int("daily account enter limit")
	if !present {
		return nil, nil
	}
	if limit < 1 || limit > 255 {
		return nil, fmt.Errorf("团本PVF每日入场上限无效")
	}
	return &currentRaidDailyRule{Mode: mode, Scope: scope, Limit: int(limit), Now: s.gameplayNow()}, nil
}

// Native1303固定读三行，每行mode/used/limit/保留字段。1a58080只比较used与limit；
// 第四字段保留已见样本的0，不赋予服务端资格含义。旧15字节包仍只供旧档。
func (s *Service) sendCurrentNativeRaidDailyAvailability(p *gameSession, source string) error {
	if !isCurrentFiendWarChannel(p) || !s.currentRaidAuthenticated(p) {
		return nil
	}
	identity, ok := s.boundGameSessionCharacterSnapshot(p)
	repos, available := s.repositoryGroup()
	if !ok || !available || repos.Settings == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	account := s.accountIDForSession(p)
	unavailable := func(err error) error {
		s.logGameEvent(p, "团本日次数快照等待重试", "error", err)
		return nil
	}
	var body []byte
	for mode := byte(1); mode <= 3; mode++ {
		rule, err := s.currentRaidDailyRule(mode)
		if err != nil {
			return unavailable(err)
		}
		if rule == nil {
			return unavailable(fmt.Errorf("团本每日次数没有对应PVF上限，不能发布三行快照"))
		}
		record, _, err := repos.Settings.Load(ctx, rule.Scope)
		if err != nil {
			return unavailable(err)
		}
		used, err := fiendwar.FiendWarDailyUsed(record, account, rule.Now)
		if err != nil {
			return unavailable(err)
		}
		if used > 255 {
			return unavailable(fmt.Errorf("团本每日次数记录超出原生字段范围"))
		}
		body = append(body, mode, byte(used), byte(rule.Limit), 0)
	}
	if !s.boundGameSessionCharacterCurrent(identity) || s.accountIDForSession(p) != account {
		return nil
	}
	if err := s.sendGameUpperRawClass(p, currentRaidDailyAvailabilityMsgID, body, 0); err != nil {
		return err
	}
	s.logGameEvent(p, "团本每日入场次数已同步", "source", source, "body_len", len(body))
	return nil
}

package dnfbridge

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/packageitem"
)

const (
	currentSeriaLuckGaugeMsgID uint16 = 412
	currentSeriaLuckEventID    uint16 = 2037
)

// 原生412读取两个字节，分别为更新前后进度；每个幸运阶段对应5个显示单位。
func buildSeriaLuckGaugeBody(before, after int64) ([]byte, error) {
	if before < 0 || before > packageitem.SeriaLuckValueMax || after < 0 || after > packageitem.SeriaLuckValueMax {
		return nil, fmt.Errorf("赛丽亚幸运值超出范围：%d -> %d", before, after)
	}
	return []byte{byte(before * 5), byte(after * 5)}, nil
}

func (s *Service) sendCurrentSeriaLuckGauge(session *gameSession, before, after int64, source string) error {
	body, err := buildSeriaLuckGaugeBody(before, after)
	if err != nil {
		return err
	}
	if err := s.sendGameUpperRawClass(session, currentSeriaLuckGaugeMsgID, body, 0); err != nil {
		return err
	}
	s.logGameEvent(session, "赛丽亚幸运值显示已同步", "source", source, "before", before, "after", after)
	return nil
}

// 在场景尾包完成后恢复账号进度。新账号缺少记录时沿用领域层的零值；
// 无法读取或记录损坏时只跳过显示，不写账号、不阻断角色进入。
func (s *Service) sendInitialSeriaLuckGauge(session *gameSession, source string) error {
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return nil
	}
	repos, ok := s.repositoryGroup()
	accountID := strings.TrimSpace(s.accountIDForSession(session))
	if !ok || repos.Account == nil || accountID == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentSpendTimeWriteTimeout)
	defer cancel()
	account, found, err := repos.Account.Load(ctx, accountID)
	if err != nil {
		s.logGameEvent(session, "赛丽亚幸运值登录同步暂缓", "error", err)
		return nil
	}
	if !found {
		return nil
	}
	stage := int64(0)
	if raw := strings.TrimSpace(account.Metadata[packageitem.SeriaLuckMetadataKey]); raw != "" {
		stage, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || stage < 0 || stage > packageitem.SeriaLuckValueMax {
			s.logGameEvent(session, "赛丽亚幸运值登录同步跳过异常记录")
			return nil
		}
	}
	return s.sendCurrentSeriaLuckGauge(session, stage, stage, source)
}

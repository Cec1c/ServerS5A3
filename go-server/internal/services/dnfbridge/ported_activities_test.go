package dnfbridge

import (
	"context"
	"testing"

	"longheng.io/server/internal/modules/dnf/onlineevent"
)

// 参考协议夹具仅验证服务端处理；不代表现用EXE已兼容。
func enablePortedActivityTest(t *testing.T, s *Service, session *gameSession) {
	t.Helper()
	s.options.currentClientVersion = "official-24.9"
	configurePortedActivityTestControls(t, s)
	if err := s.initializePortedActivityPolicy(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.portedActivityNativeReady = map[uint16]bool{1370: true, 1054: true, 222: true, 2348: true}
	if session != nil {
		session.loginPacketSeen = true
		session.accountFromToken = true
		if session.selectedCharacterID != 0 {
			if session.characterGeneration == 0 {
				session.characterGeneration = 1
			}
			if s.gameSessions == nil {
				s.gameSessions = map[uint16]*gameSession{}
			}
			s.gameSessions[session.selectedCharacterID] = session
		}
	}
}

// 仅在内存测试仓储显式配置待适配活动，不改变生产默认或客户端兼容门禁。
func configurePortedActivityTestControls(t *testing.T, s *Service) {
	t.Helper()
	ctx := context.Background()
	if err := s.initializeActivityControls(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.activityControls.Save(ctx, s.activityControls.Snapshot().Revision, []onlineevent.ControlChange{
		{ID: 1370, Enabled: true}, {ID: 1054, Enabled: true}, {ID: 222, Enabled: true}, {ID: 2348, Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.initializeActivityControls(ctx); err != nil {
		t.Fatal(err)
	}
}

package dnfbridge

import (
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
)

// 场景频道与 owner 频道必须一致：会话真实所在频道（非常驻）优先，
// 否则客户端会卡在 mode0-owner-compare 循环（现场 trace 无限刷）。
func TestCommittedChannelOwnerPrefersSessionChannel(t *testing.T) {
	service := &Service{}
	_ = service
	session := &gameSession{
		channel:         channelcatalog.Channel{ID: 252, Type: 40, Name: "ch.252", Port: 10252},
		residentChannel: channelcatalog.Channel{ID: 10, Type: 11, Name: "ch.10", Port: 10010},
	}
	owner, err := currentCommittedChannelOwner(session)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if owner != 252 {
		t.Fatalf("owner channel = %d, want 252 (must match the real scene channel)", owner)
	}

	// 常驻频道会话仍走旧语义（回落 catalog 落点）。
	guide := &gameSession{
		channel:         channelcatalog.Channel{ID: 10, Type: 11, Name: "ch.10", Port: 10010},
		residentChannel: channelcatalog.Channel{ID: 10, Type: 11, Name: "ch.10", Port: 10010},
	}
	owner2, err := currentCommittedChannelOwner(guide)
	if err != nil || owner2 != 10 {
		t.Fatalf("resident session owner = %d err=%v, want 10", owner2, err)
	}
}

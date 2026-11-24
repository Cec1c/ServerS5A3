package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	dnfonlineevent "longheng.io/server/internal/modules/dnf/onlineevent"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

// 回归背景（2026-11-21 90CN msp / dnf:10001，22:30 挂机到次日 07:16）：
//
//	07:16:4x     RECV type=632 ENUM_CMDPACKET_GET_PCROOM_TIME_POINT_ITEM（点开在线奖励面板）
//	07:16:46.767 WARN  dnfbridge handle game packet failed
//	                   error="online-event observation is older than persisted progress"
//	07:16:47.7   网关 Game 连接已关闭（上行 broken pipe / 下行 eof）
//	             ⇒ 客户端弹「网络中断」。网关泡点、反外挂上报、TCP 存活 8h46m 全绿，
//	               网络整夜是好的，是服务端自己把会话关了。
//
// 成因是 handleCurrentPCRoomTimePoint 把面板结算错误当成致命错误 return 给
// read loop。下面的用例把"面板结算失败只降级、不上抛"钉住：先把 228 面板账本
// 的日期写成远比服务日更靠后的值（任何结算都必然命中 ErrObservationStale），
// 再确认处理函数仍旧 handled=true / err=nil。
func TestPCRoom632StaleSettlementDegradesWithoutClosingSession(t *testing.T) {
	ctx := context.Background()
	repositories := memory.NewMemoryGroup()
	const accountID = "pcroom-stale-degrade"
	const characterID = "7"

	if err := repositories.Account.Save(ctx, dnfrepo.AccountRecord{
		AccountID: accountID,
		State:     "active",
		Metadata: map[string]string{
			dnfonlineevent.PCRoomTimePointMetadataKey: `{"version":1,"date":"2999-01-01","daily_mask":0,"day_complete":false,"total":0,"period_mask":0}`,
		},
	}); err != nil {
		t.Fatalf("save account: %v", err)
	}
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: characterID,
		AccountID:   accountID,
		Slot:        7,
		Name:        "pcroom-stale",
		Job:         "0",
		Level:       90,
	}); err != nil {
		t.Fatalf("save character: %v", err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: characterID,
		Slots:       map[string]dnfrepo.ItemStack{},
	}); err != nil {
		t.Fatalf("save inventory: %v", err)
	}

	queue := newFakeCurrentDungeonDeathTimerQueue()
	queue.now = time.Date(2026, 11, 21, 7, 16, 46, 0, dnfonlineevent.ChinaCalendar)
	service := &Service{
		options:                 options{currentClientVersion: "official-24.9"},
		gameplayTimers:          queue,
		initialEquipmentArchive: nil,
		repositoryProvider: func() (dnfrepo.Group, bool) {
			return repositories, true
		},
	}

	session := &gameSession{
		conn:                &bufferConn{},
		connID:              "pcroom-stale-degrade",
		channel:             channelcatalog.Channel{ID: 10},
		accountID:           accountID,
		selectedCharacterID: 7,
		loginPacketSeen:     true,
		accountFromToken:    true,
	}
	service.bindGameSessionCharacter(session, 7)
	catalog := &currentSpendTimeRuntimeCatalog{
		definition: dnfonlineevent.Definition{
			ID:     "228",
			Stages: []dnfonlineevent.Stage{{ID: "s1", RequiredSeconds: 3600, Gold: 10}},
			// 边界显式写 06:00，与线上一致，避免默认值改动影响本用例语义。
			Boundary: &dnfonlineevent.DailyBoundary{Hour: 6},
		},
		nativePCRoom: &dnfonlineevent.PCRoomTimePointCatalog{
			Seconds: [4]uint32{3600, 7200, 10800, 14400},
			Counts:  [4]uint32{1, 2, 3, 4},
		},
	}
	session.spendTime.catalog = catalog
	session.spendTime.characterID = 7
	session.spendTime.characterGeneration = session.characterGeneration
	session.spendTime.accountID = accountID
	// anchor 故意留零：时钟结算直接 no-op，让唯一可能失败的只剩 228 面板结算，
	// 这与线上"错误文案没有 persisted/observed 后缀"的现场同型。

	handled, err := service.handleCurrentPCRoomTimePoint(session, currentPCRoomTimePointRequestID, 1, []byte{0, 255})
	if !handled {
		t.Fatal("op632 should be handled")
	}
	if err != nil {
		t.Fatalf("panel settlement error must not reach the read loop (it closes the session): %v", err)
	}
}

// 上面的用例只保证"不上抛"。这里再单独确认：同一次失败下 228 结算确实返回了
// ErrObservationStale —— 否则前一个用例可能只是因为根本没走进失败路径而通过。
func TestPCRoomStaleSettlementStillReportsStaleToCallers(t *testing.T) {
	ctx := context.Background()
	repositories := memory.NewMemoryGroup()
	const accountID = "pcroom-stale-signal"
	const characterID = "9"
	if err := repositories.Account.Save(ctx, dnfrepo.AccountRecord{
		AccountID: accountID,
		State:     "active",
		Metadata: map[string]string{
			dnfonlineevent.PCRoomTimePointMetadataKey: `{"version":1,"date":"2999-01-01","daily_mask":0,"day_complete":false,"total":0,"period_mask":0}`,
		},
	}); err != nil {
		t.Fatalf("save account: %v", err)
	}
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: characterID, AccountID: accountID, Slot: 9, Name: "pcroom-stale-signal", Job: "0", Level: 90,
	}); err != nil {
		t.Fatalf("save character: %v", err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: characterID, Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
		t.Fatalf("save inventory: %v", err)
	}
	catalog := &currentSpendTimeRuntimeCatalog{
		definition: dnfonlineevent.Definition{
			ID:       "228",
			Stages:   []dnfonlineevent.Stage{{ID: "s1", RequiredSeconds: 3600, Gold: 10}},
			Boundary: &dnfonlineevent.DailyBoundary{Hour: 6},
		},
		nativePCRoom: &dnfonlineevent.PCRoomTimePointCatalog{
			Seconds: [4]uint32{3600, 7200, 10800, 14400},
			Counts:  [4]uint32{1, 2, 3, 4},
		},
	}
	now := time.Date(2026, 11, 21, 7, 16, 46, 0, dnfonlineevent.ChinaCalendar)
	_, err := dnfonlineevent.SettlePCRoomTimePoint(ctx, repositories, catalog.nativePCRoom, dnfonlineevent.PCRoomTimePointCommand{
		AccountID:       accountID,
		CharacterID:     characterID,
		Now:             now,
		ClockDefinition: catalog.definition,
		DailyClaim:      0,
		PeriodClaim:     -1,
		Deliver: func(dnfrepo.MailboxRepository, dnfonlineevent.AttendancePVFItemReward, string) (string, error) {
			return "", fmt.Errorf("unexpected delivery")
		},
	})
	if err == nil {
		t.Fatal("expected ErrObservationStale for a future-dated 228 ledger")
	}
	if !errors.Is(err, dnfonlineevent.ErrObservationStale) {
		t.Fatalf("want ErrObservationStale, got %v", err)
	}
	// 错误必须自己带上两侧日期，否则线上仍然只能看到一句哨兵文案。
	if text := err.Error(); !strings.Contains(text, "state_date=2999-01-01") || !strings.Contains(text, "service_day=2026-11-21") {
		t.Fatalf("stale error lost its diagnostic numbers: %s", text)
	}
}

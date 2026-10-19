package dnfbridge

import (
	"context"
	"fmt"

	"longheng.io/server/internal/modules/dnf/onlineevent"
)

func (s *Service) initializeActivityControls(ctx context.Context) error {
	repos, available := s.repositoryGroup()
	if !available || repos.Settings == nil {
		return nil
	}
	controls, err := onlineevent.NewControls(ctx, repos.Settings, []onlineevent.ControlDefinition{
		{ID: 216, Name: "新学期累计奖励（需兼容客户端）", Description: "默认关闭；需独立审核客户端654/689许可后启用。角色通关6062按中国06时活动日累计，8、16、24天领取邮件；每日表为空，不发日奖。", Source: "实际新学期216及event53模型 / 累计689、状态654", DefaultEnabled: false},
		{ID: 350, Name: "史诗之路：超越征程", Description: "开放当前湍流NPC及任务；祥瑞溪谷仍要求等级17和完成5579，三个高级地下城等级85且按实际PVF扣邀请函。", Source: "实际npc3135/event350及2018_epciroad2资源", DefaultEnabled: true},
		{ID: 2347, Name: "累计在线得好礼", Description: "现用PVF使用原生展开面板，显示每日在线、累计天数及奖励；点击按钮后通过邮件领取，每日06:00重置。保留旧配置键。", Source: "当前PVF PCRoomTimePoint / 原生事件228、状态561；旧版PVF保留2347", DefaultEnabled: true},
		{ID: 331, Name: "普通签到", Description: "等级17起，合法选角在线按每日06:00活动日计一次，账号共享最多28日，不补漏日；已登记未领邮件可重试。", Source: "实际AttendanceDailyEvent.evt / 原生331、1386、688", DefaultEnabled: true},
		{ID: 2208, Name: "每周签到得好礼", Description: "首次启用绑定当周周四06:00，周期70天；通关4次推荐地下城签到，20次补签，最多2次。", Source: "实际十周chn_totalattendance.evt / 原生2208", DefaultEnabled: true},
		{ID: 2370, Name: "每日签到得好礼", Description: "每日通关2次推荐地下城完成签到，账号共享进度，累计7、14、21天可领取额外奖励；每日06:00重置。", Source: "当前PVF chn_DailyAttendanceAnytimeEvent.evt / 原生事件2370", DefaultEnabled: true},
		{ID: 2365, Name: "竞技大赛（马战竞猜）", Description: "开启原生竞猜界面、赛事状态及下注流程。", Source: "当前PVF马战活动表 / 原生事件2365", DefaultEnabled: s.joustEventEnabled()},
		{ID: 2395, Name: "红尾乔纳森", Description: "开启中央公园地铁中心NPC红尾乔纳森的可见性。", Source: "npc/476_jonathan.npc [open visible event]2395", DefaultEnabled: s.currentSpendTimeCatalogIncludesTownNpcEvent()},
	})
	if err != nil {
		return fmt.Errorf("initialize activity controls: %w", err)
	}
	s.activityControls = controls
	return nil
}

func (s *Service) currentSpendTimeEventEnabled() bool {
	return s != nil && (s.activityControls == nil || s.activityControls.Enabled(uint16(currentSpendTimeEventID)))
}

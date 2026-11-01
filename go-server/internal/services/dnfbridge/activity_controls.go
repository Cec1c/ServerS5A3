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
	definitions := []onlineevent.ControlDefinition{
		{ID: 2520, Name: "战令", Description: "关闭时拒绝查询、领取和购买；保留已有权益与进度。", Source: "2520 / 0x7f11 / PVF", DefaultEnabled: true},
		{ID: 288, Name: "升级大挑战（国服中文）", Description: "新建普通角色按私有PVF设置自动参加；全职业、多角色，达级邮件发放。与旧预约222/116互斥，需客户端815/816接收许可。", Source: "event/createeventcharacter.evt、event/eventgift.evt / 创建5、状态815/816", DefaultEnabled: false},
		{ID: 1370, Name: "赛丽亚的热忱（新手/回归）", Description: "默认关闭。新账号首次进场景起30天、仅一次，老账号不补发；回归仍按离线七天和30天权益。两组任务、七天签到及邮件奖励读取PVF，沿用1369、304、1429、1430原生协议。", Source: "etc/seriawelcome.etc group0/1 / 状态1369、引导1429、领取1430", DefaultEnabled: false},
		{ID: 1054, Name: "幸运角色", Description: "兼容验证完成前默认关闭。长期向全职业开放；怪物、通关、任务和组队经验按PVF比例生效，特殊频道及决斗场除外。", Source: "etc/chn_activatecharacter.etc / 状态1054", DefaultEnabled: false},
		{ID: 222, Name: "Lv预约升级", Description: "兼容验证完成前默认关闭。长期开放，保留PVF职业、目标及奖励限制；新建角色后预约，邮件发奖。", Source: "event/levelaimingevent.evt / 状态623、请求689事件222", DefaultEnabled: false},
		{ID: 116, Name: "升级大挑战（旧预约模式）", Description: "旧版预约模式，与Lv预约222不能同时开启；需配套客户端验证。", Source: "event/decidelevelupevent.evt / 496、497、567、1122", DefaultEnabled: false},
		{ID: 2348, Name: "劳动节登录和在线送礼", Description: "默认关闭登录和在线两类奖励；开启后账号前三个登录日发PVF登录奖励，每日在线10分钟一档，最多10档，06点重置。", Source: "event/chn_event/chn_playdnfatlaborday.evt / 活动2348", DefaultEnabled: false},
		{ID: 145, Name: "大富翁骰子（幸运热舞派对）", Description: "角色基础2次，推荐地下城4/8/12次各加2次；28格及圈数奖励读取现用PVF，奖励邮件发放，06点重置每日次数。", Source: "event/attendancemarble.evt / 原生活动145、查询629、掷骰630", DefaultEnabled: true},
		{ID: 216, Name: "新学期累计奖励（需兼容客户端）", Description: "默认关闭；需独立审核客户端654/689许可后启用。角色通关6062按中国06时活动日累计，8、16、24天领取邮件；每日表为空，不发日奖。", Source: "实际新学期216及event53模型 / 累计689、状态654", DefaultEnabled: false},
		{ID: 350, Name: "史诗之路：超越征程", Description: "开放当前湍流NPC及任务；祥瑞溪谷仍要求等级17和完成5579，三个高级地下城等级85且按实际PVF扣邀请函。", Source: "实际npc3135/event350及2018_epciroad2资源", DefaultEnabled: true},
		{ID: 2347, Name: "累计在线得好礼", Description: "现用PVF使用原生展开面板，显示每日在线、累计天数及奖励；点击按钮后通过邮件领取，每日06:00重置。保留旧配置键。", Source: "当前PVF PCRoomTimePoint / 原生事件228、状态561；旧版PVF保留2347", DefaultEnabled: true},
		{ID: 331, Name: "普通签到", Description: "等级17起，合法选角在线按每日06:00活动日计一次，账号共享最多28日，不补漏日；已登记未领邮件可重试。", Source: "实际AttendanceDailyEvent.evt / 原生331、1386、688", DefaultEnabled: true},
		{ID: 2208, Name: "每周签到得好礼", Description: "首次启用绑定当周周四06:00，周期70天；通关4次推荐地下城签到，20次补签，最多2次。", Source: "实际十周chn_totalattendance.evt / 原生2208", DefaultEnabled: true},
		{ID: 2370, Name: "每日签到得好礼", Description: "每日通关2次推荐地下城完成签到，账号共享进度，累计7、14、21天可领取额外奖励；每日06:00重置。", Source: "当前PVF chn_DailyAttendanceAnytimeEvent.evt / 原生事件2370", DefaultEnabled: true},
		{ID: 2365, Name: "竞技大赛（马战竞猜）", Description: "开启原生竞猜界面、赛事状态及下注流程。", Source: "当前PVF马战活动表 / 原生事件2365", DefaultEnabled: s.joustEventEnabled()},
		{ID: 2395, Name: "红尾乔纳森", Description: "开启中央公园地铁中心NPC红尾乔纳森的可见性。", Source: "npc/476_jonathan.npc [open visible event]2395", DefaultEnabled: s.currentSpendTimeCatalogIncludesTownNpcEvent()},
	}
	definitions = append(definitions, currentImageTownActivityDefinitions()...)
	controls, err := onlineevent.NewControls(ctx, repos.Settings, definitions)
	if err != nil {
		return fmt.Errorf("initialize activity controls: %w", err)
	}
	s.activityControls = controls
	return nil
}

func (s *Service) currentSpendTimeEventEnabled() bool {
	return s != nil && (s.activityControls == nil || s.activityControls.Enabled(uint16(currentSpendTimeEventID)))
}

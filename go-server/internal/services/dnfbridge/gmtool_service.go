package dnfbridge

import (
	"context"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/services/gmtool"
)

// startGMTool 按配置启动 90CN Web GM 面板；未配置监听地址时直接跳过。
// 面板复用本进程的仓储与运行时 PVF，因此不需要第二份数据库连接池之外的资源，
// 也不会改动正在运行的组队中转/频道逻辑。
func (s *Service) startGMTool(ctx context.Context) error {
	listen := strings.TrimSpace(s.options.gmListen)
	if listen == "" {
		return nil
	}
	dsn := strings.TrimSpace(s.options.gmDSN)
	if dsn == "" {
		dsn = strings.TrimSpace(s.options.accountAPIDSN)
	}
	table := strings.TrimSpace(s.options.gmTable)
	if table == "" {
		table = strings.TrimSpace(s.options.accountAPITable)
	}
	if table == "" {
		table = defaultAccountAPITable
	}
	pvfPath := strings.TrimSpace(s.options.pvfPath)
	if pvfPath == "" {
		pvfPath = defaultPVFPath
	}

	server, err := gmtool.New(gmtool.Config{
		Listen:             listen,
		Password:           strings.TrimSpace(s.options.gmPassword),
		Table:              table,
		DSN:                dsn,
		WWWRoot:            strings.TrimSpace(s.options.gmWWWRoot),
		ItemIndexCachePath: strings.TrimSpace(s.options.gmItemIndexPath),
		PVFPath:            pvfPath,
		DatabaseLabel:      mysqlDatabaseLabel(dsn),
		Logger:             s.logInfo,
		// 默认 false = 「开启特殊装备槽」只写位（与 A21/上游 0.1.8/本仓既有的
		// grantlevel90loadout 同口径）；置 on 才顺带把授权任务标 completed。
		EquipSlotQuestSync: s.options.gmEquipSlotQuestSync,
		// P2 的 J0 写路由在线守卫：默认（未设/空/非 `on`）= false = 不检查在线 =
		// 逐字节今天的行为。开关自读环境见 gmtool/online_guard.go 的说明。
		RequireOffline: gmtool.RequireOfflineFromEnvironment(),
		// P4 的 J6「直接写背包」路由：默认（未设/空/非 `on`）= false =
		// `items/delete` 与 `items/clear` **根本不注册**，继续落 /api/ 兜底。
		// 开启等于操作者显式推翻本仓「不直写背包」的立场（api_items.go:327-329，
		// 计划书 §5.2 红线 C3），所以这个开关必须由人显式翻开。
		// 开关自读环境见 gmtool/api_items_delete.go 的 RequireItemRemoveFromEnvironment。
		RemoveInventory: gmtool.RequireItemRemoveFromEnvironment(),
	}, gmtool.Deps{
		Activities: s.activityControls,
		Repos:      func() (dnfrepo.Group, bool) { return s.repositoryGroup() },
		PVF: func() (dnfpvf.Source, error) {
			s.initialEquipmentMu.Lock()
			defer s.initialEquipmentMu.Unlock()
			return s.initialEquipmentArchiveLocked()
		},
		PremiumTypes: s.currentGMAdministrativePremiumTypes,
		// J0 在线探针：把 GM 面板的写路由守卫接到本进程真实的对局会话表上。
		// 判据与上游 0.1.8 gm.go:450/524 完全同源（Service.onlineGameSession，
		// game_sessions.go:373）；gmtool 包不持有会话表，只拿这一个函数。
		Online: func(characterID uint16) bool {
			_, online := s.onlineGameSession(characterID)
			return online
		},
		// P3 的 J7：账号晶块 6 格的物品 id 直接取本进程唯一的商品表解析结果
		// （premium_pvf.go:126-153 parseCurrentCrystalContractCubes，来源
		// stackable/stackable.lst 的 material/cubepiece_*.stk）。gmtool 不重写这段解析。
		CrystalCubeItemIDs: func() ([6]int64, error) {
			catalog, err := s.currentPremiumCatalog()
			if err != nil {
				return [6]int64{}, err
			}
			return catalog.crystalCubeIDs, nil
		},
		// P3 的 J7：晶块写回时的 0x77 行重编码，复用本进程唯一的行编码器
		// （current_item_list_stack_projection.go:27 currentItemListEntryFromStack），
		// 与上游 gm_extended.go:494-495 的语义一致，不新开第二套编码。
		EncodeStackRow: func(listType byte, slot int16, stack dnfrepo.ItemStack) []byte {
			entry := currentItemListEntryFromStack(listType, slot, stack)
			return append([]byte(nil), entry.data[:]...)
		},
	})
	if err != nil {
		return err
	}
	if server == nil {
		return nil
	}
	if err := server.Start(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	s.gmTool = server
	s.mu.Unlock()
	return nil
}

func (s *Service) stopGMTool() error {
	s.mu.Lock()
	server := s.gmTool
	s.gmTool = nil
	s.mu.Unlock()
	if server == nil {
		return nil
	}
	return server.Stop()
}

// mysqlDatabaseLabel 从 DSN 里取出库名，仅用于状态栏展示。
func mysqlDatabaseLabel(dsn string) string {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return ""
	}
	slash := strings.LastIndex(dsn, "/")
	if slash < 0 || slash+1 >= len(dsn) {
		return dsn
	}
	value := dsn[slash+1:]
	if query := strings.Index(value, "?"); query >= 0 {
		value = value[:query]
	}
	if value == "" {
		return dsn
	}
	return "MariaDB " + value
}

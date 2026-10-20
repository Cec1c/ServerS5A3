package dnfbridge

import (
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

// 本文件是「收集箱」的运行时编排：目录缓存、初始化下发（class0/op1027 缓存 +
// class1/op1021 选中应答 + 选中箱刷新）、客户端开窗/选箱请求处理。
//
// 现场根因（证据见 changlog / 报告）：`gameplay_module_collectbox.go` 原来把
// op1021~1024 一律当"未实现"丢掉，香港日志里 3 次
// `game-collectbox-deferred opcode=1021 reason=collectbox_not_implemented`
// 就是操作者开窗那几次——服务端一个字节都没回，客户端于是把
// `ui/collectbox/collectbox.xui` 里三个文本控件的默认值
// `STR:test string1/2/3`（第三个红字）原样显示出来。
//
// 下发顺序逐字节对齐厂商 9.16 参考环境（同一时刻的 6 条包）：
//
//	class0/op1027 箱1..箱N（[1]=0 缓存行）→ class1/op1021 body=`01 <选中箱>`
//	→ class0/op1027 选中箱（[1]=1）。
const (
	// collectboxModeEnv 是「收集箱」总开关。默认 reference = 按参考实现下发；
	// 置 off/0/false/no 一键回退到"只记 game-collectbox-deferred、不发包"的旧行为。
	collectboxModeEnv       = "DNFBRIDGE_COLLECTBOX_MODE"
	collectboxModeReference = "reference"
	collectboxModeOff       = "off"

	// collectboxInitEnv 决定整组缓存的下发时机：默认 select_ack = 与厂商一致
	// （选角 ack 之后、op5 权限快照之前）；request_only = 只在客户端开窗请求时补发。
	collectboxInitEnv         = "DNFBRIDGE_COLLECTBOX_INIT"
	collectboxInitSelectAck   = "select_ack"
	collectboxInitRequestOnly = "request_only"

	// collectboxWireContract 是日志里的形状自述，与厂商参考实现的措辞一致。
	collectboxWireContract = "class0_op1027_cache_then_class1_op1021_selection_ack_then_selected_refresh"
)

// collectboxEnabled 报告本次进程是否启用收集箱下发。
//
// 极性刻意与 joustEventDisabled / storyDigestDisabled 相反：这里 options 的**零值**
// （空串，测试与探针里的 Service 字面量）等价于 off = 加这个功能之前的现行为，
// 只有经过 optionsFromConfig 的构造路径才会拿到 reference。
func (s *Service) collectboxEnabled() bool {
	return s != nil && collectboxModeValue(s.options.collectboxMode) == collectboxModeReference
}

func collectboxModeValue(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case collectboxModeReference, "on", "1", "true", "yes":
		return collectboxModeReference
	default:
		return collectboxModeOff
	}
}

func collectboxInitValue(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case collectboxInitRequestOnly:
		return collectboxInitRequestOnly
	default:
		return collectboxInitSelectAck
	}
}

// currentCollectboxCatalog 返回缓存的收集箱目录（真源 = 运行期 Script.pvf）。
// 读取失败只记一次事件并返回 false，绝不让会话因此中断（与其它目录读取一致）。
func (s *Service) currentCollectboxCatalog() (currentCollectboxCatalog, bool) {
	s.collectboxCatalogMu.Lock()
	defer s.collectboxCatalogMu.Unlock()
	if s.collectboxCatalogLoaded {
		if s.collectboxCatalogLoadErr != nil {
			return currentCollectboxCatalog{}, false
		}
		return s.collectboxCatalog, true
	}
	s.collectboxCatalogLoaded = true
	s.initialEquipmentMu.Lock()
	archive, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err != nil {
		s.collectboxCatalogLoadErr = err
		s.logPacketEvent("dnf-collectbox-catalog-load-failed", "reason", "pvf_archive_unavailable", "error", err)
		return currentCollectboxCatalog{}, false
	}
	catalog, err := loadCurrentCollectboxCatalog(archive)
	if err != nil {
		s.collectboxCatalogLoadErr = err
		s.logPacketEvent("dnf-collectbox-catalog-load-failed", "path", currentCollectboxInfoPVFPath, "error", err)
		return currentCollectboxCatalog{}, false
	}
	s.collectboxCatalog = catalog
	titles := make([]string, 0, len(catalog.Boxes))
	for _, box := range catalog.Boxes {
		titles = append(titles, currentCollectboxBoxTitle(box))
	}
	s.logPacketEvent("dnf-collectbox-catalog-loaded",
		"path", catalog.Path,
		"boxes", len(catalog.Boxes),
		"indexes", joinCurrentCollectboxIndexes(catalog.Indexes()),
		"titles", strings.Join(titles, "|"))
	return catalog, true
}

func joinCurrentCollectboxIndexes(indexes []int32) string {
	parts := make([]string, 0, len(indexes))
	for _, index := range indexes {
		parts = append(parts, strconv.Itoa(int(index)))
	}
	return strings.Join(parts, ",")
}

// currentCollectboxDefaultIndex 选默认箱：**最后一个未过期的箱**（厂商参考环境里
// 只有箱 5「兽人守护神」未过期，实测 selected_box_index=5，与本口径一致）；
// 全过期时取最后一个箱，保证下发始终自洽。
func currentCollectboxDefaultIndex(catalog currentCollectboxCatalog, now time.Time) int32 {
	if len(catalog.Boxes) == 0 {
		return 0
	}
	for idx := len(catalog.Boxes) - 1; idx >= 0; idx-- {
		box := catalog.Boxes[idx]
		if !box.HasBaseExpiration || box.BaseExpiration.After(now) {
			return box.Index
		}
	}
	return catalog.Boxes[len(catalog.Boxes)-1].Index
}

// currentCollectboxSelectable 报告某个箱号能否被"客户端请求"选中。
//
// 口径（见 collectbox_wire.go 的证据说明）：请求体末字节 = 箱号；但过期的箱子
// 客户端连按钮都不显示（厂商参考环境里箱 1..4 的 [base expiration date] 全在过去），
// 香港现场那三条请求 body=`01` 恰好指向已过期的 SAO 箱（箱 1），因此把
// "箱号在目录里 **且** 未过期"作为采纳条件，其余一律当"开窗查询"处理，
// 保持服务端当前选中箱不变（= 厂商默认的箱 5）。
func (s *Service) currentCollectboxSelectable(catalog currentCollectboxCatalog, index int32, now time.Time) bool {
	box, ok := catalog.Box(index)
	if !ok {
		return false
	}
	if !box.HasBaseExpiration {
		return true
	}
	return box.BaseExpiration.After(now)
}

// currentCollectboxSelection 返回本会话当前选中箱（必要时按默认口径落定）。
func (s *Service) currentCollectboxSelection(session *gameSession, catalog currentCollectboxCatalog, now time.Time) int32 {
	if session == nil {
		return 0
	}
	if _, ok := catalog.Box(session.collectboxSelectedIndex); ok {
		return session.collectboxSelectedIndex
	}
	session.collectboxSelectedIndex = currentCollectboxDefaultIndex(catalog, now)
	return session.collectboxSelectedIndex
}

// sendCurrentCollectboxInitialization 下发整组收集箱缓存（厂商 9.16 形状与顺序）。
func (s *Service) sendCurrentCollectboxInitialization(session *gameSession, source string, replay bool) error {
	if !s.collectboxEnabled() || session == nil {
		return nil
	}
	catalog, ok := s.currentCollectboxCatalog()
	if !ok || len(catalog.Boxes) == 0 {
		s.logGameEvent(session, "game-collectbox-initialization-skipped",
			"source", source,
			"reason", "collectbox_catalog_unavailable")
		return nil
	}
	now := time.Now()
	selected := s.currentCollectboxSelection(session, catalog, now)
	// 登记条目只读一次库：5 个箱子的 [7]=item 数都从同一份 InventoryRecord 切。
	registered := s.currentCollectboxRegisteredItemsForSession(session)
	// 缓存行的选中标志**一律为 0**：厂商实测那 5 条缓存全是 `0N00…`，只有最后那条
	// 选中箱刷新是 `0501…`（同一时刻、逐字节夹具见 collectbox_wire_test.go）。
	// 把选中标志也塞进缓存行会让客户端多认一次选中态，与参考实现不一致。
	for _, box := range catalog.Boxes {
		if err := s.sendCurrentCollectboxStateWithItems(session, box, false, source, registered[box.Index]); err != nil {
			return err
		}
	}
	if err := s.sendCurrentCollectboxSelectAck(session, selected, source); err != nil {
		return err
	}
	selectedBox, ok := catalog.Box(selected)
	if ok {
		if err := s.sendCurrentCollectboxStateWithItems(session, selectedBox, true, source, registered[selected]); err != nil {
			return err
		}
	}
	session.collectboxInitialized = true
	s.logGameEvent(session, "game-collectbox-initialization-sent",
		"source", source,
		"char_id", session.selectedCharacterID,
		"box_count", len(catalog.Boxes),
		"selected_box_index", selected,
		"selected_box_title", currentCollectboxBoxTitle(selectedBox),
		"scene_ready_replay", replay,
		"wire_contract", collectboxWireContract)
	return nil
}

// sendCurrentCollectboxState 发一箱的 class0/op1027 状态（自行读登记条目）。
func (s *Service) sendCurrentCollectboxState(session *gameSession, box currentCollectboxBox, selected bool, source string) error {
	return s.sendCurrentCollectboxStateWithItems(session, box, selected, source, s.currentCollectboxRegisteredItemIDs(session, box))
}

// sendCurrentCollectboxStateWithItems 发一箱的 class0/op1027 状态，登记条目由调用方
// 给出（初始化时 5 个箱子共用一次读库；登记成功后直接用刚提交的结果，避免读到旧值）。
func (s *Service) sendCurrentCollectboxStateWithItems(session *gameSession, box currentCollectboxBox, selected bool, source string, itemIDs []int32) error {
	if session == nil {
		return nil
	}
	body := buildCurrentCollectboxStateBody(box, selected, itemIDs, time.Now())
	s.logGameEvent(session, "game-collectbox-state-send",
		"source", source,
		"char_id", session.selectedCharacterID,
		"box_index", box.Index,
		"box_title", currentCollectboxBoxTitle(box),
		"selected", selected,
		"item_count", len(currentCollectboxFilteredItemIDs(box, itemIDs)),
		"pvf_slot_count", box.SlotCount(),
		"body_len", len(body),
		"body_hex", hex.EncodeToString(body))
	return s.sendGameUpperRawClass(session, currentCollectboxStateMsgID, body, 0)
}

// sendCurrentCollectboxSelectAck 发 class1/op1021 选中应答（`01 <箱号>`）。
func (s *Service) sendCurrentCollectboxSelectAck(session *gameSession, selected int32, source string) error {
	if session == nil {
		return nil
	}
	body := buildCurrentCollectboxSelectAckBody(selected)
	s.logGameEvent(session, "game-collectbox-select-ack-send",
		"source", source,
		"char_id", session.selectedCharacterID,
		"selected_box_index", selected,
		"body_hex", hex.EncodeToString(body))
	return s.sendGameUpperRawClass(session, currentCollectboxSelectMsgID, body, 1)
}

// handleCurrentCollectboxSelect 处理客户端开窗/选箱请求（class1/op1021）。
//
// 行为：采纳可选的箱号（见 currentCollectboxSelectable），应答 `01 <箱号>`，
// 再刷新该箱状态；如果整组缓存还没发过（例如客户端在选角 ack 之前就开窗），
// 顺手把整组缓存补齐，保证客户端拿到全部箱子的状态。
func (s *Service) handleCurrentCollectboxSelect(session *gameSession, body []byte) error {
	if !s.collectboxEnabled() {
		return s.logCurrentCollectboxDeferred(session, currentCollectboxSelectMsgID, body)
	}
	catalog, ok := s.currentCollectboxCatalog()
	if !ok || len(catalog.Boxes) == 0 {
		s.logGameEvent(session, "game-collectbox-select-blocked",
			"opcode", currentCollectboxSelectMsgID,
			"body_len", len(body),
			"body_hex", hex.EncodeToString(body),
			"reason", "collectbox_catalog_unavailable")
		return nil
	}
	now := time.Now()
	requested, hasRequested := parseCurrentCollectboxSelectRequest(body)
	selected := s.currentCollectboxSelection(session, catalog, now)
	if hasRequested && s.currentCollectboxSelectable(catalog, requested, now) {
		selected = requested
		session.collectboxSelectedIndex = requested
	}
	s.logGameEvent(session, "game-collectbox-select-request",
		"char_id", session.selectedCharacterID,
		"body_len", len(body),
		"body_hex", hex.EncodeToString(body),
		"requested_box_index", requested,
		"request_decoded", hasRequested,
		"adopted", selected == requested && hasRequested,
		"selected_box_index", selected,
		"requested_box_selectable", hasRequested && s.currentCollectboxSelectable(catalog, requested, now))
	if !session.collectboxInitialized {
		return s.sendCurrentCollectboxInitialization(session, "collectbox_open_request", true)
	}
	if err := s.sendCurrentCollectboxSelectAck(session, selected, "collectbox_open_request"); err != nil {
		return err
	}
	box, ok := catalog.Box(selected)
	if !ok {
		return nil
	}
	return s.sendCurrentCollectboxState(session, box, true, "collectbox_open_request")
}

// currentCollectboxRegisteredItemIDs 返回"该角色已登记在本箱里的条目 id"。
//
// 真源是 InventoryRecord 上的保留伪容器（见 internal/modules/dnf/collectbox）：
// 操作者每放进去一颗宝珠，op1022 处理器就在**同一次背包事务**里把条目登记到
// collectbox.ListType=101 上，所以重登后这里读到的就是同一份数据，箱子不会变空。
//
// 读库失败 / 未选角时返回 nil：箱子按"空箱"下发（与厂商参考环境一致），
// 绝不因为读不到就中断选角流程。
func (s *Service) currentCollectboxRegisteredItemIDs(session *gameSession, box currentCollectboxBox) []int32 {
	if s == nil || session == nil || session.selectedCharacterID == 0 || box.Index <= 0 {
		return nil
	}
	return s.currentCollectboxRegisteredItemsForSession(session)[box.Index]
}

// logCurrentCollectboxDeferred 是开关 off 时的旧行为：只记一条 deferred，不发包。
func (s *Service) logCurrentCollectboxDeferred(session *gameSession, opcode uint16, body []byte) error {
	s.logGameEvent(session, "game-collectbox-deferred",
		"opcode", opcode,
		"body_len", len(body),
		"reason", "collectbox_not_implemented")
	return nil
}

// logCurrentCollectboxObservedMutation 只观测登记/取出/延期请求：**不发明应答**。
//
// 这三个命令的请求体在 90CN 客户端上没有取证，按项目口径"协议字段必须有证据"
// 只记录完整 body hex（含首字节与长度），供下一轮定形；对客户端来说行为与
// 修复前一致（无响应），不会产生错误的物品搬运。
func (s *Service) logCurrentCollectboxObservedMutation(session *gameSession, request gameplayRequest) error {
	name := "unknown"
	switch request.Opcode {
	case currentCollectboxAddItemMsgID:
		name = "add"
	case currentCollectboxRemoveItemMsgID:
		name = "remove"
	case currentCollectboxExtendExpiryMsgID:
		name = "extend"
	}
	s.logGameEvent(session, "game-collectbox-mutation-observed",
		"opcode", request.Opcode,
		"command", name,
		"classification", request.Classification,
		"body_len", len(request.Body),
		"body_hex", hex.EncodeToString(request.Body),
		"reason", "current_client_collectbox_mutation_body_unverified")
	return nil
}

// currentCollectboxGameplayOpcodes 是被收集箱模块独占的 opcode 列表。
var currentCollectboxGameplayOpcodes = []uint16{
	currentCollectboxSelectMsgID,
	currentCollectboxAddItemMsgID,
	currentCollectboxRemoveItemMsgID,
	currentCollectboxExtendExpiryMsgID,
}

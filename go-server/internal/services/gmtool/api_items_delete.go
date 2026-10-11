package gmtool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// J6：背包单格删除 / 整容器清空（P4；上游 `gm_extended.go:507-657`
// GMDeleteInventoryItem / GMClearInventory + `gmRemoveStack` `:978-990`）。
//
// # 为什么这批默认关，而且是"整条路由不注册"
//
// 本仓**刻意拒绝直接写背包**：`api_items.go:327-329` 的
//
//	writeFailure(w, "90CN 面板只支持邮件发放：直写背包会与在线会话的存档互相覆盖。")
//
// 就是那条立场的代码化。开启本批等于操作者**有意识地推翻该立场**，因此：
//
//   - 开关 `DNFBRIDGE_GM_ITEM_REMOVE`（解析见 RequireItemRemoveFromEnv）**默认 off**：
//     未设置/空/`off`/任何未知值（含 `true`/`1`/`yes`）= 关闭 = 逐字节今天的行为；
//   - 只有显式 `on` 才启用（与 `online_guard.go:61-63` 的 `RequireOfflineFromEnv`、
//     `equipment_slots.go` 的 `EquipSlotQuestSyncFromEnv` 同一约定：**只认一个字面量**）；
//   - 关闭时 **`routes()` 根本不注册这两条路由**（最干净的一种"完全不生效"）：
//     `POST /api/characters/{id}/items/delete` 与 `.../items/clear` 继续落到
//     `/api/` 兜底 `"该功能未移植到 90CN 面板…"`，与今天逐字节相同。
//     选"不注册"而不是"注册后恒失败"的理由：本仓**前端已经在调用**两个同族路径
//     （`wwwroot/js/inventory.js:190` 的 `/items/batch-delete`、
//     `:397` 的 `/items/delete-at`），任何"占位恒失败"的注册都会把那些调用
//     从兜底文案改成另一条文案 —— 那也是一种默认档可观察变化。
//
// # 与上游 `gm_extended.go:507-657` 的逐段对拍
//
//	:508-509  解析角色号；key 为空 → 拒绝
//	:509      **count<0 → 拒绝**（本仓同）
//	:512-514  角色在线 → 拒绝（本仓 J0 `requireOffline`，默认档不检查）
//	:515-518  仓储组不可用 → 明确失败（本仓 `s.repositories()`）
//	:519-525  Character.Load；!found → 明确失败
//	:526-527  container 小写 + trim
//	:527-550  "inventory"/"warehouse" 分支：CloneInventory → 按 container 选
//	          Slots/Warehouse → gmRemoveStack → UpdatedAt → SaveInventoryFields
//	          单字段（本仓同款；事务边界用既有的 CharacterItems）
//	:551-574  "account" 分支：**先账号级在线判定**（:555-559），再
//	          AccountItems.WithinAccountCharacterItems → CloneAccountInventory →
//	          gmRemoveStack(record.Slots) → Save（本仓同款）
//	:575-577  其它 container（含 equipment / cube）→
//	          "container is read-only or unsupported"（本仓同，且把可选值列进文案）
//	:581      提交后返回整份 GMInventory（本仓返回本次的如实计数 + 剩余条数，见 D2）
//	:584-657  GMClearInventory：无 count 参数，整容器 map 替换为空；
//	          "account" 分支账号记录缺失时**静默返回成功**（:642-644，见 D3）
//	:978-990  gmRemoveStack：key 不存在或 Count<=0 → 拒绝；
//	          **count==0 或 count>=stack.Count → 整栈删除（连 Extra 一起丢）**；
//	          仅 0<count<Count 才原地递减（Extra/RawEntry/ExpireAt/Bind 全留）
//
// # 本仓与上游的有意差异（都是收紧；每一处都有测试钉死）
//
//	D1 **缺 count 直接拒绝**。上游 DTO 的 count 零值 0 的语义是"整栈删除"，
//	   少写一个字段就会销毁操作者没打算动的整格物品 —— 与 P3 的 D3
//	   （`api_account_cubes.go:52-59`）同一类陷阱、同一处置：用指针区分
//	   "没给"与"给了 0"，缺字段拒绝，显式 count=0 仍是整栈删除（与上游同义）。
//	   请求体同时接受上游形状（下划线 `key`）与本仓前端形状（小驼峰
//	   `listType`/`slot`，见 `wwwroot/js/inventory.js:397`），但**未知 JSON 字段
//	   一律拒绝** —— 否则 `{"listType":0,"slot":12}` 会被静默当成 `key==""`。
//	D2 **不返回整份背包快照**。上游 `:581`/`:656` 提交后调 `GMInventory` 重读并
//	   返回四个容器的平表；本仓的旗舰读面是既有的 `GET /api/characters/{id}/items`
//	   （它调 `MergeAccountSharedInventory`，比上游正确 —— 计划书 §5.2 C6），
//	   本批不复制第二套快照逻辑，只如实回报本次的 条数/物品/剩余。
//	D3 **C3（计划书 §5.2 红线）：清空 `account` 容器时账号记录缺失 → 明确失败**。
//	   上游 `:642-644` 在这种情形下 `return nil` = **静默假成功**（计划书 C11）。
//	   本仓改为报错，与 `container="account"` 的删除分支（上游 `:565-567`）
//	   口径一致；这是**有意偏离**，测试
//	   `TestItemClearMissingAccountRecordFailsInsteadOfPretendingSuccess` 固化本仓口径。
//	D4 **清空 `inventory` 时保留账号共享槽 0:354..0:365**。上游 `:619-620` 把
//	   整个 `record.Slots` 换成空 map；本仓的 `record.Slots` 里可能还存着
//	   晶块/灵魂槽的**陈旧角色侧副本**（仓库层 `MergeAccountSharedInventory`，
//	   `account_inventory_repository.go:52-70` 的注释说明这些槽的持久归属是账号）。
//	   无差别清空会把它们一起毁掉，却被算成"角色的背包物品"。本仓只删真正属于
//	   本角色的槽，并把跳过的条数如实回报为 `skipped`。想清那 12 格请用
//	   `container="account"`。
//	D5 **container 与槽位必须自洽**。上游 `:526-576` 只按 container 名选 map，
//	   **完全不看 key 里的 list_type** —— `{"container":"warehouse","key":"0:5"}`
//	   会去 Warehouse map 里找 "0:5"（那是永不存在的键，于是报"槽位不存在"，
//	   但报错理由会误导操作者）。本仓直接判 container 与 list_type 不一致并点名。
//	D6 **越界输入一律拒绝，不做隐式截断**：`listType` 必须 0..255、`slot` 必须
//	   -32768..32767、`count` 不接受负数（上游 `gmSlotKey` `:954-965` 用 Atoi 后
//	   不钳制，坏 key 会以 list_type:-1 出现在响应里）。`count` 的上界不需要额外
//	   检查：请求体字段已经是 int64，上游也没有更紧的上限。
const (
	// itemRemoveEnv 是"是否启用直接写背包"的开关名（与计划书 §4.4 的建议同名）。
	itemRemoveEnv = "DNFBRIDGE_GM_ITEM_REMOVE"

	// 上游 `gm_extended.go:527-576` 只认这三个 container 名。
	itemContainerInventory = "inventory"
	itemContainerWarehouse = "warehouse"
	itemContainerAccount   = "account"

	// itemContainerInventoryListType / itemContainerWarehouseListType 是本仓
	// 持久层的槽位归属（`repository/item_normalization_audit.go:163-175` 的不变量：
	// `InventoryRecord.Slots` 承载 list_type 0/1/7/38，`Warehouse` 只承载 list_type 2）。
	itemContainerInventoryListType = byte(listTypeMain)
	itemContainerWarehouseListType = byte(listTypePersonalCargo)
)

var (
	// 这一组哨兵只服务于"拒绝理由分类"（itemRemoveReasonFromError），
	// 文案会原样进响应体，所以每一句都要能让操作者看懂"该改哪里"。
	errItemRemoveUnsupportedContainer = errors.New("container 是只读或不支持的容器")
	errItemRemoveKeyMalformed         = errors.New("key 格式不正确")
	errItemRemoveKeyMissing           = errors.New("缺少 key")
	errItemRemoveSlotMismatch         = errors.New("key 的 list_type 与 container 不一致")
	errItemRemoveReservedSlot         = errors.New("该槽位不属于该容器")
	errItemRemoveCountMissing         = errors.New("缺少 count")
	errItemRemoveCountOutOfRange      = errors.New("数量超出范围")
	errItemRemoveListTypeOutOfRange   = errors.New("listType 超出范围")
	errItemRemoveSlotOutOfRange       = errors.New("slot 超出范围")

	// errItemRemoveCharacterMissing 对应上游 `:519-525` 的 ErrGMCharacterNotFound。
	// 本仓这个失败由共用的 `characterRequest`（api_characters.go:1028-1048 →
	// deps.go:357-370）在解析阶段就写出响应，所以本文件不产生它；它留在这里是
	// 为了让 itemRemoveReasonFromError 的分类表覆盖上游的**全部**失败模式
	// （理由串 character_missing 不会凭空冒出来，但也不会漏）。
	errItemRemoveCharacterMissing   = errors.New("角色不存在，无法定位背包")
	errItemRemoveInventoryMissing   = errors.New("角色没有背包记录")
	errItemRemoveAccountInvMissing  = errors.New("账号没有共享背包记录")
	errItemRemoveAccountOnCharacter = errors.New("角色记录缺少归属账号，无法定位账号共享背包")
	errItemRemoveItemsUnavailable   = errors.New("背包事务能力未就绪")
	errItemRemoveAccountUnavailable = errors.New("账号共享背包事务能力未就绪")
)

// RequireItemRemoveFromEnv 解析 DNFBRIDGE_GM_ITEM_REMOVE。
//
// **只有显式 `on`（大小写不敏感、两端裁剪后逐字节相等）才启用**；未设置/空/
// 其它任何值（含 `true`/`1`/`yes`/`off`/`0`/`enable`/`onn`/`on,off`）都取默认档
// false = 关闭 = 逐字节今天的行为。与 `RequireOfflineFromEnv`（online_guard.go:61-63）
// 同一约定：本仓的开关**只认一个字面量**。
func RequireItemRemoveFromEnv(value string) bool {
	return strings.EqualFold(strings.TrimSpace(value), "on")
}

// RequireItemRemoveFromEnvironment 读取进程环境里的同一个开关。
//
// 与 `RequireOfflineFromEnvironment`（online_guard.go:71-73）同样的取舍：
// 直接 `os.Getenv`，不动改动最密集的 `dnfbridge/options.go`；解析规则仍然
// 只有 RequireItemRemoveFromEnv 一处，默认档语义不因放置位置而改变。
func RequireItemRemoveFromEnvironment() bool {
	return RequireItemRemoveFromEnv(os.Getenv(itemRemoveEnv))
}

// ---------------------------------------------------------------------------
// 请求体
// ---------------------------------------------------------------------------

// itemRemoveRequest 是 `POST …/items/delete` 的请求体。
//
// 两种形状都收（见 D1）：
//
//	上游形状  {"container":"inventory","key":"0:12","count":0}
//	前端形状  {"listType":0,"slot":12,"count":0}          （wwwroot/js/inventory.js:397）
//
// 两种都归一化成 (container, key, count)；未知字段由
// decodeItemRemoveBody 的 DisallowUnknownFields 拒绝。
//
// Container 用指针：**只有字段完全没出现**才走"按 listType/key 反推"的缺省，
// 写了但归一化后为空（`"  "`）或写了未知值都必须被白名单拒绝 —— 否则
// `{"container":"  ","key":"0:12"}` 会被静默当成"删主背包 0:12"。
type itemRemoveRequest struct {
	Container *string `json:"container"`
	Key       string  `json:"key"`
	Count     *int64  `json:"count"`
	ListType  *int    `json:"listType"`
	Slot      *int    `json:"slot"`
}

// rawContainer 把可空的 container 字段还原成"用于报错文案"的原文字符串。
func (r itemRemoveRequest) rawContainer() string {
	if r.Container == nil {
		return ""
	}
	return *r.Container
}

// itemClearRequest 是 `POST …/items/clear` 的请求体。上游 `:584-591` 只有
// container 一个参数（**没有 count**）—— 整容器清空是不可分级操作。
type itemClearRequest struct {
	Container string `json:"container"`
}

// itemRemovedEntry 是一次删除里**实际被删掉**的一条，作为审计证据回给操作者。
type itemRemovedEntry struct {
	Container    string `json:"container"`
	Key          string `json:"key"`
	ListType     int    `json:"listType"`
	Slot         int    `json:"slot"`
	ItemID       int64  `json:"itemId"`
	Name         string `json:"name"`
	Count        int64  `json:"count"`
	WholeStack   bool   `json:"wholeStack"`
	ExtraDropped bool   `json:"extraDropped"`
}

// itemRemoveTarget 是一次删除请求归一化之后的落点。
type itemRemoveTarget struct {
	Container string
	Key       string
	ListType  byte
	Slot      int16
	Count     int64
}

// itemRemoveRejection 是业务拒绝的载体：外层用它同时写具名事件与响应体。
// 任何一次拒绝都发生在事务之外，因此**零写入**。
type itemRemoveRejection struct {
	reason    string
	container string
	key       string
	err       error
}

func (e *itemRemoveRejection) Error() string {
	message := strings.TrimSpace(e.err.Error())
	if strings.HasSuffix(message, "。") || strings.HasSuffix(message, "）") {
		return message
	}
	return message + "（未做任何写入）。"
}

func (e *itemRemoveRejection) Unwrap() error { return e.err }

func newItemRemoveRejection(reason, container, key string, err error) error {
	return &itemRemoveRejection{reason: reason, container: container, key: key, err: err}
}

// decodeItemRemoveBody 严格解码请求体：空体允许（clear 可以无体），
// 未知字段一律拒绝（见 D1 的理由）。
func decodeItemRemoveBody[T any](w http.ResponseWriter, r *http.Request) (T, bool) {
	var payload T
	body := http.MaxBytesReader(w, r.Body, maxBodyBytes)
	defer func() { _ = r.Body.Close() }()
	raw, err := io.ReadAll(body)
	if err != nil {
		writeFailure(w, "请求读取失败")
		return payload, false
	}
	raw = []byte(strings.TrimSpace(string(raw)))
	if len(raw) == 0 {
		return payload, true
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		writeFailure(w, "请求格式不正确（本接口只接受 container/key/count 或 container/listType/slot，未知字段一律拒绝）："+err.Error())
		return payload, false
	}
	return payload, true
}

// ---------------------------------------------------------------------------
// 槽位 key
// ---------------------------------------------------------------------------

// itemSlotKey 是本仓持久层的槽位键格式（`<listType>:<slot>`），与
// `dnfbridge/cera_shop_grant.go:590-592` 的 currentCeraShopInventorySlotKey、
// `dnfbridge/scene_inventory.go:498-512` 的 parseSceneInventorySlotKey 同源。
func itemSlotKey(listType byte, slot int16) string {
	return strconv.Itoa(int(listType)) + ":" + strconv.FormatInt(int64(slot), 10)
}

// parseItemSlotKey 解析 key。与上游 `gmSlotKey`（`gm_extended.go:954-965`）不同：
// 上游**不做范围钳制**（坏 key 会以 list_type:-1,slot:-1 出现在响应里），
// 本仓把它当成拒绝理由 —— 一个写接口不该把"看不懂的 key"解释成"某个槽"。
func parseItemSlotKey(key string) (byte, int16, error) {
	listRaw, slotRaw, found := strings.Cut(strings.TrimSpace(key), ":")
	if !found {
		return 0, 0, fmt.Errorf("%w: %q（期望 <list_type>:<slot>）", errItemRemoveKeyMalformed, key)
	}
	listValue, err := strconv.ParseUint(strings.TrimSpace(listRaw), 10, 8)
	if err != nil {
		return 0, 0, fmt.Errorf("%w: %q 的 list_type 不是 0..255", errItemRemoveKeyMalformed, key)
	}
	slotValue, err := strconv.ParseInt(strings.TrimSpace(slotRaw), 10, 16)
	if err != nil {
		return 0, 0, fmt.Errorf("%w: %q 的 slot 不是 -32768..32767", errItemRemoveKeyMalformed, key)
	}
	return byte(listValue), int16(slotValue), nil
}

// resolveItemRemoveTarget 把两种请求体形状归一化成 (container, key, count)。
//
// 归一化顺序刻意固定：先看显式 key，再看 listType/slot，最后才看 container 的
// 缺省值 —— 这样"给了 key 又给了 listType/slot"能被检查出自洽性。
func resolveItemRemoveTarget(body itemRemoveRequest) (itemRemoveTarget, error) {
	container := normalizeItemContainer(body.rawContainer())
	// 见 itemRemoveRequest 的注释：**只有字段完全没出现**才走"按 listType/key
	// 反推"的缺省；写了但归一化后为空（`"  "`）或写了未知值都必须被白名单拒绝，
	// 否则 `{"container":"  ","key":"0:12"}` 会被静默当成"删主背包 0:12"。
	containerOmitted := body.Container == nil
	explicitKey := strings.TrimSpace(body.Key)

	if containerOmitted {
		// 本仓前端 delete-at 从不发 container（它发 listType/slot），所以在这里
		// 按 listType 反推；两者都没有 = 主背包（list_type 0）。
		switch {
		case explicitKey != "":
			listType, _, err := parseItemSlotKey(explicitKey)
			if err != nil {
				return itemRemoveTarget{}, err
			}
			container = itemContainerForListType(listType)
		case body.ListType != nil:
			listType, err := itemRemoveListType(*body.ListType)
			if err != nil {
				return itemRemoveTarget{}, err
			}
			container = itemContainerForListType(listType)
		default:
			container = itemContainerInventory
		}
	}
	if !itemContainerSupported(container) {
		return itemRemoveTarget{}, itemRemoveContainerError(body.rawContainer())
	}

	var (
		key      string
		listType byte
		slot     int16
	)
	switch {
	case explicitKey != "":
		parsedList, parsedSlot, err := parseItemSlotKey(explicitKey)
		if err != nil {
			return itemRemoveTarget{}, err
		}
		// 同时给了 listType/slot 就必须自洽，避免"看着像删 A、其实删 B"。
		if body.ListType != nil && *body.ListType != int(parsedList) {
			return itemRemoveTarget{}, fmt.Errorf("%w: key=%q 的 list_type=%d，body.listType=%d",
				errItemRemoveSlotMismatch, explicitKey, parsedList, *body.ListType)
		}
		if body.Slot != nil && *body.Slot != int(parsedSlot) {
			return itemRemoveTarget{}, fmt.Errorf("%w: key=%q 的 slot=%d，body.slot=%d",
				errItemRemoveSlotMismatch, explicitKey, parsedSlot, *body.Slot)
		}
		key, listType, slot = explicitKey, parsedList, parsedSlot
	case body.ListType != nil && body.Slot != nil:
		list, listErr := itemRemoveListType(*body.ListType)
		if listErr != nil {
			return itemRemoveTarget{}, listErr
		}
		slotNumber, slotErr := itemRemoveSlot(*body.Slot)
		if slotErr != nil {
			return itemRemoveTarget{}, slotErr
		}
		listType, slot = list, slotNumber
		key = itemSlotKey(listType, slot)
	default:
		return itemRemoveTarget{}, fmt.Errorf(
			"%w: 必须给出 key（<list_type>:<slot>）或同时给出 listType 与 slot；"+
				"本接口只接受 container/key/count 或 container/listType/slot",
			errItemRemoveKeyMissing)
	}

	if err := requireItemSlotMatchesContainer(container, listType, slot); err != nil {
		return itemRemoveTarget{}, err
	}
	if body.Count == nil {
		// 见 D1：上游的 count 零值是"整栈删除"，缺字段等于最危险的那个动作。
		return itemRemoveTarget{}, fmt.Errorf("%w（0 表示整栈删除，必须显式给出）", errItemRemoveCountMissing)
	}
	if *body.Count < 0 {
		return itemRemoveTarget{}, fmt.Errorf("%w: count=%d 不能为负", errItemRemoveCountOutOfRange, *body.Count)
	}
	return itemRemoveTarget{
		Container: container, Key: key, ListType: listType, Slot: slot, Count: *body.Count,
	}, nil
}

func normalizeItemContainer(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func itemContainerSupported(container string) bool {
	switch container {
	case itemContainerInventory, itemContainerWarehouse, itemContainerAccount:
		return true
	default:
		return false
	}
}

func itemContainerForListType(listType byte) string {
	switch listType {
	case itemContainerInventoryListType:
		return itemContainerInventory
	case itemContainerWarehouseListType:
		return itemContainerWarehouse
	default:
		// 其它 listType 在本仓持久层里既不属于 Slots 主表也不属于 Warehouse，
		// 交给 requireItemSlotMatchesContainer 报错（不猜一个容器出来）。
		return ""
	}
}

func itemRemoveListType(value int) (byte, error) {
	if value < 0 || value > 255 {
		return 0, fmt.Errorf("%w: listType=%d 必须在 0..255", errItemRemoveListTypeOutOfRange, value)
	}
	return byte(value), nil
}

func itemRemoveSlot(value int) (int16, error) {
	if value < -32768 || value > 32767 {
		return 0, fmt.Errorf("%w: slot=%d 必须在 -32768..32767", errItemRemoveSlotOutOfRange, value)
	}
	return int16(value), nil
}

// itemRemoveContainerError 给"container 不在白名单"造同一句文案（空 container
// 也走这里：上游 `:592` 的空值落 switch 的 default）。
func itemRemoveContainerError(raw string) error {
	return fmt.Errorf("%w: container=%q（本接口只接受 %s / %s / %s）",
		errItemRemoveUnsupportedContainer, raw, itemContainerInventory,
		itemContainerWarehouse, itemContainerAccount)
}

// requireItemSlotMatchesContainer 钉死 container 与槽位归属的一致性（见 D5）。
func requireItemSlotMatchesContainer(container string, listType byte, slot int16) error {
	switch container {
	case itemContainerInventory:
		if listType != itemContainerInventoryListType {
			return fmt.Errorf("%w: container=inventory 要求 list_type=%d，实际 key=%q",
				errItemRemoveSlotMismatch, itemContainerInventoryListType, itemSlotKey(listType, slot))
		}
		if isAccountSharedItemSlot(listType, slot) {
			return fmt.Errorf("%w: key=%q 的 slot=%d 落在账号共享槽 0:%d..0:%d，请用 container=account",
				errItemRemoveReservedSlot, itemSlotKey(listType, slot), slot,
				dnfrepo.CrystalWarehouseFirstSlot, dnfrepo.SoulWarehouseLastSlot)
		}
	case itemContainerWarehouse:
		if listType != itemContainerWarehouseListType {
			return fmt.Errorf("%w: container=warehouse 要求 list_type=%d，实际 key=%q",
				errItemRemoveSlotMismatch, itemContainerWarehouseListType, itemSlotKey(listType, slot))
		}
	case itemContainerAccount:
		if !isAccountSharedItemSlot(listType, slot) {
			return fmt.Errorf("%w: container=account 只承载 0:%d..0:%d（晶块/灵魂），实际 key=%q",
				errItemRemoveReservedSlot, dnfrepo.CrystalWarehouseFirstSlot,
				dnfrepo.SoulWarehouseLastSlot, itemSlotKey(listType, slot))
		}
	default:
		return fmt.Errorf("%w: container=%q", errItemRemoveSlotMismatch, container)
	}
	return nil
}

func isAccountSharedItemSlot(listType byte, slot int16) bool {
	return dnfrepo.IsAccountSharedInventorySlot(listType, slot)
}

// ---------------------------------------------------------------------------
// 写：POST /api/characters/{id}/items/delete
// ---------------------------------------------------------------------------

func (s *Server) handleItemRemove(w http.ResponseWriter, r *http.Request) {
	// 第二层守卫（第一层是 routes() 的条件注册）。关闭档下这两条路由**根本不
	// 存在**，所以正常流量永远到不了这里；这一层是"就算被绕过也必须逐字节
	// 等于兜底"的保险：**零探针、零日志、零仓储访问、零写入**，
	// 而且文案与 `/api/` 兜底逐字相同。
	if s == nil || !s.cfg.RemoveInventory {
		writeFailure(w, unportedFeatureMessage)
		return
	}
	// 顺序与上游 `gm_extended.go:508-527` 一致：
	// 解析角色 → 校验请求体 → 守卫 → 事务。
	ctx, group, panelID, character, ok := s.characterRequest(w, r)
	if !ok {
		return
	}
	body, ok := decodeItemRemoveBody[itemRemoveRequest](w, r)
	if !ok {
		return
	}
	target, err := resolveItemRemoveTarget(body)
	if err != nil {
		s.logItemRemoveRejection(panelID, character, "delete", itemRemoveTarget{
			Container: normalizeItemContainer(body.rawContainer()),
			Key:       strings.TrimSpace(body.Key),
		}, 0, err)
		writeFailure(w, err.Error())
		return
	}

	// J0 守卫：角色级（上游 :512-514），账号容器再叠一层账号级（上游 :555-559）。
	action := "items-delete"
	if !s.requireOffline(w, action, panelID) {
		return
	}
	if target.Container == itemContainerAccount {
		accountID := strings.TrimSpace(character.AccountID)
		if accountID == "" {
			s.rejectItemRemoveWrite(w, panelID, character, "delete", target, 0, "character_account_missing",
				errItemRemoveAccountOnCharacter)
			return
		}
		if !s.requireAccountOffline(ctx, w, action, group, accountID, panelID) {
			return
		}
	}

	removed, remaining, err := s.removeItemStack(ctx, group, character, target)
	if err != nil {
		var rejection *itemRemoveRejection
		if errors.As(err, &rejection) {
			s.logItemRemoveRejection(panelID, character, "delete", target, 0, rejection)
			writeFailure(w, rejection.Error())
			return
		}
		s.logItemRemoveFailure(panelID, character, "delete", target, err)
		writeFailure(w, "删除物品失败: "+err.Error())
		return
	}

	s.logEvent("gm-item-remove",
		"operation", "delete",
		"panel_id", panelID,
		"character_id", character.CharacterID,
		"account_id", character.AccountID,
		"container", target.Container,
		"key", target.Key,
		"list_type", int(target.ListType),
		"slot", int(target.Slot),
		"item_id", removed.ItemID,
		"name", removed.Name,
		"count", removed.Count,
		"requested", target.Count,
		"whole_stack", removed.WholeStack,
		"extra_dropped", removed.ExtraDropped,
		"remaining", remaining,
		"result", "applied",
	)
	writeOK(w, map[string]any{
		"characterId": panelIDOfCharacter(character),
		"container":   target.Container,
		"key":         target.Key,
		"listType":    int(target.ListType),
		"slot":        int(target.Slot),
		"requested":   target.Count,
		"deleted":     removed.Count,
		"remaining":   remaining,
		"entry":       removed,
	})
}

// removeItemStack 是单格删除的落库部分：复用既有两条事务边界
// （`CharacterItems.WithinCharacterItems` / `AccountItems.WithinAccountCharacterItems`），
// **不新开第二套写库逻辑**。
func (s *Server) removeItemStack(
	ctx context.Context,
	group dnfrepo.Group,
	character dnfrepo.CharacterRecord,
	target itemRemoveTarget,
) (itemRemovedEntry, int64, error) {
	switch target.Container {
	case itemContainerInventory, itemContainerWarehouse:
		if group.CharacterItems == nil {
			return itemRemovedEntry{}, 0, newItemRemoveRejection(
				"character_items_transaction_unavailable", target.Container, target.Key,
				errItemRemoveItemsUnavailable)
		}
		var (
			removed   itemRemovedEntry
			remaining int64
		)
		err := group.CharacterItems.WithinCharacterItems(ctx, character.CharacterID, func(
			inventories dnfrepo.InventoryRepository,
			_ dnfrepo.EquipmentRepository,
		) error {
			record, exists, loadErr := inventories.Load(ctx, character.CharacterID)
			if loadErr != nil {
				return loadErr
			}
			if !exists {
				// 上游 :537-539：角色背包记录缺失 → 明确失败（不静默假成功）。
				return newItemRemoveRejection("inventory_record_missing", target.Container,
					target.Key, errItemRemoveInventoryMissing)
			}
			record = dnfrepo.CloneInventory(record)
			entries := record.Slots
			field := dnfrepo.InventoryFieldSlots
			if target.Container == itemContainerWarehouse {
				entries = record.Warehouse
				field = dnfrepo.InventoryFieldWarehouse
			}
			entry, mutateErr := s.planItemStackRemoval(target, entries)
			if mutateErr != nil {
				return mutateErr
			}
			applyItemStackRemoval(entries, target, entry)
			record.UpdatedAt = time.Now().UTC()
			removed, remaining = entry, int64(len(entries))
			// 上游 :549：单字段保存（Slots 或 Warehouse），一次写入点。
			return dnfrepo.SaveInventoryFields(ctx, inventories, record, field)
		})
		if err != nil {
			return itemRemovedEntry{}, 0, err
		}
		return removed, remaining, nil

	case itemContainerAccount:
		if group.AccountItems == nil {
			return itemRemovedEntry{}, 0, newItemRemoveRejection(
				"account_items_transaction_unavailable", target.Container, target.Key,
				errItemRemoveAccountUnavailable)
		}
		accountID := strings.TrimSpace(character.AccountID)
		if accountID == "" {
			return itemRemovedEntry{}, 0, newItemRemoveRejection(
				"character_account_missing", target.Container, target.Key,
				errItemRemoveAccountOnCharacter)
		}
		var (
			removed   itemRemovedEntry
			remaining int64
		)
		err := group.AccountItems.WithinAccountCharacterItems(ctx, accountID, character.CharacterID, func(
			accounts dnfrepo.AccountInventoryRepository,
			_ dnfrepo.InventoryRepository,
		) error {
			record, exists, loadErr := accounts.Load(ctx, accountID)
			if loadErr != nil {
				return loadErr
			}
			if !exists {
				// 上游 :565-567：账号共享背包缺失 → 明确失败。
				return newItemRemoveRejection("account_inventory_missing", target.Container,
					target.Key, errItemRemoveAccountInvMissing)
			}
			record = dnfrepo.CloneAccountInventory(record)
			entry, mutateErr := s.planItemStackRemoval(target, record.Slots)
			if mutateErr != nil {
				return mutateErr
			}
			applyItemStackRemoval(record.Slots, target, entry)
			record.UpdatedAt = time.Now().UTC()
			removed, remaining = entry, int64(len(record.Slots))
			// 上游 :573：整条账号共享背包一次 Save。
			return accounts.Save(ctx, record)
		})
		if err != nil {
			return itemRemovedEntry{}, 0, err
		}
		return removed, remaining, nil

	default:
		return itemRemovedEntry{}, 0, newItemRemoveRejection("container_unsupported",
			target.Container, target.Key, fmt.Errorf("%w: container=%q",
				errItemRemoveItemsUnavailable, target.Container))
	}
}

// planItemStackRemoval 是上游 `gmRemoveStack`（`gm_extended.go:978-990`）的同一判定，
// 但**只做判定、不落库**，好让拒绝路径天然零写入。
func (s *Server) planItemStackRemoval(
	target itemRemoveTarget,
	entries map[string]dnfrepo.ItemStack,
) (itemRemovedEntry, error) {
	stack, found := entries[target.Key]
	if !found || stack.Count <= 0 {
		// 上游 :980-982：key 不存在或 Count<=0 → 拒绝（不静默成功）。
		return itemRemovedEntry{}, newItemRemoveRejection("slot_not_found", target.Container,
			target.Key, fmt.Errorf("背包槽位 %q 不存在或数量为 0", target.Key))
	}
	wholeStack := target.Count == 0 || target.Count >= stack.Count
	removedCount := target.Count
	if wholeStack {
		removedCount = stack.Count
	}
	return itemRemovedEntry{
		Container:    target.Container,
		Key:          target.Key,
		ListType:     int(target.ListType),
		Slot:         int(target.Slot),
		ItemID:       stack.ItemID,
		Name:         s.itemsName(stack.ItemID),
		Count:        removedCount,
		WholeStack:   wholeStack,
		ExtraDropped: wholeStack && len(stack.Extra) > 0,
	}, nil
}

// applyItemStackRemoval 执行上游 `gm_extended.go:983-988` 的两种落库形态：
// 整栈删除（连 Extra 一起丢）或原地递减（Extra/RawEntry/ExpireAt/Bind 全留）。
func applyItemStackRemoval(entries map[string]dnfrepo.ItemStack, target itemRemoveTarget, entry itemRemovedEntry) {
	if entry.WholeStack {
		delete(entries, target.Key)
		return
	}
	stack := entries[target.Key]
	stack.Count -= target.Count
	entries[target.Key] = stack
}

// itemsName 借既有物品索引取显示名。索引没建好/查不到时返回空串 ——
// **绝不发明名字**（不返回"未知物品 N"之类的合成文案，`api_items.go:553`
// 的读面已经承担了那个展示职责）。
func (s *Server) itemsName(itemID int64) string {
	if itemID <= 0 || itemID > int64(^uint32(0)) {
		return ""
	}
	return s.items.name(uint32(itemID))
}

// ---------------------------------------------------------------------------
// 写：POST /api/characters/{id}/items/clear
// ---------------------------------------------------------------------------

func (s *Server) handleItemClear(w http.ResponseWriter, r *http.Request) {
	// 第二层守卫，见 handleItemRemove。
	if s == nil || !s.cfg.RemoveInventory {
		writeFailure(w, unportedFeatureMessage)
		return
	}
	ctx, group, panelID, character, ok := s.characterRequest(w, r)
	if !ok {
		return
	}
	body, ok := decodeItemRemoveBody[itemClearRequest](w, r)
	if !ok {
		return
	}
	container := normalizeItemContainer(body.Container)
	if !itemContainerSupported(container) {
		// 上游 `:592` 之后直接 switch，空值与未知值都落 default 被拒。本仓同口径。
		err := itemRemoveContainerError(body.Container)
		s.logItemClearRejection(panelID, character, container, "container_unsupported", err)
		writeFailure(w, err.Error()+"（未做任何写入）。")
		return
	}

	action := "items-clear"
	if !s.requireOffline(w, action, panelID) {
		return
	}
	if container == itemContainerAccount {
		accountID := strings.TrimSpace(character.AccountID)
		if accountID == "" {
			s.logItemClearRejection(panelID, character, container, "character_account_missing",
				errItemRemoveAccountOnCharacter)
			writeFailure(w, errItemRemoveAccountOnCharacter.Error()+"（未做任何写入）。")
			return
		}
		if !s.requireAccountOffline(ctx, w, action, group, accountID, panelID) {
			return
		}
	}

	result, err := s.clearItemContainer(ctx, group, character, container)
	if err != nil {
		var rejection *itemRemoveRejection
		if errors.As(err, &rejection) {
			s.logItemClearRejection(panelID, character, container, rejection.reason, rejection)
			writeFailure(w, rejection.Error())
			return
		}
		s.logEvent("gm-item-remove-failed",
			"operation", "clear",
			"panel_id", panelID,
			"character_id", character.CharacterID,
			"account_id", character.AccountID,
			"container", container,
			"reason", "write_failed",
			"error", err.Error(),
			"result", "failed",
		)
		writeFailure(w, "清空容器失败: "+err.Error())
		return
	}

	s.logEvent("gm-item-remove",
		"operation", "clear",
		"panel_id", panelID,
		"character_id", character.CharacterID,
		"account_id", character.AccountID,
		"container", container,
		"key", "",
		"deleted", result.Deleted,
		"skipped", result.Skipped,
		"deleted_entries", len(result.Removed),
		"remaining", result.Remaining,
		"result", "applied",
	)
	writeOK(w, map[string]any{
		"characterId": panelIDOfCharacter(character),
		"container":   container,
		"deleted":     result.Deleted,
		"skipped":     result.Skipped,
		"remaining":   result.Remaining,
		"removed":     result.Removed,
	})
}

// itemClearResult 是整容器清空的如实回报（C11 的另一半：绝不静默假成功）。
type itemClearResult struct {
	Deleted   int
	Skipped   int64
	Remaining int64
	Removed   []itemRemovedEntry
}

func (s *Server) clearItemContainer(
	ctx context.Context,
	group dnfrepo.Group,
	character dnfrepo.CharacterRecord,
	container string,
) (itemClearResult, error) {
	switch container {
	case itemContainerInventory, itemContainerWarehouse:
		if group.CharacterItems == nil {
			return itemClearResult{}, newItemRemoveRejection(
				"character_items_transaction_unavailable", container, "", errItemRemoveItemsUnavailable)
		}
		var result itemClearResult
		err := group.CharacterItems.WithinCharacterItems(ctx, character.CharacterID, func(
			inventories dnfrepo.InventoryRepository,
			_ dnfrepo.EquipmentRepository,
		) error {
			record, exists, loadErr := inventories.Load(ctx, character.CharacterID)
			if loadErr != nil {
				return loadErr
			}
			if !exists {
				return newItemRemoveRejection("inventory_record_missing", container, "",
					errItemRemoveInventoryMissing)
			}
			record = dnfrepo.CloneInventory(record)
			field := dnfrepo.InventoryFieldSlots
			entries := record.Slots
			if container == itemContainerWarehouse {
				entries = record.Warehouse
				field = dnfrepo.InventoryFieldWarehouse
			}
			result = s.planItemContainerClear(container, field, entries)
			applyItemContainerClear(entries, container, field)
			record.UpdatedAt = time.Now().UTC()
			// 上游 :626：单字段保存（Slots 或 Warehouse）。空容器也写一次
			// （UpdatedAt 会变），但如实回报 deleted=0。
			return dnfrepo.SaveInventoryFields(ctx, inventories, record, field)
		})
		if err != nil {
			return itemClearResult{}, err
		}
		return result, nil

	case itemContainerAccount:
		if group.AccountItems == nil {
			return itemClearResult{}, newItemRemoveRejection(
				"account_items_transaction_unavailable", container, "", errItemRemoveAccountUnavailable)
		}
		accountID := strings.TrimSpace(character.AccountID)
		if accountID == "" {
			return itemClearResult{}, newItemRemoveRejection(
				"character_account_missing", container, "", errItemRemoveAccountOnCharacter)
		}
		var result itemClearResult
		err := group.AccountItems.WithinAccountCharacterItems(ctx, accountID, character.CharacterID, func(
			accounts dnfrepo.AccountInventoryRepository,
			_ dnfrepo.InventoryRepository,
		) error {
			record, exists, loadErr := accounts.Load(ctx, accountID)
			if loadErr != nil {
				return loadErr
			}
			if !exists {
				// 见 D3（计划书 §5.2 红线 C11）：上游 :642-644 在这里
				// `return nil` 静默假成功；本仓改成明确失败。
				return newItemRemoveRejection("account_inventory_missing", container, "",
					errItemRemoveAccountInvMissing)
			}
			record = dnfrepo.CloneAccountInventory(record)
			field := dnfrepo.InventoryFieldSlots
			result = s.planItemContainerClear(container, field, record.Slots)
			applyItemContainerClear(record.Slots, container, field)
			record.UpdatedAt = time.Now().UTC()
			// 上游 :648：整条账号共享背包一次 Save。
			return accounts.Save(ctx, record)
		})
		if err != nil {
			return itemClearResult{}, err
		}
		return result, nil

	default:
		return itemClearResult{}, newItemRemoveRejection("container_unsupported", container, "",
			fmt.Errorf("%w: container=%q", errItemRemoveItemsUnavailable, container))
	}
}

// planItemContainerClear 先算一遍本仓口径下"会删掉哪些、会跳过哪些"，
// 供响应与审计如实回报（**不落库**）。
func (s *Server) planItemContainerClear(
	container string,
	field dnfrepo.InventoryField,
	entries map[string]dnfrepo.ItemStack,
) itemClearResult {
	result := itemClearResult{Removed: make([]itemRemovedEntry, 0, len(entries))}
	for key, stack := range entries {
		if !itemContainerClearRemoves(container, field, key) {
			result.Skipped++
			continue
		}
		result.Deleted++
		result.Removed = append(result.Removed, itemRemovedEntry{
			Container:    container,
			Key:          key,
			ListType:     int(itemClearListType(field)),
			Slot:         itemClearSlot(key),
			ItemID:       stack.ItemID,
			Name:         s.itemsName(stack.ItemID),
			Count:        stack.Count,
			WholeStack:   true,
			ExtraDropped: len(stack.Extra) > 0,
		})
	}
	// remaining = 清空之后容器里还会剩下的条数 = 被跳过的条数（见 D4）。
	result.Remaining = result.Skipped
	sortItemRemovedEntries(result.Removed)
	return result
}

// applyItemContainerClear 执行清空：只删本仓口径下属于该容器的键，
// `inventory` 刻意保留账号共享槽（见 D4）。
func applyItemContainerClear(entries map[string]dnfrepo.ItemStack, container string, field dnfrepo.InventoryField) {
	for key := range entries {
		if itemContainerClearRemoves(container, field, key) {
			delete(entries, key)
		}
	}
}

// itemContainerClearRemoves 是本仓对"清空这个容器到底删哪些键"的唯一判据。
//
//	inventory（field=Slots）    : list_type==0 且**不是**账号共享槽 0:354..0:365
//	warehouse（field=Warehouse）: 全部（该 map 只承载 list_type==2）
//	account                     : 全部（该 map 只承载账号共享槽）
//
// 解析不出 list_type 的坏键一律**保留**（不删看不懂的东西）。
func itemContainerClearRemoves(container string, field dnfrepo.InventoryField, key string) bool {
	if container == itemContainerWarehouse || field == dnfrepo.InventoryFieldWarehouse {
		return true
	}
	if container == itemContainerAccount {
		return true
	}
	listType, slot, err := parseItemSlotKey(key)
	if err != nil {
		return false
	}
	if listType != itemContainerInventoryListType {
		return false
	}
	return !isAccountSharedItemSlot(listType, slot)
}

func itemClearListType(field dnfrepo.InventoryField) byte {
	if field == dnfrepo.InventoryFieldWarehouse {
		return itemContainerWarehouseListType
	}
	return itemContainerInventoryListType
}

func itemClearSlot(key string) int {
	_, slot, err := parseItemSlotKey(key)
	if err != nil {
		return 0
	}
	return int(slot)
}

// sortItemRemovedEntries 让响应与审计里的条目按 (listType, slot) 升序，
// 避免 Go map 随机序让操作者看到跳动的输出（与上游 `:362-373` 的排序意图一致）。
func sortItemRemovedEntries(entries []itemRemovedEntry) {
	for i := 1; i < len(entries); i++ {
		for j := i; j > 0; j-- {
			left, right := entries[j-1], entries[j]
			if left.ListType < right.ListType ||
				(left.ListType == right.ListType && left.Slot <= right.Slot) {
				break
			}
			entries[j-1], entries[j] = entries[j], entries[j-1]
		}
	}
}

// ---------------------------------------------------------------------------
// 审计事件
// ---------------------------------------------------------------------------

// rejectItemRemoveWrite 是"还没进事务就确定要拒"的统一出口（角色缺账号等），
// 保证响应与事件都带上完整上下文。
func (s *Server) rejectItemRemoveWrite(
	w http.ResponseWriter,
	panelID int,
	character dnfrepo.CharacterRecord,
	operation string,
	target itemRemoveTarget,
	itemID int64,
	reason string,
	cause error,
) {
	s.logItemRemoveRejection(panelID, character, operation, target, itemID,
		newItemRemoveRejection(reason, target.Container, target.Key, cause))
	writeFailure(w, cause.Error()+"（未做任何写入）。")
}

// logItemRemoveRejection 记一条具名拒绝事件。字段与成功事件同形，
// 便于按 reason 聚合；**关闭档永远到不了这里**（路由未注册）。
func (s *Server) logItemRemoveRejection(
	panelID int,
	character dnfrepo.CharacterRecord,
	operation string,
	target itemRemoveTarget,
	itemID int64,
	err error,
) {
	s.logEvent("gm-item-remove-rejected",
		"operation", operation,
		"panel_id", panelID,
		"character_id", character.CharacterID,
		"account_id", character.AccountID,
		"container", target.Container,
		"key", target.Key,
		"list_type", int(target.ListType),
		"slot", int(target.Slot),
		"item_id", itemID,
		"count", target.Count,
		"reason", itemRemoveReasonFromError(err),
		"error", err.Error(),
		"result", "rejected",
	)
}

func (s *Server) logItemClearRejection(
	panelID int,
	character dnfrepo.CharacterRecord,
	container string,
	reason string,
	err error,
) {
	if reason == "" {
		reason = itemRemoveReasonFromError(err)
	}
	s.logEvent("gm-item-remove-rejected",
		"operation", "clear",
		"panel_id", panelID,
		"character_id", character.CharacterID,
		"account_id", character.AccountID,
		"container", container,
		"key", "",
		"reason", reason,
		"error", err.Error(),
		"result", "rejected",
	)
}

func (s *Server) logItemRemoveFailure(
	panelID int,
	character dnfrepo.CharacterRecord,
	operation string,
	target itemRemoveTarget,
	err error,
) {
	s.logEvent("gm-item-remove-failed",
		"operation", operation,
		"panel_id", panelID,
		"character_id", character.CharacterID,
		"account_id", character.AccountID,
		"container", target.Container,
		"key", target.Key,
		"list_type", int(target.ListType),
		"slot", int(target.Slot),
		"count", target.Count,
		"reason", "write_failed",
		"error", err.Error(),
		"result", "failed",
	)
}

// itemRemoveReasonFromError 把哨兵错误归类成稳定的 reason 字符串，
// 好让操作者的日志聚合不依赖中文文案。
func itemRemoveReasonFromError(err error) string {
	var rejection *itemRemoveRejection
	if errors.As(err, &rejection) && rejection.reason != "" {
		return rejection.reason
	}
	switch {
	case err == nil:
		return "rejected"
	case errors.Is(err, errItemRemoveUnsupportedContainer):
		return "container_unsupported"
	case errors.Is(err, errItemRemoveKeyMissing):
		return "key_missing"
	case errors.Is(err, errItemRemoveKeyMalformed):
		return "key_malformed"
	case errors.Is(err, errItemRemoveSlotMismatch):
		return "slot_mismatch"
	case errors.Is(err, errItemRemoveReservedSlot):
		return "slot_not_in_container"
	case errors.Is(err, errItemRemoveCountMissing):
		return "missing_count"
	case errors.Is(err, errItemRemoveCountOutOfRange):
		return "count_out_of_range"
	case errors.Is(err, errItemRemoveListTypeOutOfRange):
		return "list_type_out_of_range"
	case errors.Is(err, errItemRemoveSlotOutOfRange):
		return "slot_out_of_range"
	case errors.Is(err, errItemRemoveCharacterMissing):
		return "character_missing"
	case errors.Is(err, errItemRemoveInventoryMissing):
		return "inventory_record_missing"
	case errors.Is(err, errItemRemoveAccountInvMissing):
		return "account_inventory_missing"
	case errors.Is(err, errItemRemoveAccountOnCharacter):
		return "character_account_missing"
	case errors.Is(err, errItemRemoveItemsUnavailable):
		return "character_items_transaction_unavailable"
	case errors.Is(err, errItemRemoveAccountUnavailable):
		return "account_items_transaction_unavailable"
	default:
		return "rejected"
	}
}

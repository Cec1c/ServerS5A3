package dnfbridge

import (
	"context"
	"encoding/binary"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// ---------------------------------------------------------------------------
// GM P7b：GM 发放的 expire_days 在重登后不被 PVF 期限覆盖回去
//
// 本文件钉住四件事：
//  1. 默认档（开关未设置/空/非 on）**不消费任何标记**：带标记的实例与不带标记的
//     实例得到同一个到期结果（堆叠物 / 装备 / 时装 / 永久物各一类），标记键本身
//     也不被改写。
//  2. 开关 on 时带标记的堆叠物保留 GM 期限；装备**邮件附件修补**路径仍逐字节不动。
//  3. 标记在、值非法/读不出来时：记一条具名事件 + 确定行为（保留或回退），不 panic。
//  4. 两层真的接上了：写入端（gmtool 发放 → 邮件附件 Extra）与消费端
//     （dnfbridge 登录投影 → 背包堆栈 Extra）是同一个键，且领取桥
//     （mail/owner.go 的 mailboxAttachmentStack）把附件 Extra 原样带进堆栈 Extra。
// ---------------------------------------------------------------------------

const (
	p7bStackableItemID uint32 = 3227
	p7bEquipmentItemID uint32 = 9001
	p7bStackablePath          = "stackable/event/p7b_bead.stk"
	p7bEquipmentPath          = "equipment/event/p7b_equipment.equ"
	// P7 发放端写进邮件附件 Extra 的证据键（gmtool/api_items_durability.go:49-71
	// 的 grantAttachmentExtra，键名常量在同包内）。
	p7bMailboxKindKey = "mailbox_pvf_kind"

	// p7bEffectiveExpirationKeys 是"到期结果"的键：默认档下带标记与不带标记的
	// 实例，这几个键必须完全一致。
	p7bEffectiveExpirationKeys = "expire_time,expire_unix"
)

// p7bPVFDeadline 是 fixture 里 [expiration date] `2028-08-16 06:00:00`（CST=UTC+8）
// 的 UTC 表示 —— 与既有用例（item_expiration_test.go:108）同一个值。
func p7bPVFDeadline() time.Time {
	return time.Date(2028, time.August, 15, 22, 0, 0, 0, time.UTC)
}

// p7bGrantedDeadline 造一个"GM 给了 30 天"的期限（整秒，与
// applyCurrentGMExpirationOverrideAt 的 time.Unix(...).UTC() 同形）。
func p7bGrantedDeadline(now time.Time) time.Time {
	return time.Unix(now.Unix()+30*86400, 0).UTC()
}

func mustP7BCatalog(t *testing.T) *pvfDungeonDropCatalog {
	t.Helper()
	catalog, err := newPVFDungeonDropCatalog(dungeonDropCatalogTestSource{
		"monster/monster.lst":     "",
		"stackable/stackable.lst": strconv.FormatUint(uint64(p7bStackableItemID), 10) + " `event/p7b_bead.stk`\n",
		"equipment/equipment.lst": strconv.FormatUint(uint64(p7bEquipmentItemID), 10) + " `event/p7b_equipment.equ`\n",
		p7bStackablePath:          "[stackable type] `[etc]`\n[stack limit] 1000\n[expiration date] `2028-08-16 06:00:00`\n",
		p7bEquipmentPath:          "[equipment type] `[weapon]`\n[durability] 50\n[expiration date] `2028-08-16 06:00:00`\n",
	})
	if err != nil {
		t.Fatalf("build p7b catalog: %v", err)
	}
	return catalog
}

func p7bStackableDefinition() dungeonDropItemDefinition {
	return dungeonDropItemDefinition{
		ItemID:         p7bStackableItemID,
		Kind:           dungeonDropItemStackable,
		PVFPath:        p7bStackablePath,
		StackableType:  "[etc]",
		StackLimit:     1000,
		ExpirationDate: p7bPVFDeadline(),
	}
}

func p7bEquipmentDefinition() dungeonDropItemDefinition {
	return dungeonDropItemDefinition{
		ItemID:         p7bEquipmentItemID,
		Kind:           dungeonDropItemEquipment,
		PVFPath:        p7bEquipmentPath,
		EquipmentType:  "[weapon]",
		Durability:     50,
		ExpirationDate: p7bPVFDeadline(),
	}
}

// p7bStackableExtra 是"今天形状"的堆叠物 Extra（发放/领取路径都会写的身份证据）。
func p7bStackableExtra() map[string]string {
	return map[string]string{
		"item_kind":      string(dungeonDropItemStackable),
		"stackable_type": "[etc]",
		"pvf_path":       p7bStackablePath,
	}
}

// p7bGrantedExtra 是 P7 发放端在堆叠物附件 Extra 上写下的完整形状
// （gmtool/api_items_grant_options.go:380-397 + api_items_durability.go:49-71）。
// days 为空串时不写 gm_expire_days 记账键。
func p7bGrantedExtra(deadline time.Time, days string) map[string]string {
	extra := p7bStackableExtra()
	unix := strconv.FormatInt(deadline.Unix(), 10)
	extra["expire_time"] = unix
	extra["expire_unix"] = unix
	extra["expiration_source"] = currentGMExpirationOverrideSource
	if days != "" {
		extra["gm_expire_days"] = days
	}
	extra[p7bMailboxKindKey] = string(dungeonDropItemStackable)
	extra["mailbox_equipment_type"] = "etc"
	return extra
}

// p7bWithoutMarkerKeys 只摘掉两个 GM 标记键，其余（含 expire_time/expire_unix/
// ExpireAt）保持逐字节相同 —— 用来构造"同一个实例但不带 GM 标记"的对照组。
func p7bWithoutMarkerKeys(extra map[string]string) map[string]string {
	cloned := map[string]string{}
	for key, value := range extra {
		switch key {
		case "expiration_source", "gm_expire_days":
			continue
		}
		cloned[key] = value
	}
	return cloned
}

func p7bRow(listType byte, slot int16, stack dnfrepo.ItemStack) []byte {
	entry := currentItemListEntryFromStack(listType, slot, stack)
	return append([]byte(nil), entry.data[:]...)
}

// p7bAssertSameExpirationOutcome 断言两条实例的"到期结果"一致：ExpireAt、
// expire_time/expire_unix、行字节。标记键（expiration_source/gm_expire_days）不参与，
// 因为"不消费标记"的意思正是：**这两个键对结果没有影响**（见下面各用例对它们
// 原样保留的单独断言）。
func p7bAssertSameExpirationOutcome(t *testing.T, marked, unmarked dnfrepo.ItemStack) {
	t.Helper()
	if !marked.ExpireAt.Equal(unmarked.ExpireAt) {
		t.Fatalf("到期时间不同：带标记=%s 不带标记=%s", marked.ExpireAt, unmarked.ExpireAt)
	}
	for _, key := range strings.Split(p7bEffectiveExpirationKeys, ",") {
		if marked.Extra[key] != unmarked.Extra[key] {
			t.Fatalf("Extra[%q] 不同：带标记=%q 不带标记=%q", key, marked.Extra[key], unmarked.Extra[key])
		}
	}
	if !reflect.DeepEqual(marked.RawEntry, unmarked.RawEntry) {
		t.Fatalf("行字节不同：\n带标记=%x\n不带标记=%x", marked.RawEntry, unmarked.RawEntry)
	}
	// 除标记键与到期结果键之外，其余键也必须一致（身份证据等不许被改写）。
	for key, value := range unmarked.Extra {
		if key == "expiration_source" || key == "gm_expire_days" {
			continue
		}
		if marked.Extra[key] != value {
			t.Fatalf("Extra[%q] 不同：带标记=%q 不带标记=%q", key, marked.Extra[key], value)
		}
	}
}

// ---------------------------------------------------------------------------
// 1. 开关：与写入端同一口径、同一个环境变量名
// ---------------------------------------------------------------------------

// Consumer contract: only explicit on (case/whitespace insensitive) enables
// imported expiration metadata. Writer behavior is tested in Cec1c/DNF90GM.
func TestGMExpirationOverrideGateContract(t *testing.T) {
	for _, tc := range []struct {
		value   string
		enabled bool
	}{
		{"", false}, {" ", false}, {"on", true}, {"ON", true}, {" on ", true},
		{"On", true}, {"oN", true}, {"off", false}, {"true", false}, {"1", false},
		{"yes", false}, {"0", false}, {"false", false}, {"enforce", false},
	} {
		t.Setenv(currentGMExpirationOverrideEnv, tc.value)
		if got := currentGMExpirationOverrideEnabled(); got != tc.enabled {
			t.Fatalf("gate(%q)=%t, want %t", tc.value, got, tc.enabled)
		}
	}
}

// ---------------------------------------------------------------------------
// 2. 写入点 ↔ 消费点：同一个键、同一层
// ---------------------------------------------------------------------------

// The game retains the persisted mail metadata contract after the sender
// implementation moves to its own repository. Native projection regressions
// below exercise the complete consumer path without importing the GM binary.
func TestGMExpirationOverridePersistedMailContract(t *testing.T) {
	if currentGMExpirationOverrideSource != "gm_override" || currentGMExpirationOverrideEnv != "DNFBRIDGE_GM_MAIL_OPTIONS" {
		t.Fatal("persisted GM mail contract changed")
	}
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Dir(currentFile)
	// 领取桥：mail/owner.go 的 mailboxAttachmentStack 必须把**附件 Extra 原样**
	// 带进落地堆栈 Extra（AST 断言，避免依赖 gofmt 对齐空格）。
	ownerPath := filepath.Join(root, "..", "..", "modules", "dnf", "mail", "owner.go")
	ownerRaw, err := os.ReadFile(ownerPath)
	if err != nil {
		t.Fatalf("read mail owner source: %v", err)
	}
	ownerParsed, err := parser.ParseFile(token.NewFileSet(), ownerPath, ownerRaw, 0)
	if err != nil {
		t.Fatalf("parse mail owner source: %v", err)
	}
	clonesAttachmentExtra, stackCarriesExtra := false, false
	for _, declaration := range ownerParsed.Decls {
		function, isFunction := declaration.(*ast.FuncDecl)
		if !isFunction || function.Name.Name != "mailboxAttachmentStack" || function.Body == nil {
			continue
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			switch typed := node.(type) {
			case *ast.AssignStmt:
				for _, right := range typed.Rhs {
					call, isCall := right.(*ast.CallExpr)
					if !isCall || len(call.Args) != 1 {
						continue
					}
					identifier, isIdent := call.Fun.(*ast.Ident)
					if !isIdent || identifier.Name != "cloneStringMap" {
						continue
					}
					selector, isSelector := call.Args[0].(*ast.SelectorExpr)
					if isSelector && selector.Sel.Name == "Extra" {
						clonesAttachmentExtra = true
					}
				}
			case *ast.KeyValueExpr:
				key, isIdent := typed.Key.(*ast.Ident)
				value, valueIsIdent := typed.Value.(*ast.Ident)
				if isIdent && valueIsIdent && key.Name == "Extra" && value.Name == "extra" {
					stackCarriesExtra = true
				}
			}
			return true
		})
	}
	if !clonesAttachmentExtra {
		t.Fatal("mailboxAttachmentStack 没有从 attachment.Extra 复制 Extra：附件上的 GM 标记进不了堆栈")
	}
	if !stackCarriesExtra {
		t.Fatal("mailboxAttachmentStack 没有把 extra 写进落地堆栈：两层没接上")
	}
}

// ---------------------------------------------------------------------------
// 3. 默认档：带标记的物品不产生任何不同的投影结果
// ---------------------------------------------------------------------------

// TestGMExpirationOverrideProjectionBranchIsGatedOff 直接钉住消费支的闸门本身：
// 开关不是显式 on 时，判定函数必须报告 handled=false、changed=false，并且输入
// 一个字节都不改（"off 档连标记都不看"的机械证明）；显式 on 时才接管。
//
// 这条对"把闸门写死成 true"这类变异是敏感的：一旦闸门失效，handled 会变 true。
func TestGMExpirationOverrideProjectionBranchIsGatedOff(t *testing.T) {
	now := time.Date(2026, time.September, 27, 12, 0, 0, 0, time.UTC)
	granted := p7bGrantedDeadline(now)
	marked := dnfrepo.ItemStack{
		ItemID:   int64(p7bStackableItemID),
		Count:    1,
		ExpireAt: granted,
		RawEntry: p7bRow(dnfrepo.MainInventoryListType, 75, dnfrepo.ItemStack{ItemID: int64(p7bStackableItemID), Count: 1, Extra: p7bGrantedExtra(granted, "30")}),
		Extra:    p7bGrantedExtra(granted, "30"),
	}
	for _, gate := range []string{"", "off", "true", "1", "yes"} {
		t.Run("gate="+strconv.Quote(gate), func(t *testing.T) {
			t.Setenv(currentGMExpirationOverrideEnv, gate)
			got, changed, handled := applyCurrentGMExpirationOverrideProjectionAt(marked, now)
			if handled {
				t.Fatalf("闸门失效：%q 档也接管了 GM 期限", gate)
			}
			if changed {
				t.Fatalf("闸门失效：%q 档报告了改动", gate)
			}
			if !reflect.DeepEqual(got, marked) {
				t.Fatalf("闸门失效：%q 档改了输入：\n%+v", gate, got)
			}
		})
	}
	t.Setenv(currentGMExpirationOverrideEnv, "on")
	got, _, handled := applyCurrentGMExpirationOverrideProjectionAt(marked, now)
	if !handled {
		t.Fatal("显式 on 档没有接管 GM 期限")
	}
	if !got.ExpireAt.Equal(granted) {
		t.Fatalf("on 档改掉了 GM 期限：%s", got.ExpireAt)
	}
}

// TestGMExpirationOverrideJoustItemKeepsGrantedDeadlineOnlyWhenOn 是**有判别力**的
// 那一对：马战 9 件（490005585..490005593）的"退役实例日期"规则
// （currentJoustEventHasRetiredInstanceExpiration，item_expiration_projection.go:100-111）
// 会让 PVF 期限吃掉比它早的实例日期 —— 这正是"库存投影里 GM 期限会输"的唯一现场。
//
//	off 档 = 今天的行为：PVF 的 2028 期限赢
//	on  档 = P7b：GM 给的那一天赢
func TestGMExpirationOverrideJoustItemKeepsGrantedDeadlineOnlyWhenOn(t *testing.T) {
	now := time.Date(2026, time.September, 27, 12, 0, 0, 0, time.UTC)
	granted := p7bGrantedDeadline(now)
	if !granted.Before(p7bPVFDeadline()) {
		t.Fatal("fixture 无效：GM 期限必须早于 PVF 期限，这条规则才会触发")
	}
	definition := p7bStackableDefinition()
	definition.ItemID = currentJoustEventItemFirst
	stack := dnfrepo.ItemStack{
		ItemID:   int64(currentJoustEventItemFirst),
		Count:    1,
		ExpireAt: granted,
		Extra:    p7bGrantedExtra(granted, "30"),
	}

	t.Setenv(currentGMExpirationOverrideEnv, "")
	offResult, _ := applyCurrentPVFItemExpirationAt(stack, definition, now)
	if !offResult.ExpireAt.Equal(p7bPVFDeadline()) {
		t.Fatalf("默认档应当保持今天的行为（马战退役规则让 PVF 期限赢）：got=%s want=%s",
			offResult.ExpireAt, p7bPVFDeadline())
	}

	t.Setenv(currentGMExpirationOverrideEnv, "on")
	onResult, changed := applyCurrentPVFItemExpirationAt(stack, definition, now)
	if !changed {
		t.Fatal("on 档应当补出行字节（changed=false 说明支路没走到）")
	}
	if !onResult.ExpireAt.Equal(granted) {
		t.Fatalf("on 档 GM 期限被 PVF 覆盖：got=%s want=%s", onResult.ExpireAt, granted)
	}
	if onResult.Extra["expire_time"] != strconv.FormatInt(granted.Unix(), 10) {
		t.Fatalf("on 档 expire_time=%q want=%d", onResult.Extra["expire_time"], granted.Unix())
	}
	if got := binary.LittleEndian.Uint32(onResult.RawEntry[currentItemListExpireTimeOffset : currentItemListExpireTimeOffset+4]); got != uint32(granted.Unix()) {
		t.Fatalf("on 档行 0x38=%d want=%d", got, uint32(granted.Unix()))
	}
	if onResult.Extra["expiration_source"] != currentGMExpirationOverrideSource {
		t.Fatalf("on 档标记被抹掉：%q", onResult.Extra["expiration_source"])
	}
}

// TestGMExpirationOverrideDefaultGateLeavesMarkedItemsUnconsumed 是 P7b 的
// "默认档零变化"钉子：开关未开时**不消费任何标记**。
//
// 对照组的构造是关键：把标记键（expiration_source / gm_expire_days）从同一个输入里
// 去掉，其余一个字节不动（含 expire_time/expire_unix/ExpireAt —— 那三个键由既有
// 的"实例到期优先"规则消费，与 GM 标记无关）。默认档下两者的到期结果必须完全一致，
// 且带标记那一侧的标记键必须原样保留（不消费 = 不改写、也不生效）。
func TestGMExpirationOverrideDefaultGateLeavesMarkedItemsUnconsumed(t *testing.T) {
	now := time.Date(2026, time.September, 27, 12, 0, 0, 0, time.UTC)
	granted := p7bGrantedDeadline(now)
	if granted.Equal(p7bPVFDeadline()) {
		t.Fatal("fixture 无效：GM 期限与 PVF 期限相同，无法判别谁赢了")
	}
	withUsablePeriod := p7bStackableDefinition()
	withUsablePeriod.UsablePeriodDays = 1

	equipmentExtra := func() map[string]string {
		extra := p7bGrantedExtra(granted, "30")
		extra["item_kind"] = string(dungeonDropItemEquipment)
		extra["equipment_type"] = "[weapon]"
		return extra
	}
	avatarExtra := func() map[string]string {
		extra := p7bGrantedExtra(granted, "30")
		extra["item_kind"] = "avatar"
		extra["equipment_type"] = "[avatar] coat"
		extra["avatar_socket_data"] = "0102"
		return extra
	}

	cases := []struct {
		name string
		// pvfOverridesMarkerKey 报告这条定义的 PVF 路径本来就会把 expiration_source
		// 改写成 runtime_pvf_usable_period_grant（item_expiration_projection.go:305-315）。
		pvfOverridesMarkerKey bool
		stack                 dnfrepo.ItemStack
		definition            dungeonDropItemDefinition
	}{
		{
			name:                  "堆叠物/相对usable period",
			pvfOverridesMarkerKey: true,
			stack:                 dnfrepo.ItemStack{ItemID: int64(p7bStackableItemID), Count: 1, ExpireAt: granted, Extra: p7bGrantedExtra(granted, "30")},
			definition:            withUsablePeriod,
		},
		{
			name:       "堆叠物/PVF绝对日期",
			stack:      dnfrepo.ItemStack{ItemID: int64(p7bStackableItemID), Count: 1, ExpireAt: granted, Extra: p7bGrantedExtra(granted, "30")},
			definition: p7bStackableDefinition(),
		},
		{
			name:       "装备条目",
			stack:      dnfrepo.ItemStack{ItemID: int64(p7bEquipmentItemID), Count: 1, ExpireAt: granted, Extra: equipmentExtra()},
			definition: p7bEquipmentDefinition(),
		},
		{
			name:       "时装条目",
			stack:      dnfrepo.ItemStack{ItemID: int64(p7bEquipmentItemID), Count: 1, ExpireAt: granted, Extra: avatarExtra()},
			definition: p7bEquipmentDefinition(),
		},
		{
			name:       "永久堆叠物（PVF 无到期日）",
			stack:      dnfrepo.ItemStack{ItemID: int64(p7bStackableItemID), Count: 3, ExpireAt: granted, Extra: p7bGrantedExtra(granted, "30")},
			definition: dungeonDropItemDefinition{ItemID: p7bStackableItemID, Kind: dungeonDropItemStackable, PVFPath: p7bStackablePath, StackableType: "[etc]", StackLimit: 1000},
		},
	}

	gates := []string{"", "off", "true", "1", "yes"}
	for _, gate := range gates {
		for _, testCase := range cases {
			t.Run("gate="+strconv.Quote(gate)+"/"+testCase.name, func(t *testing.T) {
				t.Setenv(currentGMExpirationOverrideEnv, gate)

				// 对照组：只摘掉两个标记键，其余（含 expire_time/expire_unix/
				// ExpireAt/RawEntry）与带标记那一侧逐字节相同。
				control := testCase.stack
				control.Extra = p7bWithoutMarkerKeys(testCase.stack.Extra)

				markedResult, markedChanged := applyCurrentPVFItemExpirationAt(testCase.stack, testCase.definition, now)
				controlResult, controlChanged := applyCurrentPVFItemExpirationAt(control, testCase.definition, now)

				p7bAssertSameExpirationOutcome(t, markedResult, controlResult)
				if markedChanged != controlChanged {
					t.Fatalf("changed 不一致（说明标记键影响了投影）：带标记=%t 对照组=%t", markedChanged, controlChanged)
				}

				// 标记键：PVF 路径本来就会改写 expiration_source 的定义除外
				// （相对 [usable period]，item_expiration_projection.go:305-315），
				// 其余情形必须原样保留 —— 不消费 = 不生效也不改写。
				if testCase.pvfOverridesMarkerKey {
					if markedResult.Extra["expiration_source"] != "runtime_pvf_usable_period_grant" {
						t.Fatalf("PVF 路径应当自己覆盖 expiration_source：%q", markedResult.Extra["expiration_source"])
					}
					// 该分支会把 expiration_source 归一化掉；剩下的差异只允许是
					// gm_expire_days 这个纯记账键（对照组本来就没有它）。
					for key, value := range controlResult.Extra {
						if key == "gm_expire_days" {
							continue
						}
						if markedResult.Extra[key] != value {
							t.Fatalf("默认档结果与对照组不同：Extra[%q]=%q 对照组=%q",
								key, markedResult.Extra[key], value)
						}
					}
					if len(markedResult.Extra) != len(controlResult.Extra)+1 {
						t.Fatalf("默认档 Extra 键数不对：带标记=%d 对照组=%d",
							len(markedResult.Extra), len(controlResult.Extra))
					}
					return
				}
				if markedResult.Extra["expiration_source"] != currentGMExpirationOverrideSource {
					t.Fatalf("默认档改写了标记键：%q", markedResult.Extra["expiration_source"])
				}
				if markedResult.Extra["gm_expire_days"] != "30" {
					t.Fatalf("默认档改写了 gm_expire_days：%q", markedResult.Extra["gm_expire_days"])
				}
			})
		}
	}
}

// TestGMExpirationOverrideUnmarkedItemsByteIdenticalAcrossGateStates 钉住
// "只影响带标记的物品"：不带标记的堆叠物/装备/时装，在开关 off 与 on 两种档位下
// 投影结果逐字节相同（含行字节）。
func TestGMExpirationOverrideUnmarkedItemsByteIdenticalAcrossGateStates(t *testing.T) {
	now := time.Date(2026, time.September, 27, 12, 0, 0, 0, time.UTC)
	stackable := dnfrepo.ItemStack{
		ItemID: int64(p7bStackableItemID), Count: 2,
		Extra: map[string]string{"item_kind": string(dungeonDropItemStackable), "stackable_type": "[etc]", "pvf_path": p7bStackablePath},
	}
	stackable.RawEntry = p7bRow(dnfrepo.MainInventoryListType, 75, stackable)
	equipment := dnfrepo.ItemStack{
		ItemID: int64(p7bEquipmentItemID), Count: 1,
		Extra: map[string]string{"item_kind": string(dungeonDropItemEquipment), "equipment_type": "[weapon]", "durability": "50", "max_durability": "50"},
	}
	equipment.RawEntry = p7bRow(dnfrepo.MainInventoryListType, 3, equipment)
	avatar := dnfrepo.ItemStack{
		ItemID: int64(p7bEquipmentItemID), Count: 1,
		Extra: map[string]string{"item_kind": "avatar", "equipment_type": "[avatar] coat", "avatar_socket_data": "0102"},
	}
	avatar.RawEntry = p7bRow(currentAvatarInventoryListType, 5, avatar)

	cases := []struct {
		name       string
		stack      dnfrepo.ItemStack
		definition dungeonDropItemDefinition
	}{
		{name: "堆叠物", stack: stackable, definition: p7bStackableDefinition()},
		{name: "装备", stack: equipment, definition: p7bEquipmentDefinition()},
		{name: "时装", stack: avatar, definition: p7bEquipmentDefinition()},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv(currentGMExpirationOverrideEnv, "")
			offResult, offChanged := applyCurrentPVFItemExpirationAt(testCase.stack, testCase.definition, now)
			t.Setenv(currentGMExpirationOverrideEnv, "on")
			onResult, onChanged := applyCurrentPVFItemExpirationAt(testCase.stack, testCase.definition, now)
			if offChanged != onChanged || !reflect.DeepEqual(offResult, onResult) {
				t.Fatalf("无标记物品在两个档位下不同：\noff(changed=%t)=%+v\non(changed=%t)=%+v",
					offChanged, offResult, onChanged, onResult)
			}
			if !offResult.ExpireAt.Equal(p7bPVFDeadline()) {
				t.Fatalf("无标记物品的 PVF 投影失效：%s", offResult.ExpireAt)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 4. 开关 on：堆叠物保留 GM 期限；装备路径仍不受影响
// ---------------------------------------------------------------------------

// TestGMExpirationOverrideStackableKeepsGrantedDeadline 是 P7b 的主行为：
// 开关 on 时，带 expiration_source=gm_override 的堆叠物在登录投影里保留 GM 期限，
// PVF 的 2028 绝对日期不得覆盖它；重复投影幂等。
func TestGMExpirationOverrideStackableKeepsGrantedDeadline(t *testing.T) {
	t.Setenv(currentGMExpirationOverrideEnv, "on")
	now := time.Date(2026, time.September, 27, 12, 0, 0, 0, time.UTC)
	granted := p7bGrantedDeadline(now)
	if granted.Equal(p7bPVFDeadline()) {
		t.Fatal("fixture 无效：GM 期限与 PVF 期限相同，无法判别谁赢了")
	}
	definition := p7bStackableDefinition()

	// (a) 刚领取的形状：gmtool 不写行字节，所以 RawEntry 为空。
	claimed := dnfrepo.ItemStack{
		ItemID:   int64(p7bStackableItemID),
		Count:    1,
		ExpireAt: granted,
		Extra:    p7bGrantedExtra(granted, "30"),
	}
	projected, changed := applyCurrentPVFItemExpirationAt(claimed, definition, now)
	if !changed {
		t.Fatal("首次投影应当为领取形状补出行字节（changed=false 说明整条支路没走到）")
	}
	if !projected.ExpireAt.Equal(granted) {
		t.Fatalf("GM 期限被覆盖：got=%s want=%s（PVF=%s）", projected.ExpireAt, granted, p7bPVFDeadline())
	}
	if got := projected.Extra["expire_time"]; got != strconv.FormatInt(granted.Unix(), 10) {
		t.Fatalf("expire_time=%q want=%d", got, granted.Unix())
	}
	if projected.Extra["expire_unix"] != strconv.FormatInt(granted.Unix(), 10) {
		t.Fatalf("expire_unix=%q want=%d", projected.Extra["expire_unix"], granted.Unix())
	}
	if projected.Extra["expiration_source"] != currentGMExpirationOverrideSource {
		t.Fatalf("标记被抹掉：%q", projected.Extra["expiration_source"])
	}
	if projected.Extra["gm_expire_days"] != "30" {
		t.Fatalf("gm_expire_days=%q want=30", projected.Extra["gm_expire_days"])
	}
	if len(projected.RawEntry) != currentItemListEntryWireSize {
		t.Fatalf("行长度=%d want=%d", len(projected.RawEntry), currentItemListEntryWireSize)
	}
	if got := binary.LittleEndian.Uint32(projected.RawEntry[currentItemListExpireTimeOffset : currentItemListExpireTimeOffset+4]); got != uint32(granted.Unix()) {
		t.Fatalf("行 0x38=%d want=%d（PVF 的是 %d）", got, uint32(granted.Unix()), uint32(p7bPVFDeadline().Unix()))
	}
	if got := binary.LittleEndian.Uint32(projected.RawEntry[legacyWrongCurrentItemListExpireTimeOffset : legacyWrongCurrentItemListExpireTimeOffset+4]); got != 0 {
		t.Fatalf("行旧错位 0x6E 被写了：%d", got)
	}
	if got := binary.LittleEndian.Uint16(projected.RawEntry[0x0B:0x0D]); got != 0xFFFF {
		// 30 天 = 2592000 秒，超过 u16 上限，按既有口径钳到 0xFFFF。
		t.Fatalf("行 0x0B（剩余使用期限秒）=%d want=65535", got)
	}
	if projected.Extra["expire_time"] == strconv.FormatInt(p7bPVFDeadline().Unix(), 10) {
		t.Fatal("Extra 里的到期时间变成了 PVF 期限")
	}

	// (b) 幂等：同一条实例重复投影不改任何字节。
	again, changedAgain := applyCurrentPVFItemExpirationAt(projected, definition, now)
	if changedAgain {
		t.Fatalf("重复投影不幂等：changed=true got=%+v", again)
	}
	if !reflect.DeepEqual(again, projected) {
		t.Fatalf("重复投影改了字节：\n第一次=%+v\n第二次=%+v", projected, again)
	}

	// (c) GM 期限已经过去时仍然保留（不许借 PVF 的期限把过期实例静默续活）。
	expired := time.Unix(now.Unix()-48*3600, 0).UTC()
	stale := dnfrepo.ItemStack{ItemID: int64(p7bStackableItemID), Count: 1, ExpireAt: expired, Extra: p7bGrantedExtra(expired, "30")}
	staleResult, _ := applyCurrentPVFItemExpirationAt(stale, definition, now)
	if !staleResult.ExpireAt.Equal(expired) {
		t.Fatalf("已过期的 GM 期限被静默续活：got=%s want=%s", staleResult.ExpireAt, expired)
	}
}

// TestGMExpirationOverrideEquipmentEntryFollowsUpstreamBranch 钉住装备**条目**的
// 投影与上游同形（UP item_expiration_projection.go:180-192 位于所有分支之前，
// normalizeCurrentPVFEquipmentEntry 也走同一个函数）：带标记时保留 GM 期限。
// 不带标记的装备逐字节不变由 TestGMExpirationOverrideUnmarkedItemsByteIdenticalAcrossGateStates 钉住。
func TestGMExpirationOverrideEquipmentEntryFollowsUpstreamBranch(t *testing.T) {
	t.Setenv(currentGMExpirationOverrideEnv, "on")
	now := time.Date(2026, time.September, 27, 12, 0, 0, 0, time.UTC)
	granted := p7bGrantedDeadline(now)

	extra := p7bGrantedExtra(granted, "30")
	extra["item_kind"] = string(dungeonDropItemEquipment)
	extra["equipment_type"] = "[weapon]"
	extra["durability"] = "50"
	extra["max_durability"] = "50"
	entry := dnfrepo.EquipmentEntry{
		SlotIndex: 0,
		ItemID:    int64(p7bEquipmentItemID),
		ExpireAt:  granted,
		RawEntry:  make([]byte, currentItemListEntryWireSize),
		Extra:     extra,
	}
	projected, _ := normalizeCurrentPVFEquipmentEntry(entry, p7bEquipmentDefinition(), now)
	if !projected.ExpireAt.Equal(granted) {
		t.Fatalf("装备条目的 GM 期限被覆盖：got=%s want=%s（PVF=%s）", projected.ExpireAt, granted, p7bPVFDeadline())
	}
	if projected.Extra["expiration_source"] != currentGMExpirationOverrideSource {
		t.Fatalf("装备条目标记被抹掉：%q", projected.Extra["expiration_source"])
	}
	if got := binary.LittleEndian.Uint32(projected.RawEntry[currentItemListExpireTimeOffset : currentItemListExpireTimeOffset+4]); got != uint32(granted.Unix()) {
		t.Fatalf("装备行 0x38=%d want=%d", got, uint32(granted.Unix()))
	}
}

// TestGMExpirationOverrideLeavesEquipmentMailAttachmentRepairByteIdentical 钉住
// P7 报告 §5.2 的那条性质仍然成立：**邮件附件修补**里装备/时装本来就不被重算
// （非堆叠物在 joust_settlement_mail_expiration.go 里 continue），所以带不带 GM 标记、
// 开不开开关，装备附件的 ExpireAt 都一个字节不动。
func TestGMExpirationOverrideLeavesEquipmentMailAttachmentRepairByteIdentical(t *testing.T) {
	ctx := context.Background()
	granted := p7bGrantedDeadline(time.Now().UTC())

	run := func(t *testing.T, gate string) (dnfrepo.MailAttachment, int) {
		t.Helper()
		t.Setenv(currentGMExpirationOverrideEnv, gate)
		repositories := dnfrepomemory.NewMemoryGroup()
		if err := repositories.Mailbox.Save(ctx, dnfrepo.MailboxRecord{
			CharacterID: "31",
			Mails: map[string]dnfrepo.MailRecord{
				"5": {
					MailID:    "5",
					CreatedAt: time.Now().UTC(),
					Attachments: []dnfrepo.MailAttachment{{
						ItemID:   int64(p7bEquipmentItemID),
						Count:    1,
						ExpireAt: granted,
						// 照抄 P7 发放端的装备附件形状（grantAttachmentExtra 会写
						// durability/max_durability），这样登录期修补无事可做、
						// changed 必然为 0。
						Extra: map[string]string{
							"expiration_source": currentGMExpirationOverrideSource,
							"gm_expire_days":    "30",
							"durability":        "50",
							"max_durability":    "50",
							p7bMailboxKindKey:   string(dungeonDropItemEquipment),
						},
					}},
				},
			},
		}); err != nil {
			t.Fatal(err)
		}
		service := &Service{
			pvfItemCatalog:     mustP7BCatalog(t),
			repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
		}
		changed, err := service.reconcileCurrentMailAttachmentExpirations(ctx, 31)
		if err != nil {
			t.Fatalf("repair: %v", err)
		}
		mailbox, found, err := repositories.Mailbox.Load(ctx, "31")
		if err != nil || !found {
			t.Fatalf("load mailbox found=%t err=%v", found, err)
		}
		return mailbox.Mails["5"].Attachments[0], changed
	}

	offAttachment, offChanged := run(t, "")
	onAttachment, onChanged := run(t, "on")
	if offChanged != 0 || onChanged != 0 {
		t.Fatalf("装备附件被当成堆叠物重算了：off=%d on=%d", offChanged, onChanged)
	}
	if !offAttachment.ExpireAt.Equal(granted) || !onAttachment.ExpireAt.Equal(granted) {
		t.Fatalf("装备附件到期时间被改写：off=%s on=%s want=%s", offAttachment.ExpireAt, onAttachment.ExpireAt, granted)
	}
	if !reflect.DeepEqual(offAttachment, onAttachment) {
		t.Fatalf("开关档位影响了装备附件修补：\noff=%+v\non=%+v", offAttachment, onAttachment)
	}
}

// ---------------------------------------------------------------------------
// 5. 不静默：标记在、值非法 -> 具名事件 + 确定行为
// ---------------------------------------------------------------------------

type p7bRecordedEvent struct {
	kind   string
	fields []any
}

func (event p7bRecordedEvent) field(name string) string {
	for index := 0; index+1 < len(event.fields); index += 2 {
		key, isString := event.fields[index].(string)
		if !isString || key != name {
			continue
		}
		switch value := event.fields[index+1].(type) {
		case string:
			return strings.TrimSpace(value)
		case int:
			return strconv.Itoa(value)
		case int64:
			return strconv.FormatInt(value, 10)
		case uint32:
			return strconv.FormatUint(uint64(value), 10)
		default:
			return ""
		}
	}
	return ""
}

func captureP7BEvents(t *testing.T) *[]p7bRecordedEvent {
	t.Helper()
	recorded := &[]p7bRecordedEvent{}
	previous := currentGMExpirationOverrideInvalidEventSink
	currentGMExpirationOverrideInvalidEventSink = func(kind string, fields ...any) {
		*recorded = append(*recorded, p7bRecordedEvent{kind: kind, fields: fields})
	}
	t.Cleanup(func() { currentGMExpirationOverrideInvalidEventSink = previous })
	return recorded
}

// TestGMExpirationOverrideInvalidMarkerIsLoudAndBounded 钉住三种"标记在、值不可用"
// 的情形：一定记具名事件、一定不 panic、行为确定（不是静默，也不改写别的字段）。
func TestGMExpirationOverrideInvalidMarkerIsLoudAndBounded(t *testing.T) {
	now := time.Date(2026, time.September, 27, 12, 0, 0, 0, time.UTC)
	granted := p7bGrantedDeadline(now)
	definition := p7bStackableDefinition()

	t.Run("标记在但没有期限:回退到PVF并记事件", func(t *testing.T) {
		t.Setenv(currentGMExpirationOverrideEnv, "on")
		recorded := captureP7BEvents(t)
		stack := dnfrepo.ItemStack{
			ItemID: int64(p7bStackableItemID), Count: 1,
			Extra: map[string]string{
				"item_kind":         string(dungeonDropItemStackable),
				"expiration_source": currentGMExpirationOverrideSource,
				"gm_expire_days":    "30",
			},
		}
		got, _ := applyCurrentPVFItemExpirationAt(stack, definition, now)
		if !got.ExpireAt.Equal(p7bPVFDeadline()) {
			t.Fatalf("没有期限时应当回退到今天的行为（PVF 期限 %s），实际 %s", p7bPVFDeadline(), got.ExpireAt)
		}
		if len(*recorded) != 1 {
			t.Fatalf("事件条数=%d want=1（%+v）", len(*recorded), *recorded)
		}
		event := (*recorded)[0]
		if event.kind != currentGMExpirationOverrideInvalidEvent {
			t.Fatalf("事件名=%q want=%q", event.kind, currentGMExpirationOverrideInvalidEvent)
		}
		if event.field("reason") != "missing_deadline" || event.field("result") != "pvf_fallback" {
			t.Fatalf("事件字段不对：reason=%q result=%q", event.field("reason"), event.field("result"))
		}
		if event.field("item_id") != strconv.FormatUint(uint64(p7bStackableItemID), 10) {
			t.Fatalf("事件 item_id=%q", event.field("item_id"))
		}
	})

	t.Run("gm_expire_days非法:保留GM期限且不写回非法值", func(t *testing.T) {
		t.Setenv(currentGMExpirationOverrideEnv, "on")
		for _, days := range []string{"abc", "99999999999999999999", "-5", "0"} {
			t.Run("days="+days, func(t *testing.T) {
				recorded := captureP7BEvents(t)
				stack := dnfrepo.ItemStack{ItemID: int64(p7bStackableItemID), Count: 1, ExpireAt: granted, Extra: p7bGrantedExtra(granted, days)}
				got, _ := applyCurrentPVFItemExpirationAt(stack, definition, now)
				if !got.ExpireAt.Equal(granted) {
					t.Fatalf("非法天数不该丢掉 GM 期限：got=%s want=%s（PVF=%s）", got.ExpireAt, granted, p7bPVFDeadline())
				}
				if got.Extra["gm_expire_days"] != days {
					t.Fatalf("非法 gm_expire_days 被改写：%q -> %q", days, got.Extra["gm_expire_days"])
				}
				if got.Extra["expiration_source"] != currentGMExpirationOverrideSource {
					t.Fatalf("标记被抹掉：%q", got.Extra["expiration_source"])
				}
				if len(*recorded) != 1 {
					t.Fatalf("事件条数=%d want=1（%+v）", len(*recorded), *recorded)
				}
				event := (*recorded)[0]
				if event.field("reason") != "invalid_expire_days" || event.field("result") != "gm_deadline_kept" {
					t.Fatalf("事件字段不对：reason=%q result=%q", event.field("reason"), event.field("result"))
				}
				if event.field("gm_expire_days") != days {
					t.Fatalf("事件没有带上非法的原始值：%q", event.field("gm_expire_days"))
				}
			})
		}
	})

	t.Run("缺gm_expire_days记账键:保留期限并记事件", func(t *testing.T) {
		t.Setenv(currentGMExpirationOverrideEnv, "on")
		recorded := captureP7BEvents(t)
		stack := dnfrepo.ItemStack{ItemID: int64(p7bStackableItemID), Count: 1, ExpireAt: granted, Extra: p7bGrantedExtra(granted, "")}
		got, _ := applyCurrentPVFItemExpirationAt(stack, definition, now)
		if !got.ExpireAt.Equal(granted) {
			t.Fatalf("缺记账键不该丢掉 GM 期限：got=%s want=%s", got.ExpireAt, granted)
		}
		if _, exists := got.Extra["gm_expire_days"]; exists {
			t.Fatalf("不该凭空补 gm_expire_days：%q", got.Extra["gm_expire_days"])
		}
		if len(*recorded) != 1 || (*recorded)[0].field("reason") != "missing_expire_days" ||
			(*recorded)[0].field("result") != "gm_deadline_kept" {
			t.Fatalf("事件不对：%+v", *recorded)
		}
	})

	t.Run("默认档对非法标记一声不吭(不消费标记)", func(t *testing.T) {
		t.Setenv(currentGMExpirationOverrideEnv, "")
		recorded := captureP7BEvents(t)
		stack := dnfrepo.ItemStack{
			ItemID: int64(p7bStackableItemID), Count: 1,
			Extra: map[string]string{"item_kind": string(dungeonDropItemStackable), "expiration_source": currentGMExpirationOverrideSource},
		}
		got, _ := applyCurrentPVFItemExpirationAt(stack, definition, now)
		if len(*recorded) != 0 {
			t.Fatalf("默认档不该记 GM 事件：%+v", *recorded)
		}
		if !got.ExpireAt.Equal(p7bPVFDeadline()) {
			t.Fatalf("默认档行为应当与今天相同：%s", got.ExpireAt)
		}
	})

	t.Run("邮件附件修补:标记在但期限为零时记事件并回退", func(t *testing.T) {
		ctx := context.Background()
		t.Setenv(currentGMExpirationOverrideEnv, "on")
		recorded := captureP7BEvents(t)
		repositories := dnfrepomemory.NewMemoryGroup()
		createdAt := time.Now().UTC().Truncate(time.Second)
		if err := repositories.Mailbox.Save(ctx, dnfrepo.MailboxRecord{
			CharacterID: "41",
			Mails: map[string]dnfrepo.MailRecord{
				"2": {
					MailID:    "2",
					CreatedAt: createdAt,
					Attachments: []dnfrepo.MailAttachment{{
						ItemID: int64(p7bStackableItemID),
						Count:  1,
						Extra:  map[string]string{"expiration_source": currentGMExpirationOverrideSource},
					}},
				},
			},
		}); err != nil {
			t.Fatal(err)
		}
		service := &Service{
			pvfItemCatalog:     mustP7BCatalog(t),
			repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
		}
		changed, err := service.reconcileCurrentMailAttachmentExpirations(ctx, 41)
		if err != nil {
			t.Fatalf("repair: %v", err)
		}
		if changed != 1 {
			t.Fatalf("没有期限的标记应当回退到今天的行为（按 PVF 补一次），changed=%d", changed)
		}
		if len(*recorded) != 1 || (*recorded)[0].field("reason") != "missing_deadline" ||
			(*recorded)[0].field("result") != "pvf_fallback" {
			t.Fatalf("事件不对：%+v", *recorded)
		}
	})
}

// ---------------------------------------------------------------------------
// 6. 端到端：写入层的附件形状 -> 登录期修补 -> 领取 -> 登录期背包投影
// ---------------------------------------------------------------------------

// TestGMExpirationOverrideSurvivesMailRepairClaimAndRelogin 把两层接起来跑一遍：
//
//	gmtool 发放写下的附件形状（Extra: expire_* + expiration_source=gm_override + gm_expire_days）
//	  -> 登录期邮件附件修补（joust_settlement_mail_expiration.go）
//	  -> 玩家领取（mail/owner.go 的 mailboxAttachmentStack）
//	  -> 登录期背包投影（item_expiration_owner.go）
//
// 开关 on 时 GM 期限必须一路活下来；开关 off（= 今天）时它必须在第一步就被 PVF
// 期限覆盖回去 —— 后者同时是本条断言的证伪对照：修好之前两种档位都是 off 的结果。
func TestGMExpirationOverrideSurvivesMailRepairClaimAndRelogin(t *testing.T) {
	ctx := context.Background()
	granted := p7bGrantedDeadline(time.Now().UTC())
	if granted.Equal(p7bPVFDeadline()) {
		t.Fatal("fixture 无效：GM 期限与 PVF 期限相同")
	}

	run := func(t *testing.T, gate string) (dnfrepo.ItemStack, int16) {
		t.Helper()
		t.Setenv(currentGMExpirationOverrideEnv, gate)
		now := time.Now().UTC()
		repositories := dnfrepomemory.NewMemoryGroup()
		if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
			CharacterID: "77", AccountID: "acc-p7b", Name: "claimer", Job: "0", Level: 90,
		}); err != nil {
			t.Fatalf("save character: %v", err)
		}
		if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
			CharacterID: "77", Slots: map[string]dnfrepo.ItemStack{},
		}); err != nil {
			t.Fatalf("save inventory: %v", err)
		}
		// 附件的 Extra 逐键照抄 P7 发放端写下的形状。
		if err := repositories.Mailbox.Save(ctx, dnfrepo.MailboxRecord{
			CharacterID: "77",
			Mails: map[string]dnfrepo.MailRecord{
				"1": {
					MailID:    "1",
					CreatedAt: now,
					Attachments: []dnfrepo.MailAttachment{{
						ItemID:   int64(p7bStackableItemID),
						Count:    1,
						ExpireAt: granted,
						Extra:    p7bGrantedExtra(granted, "30"),
					}},
				},
			},
		}); err != nil {
			t.Fatalf("save mailbox: %v", err)
		}

		service := &Service{
			options:            options{gameUpperHeader: gameUpperHeaderServer16, gameUpperBodyCodec: gameUpperBodyCodecPlain},
			pvfItemCatalog:     mustP7BCatalog(t),
			repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
		}

		// 第 1 步：登录期邮件附件修补 —— P7 报告 §5.2 点名的重算点。
		repaired, err := service.reconcileCurrentMailAttachmentExpirations(ctx, 77)
		if err != nil {
			t.Fatalf("mail repair: %v", err)
		}
		mailbox, found, err := repositories.Mailbox.Load(ctx, "77")
		if err != nil || !found {
			t.Fatalf("load mailbox found=%t err=%v", found, err)
		}
		attachment := mailbox.Mails["1"].Attachments[0]
		if gate == "on" {
			if repaired != 0 || !attachment.ExpireAt.Equal(granted) {
				t.Fatalf("开关 on 时登录期修补改写了 GM 期限：repaired=%d expire=%s want=%s", repaired, attachment.ExpireAt, granted)
			}
		} else if !attachment.ExpireAt.Equal(p7bPVFDeadline()) {
			t.Fatalf("开关 off 时（= 今天）应当被 PVF 期限覆盖：expire=%s want=%s", attachment.ExpireAt, p7bPVFDeadline())
		}

		// 第 2 步：领取。附件 Extra 必须原样进堆栈。
		session := &gameSession{
			conn:                &bufferConn{},
			channel:             channelcatalog.Channel{ID: 38, Type: 1, Name: "ch.38", Port: 10038},
			selectedCharacterID: 77,
		}
		service.bindGameSessionCharacter(session, 77)
		handled, err := service.handleAlignedGameCommand(
			session,
			byte(dnfenum.GameCmdCommand),
			uint16(dnfenum.CmdPacketMailboxExtractItem),
			mailIDListBody(1),
		)
		if err != nil || !handled {
			t.Fatalf("mailbox extract handled=%t err=%v", handled, err)
		}

		// 第 3 步：登录期背包投影（重登）。
		if _, err := service.reconcileCurrentPVFItemExpirations(ctx, repositories, "77", "acc-p7b"); err != nil {
			t.Fatalf("inventory reconcile: %v", err)
		}
		inventory, found, err := repositories.Inventory.Load(ctx, "77")
		if err != nil || !found {
			t.Fatalf("load inventory found=%t err=%v", found, err)
		}
		final, slot := dnfrepo.ItemStack{}, int16(-1)
		for key, stack := range inventory.Slots {
			if stack.ItemID != int64(p7bStackableItemID) {
				continue
			}
			_, parsedSlot, ok := parseSceneInventorySlotKey(key)
			if !ok {
				t.Fatalf("堆栈落在一个无法解析的槽位键上：%q", key)
			}
			final, slot = stack, parsedSlot
			break
		}
		if final.ItemID != int64(p7bStackableItemID) {
			t.Fatalf("重登后找不到那条堆栈：%+v", inventory.Slots)
		}
		if final.Extra["expiration_source"] != currentGMExpirationOverrideSource {
			t.Fatalf("领取桥/重登没有把 GM 标记带下来：%+v", final.Extra)
		}
		return final, slot
	}

	t.Run("开关on:GM期限一路活到重登之后", func(t *testing.T) {
		on, slot := run(t, "on")
		if !on.ExpireAt.Equal(granted) {
			t.Fatalf("重登后 GM 期限被覆盖：got=%s want=%s（PVF=%s）", on.ExpireAt, granted, p7bPVFDeadline())
		}
		if on.Extra["gm_expire_days"] != "30" {
			t.Fatalf("重登后 gm_expire_days 丢失：%+v", on.Extra)
		}
		if on.Extra["expire_time"] != strconv.FormatInt(granted.Unix(), 10) {
			t.Fatalf("重登后 expire_time=%q want=%d", on.Extra["expire_time"], granted.Unix())
		}
		entry := currentItemListEntryFromStack(dnfrepo.MainInventoryListType, slot, on)
		if got := binary.LittleEndian.Uint32(entry.data[currentItemListExpireTimeOffset : currentItemListExpireTimeOffset+4]); got != uint32(granted.Unix()) {
			t.Fatalf("重登后行 0x38=%d want=%d", got, uint32(granted.Unix()))
		}
	})

	// 开关 off 的对照只断言"今天的行为"发生在哪一层：登录期邮件附件修补会把附件
	// ExpireAt 改成 PVF 期限（这一步由 run 内部断言）。**注意**：附件 Extra 里的
	// expire_time 仍是 GM 的值，而库存投影里"实例到期优先"这条既有规则读的正是
	// Extra，所以落地堆栈的 ExpireAt 可能又被拉回 GM 日期 —— 那是 P7b 之前就有的
	// 行为（与标记无关），这里不重复断言，免得把既有行为错记成本批的语义。
	t.Run("开关off:邮件层的PVF覆盖仍然发生(=今天)", func(t *testing.T) {
		off, _ := run(t, "")
		if off.Extra["expiration_source"] != currentGMExpirationOverrideSource {
			t.Fatalf("领取桥应当原样搬运 Extra：%+v", off.Extra)
		}
	})
}

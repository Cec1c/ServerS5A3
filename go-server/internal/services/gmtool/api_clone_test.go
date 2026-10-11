package gmtool

// 本文件是「复制角色」（api_clone.go）的回归用例。
//
// 覆盖三类东西：
//
//	1. 前端契约（字段名/取值）—— character.js:217/334/361/398 读写的字段，逐字段断言；
//	2. 移植过来的语义 —— A21 CharacterClone*.cs 的名字校验（:923-931）、选项规范化
//	   （:265-274）、槽位分配（:856-889）、"复制永不破坏源角色"（:251-258）；
//	3. 安全声明 —— cloneOptionOwnedTables 必须为每个选项、每张表声明归属列
//	   （A21 CharacterCloneSafety.cs:46-75 的静态版本）。
//
// 需要 s.db（启动器账号表）的那半段路由不在这里：本包的单测没有数据库，所以
// clone-plan 只测 clonePlanPayload、clone 只测 applyCharacterClone —— 与
// api_accounts.go:201-206 把 applyAccountCurrency 单独拆出来的理由完全相同。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	dnfskillcmd "longheng.io/server/internal/modules/dnf/skillcmd"
)

// cloneHarness 是最小测试台：一个内存仓储组 + 一个只依赖它的 Server。
// 不需要 s.db 的那些入口（name-available、applyCharacterClone、clonePlanPayload）
// 都能直接调用。
type cloneHarness struct {
	t      *testing.T
	server *Server
	group  dnfrepo.Group
}

func cloneNewHarness(t *testing.T) *cloneHarness {
	t.Helper()
	group := memory.NewMemoryGroup()
	server := &Server{
		cfg:      Config{Logger: func(string, ...any) {}},
		deps:     Deps{Repos: func() (dnfrepo.Group, bool) { return group, true }},
		pvf:      newPVFCache(),
		sessions: map[string]time.Time{},
	}
	return &cloneHarness{t: t, server: server, group: group}
}

func (h *cloneHarness) ctx() context.Context { return context.Background() }

// cloneSourceFixture 是"源角色"的完整夹具：基础行 + 每个可复制子记录各一条。
type cloneSourceFixture struct {
	character      dnfrepo.CharacterRecord
	inventory      dnfrepo.InventoryRecord
	equipment      dnfrepo.EquipmentRecord
	skill          dnfrepo.SkillRecord
	skillCommands  dnfrepo.SettingsRecord
	quest          dnfrepo.QuestRecord
	pet            dnfrepo.PetRecord
	containerState dnfrepo.SettingsRecord
	dungeon        []dnfrepo.DungeonPermissionEntry
}

func cloneNewSourceFixture() cloneSourceFixture {
	return cloneSourceFixture{
		character: dnfrepo.CharacterRecord{
			CharacterID: "1",
			AccountID:   "dnf:10001",
			Slot:        0,
			Name:        "源角色",
			Job:         "11",
			Level:       90,
			Stats: map[string]int64{
				"delete_flag":      0,
				"grow_type":        1,
				"gold":             12345,
				"extra_custom_key": 7,
				"roster_object_id": 1,
			},
			Location: dnfrepo.CharacterLocation{ChannelID: 2, TownID: 38, DungeonID: 0, RoomID: ""},
			Roster: dnfrepo.CharacterRoster{
				Header: dnfrepo.CharacterRosterHeader{TotalOrSlotLimit: 32},
				Entry: dnfrepo.CharacterRosterEntry{
					ObjectID: 1,
					EquipSummary: []dnfrepo.CharacterRosterEquipSummary{
						{Slot: 0, ItemIDOrIcon: 500, RawEntry: []byte{9, 9}},
					},
				},
			},
		},
		inventory: dnfrepo.InventoryRecord{
			CharacterID: "1",
			Slots: map[string]dnfrepo.ItemStack{
				"0:5":   {ItemID: 100, Count: 3, Extra: map[string]string{"pvf_path": "stackable/x.stk"}},
				"0:354": {ItemID: 200, Count: 1},
				"1:3":   {ItemID: 300, Count: 1},
			},
			Warehouse: map[string]dnfrepo.ItemStack{
				"2:0": {ItemID: 400, Count: 1},
			},
		},
		equipment: dnfrepo.EquipmentRecord{
			CharacterID: "1",
			Entries: map[string]dnfrepo.EquipmentEntry{
				"0": {SlotIndex: 0, ItemID: 500, Bind: true, RawEntry: []byte{1, 2, 3}, Extra: map[string]string{"lock": "1"}},
			},
		},
		skill: dnfrepo.SkillRecord{
			CharacterID: "1",
			Skills:      map[int64]dnfrepo.SkillState{101: {Level: 5, Enabled: true}},
			Points:      dnfrepo.SkillPointState{TotalSP: 100, RemainingSP: 10, TotalTP: 5, RemainingTP: 1, SyncedLevel: 90},
			Layouts:     map[int]dnfrepo.SkillLayout{0: {0: 101}},
		},
		skillCommands: dnfrepo.SettingsRecord{
			Scope:  dnfskillcmd.SkillCommandScope("1"),
			Values: map[string]string{"command_101": "0102", "command_count": "1"},
		},
		quest: dnfrepo.QuestRecord{
			CharacterID: "1",
			States: map[int64]dnfrepo.QuestState{
				777: {Status: "completed", ProgressValue: 1, Extra: map[string]string{"a": "b"}},
			},
		},
		pet: dnfrepo.PetRecord{
			CharacterID: "1",
			Entries: map[string]dnfrepo.PetEntry{
				"pet-a": {PetKey: "pet-a", ItemID: 600, Level: 10, Name: "小家伙"},
			},
			EquippedKey: "pet-a",
			TownDisplay: true,
		},
		containerState: dnfrepo.SettingsRecord{
			Scope: dnfrepo.CharacterContainerStateScope("1"),
			Values: map[string]string{
				"main_list_param16":           "24",
				"avatar_list_param16":         "0",
				"personal_cargo_list_param16": "8",
				"source":                      "client",
			},
		},
		dungeon: []dnfrepo.DungeonPermissionEntry{
			{DungeonID: 100, ClearState: 2, SortOrder: 0},
			{DungeonID: 200, ClearState: 1, SortOrder: 1},
		},
	}
}

// seed 把夹具写进内存仓储（源角色 + 全部子记录 + 副本状态）。
func (h *cloneHarness) seed(fixture cloneSourceFixture) {
	h.t.Helper()
	ctx := h.ctx()
	if err := h.group.Character.Save(ctx, fixture.character); err != nil {
		h.t.Fatalf("save character: %v", err)
	}
	if err := h.group.Inventory.Save(ctx, fixture.inventory); err != nil {
		h.t.Fatalf("save inventory: %v", err)
	}
	if err := h.group.Equipment.Save(ctx, fixture.equipment); err != nil {
		h.t.Fatalf("save equipment: %v", err)
	}
	if err := h.group.Skill.Save(ctx, fixture.skill); err != nil {
		h.t.Fatalf("save skill: %v", err)
	}
	if err := h.group.Settings.Save(ctx, fixture.skillCommands); err != nil {
		h.t.Fatalf("save skill commands: %v", err)
	}
	if err := h.group.Quest.Save(ctx, fixture.quest); err != nil {
		h.t.Fatalf("save quest: %v", err)
	}
	if err := h.group.Pet.Save(ctx, fixture.pet); err != nil {
		h.t.Fatalf("save pet: %v", err)
	}
	if err := h.group.Settings.Save(ctx, fixture.containerState); err != nil {
		h.t.Fatalf("save container state: %v", err)
	}
	for _, entry := range fixture.dungeon {
		if _, _, err := h.group.DungeonPermission.UpsertMax(ctx, fixture.character.CharacterID, entry.DungeonID, entry.ClearState); err != nil {
			h.t.Fatalf("upsert dungeon permission: %v", err)
		}
	}
}

// cloneSourceSnapshotJSON 冻结"源角色一侧"的全部记录（源角色必须逐字节不变）。
func (h *cloneHarness) cloneSourceSnapshotJSON() string {
	h.t.Helper()
	ctx := h.ctx()
	payload := map[string]any{}
	if record, found, err := h.group.Character.Load(ctx, "1"); err != nil {
		h.t.Fatalf("load character: %v", err)
	} else if found {
		payload["character"] = record
	}
	if record, found, err := h.group.Inventory.Load(ctx, "1"); err != nil {
		h.t.Fatalf("load inventory: %v", err)
	} else if found {
		payload["inventory"] = record
	}
	if record, found, err := h.group.Equipment.Load(ctx, "1"); err != nil {
		h.t.Fatalf("load equipment: %v", err)
	} else if found {
		payload["equipment"] = record
	}
	if record, found, err := h.group.Skill.Load(ctx, "1"); err != nil {
		h.t.Fatalf("load skill: %v", err)
	} else if found {
		payload["skill"] = record
	}
	if record, found, err := h.group.Quest.Load(ctx, "1"); err != nil {
		h.t.Fatalf("load quest: %v", err)
	} else if found {
		payload["quest"] = record
	}
	if record, found, err := h.group.Pet.Load(ctx, "1"); err != nil {
		h.t.Fatalf("load pet: %v", err)
	} else if found {
		payload["pet"] = record
	}
	for _, scope := range []string{dnfrepo.CharacterContainerStateScope("1"), dnfskillcmd.SkillCommandScope("1")} {
		if record, found, err := h.group.Settings.Load(ctx, scope); err != nil {
			h.t.Fatalf("load settings %s: %v", scope, err)
		} else if found {
			payload["setting:"+scope] = record
		}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		h.t.Fatalf("marshal source snapshot: %v", err)
	}
	return string(raw)
}

func cloneDecodeBody(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("响应不是 JSON: %v body=%s", err, recorder.Body.String())
	}
	return payload
}

// cloneRequireFailure 断言一次"HTTP 200 + success:false + 指定文案"失败，
// 形态与 writeFailure（gmtool.go:498-500）一致。
func cloneRequireFailure(t *testing.T, recorder *httptest.ResponseRecorder, wantMessage string) map[string]any {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("失败响应必须是 HTTP 200（面板契约），得到 %d: %s", recorder.Code, recorder.Body.String())
	}
	payload := cloneDecodeBody(t, recorder)
	if payload["success"] != false {
		t.Fatalf("失败响应必须 success:false，得到 %v", payload)
	}
	message, _ := payload["error"].(string)
	if message != wantMessage {
		t.Fatalf("失败文案 = %q，期望 %q", message, wantMessage)
	}
	return payload
}

func cloneRequireSuccess(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("成功响应必须是 HTTP 200，得到 %d: %s", recorder.Code, recorder.Body.String())
	}
	payload := cloneDecodeBody(t, recorder)
	if payload["success"] != true {
		t.Fatalf("成功响应必须 success:true，得到 %v", payload)
	}
	return payload
}

// cloneNumber 读一个数值字段。同一个断言要同时服务两种载荷：直接调用 handler 拿到
// 的 Go map（值是 int/int64）与经 JSON 往返的响应（值是 float64），所以两种都收。
func cloneNumber(t *testing.T, payload map[string]any, key string) float64 {
	t.Helper()
	switch value := payload[key].(type) {
	case float64:
		return value
	case float32:
		return float64(value)
	case int:
		return float64(value)
	case int64:
		return float64(value)
	default:
		t.Fatalf("字段 %s 不是数字: %#v（payload=%v）", key, payload[key], payload)
		return 0
	}
}

// cloneTargetAccount 是目标账号（webAccount 由 s.db 解析；单测直接构造）。
func cloneTargetAccount(panelID int64, accountName, accountID string) webAccount {
	return webAccount{PanelID: panelID, Account: accountName, AccountID: accountID}
}

func cloneAllOptionsExcept(except ...string) []string {
	skip := make(map[string]bool, len(except))
	for _, key := range except {
		skip[key] = true
	}
	options := make([]string, 0, len(cloneOptions))
	for _, option := range cloneOptions {
		if skip[option.Key] {
			continue
		}
		options = append(options, option.Key)
	}
	return options
}

// ---------------------------------------------------------------------------
// 纯函数：名字校验 / 账号校验 / 选项规范化 / 槽位 / 账号共享格
// ---------------------------------------------------------------------------

// TestCloneValidateCharacterNamePortsA21Rules 钉死 A21 CharacterClone.cs:923-931 的口径：
// 空名拒绝；UTF-8 字节数必须在 2..18 之间（中文按 3 字节算）。
func TestCloneValidateCharacterNamePortsA21Rules(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"空串", "", "角色名不能为空"},
		{"纯空白", "   ", "角色名不能为空"},
		{"1 字节", "a", "角色名长度需要为 2-18 字节"},
		{"2 字节", "ab", ""},
		{"18 字节", strings.Repeat("z", 18), ""},
		{"19 字节", strings.Repeat("z", 19), "角色名长度需要为 2-18 字节"},
		{"6 个汉字=18 字节", "一二三四五六", ""},
		{"7 个汉字=21 字节", "一二三四五六七", "角色名长度需要为 2-18 字节"},
		{"两端空白被裁剪", "  ab  ", ""},
	}
	for _, testCase := range cases {
		if got := cloneValidateCharacterName(testCase.input); got != testCase.want {
			t.Fatalf("%s: cloneValidateCharacterName(%q) = %q，期望 %q", testCase.name, testCase.input, got, testCase.want)
		}
	}
}

// TestCloneValidateAccountRequestPortsBothSources 钉死"面板建号"与启动器注册同规则
// （A21 CharacterClone.cs:138-144 + dnfbridge/account_api.go:54、469-480）。
func TestCloneValidateAccountRequestPortsBothSources(t *testing.T) {
	cases := []struct {
		name            string
		account         string
		password        string
		confirmPassword string
		want            string
	}{
		{"空账号名", "", "secret1", "secret1", "账号名不能为空"},
		{"账号名太短", "ab", "secret1", "secret1", "账号名只能是 3-20 位字母、数字或下划线"},
		{"账号名含中文", "账号名", "secret1", "secret1", "账号名只能是 3-20 位字母、数字或下划线"},
		{"空密码", "acc_1", "", "", "密码不能为空"},
		{"两次不一致", "acc_1", "secret1", "secret2", "两次输入的密码不一致"},
		{"密码太短", "acc_1", "12345", "12345", "密码至少 6 位"},
		{"密码太长", "acc_1", strings.Repeat("p", 65), strings.Repeat("p", 65), "密码不能超过 64 位"},
		{"合法", "acc_1", "secret1", "secret1", ""},
	}
	for _, testCase := range cases {
		got := cloneValidateAccountRequest(testCase.account, testCase.password, testCase.confirmPassword)
		if got != testCase.want {
			t.Fatalf("%s: = %q，期望 %q", testCase.name, got, testCase.want)
		}
	}
}

// TestCloneLauncherAccountIDMatchesAccountAPI 钉死账号号算法与
// dnfbridge/account_api.go:45/:363 逐字一致（基号 10000 + 自增主键）。
func TestCloneLauncherAccountIDMatchesAccountAPI(t *testing.T) {
	if got := cloneLauncherAccountID(1); got != "dnf:10001" {
		t.Fatalf("cloneLauncherAccountID(1) = %q，期望 dnf:10001", got)
	}
	if got := cloneLauncherAccountID(3); got != "dnf:10003" {
		t.Fatalf("cloneLauncherAccountID(3) = %q，期望 dnf:10003", got)
	}
}

// TestCloneNormalizeOptions 钉死 A21 :265-274 的两条语义（nil=默认勾选、basic 强制）
// 与本实现的有意差异 D1（未知键**报错**而不是静默丢弃）。
func TestCloneNormalizeOptions(t *testing.T) {
	defaults, err := cloneNormalizeOptions(nil)
	if err != nil {
		t.Fatalf("nil 选项不该失败: %v", err)
	}
	for _, option := range cloneOptions {
		if option.DefaultChecked && !defaults[option.Key] {
			t.Fatalf("默认勾选的 %s 没有进入集合: %v", option.Key, defaults)
		}
	}
	if !defaults["basic"] {
		t.Fatal("basic 必须永远在集合里（A21 :272）")
	}

	explicit, err := cloneNormalizeOptions([]string{"pets"})
	if err != nil {
		t.Fatalf("显式选项不该失败: %v", err)
	}
	if !explicit["pets"] || !explicit["basic"] {
		t.Fatalf("显式选择 pets 后集合应为 {basic,pets}: %v", explicit)
	}
	if explicit["skills"] || explicit["quests"] {
		t.Fatalf("显式选择时不得带入未选中的默认项: %v", explicit)
	}

	empty, err := cloneNormalizeOptions([]string{})
	if err != nil {
		t.Fatalf("空数组不该失败: %v", err)
	}
	if len(empty) != 1 || !empty["basic"] {
		t.Fatalf("空数组应当只剩 basic（与 A21 的 nil/空区分一致）: %v", empty)
	}

	blank, err := cloneNormalizeOptions([]string{"", "  ", "pets"})
	if err != nil {
		t.Fatalf("空串元素应被忽略: %v", err)
	}
	if !blank["pets"] {
		t.Fatalf("空串元素影响了正常选项: %v", blank)
	}

	for _, unknown := range []string{"titlebook", "daily", "wallet", "avatars", "audit", "locks", "misc", "quickSlots", "nope"} {
		if _, err := cloneNormalizeOptions([]string{unknown}); err == nil {
			t.Fatalf("未实现的选项 %q 必须报具名错误，不能静默丢弃（差异 D1）", unknown)
		} else if !strings.Contains(err.Error(), unknown) {
			t.Fatalf("未知选项错误里必须带上原键 %q: %v", unknown, err)
		}
	}

	// 大小写不敏感（A21 用 StringComparer.OrdinalIgnoreCase）。
	upper, err := cloneNormalizeOptions([]string{"PETS"})
	if err != nil {
		t.Fatalf("大写选项不该失败: %v", err)
	}
	if !upper["pets"] {
		t.Fatalf("大写 PETS 没有归一到 pets: %v", upper)
	}
}

// TestCloneOptionTablesDeclareOwnership 是 A21 CharacterCloneSafety.cs:46-75
// （ValidateCloneTableSafety）在 90CN 的静态版本：每个选项、每张被写的表都必须
// 声明一个可安全改写的归属列，且归属列只能是 character_id / account_id / scope。
func TestCloneOptionTablesDeclareOwnership(t *testing.T) {
	allowedOwners := map[string]bool{"character_id": true, "account_id": true, "scope": true}
	declared := map[string]bool{}
	for _, option := range cloneOptions {
		tables, ok := cloneOptionOwnedTables[option.Key]
		if !ok {
			t.Fatalf("选项 %s 没有声明它写入的表（A21 Safety:51-52 要求每张表都有归属列）", option.Key)
		}
		if len(tables) == 0 {
			t.Fatalf("选项 %s 的表清单是空的", option.Key)
		}
		for _, table := range tables {
			if strings.TrimSpace(table.Table) == "" || strings.TrimSpace(table.OwnerColumn) == "" {
				t.Fatalf("选项 %s 有表缺表名或归属列: %+v", option.Key, table)
			}
			if !allowedOwners[table.OwnerColumn] {
				t.Fatalf("选项 %s 的表 %s 归属列 %q 不在允许集合里", option.Key, table.Table, table.OwnerColumn)
			}
			declared[table.Table] = true
		}
	}
	for key := range cloneOptionOwnedTables {
		found := false
		for _, option := range cloneOptions {
			if option.Key == key {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("cloneOptionOwnedTables 里的 %s 不是可选选项（清单腐化）", key)
		}
	}
	// 反向：本实现真的会碰的核心表必须在声明里（漏声明的表等于绕过了安全自查）。
	for _, table := range []string{
		"dnf_characters", "dnf_inventory_items", "dnf_equipment_entries",
		"dnf_skills", "dnf_quest_states", "dnf_pet_entries", "dnf_dungeon_permissions", "dnf_settings",
	} {
		if !declared[table] {
			t.Fatalf("核心表 %s 没有被任何选项声明归属列", table)
		}
	}
}

// TestCloneFreeSlotPicksLowestFree 钉死 A21 :856-889 的"最小空闲槽"。
func TestCloneFreeSlotPicksLowestFree(t *testing.T) {
	records := []dnfrepo.CharacterRecord{{Slot: 0}, {Slot: 2}, {Slot: 3}}
	slot, ok := cloneFreeSlot(records, 4)
	if !ok || slot != 1 {
		t.Fatalf("cloneFreeSlot = (%d,%t)，期望 (1,true)", slot, ok)
	}
	full := []dnfrepo.CharacterRecord{{Slot: 0}, {Slot: 1}}
	if _, ok := cloneFreeSlot(full, 2); ok {
		t.Fatal("槽位用满时必须返回 false")
	}
	if _, ok := cloneFreeSlot(nil, 0); ok {
		t.Fatal("上限为 0 时必须返回 false")
	}
	if slot, ok := cloneFreeSlot(nil, 1); !ok || slot != 0 {
		t.Fatalf("空列表 + 上限 1 应当给出槽 0，得到 (%d,%t)", slot, ok)
	}
}

// TestCloneSlotIsAccountShared 钉死"账号共享格不进新角色"（差异 D3）的判据：
// 只有 list 0 的 354..365 是账号共享（account_inventory_repository.go:41-46）。
func TestCloneSlotIsAccountShared(t *testing.T) {
	cases := map[string]bool{
		"0:353":   false,
		"0:354":   true,
		"0:359":   true,
		"0:360":   true,
		"0:365":   true,
		"0:366":   false,
		"1:354":   false,
		"2:354":   false,
		"12:354":  false,
		"":        false,
		"garbage": false,
		"0:x":     false,
	}
	for key, want := range cases {
		if got := cloneSlotIsAccountShared(key); got != want {
			t.Fatalf("cloneSlotIsAccountShared(%q) = %t，期望 %t", key, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// 前端契约：JSON 字段名
// ---------------------------------------------------------------------------

// TestCloneRequestFieldNamesMatchFrontend 钉死 character.js:361 / :398 发出的
// 请求体字段名就是本文件解析的字段名（大小写写错 = 前端点了没反应）。
func TestCloneRequestFieldNamesMatchFrontend(t *testing.T) {
	var request cloneRequest
	raw := `{"targetAccountId":10002,"newName":"复制角色","options":["basic","skills"]}`
	if err := json.Unmarshal([]byte(raw), &request); err != nil {
		t.Fatalf("unmarshal clone request: %v", err)
	}
	if request.TargetAccountID != 10002 {
		t.Fatalf("targetAccountId 没解析出来: %+v", request)
	}
	if request.NewName != "复制角色" {
		t.Fatalf("newName 没解析出来: %+v", request)
	}
	if len(request.Options) != 2 || request.Options[0] != "basic" {
		t.Fatalf("options 没解析出来: %+v", request)
	}

	var account cloneAccountRequest
	rawAccount := `{"accountName":"acc_1","password":"secret1","confirmPassword":"secret1"}`
	if err := json.Unmarshal([]byte(rawAccount), &account); err != nil {
		t.Fatalf("unmarshal account request: %v", err)
	}
	if account.AccountName != "acc_1" || account.Password != "secret1" || account.ConfirmPassword != "secret1" {
		t.Fatalf("建号请求字段没解析出来: %+v", account)
	}
}

// TestClonePlanPayloadMatchesFrontendContract 钉死 clone-plan 的响应形状
// （character.js:232-257 读 accounts[].accountId/name/characterCount/slotLimit/
// isCurrent/canAcceptCharacter，:246-249 读 options[].key/label/defaultChecked）。
func TestClonePlanPayloadMatchesFrontendContract(t *testing.T) {
	harness := cloneNewHarness(t)
	fixture := cloneNewSourceFixture()
	harness.seed(fixture)
	// 目标账号里放两个角色，用来验证 characterCount/canAcceptCharacter。
	for _, record := range []dnfrepo.CharacterRecord{
		{CharacterID: "2", AccountID: "dnf:10002", Slot: 0, Name: "二号", Stats: map[string]int64{"delete_flag": 0}},
		{CharacterID: "3", AccountID: "dnf:10002", Slot: 1, Name: "三号", Stats: map[string]int64{"delete_flag": 0}},
		// 软删角色不计入数量、也不占槽位（A21 :80 的 delete_flag = 0）。
		{CharacterID: "4", AccountID: "dnf:10002", Slot: 2, Name: "已删除", Stats: map[string]int64{"delete_flag": 1}},
	} {
		if err := harness.group.Character.Save(harness.ctx(), record); err != nil {
			t.Fatalf("save character %s: %v", record.CharacterID, err)
		}
	}
	accounts := []webAccount{
		cloneTargetAccount(10001, "accA", "dnf:10001"),
		cloneTargetAccount(10002, "accB", "dnf:10002"),
	}
	source, found, err := harness.group.Character.Load(harness.ctx(), "1")
	if err != nil || !found {
		t.Fatalf("load source character: found=%t err=%v", found, err)
	}

	payload := harness.server.clonePlanPayload(harness.ctx(), harness.group, source, 1, accounts)
	if got := cloneNumber(t, payload, "sourceCharacterId"); got != 1 {
		t.Fatalf("sourceCharacterId = %v，期望 1", got)
	}
	if got := cloneNumber(t, payload, "sourceAccountId"); got != 10001 {
		t.Fatalf("sourceAccountId = %v，期望 10001（由 dnf:10001 反解）", got)
	}
	if got := cloneNumber(t, payload, "slotLimit"); got != float64(cloneDefaultSlotLimit) {
		t.Fatalf("slotLimit = %v，期望 %d", got, cloneDefaultSlotLimit)
	}

	rows, ok := payload["accounts"].([]map[string]any)
	if !ok || len(rows) != 2 {
		t.Fatalf("accounts 形状不对: %#v", payload["accounts"])
	}
	first, second := rows[0], rows[1]
	if cloneNumber(t, first, "accountId") != 10001 || first["name"] != "accA" {
		t.Fatalf("账号行 0 不对: %v", first)
	}
	if first["isCurrent"] != true {
		t.Fatalf("源角色所在账号必须 isCurrent:true: %v", first)
	}
	if cloneNumber(t, first, "characterCount") != 1 || first["canAcceptCharacter"] != true {
		// 源账号里只有角色 "1"。
		t.Fatalf("账号行 0 的数量/可容纳不对: %v", first)
	}
	if second["isCurrent"] != false {
		t.Fatalf("非源账号必须 isCurrent:false: %v", second)
	}
	if cloneNumber(t, second, "characterCount") != 2 {
		t.Fatalf("账号行 1 的 characterCount 应为 2（软删角色不计）: %v", second)
	}
	if second["canAcceptCharacter"] != true || cloneNumber(t, second, "slotLimit") != float64(cloneDefaultSlotLimit) {
		t.Fatalf("账号行 1 的可容纳/slotLimit 不对: %v", second)
	}

	options, ok := payload["options"].([]map[string]any)
	if !ok || len(options) != len(cloneOptions) {
		t.Fatalf("options 形状不对: %#v", payload["options"])
	}
	for idx, option := range cloneOptions {
		row := options[idx]
		if row["key"] != option.Key || row["label"] != option.Label || row["defaultChecked"] != option.DefaultChecked {
			t.Fatalf("options[%d] = %v，期望 {%s %s %t}", idx, row, option.Key, option.Label, option.DefaultChecked)
		}
	}
}

// TestCloneNameAvailableThroughHandler 走真实 handler（不需要 s.db）钉死
// character.js:334-337 的读取字段 `available` / `reason`。
func TestCloneNameAvailableThroughHandler(t *testing.T) {
	harness := cloneNewHarness(t)
	fixture := cloneNewSourceFixture()
	harness.seed(fixture)

	cases := []struct {
		name        string
		query       string
		wantAvail   bool
		wantReason  string
		statusCheck bool
	}{
		{"重名", "源角色", false, "角色名已存在", true},
		{"可用", "全新的名字", true, "", true},
		{"空名", "", false, "角色名不能为空", true},
		{"太短", "a", false, "角色名长度需要为 2-18 字节", true},
		{"太长", strings.Repeat("z", 19), false, "角色名长度需要为 2-18 字节", true},
	}
	for _, testCase := range cases {
		request := httptest.NewRequest(http.MethodGet,
			"/api/characters/name-available?name="+url.QueryEscape(testCase.query), nil)
		recorder := httptest.NewRecorder()
		harness.server.handleCharacterNameAvailable(recorder, request)
		payload := cloneRequireSuccess(t, recorder)
		available, ok := payload["available"].(bool)
		if !ok {
			t.Fatalf("%s: available 字段缺失或不是布尔: %v", testCase.name, payload)
		}
		if available != testCase.wantAvail {
			t.Fatalf("%s: available = %t，期望 %t（payload=%v）", testCase.name, available, testCase.wantAvail, payload)
		}
		reason, _ := payload["reason"].(string)
		if reason != testCase.wantReason {
			t.Fatalf("%s: reason = %q，期望 %q", testCase.name, reason, testCase.wantReason)
		}
	}
}

// ---------------------------------------------------------------------------
// 复制本体
// ---------------------------------------------------------------------------

// TestCloneCopiesSelectedRecordsAndNeverTouchesSource 是主用例：选中除 dungeon 之外的
// 全部范围做一次复制，断言
//
//  1. 新角色的每一条子记录与源逐字节相同（同 id 字段除外）；
//  2. 账号共享格 354 没有被复制，条数如实回报；
//  3. 源角色一侧在复制前后逐字节不变（复制永不破坏源）。
func TestCloneCopiesSelectedRecordsAndNeverTouchesSource(t *testing.T) {
	harness := cloneNewHarness(t)
	fixture := cloneNewSourceFixture()
	harness.seed(fixture)
	before := harness.cloneSourceSnapshotJSON()

	source, found, err := harness.group.Character.Load(harness.ctx(), "1")
	if err != nil || !found {
		t.Fatalf("load source: found=%t err=%v", found, err)
	}
	// 请求里**故意不带** basic：服务端必须按 A21 :272 强制加上它。
	selected := cloneAllOptionsExcept("dungeon", "basic")
	recorder := httptest.NewRecorder()
	harness.server.applyCharacterClone(recorder, harness.ctx(), harness.group, source, 1,
		cloneTargetAccount(10002, "accB", "dnf:10002"),
		cloneRequest{TargetAccountID: 10002, NewName: "复制角色", Options: selected})
	payload := cloneRequireSuccess(t, recorder)

	if got := cloneNumber(t, payload, "characterId"); got != 2 {
		t.Fatalf("characterId = %v，期望 2（NextNumericID 的 MAX+1）", got)
	}
	if payload["name"] != "复制角色" {
		t.Fatalf("name = %v", payload["name"])
	}
	if got := cloneNumber(t, payload, "targetAccountId"); got != 10002 {
		t.Fatalf("targetAccountId = %v", got)
	}
	if got := cloneNumber(t, payload, "sourceCharacterId"); got != 1 {
		t.Fatalf("sourceCharacterId = %v", got)
	}
	if got := cloneNumber(t, payload, "sourceAccountId"); got != 10001 {
		t.Fatalf("sourceAccountId = %v", got)
	}
	if got := cloneNumber(t, payload, "slotIndex"); got != 0 {
		t.Fatalf("slotIndex = %v，期望 0（目标账号空着）", got)
	}
	if got := cloneNumber(t, payload, "skippedAccountSharedSlots"); got != 1 {
		t.Fatalf("skippedAccountSharedSlots = %v，期望 1（0:354）", got)
	}
	keys, ok := payload["copiedOptions"].([]any)
	if !ok {
		t.Fatalf("copiedOptions 形状不对: %#v", payload["copiedOptions"])
	}
	got := make([]string, 0, len(keys))
	for _, key := range keys {
		text, _ := key.(string)
		got = append(got, text)
	}
	want := append([]string{"basic"}, selected...)
	if strings.Join(got, ",") != strings.Join(cloneSortedCopy(want), ",") {
		t.Fatalf("copiedOptions = %v，期望 %v", got, cloneSortedCopy(want))
	}

	// ---- 新角色基础行 ----
	stored, ok2, err := harness.group.Character.Load(harness.ctx(), "2")
	if err != nil || !ok2 {
		t.Fatalf("load new character: found=%t err=%v", ok2, err)
	}
	if stored.Name != "复制角色" || stored.AccountID != "dnf:10002" || stored.Slot != 0 {
		t.Fatalf("新角色基础字段不对: %+v", stored)
	}
	if stored.Job != source.Job || stored.Level != source.Level {
		t.Fatalf("职业/等级必须逐字节复制: job=%q level=%d", stored.Job, stored.Level)
	}
	if stored.Stats["gold"] != 12345 || stored.Stats["grow_type"] != 1 || stored.Stats["extra_custom_key"] != 7 {
		t.Fatalf("数值没有完整复制: %v", stored.Stats)
	}
	if stored.Stats["delete_flag"] != 0 {
		t.Fatalf("新角色的 delete_flag 必须是 0: %v", stored.Stats)
	}
	if stored.Stats["roster_object_id"] != 2 || stored.Roster.Entry.ObjectID != 2 {
		t.Fatalf("角色卡对象号必须指向新角色: stat=%d entry=%d",
			stored.Stats["roster_object_id"], stored.Roster.Entry.ObjectID)
	}
	if stored.Location != source.Location {
		t.Fatalf("位置没有复制: %+v vs %+v", stored.Location, source.Location)
	}
	if len(stored.Roster.Entry.EquipSummary) != 1 || stored.Roster.Entry.EquipSummary[0].ItemIDOrIcon != 500 {
		t.Fatalf("角色卡装备摘要没有复制: %+v", stored.Roster.Entry.EquipSummary)
	}

	// ---- 背包：账号共享格必须被剔除 ----
	inventory, ok3, err := harness.group.Inventory.Load(harness.ctx(), "2")
	if err != nil || !ok3 {
		t.Fatalf("load new inventory: found=%t err=%v", ok3, err)
	}
	if _, exists := inventory.Slots["0:354"]; exists {
		t.Fatal("账号共享格 0:354 被复制进了新角色（差异 D3）")
	}
	for _, key := range []string{"0:5", "1:3"} {
		if _, exists := inventory.Slots[key]; !exists {
			t.Fatalf("背包槽 %s 没有复制: %v", key, inventory.Slots)
		}
	}
	if stack := inventory.Slots["0:5"]; stack.ItemID != 100 || stack.Count != 3 || stack.Extra["pvf_path"] != "stackable/x.stk" {
		t.Fatalf("背包行内容不对: %+v", stack)
	}
	if _, exists := inventory.Warehouse["2:0"]; !exists {
		t.Fatalf("个人仓库没有复制: %v", inventory.Warehouse)
	}

	// ---- 其它子记录 ----
	equipment, ok4, err := harness.group.Equipment.Load(harness.ctx(), "2")
	if err != nil || !ok4 {
		t.Fatalf("load new equipment: found=%t err=%v", ok4, err)
	}
	if equipment.Entries["0"].ItemID != 500 || equipment.Entries["0"].Extra["lock"] != "1" {
		t.Fatalf("穿戴装备没有复制: %+v", equipment.Entries)
	}
	skill, ok5, err := harness.group.Skill.Load(harness.ctx(), "2")
	if err != nil || !ok5 {
		t.Fatalf("load new skill: found=%t err=%v", ok5, err)
	}
	if skill.Skills[101].Level != 5 || skill.Points.TotalSP != 100 || skill.Layouts[0][0] != 101 {
		t.Fatalf("技能没有复制: %+v", skill)
	}
	quest, ok6, err := harness.group.Quest.Load(harness.ctx(), "2")
	if err != nil || !ok6 {
		t.Fatalf("load new quest: found=%t err=%v", ok6, err)
	}
	if quest.States[777].Status != "completed" || quest.States[777].Extra["a"] != "b" {
		t.Fatalf("任务没有复制: %+v", quest.States)
	}
	pet, ok7, err := harness.group.Pet.Load(harness.ctx(), "2")
	if err != nil || !ok7 {
		t.Fatalf("load new pet: found=%t err=%v", ok7, err)
	}
	if pet.Entries["pet-a"].ItemID != 600 || pet.EquippedKey != "pet-a" || !pet.TownDisplay {
		t.Fatalf("宠物没有复制: %+v", pet)
	}
	containerState, ok8, err := harness.group.Settings.Load(harness.ctx(), dnfrepo.CharacterContainerStateScope("2"))
	if err != nil || !ok8 {
		t.Fatalf("load new container state: found=%t err=%v", ok8, err)
	}
	if containerState.Values["main_list_param16"] != "24" || containerState.Values["personal_cargo_list_param16"] != "8" {
		t.Fatalf("容器状态没有复制: %v", containerState.Values)
	}
	commands, ok9, err := harness.group.Settings.Load(harness.ctx(), dnfskillcmd.SkillCommandScope("2"))
	if err != nil || !ok9 {
		t.Fatalf("load new skill commands: found=%t err=%v", ok9, err)
	}
	if commands.Values["command_101"] != "0102" {
		t.Fatalf("技能指令没有复制: %v", commands.Values)
	}

	// ---- 源角色一个字节都不能变 ----
	if after := harness.cloneSourceSnapshotJSON(); after != before {
		t.Fatalf("复制改动了源角色一侧的数据:\nbefore=%s\nafter =%s", before, after)
	}
}

// TestCloneInventoryOnlyOptionDoesNotWriteOtherRecords 钉死"没选的就不写"：
// 只选 inventory 时，仓库/技能/任务/宠物/扩容状态都不应出现在新角色下。
func TestCloneInventoryOnlyOptionDoesNotWriteOtherRecords(t *testing.T) {
	harness := cloneNewHarness(t)
	fixture := cloneNewSourceFixture()
	harness.seed(fixture)

	source, _, err := harness.group.Character.Load(harness.ctx(), "1")
	if err != nil {
		t.Fatalf("load source: %v", err)
	}
	recorder := httptest.NewRecorder()
	harness.server.applyCharacterClone(recorder, harness.ctx(), harness.group, source, 1,
		cloneTargetAccount(10002, "accB", "dnf:10002"),
		cloneRequest{TargetAccountID: 10002, NewName: "只复制背包", Options: []string{"inventory"}})
	cloneRequireSuccess(t, recorder)

	inventory, found, err := harness.group.Inventory.Load(harness.ctx(), "2")
	if err != nil || !found {
		t.Fatalf("load new inventory: found=%t err=%v", found, err)
	}
	if len(inventory.Warehouse) != 0 {
		t.Fatalf("没选 personalCargo 却写了仓库: %v", inventory.Warehouse)
	}
	if _, found, _ := harness.group.Skill.Load(harness.ctx(), "2"); found {
		t.Fatal("没选 skills 却写了技能")
	}
	if _, found, _ := harness.group.Quest.Load(harness.ctx(), "2"); found {
		t.Fatal("没选 quests 却写了任务")
	}
	if _, found, _ := harness.group.Pet.Load(harness.ctx(), "2"); found {
		t.Fatal("没选 pets 却写了宠物")
	}
	if _, found, _ := harness.group.Equipment.Load(harness.ctx(), "2"); found {
		t.Fatal("没选 equipment 却写了穿戴装备")
	}
	if _, found, _ := harness.group.Settings.Load(harness.ctx(), dnfrepo.CharacterContainerStateScope("2")); found {
		t.Fatal("没选 containerState 却写了容器状态")
	}
}

func cloneSortedCopy(values []string) []string {
	out := append([]string(nil), values...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// 拒绝路径：全部必须"零写入"
// ---------------------------------------------------------------------------

// TestCloneRejectsBadRequestsWithoutWriting 逐条打 A21 :172-181/:193-210 的拒绝分支，
// 每条都要求：HTTP 200 + success:false + 指定文案 + 库里没有新角色行。
func TestCloneRejectsBadRequestsWithoutWriting(t *testing.T) {
	cases := []struct {
		name    string
		options []string
		newName string
		account webAccount
		want    string
	}{
		{
			name:    "未选目标账号",
			options: []string{"basic"},
			newName: "复制角色",
			account: cloneTargetAccount(0, "", ""),
			want:    "请选择目标账号",
		},
		{
			name:    "新角色名为空",
			options: []string{"basic"},
			newName: "   ",
			account: cloneTargetAccount(10002, "accB", "dnf:10002"),
			want:    "角色名不能为空",
		},
		{
			name:    "新角色名过长",
			options: []string{"basic"},
			newName: strings.Repeat("z", 19),
			account: cloneTargetAccount(10002, "accB", "dnf:10002"),
			want:    "角色名长度需要为 2-18 字节",
		},
		{
			name:    "未知选项",
			options: []string{"titlebook"},
			newName: "复制角色",
			account: cloneTargetAccount(10002, "accB", "dnf:10002"),
			want:    "不支持的复制选项 \"titlebook\"（本面板支持: " + strings.Join(cloneOptionKeys(), ", ") + "）",
		},
		{
			name:    "目标账号不存在",
			options: []string{"basic"},
			newName: "复制角色",
			account: cloneTargetAccount(10002, "accB", ""),
			want:    "目标账号不存在。",
		},
		{
			name:    "新角色名已存在",
			options: []string{"basic"},
			newName: "占用名",
			account: cloneTargetAccount(10002, "accB", "dnf:10002"),
			want:    "角色名已存在: 占用名",
		},
	}
	for _, testCase := range cases {
		harness := cloneNewHarness(t)
		fixture := cloneNewSourceFixture()
		harness.seed(fixture)
		if testCase.newName == "占用名" {
			if err := harness.group.Character.Save(harness.ctx(), dnfrepo.CharacterRecord{
				CharacterID: "9", AccountID: "dnf:10002", Slot: 5, Name: "占用名",
				Stats: map[string]int64{"delete_flag": 0},
			}); err != nil {
				t.Fatalf("seed taken name: %v", err)
			}
		}
		source, _, err := harness.group.Character.Load(harness.ctx(), "1")
		if err != nil {
			t.Fatalf("load source: %v", err)
		}
		before := harness.cloneSourceSnapshotJSON()

		recorder := httptest.NewRecorder()
		harness.server.applyCharacterClone(recorder, harness.ctx(), harness.group, source, 1,
			testCase.account,
			cloneRequest{TargetAccountID: testCase.account.PanelID, NewName: testCase.newName, Options: testCase.options})
		cloneRequireFailure(t, recorder, testCase.want)

		if _, found, _ := harness.group.Character.Load(harness.ctx(), "2"); found {
			t.Fatalf("%s: 被拒绝的请求却写了新角色行", testCase.name)
		}
		if after := harness.cloneSourceSnapshotJSON(); after != before {
			t.Fatalf("%s: 被拒绝的请求改动了库内容", testCase.name)
		}
	}
}

// TestCloneRejectsDeletedSourceCharacter 钉死 A21 CharacterExists 的
// `AND delete_flag = 0`（:814）：软删角色不能当复制源。
func TestCloneRejectsDeletedSourceCharacter(t *testing.T) {
	harness := cloneNewHarness(t)
	fixture := cloneNewSourceFixture()
	fixture.character.Stats["delete_flag"] = 1
	harness.seed(fixture)

	source, _, err := harness.group.Character.Load(harness.ctx(), "1")
	if err != nil {
		t.Fatalf("load source: %v", err)
	}
	recorder := httptest.NewRecorder()
	harness.server.applyCharacterClone(recorder, harness.ctx(), harness.group, source, 1,
		cloneTargetAccount(10002, "accB", "dnf:10002"),
		cloneRequest{TargetAccountID: 10002, NewName: "复制角色", Options: []string{"basic"}})
	cloneRequireFailure(t, recorder, "源角色 1 已被删除，无法复制。")
	if _, found, _ := harness.group.Character.Load(harness.ctx(), "2"); found {
		t.Fatal("被拒绝的请求写了新角色行")
	}
}

// TestCloneRejectsFullTargetAccount 钉死 A21 :200-203 的槽位上限拒绝。
func TestCloneRejectsFullTargetAccount(t *testing.T) {
	harness := cloneNewHarness(t)
	fixture := cloneNewSourceFixture()
	harness.seed(fixture)
	for slot := 0; slot < cloneDefaultSlotLimit; slot++ {
		record := dnfrepo.CharacterRecord{
			CharacterID: "10" + string(rune('a'+slot%26)) + string(rune('a'+slot/26)),
			AccountID:   "dnf:10002",
			Slot:        slot,
			Name:        "满员" + string(rune('a'+slot%26)) + string(rune('a'+slot/26)),
			Stats:       map[string]int64{"delete_flag": 0},
		}
		if err := harness.group.Character.Save(harness.ctx(), record); err != nil {
			t.Fatalf("seed slot %d: %v", slot, err)
		}
	}
	source, _, err := harness.group.Character.Load(harness.ctx(), "1")
	if err != nil {
		t.Fatalf("load source: %v", err)
	}
	recorder := httptest.NewRecorder()
	harness.server.applyCharacterClone(recorder, harness.ctx(), harness.group, source, 1,
		cloneTargetAccount(10002, "accB", "dnf:10002"),
		cloneRequest{TargetAccountID: 10002, NewName: "复制角色", Options: []string{"basic"}})
	cloneRequireFailure(t, recorder, "目标账号角色数量已达上限 32")
}

// TestCloneDungeonRequiresTransactionalRepository 钉死"不能原子就不假装成功"：
// 内存仓储的事务作用域里没有副本权限仓储（memory_transaction.go:220-253），
// 因此选中 dungeon 且源角色真有副本行时必须整体失败、且一行都不写。
func TestCloneDungeonRequiresTransactionalRepository(t *testing.T) {
	harness := cloneNewHarness(t)
	fixture := cloneNewSourceFixture()
	harness.seed(fixture)

	source, _, err := harness.group.Character.Load(harness.ctx(), "1")
	if err != nil {
		t.Fatalf("load source: %v", err)
	}
	before := harness.cloneSourceSnapshotJSON()
	recorder := httptest.NewRecorder()
	harness.server.applyCharacterClone(recorder, harness.ctx(), harness.group, source, 1,
		cloneTargetAccount(10002, "accB", "dnf:10002"),
		cloneRequest{TargetAccountID: 10002, NewName: "复制角色", Options: []string{"dungeon"}})
	payload := cloneRequireFailure(t, recorder,
		"复制失败，事务已回滚: 复制地图难度/副本状态需要事务作用域内的副本权限仓储（当前仓储组未提供，本次未做任何写入）")
	if payload["success"] != false {
		t.Fatalf("必须是失败响应: %v", payload)
	}
	if _, found, _ := harness.group.Character.Load(harness.ctx(), "2"); found {
		t.Fatal("失败的事务留下了新角色行（回滚没生效）")
	}
	if _, found, _ := harness.group.Settings.Load(harness.ctx(), dnfrepo.CharacterContainerStateScope("2")); found {
		t.Fatal("失败的事务留下了新角色的设置行")
	}
	if after := harness.cloneSourceSnapshotJSON(); after != before {
		t.Fatal("失败的复制改动了源角色一侧的数据")
	}
}

// TestCloneDungeonOptionSkipsWhenSourceHasNoRows 反向：源角色没有副本行时，
// 选中 dungeon 不该因为仓储能力缺失而失败（没有要复制的东西）。
func TestCloneDungeonOptionSkipsWhenSourceHasNoRows(t *testing.T) {
	harness := cloneNewHarness(t)
	fixture := cloneNewSourceFixture()
	fixture.dungeon = nil
	harness.seed(fixture)

	source, _, err := harness.group.Character.Load(harness.ctx(), "1")
	if err != nil {
		t.Fatalf("load source: %v", err)
	}
	recorder := httptest.NewRecorder()
	harness.server.applyCharacterClone(recorder, harness.ctx(), harness.group, source, 1,
		cloneTargetAccount(10002, "accB", "dnf:10002"),
		cloneRequest{TargetAccountID: 10002, NewName: "复制角色", Options: []string{"dungeon"}})
	cloneRequireSuccess(t, recorder)
	if _, found, _ := harness.group.Character.Load(harness.ctx(), "2"); !found {
		t.Fatal("没有副本行时复制仍然应当成功")
	}
}

// TestCloneUsesNilOptionsAsDefaults 钉死"前端没给 options（null）"时的默认档：
// 与 A21 :268-269 相同 —— 取 defaultChecked 集合，但 dungeon 需要事务仓储，
// 所以这里用内存仓储时必须落在具名错误上（而不是假装成功）。
func TestCloneUsesNilOptionsAsDefaults(t *testing.T) {
	harness := cloneNewHarness(t)
	fixture := cloneNewSourceFixture()
	fixture.dungeon = nil
	harness.seed(fixture)

	source, _, err := harness.group.Character.Load(harness.ctx(), "1")
	if err != nil {
		t.Fatalf("load source: %v", err)
	}
	recorder := httptest.NewRecorder()
	harness.server.applyCharacterClone(recorder, harness.ctx(), harness.group, source, 1,
		cloneTargetAccount(10002, "accB", "dnf:10002"),
		cloneRequest{TargetAccountID: 10002, NewName: "默认档"})
	payload := cloneRequireSuccess(t, recorder)
	keys, _ := payload["copiedOptions"].([]any)
	if len(keys) != len(cloneOptions) {
		t.Fatalf("nil options 应当选中全部默认项（%d 个），得到 %v", len(cloneOptions), keys)
	}
}

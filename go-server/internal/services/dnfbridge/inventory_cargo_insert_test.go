package dnfbridge

import (
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func currentCargoFixtureCatalog(t *testing.T) *pvfDungeonDropCatalog {
	t.Helper()
	source := dungeonDropCatalogTestSource{"monster/monster.lst": "", "equipment/equipment.lst": "", "stackable/stackable.lst": "417 `event/chn_event_tau_2009/chn_tau_vita.stk`\n", "stackable/event/chn_event_tau_2009/chn_tau_vita.stk": "[stackable type] `[waste]`\n[attach type] `[free]`\n[stack limit] 1000\n"}
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func TestCurrentCargoInsertRulesFollowPVFAndInstanceRestrictions(t *testing.T) {
	now := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	base := dungeonDropItemDefinition{ItemID: 100, Kind: dungeonDropItemEquipment, EquipmentType: "coat", AttachType: "[free]", SlotStart: 9, SlotEnd: 64}
	for _, tc := range []struct {
		name      string
		list      byte
		attach    string
		forbidden string
		extra     map[string]string
		expiry    time.Time
		wantError bool
	}{
		{name: "个人正常装备", list: 2},
		{name: "账号正常装备", list: 12},
		{name: "封装可存账号且不误判Bind", list: 12, attach: "[sealing]", extra: map[string]string{"seal_flag": "1"}},
		{name: "账号绑定属性", list: 12, attach: "[account]"},
		{name: "个人PVF禁止", list: 2, forbidden: "[charac cargo]", wantError: true},
		{name: "账号PVF禁止", list: 12, forbidden: "account cargo", wantError: true},
		{name: "个人禁存不影响账号", list: 12, forbidden: "charac cargo"},
		{name: "账号禁存不影响个人", list: 2, forbidden: "account cargo"},
		{name: "个人交易删除拒绝", list: 2, attach: "[trade delete]", wantError: true},
		{name: "账号不可交易拒绝", list: 12, attach: "[untradable]", wantError: true},
		{name: "装备锁允许个人", list: 2, extra: map[string]string{"equipment_lock_state": "1"}},
		{name: "装备锁拒绝账号", list: 12, extra: map[string]string{"equipment_lock_state": "1"}, wantError: true},
		{name: "实例禁止交易拒绝账号", list: 12, extra: map[string]string{"trade_restriction": "1"}, wantError: true},
		{name: "实例禁止交易仍可存个人", list: 2, extra: map[string]string{"trade_restriction": "1"}},
		{name: "过期拒绝", list: 2, expiry: now.Add(-time.Second), wantError: true},
		{name: "期限尚未到期", list: 12, expiry: now.Add(time.Hour)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			definition := base
			if tc.attach != "" {
				definition.AttachType = tc.attach
			}
			if tc.forbidden != "" {
				definition.ImpossibleContents = []string{tc.forbidden}
			}
			stack := dnfrepo.ItemStack{ItemID: 100, Count: 1, Bind: tc.attach == "[sealing]", ExpireAt: tc.expiry, Extra: tc.extra}
			err := validateCurrentCargoInsert(stack, tc.list, definition, now)
			if (err != nil) != tc.wantError {
				t.Fatalf("资格判定不符: %v", err)
			}
		})
	}
	for _, definition := range []dungeonDropItemDefinition{
		{Kind: dungeonDropItemStackable, StackableType: "[quest]", SlotStart: 177, SlotEnd: 232, AttachType: "free"},
		{Kind: dungeonDropItemStackable, StackableType: "[avatar emblem]", SlotStart: 289, SlotEnd: 344, AttachType: "free"},
		{Kind: dungeonDropItemEquipment, EquipmentType: "creature", AttachType: "free"},
	} {
		for _, list := range []byte{2, 12} {
			if err := validateCurrentCargoInsert(dnfrepo.ItemStack{ItemID: 100, Count: 1}, list, definition, now); err == nil {
				t.Fatalf("任务品/徽章/宠物不能存入金库: %+v list=%d", definition, list)
			}
		}
	}
}

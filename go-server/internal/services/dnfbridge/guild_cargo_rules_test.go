package dnfbridge

import (
	"os"
	"testing"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestGuildCargoRulesPVFAndInstanceRestrictions(t *testing.T) {
	for _, tc := range []struct {
		name, text             string
		equipment, bind, allow bool
		extra                  map[string]string
		expired                bool
	}{
		{name: "ordinary", text: "[attach type]\n`[trade]`", allow: true},
		{name: "packagable zero", text: "[attach type]\n`[trade]`\n[packagable]\n0"},
		{name: "packagable category", text: "[attach type]\n`[trade]`\n[item category]\n`[packagable]`"},
		{name: "account", text: "[attach type]\n`[account]`"},
		{name: "missing attach"},
		{name: "bound ordinary", text: "[attach type]\n`[free]`", bind: true},
		{name: "trade locked", text: "[attach type]\n`[trade]`", extra: map[string]string{"trade_restriction": "1"}},
		{name: "expired", text: "[attach type]\n`[trade]`", expired: true},
		{name: "sealed", equipment: true, bind: true, text: "[attach type]\n`[sealing]`", extra: map[string]string{"seal_flag": "1", "enhance": "7"}, allow: true},
		{name: "bind alone", equipment: true, bind: true, text: "[attach type]\n`[sealing]`"},
		{name: "unsealed", equipment: true, text: "[attach type]\n`[sealing]`", extra: map[string]string{"seal_flag": "0"}},
		{name: "first equipped", equipment: true, text: "[attach type]\n`[sealing]`", extra: map[string]string{"seal_flag": "1", "trade_locked_by_first_equip": "1"}},
		{name: "cash equipment", equipment: true, text: "[attach type]\n`[trade]`\n[cash]\n1"},
		{name: "free equipment", equipment: true, text: "[attach type]\n`[free]`", allow: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := cargoQuerySource()
			id := int64(100)
			path := "stackable/cargo.stk"
			if tc.equipment {
				id = 102
				path = "equipment/artifact.equ"
			}
			src[path] += "\n" + tc.text
			items, err := newPVFDungeonDropCatalog(src)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			stack := r.ItemStack{ItemID: id, Count: 1, Bind: tc.bind, Extra: tc.extra}
			if tc.expired {
				stack.ExpireAt = now.Add(-time.Second)
			}
			got, err := currentGuildCargoRules(items, 56, now).Normalize(stack)
			if (err == nil) != tc.allow {
				t.Fatalf("allow=%v err=%v", tc.allow, err)
			}
			if err == nil && (got.ItemID != stack.ItemID || got.Count != stack.Count || got.Bind != stack.Bind || got.Extra["pvf_path"] != path || got.Extra["enhance"] != stack.Extra["enhance"]) {
				t.Fatal("instance/provenance changed")
			}
		})
	}
}

func TestGuildCargoRulesRealPVF(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF not configured")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id    int64
		allow bool
	}{{3175, true}, {3161, true}, {90000, false}, {490001384, false}} {
		def, err := items.ResolveItem(uint32(tc.id))
		if err != nil {
			t.Fatal(err)
		}
		_, err = currentGuildCargoRules(items, 56, time.Now()).Normalize(r.ItemStack{ItemID: tc.id, Count: 1})
		if (err == nil) != tc.allow {
			t.Fatalf("id=%d attach=%s path=%s err=%v", tc.id, def.AttachType, def.PVFPath, err)
		}
	}
}

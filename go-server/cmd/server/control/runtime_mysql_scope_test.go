package main

import (
	"strings"
	"testing"
)

func TestControllerMySQLSaveScopesAreExplicitAndKeepBatchAndMemorySettings(t *testing.T) {
	t.Setenv("DNFBRIDGE_INVENTORY_SAVE_SCOPE", "slot")
	t.Setenv("DNFBRIDGE_EQUIPMENT_SAVE_SCOPE", "entry")
	t.Setenv("DNF90_MYSQL_INSERT_BATCH", "200")
	t.Setenv("GOGC", "50")
	t.Setenv("GOMEMLIMIT", "3GiB")
	for _, tc := range []struct{ inv, equip, wantInv, wantEquip string }{
		{"", "", "", ""},
		{"slot", "entry", "slot", "entry"},
		{" collection ", " COLLECTION ", "collection", "collection"},
		{" SLOT ", "Entry", "slot", "entry"},
		{"slot", "", "slot", ""},
		{"", "entry", "", "entry"},
		{"entry", "slot", "", ""},
		{"bad", "true", "", ""},
	} {
		t.Run(tc.inv+"/"+tc.equip, func(t *testing.T) {
			t.Setenv("DNF90_INVENTORY_SAVE_SCOPE", tc.inv)
			t.Setenv("DNF90_EQUIPMENT_SAVE_SCOPE", tc.equip)
			values := map[string]string{}
			for _, entry := range runtimeEnvironment(validTestInstance()) {
				key, value, _ := strings.Cut(entry, "=")
				values[key] = value
			}
			for key, want := range map[string]string{
				"DNFBRIDGE_INVENTORY_SAVE_SCOPE": tc.wantInv,
				"DNFBRIDGE_EQUIPMENT_SAVE_SCOPE": tc.wantEquip,
				"DNFBRIDGE_MYSQL_INSERT_BATCH":   "200",
				"GOGC":                           "50",
				"GOMEMLIMIT":                     "3GiB",
			} {
				if got := values[key]; got != want {
					t.Fatalf("运行参数 %s=%q，期望 %q", key, got, want)
				}
				if want == "" {
					if _, found := values[key]; found {
						t.Fatalf("未显式配置时继承值没有清理：%s", key)
					}
				}
			}
		})
	}
}

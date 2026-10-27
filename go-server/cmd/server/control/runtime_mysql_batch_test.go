package main

import (
	"strings"
	"testing"
)

func TestControllerMySQLBatchKeepsDefaultAndInheritedOverridesScrubbed(t *testing.T) {
	t.Setenv("DNFBRIDGE_MYSQL_INSERT_BATCH", "999")
	t.Setenv("DNFBRIDGE_INVENTORY_SAVE_SCOPE", "slot")
	t.Setenv("DNFBRIDGE_EQUIPMENT_SAVE_SCOPE", "entry")
	for _, tc := range []struct{ raw, want string }{
		{"", "200"}, {"0", "200"}, {"-1", "200"}, {"bad", "200"}, {"1001", "200"},
		{"1", "1"}, {"200", "200"}, {" 200 ", "200"}, {"1000", "1000"},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			t.Setenv("DNF90_MYSQL_INSERT_BATCH", tc.raw)
			values := map[string]string{}
			for _, entry := range runtimeEnvironment(validTestInstance()) {
				key, value, _ := strings.Cut(entry, "=")
				values[key] = value
			}
			if got := values["DNFBRIDGE_MYSQL_INSERT_BATCH"]; got != tc.want {
				t.Fatalf("raw=%q batch=%q want=%q", tc.raw, got, tc.want)
			}
			for _, name := range []string{"DNFBRIDGE_INVENTORY_SAVE_SCOPE", "DNFBRIDGE_EQUIPMENT_SAVE_SCOPE"} {
				if _, found := values[name]; found {
					t.Fatalf("继承环境绕过了清理: %s", name)
				}
			}
		})
	}
}

package main

import (
	"strings"
	"testing"
)

func TestControllerMapCacheIsExplicitAndScrubsInheritedValue(t *testing.T) {
	t.Setenv("DNFBRIDGE_MAP_CACHE", "true")
	for _, tc := range []struct{ raw, want string }{{"", ""}, {"bad", ""}, {"on", "true"}, {"TRUE", "true"}, {"1", "true"}, {" off ", "false"}, {"false", "false"}, {"0", "false"}} {
		t.Run(tc.raw, func(t *testing.T) {
			t.Setenv("DNF90_MAP_CACHE", tc.raw)
			values := map[string]string{}
			for _, entry := range runtimeEnvironment(validTestInstance()) {
				key, value, _ := strings.Cut(entry, "=")
				values[key] = value
			}
			if values["DNFBRIDGE_MAP_CACHE"] != tc.want {
				t.Fatalf("raw=%q got=%q want=%q", tc.raw, values["DNFBRIDGE_MAP_CACHE"], tc.want)
			}
		})
	}
}

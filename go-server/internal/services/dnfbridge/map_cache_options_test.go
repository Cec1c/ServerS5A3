package dnfbridge

import (
	"longheng.io/server/internal/platform/config"
	"testing"
)

func TestMapCacheOptionDefaultsToFullPreload(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want bool
	}{{"", false}, {"false", false}, {"bad", false}, {"on", true}, {"true", true}} {
		t.Run(tc.raw, func(t *testing.T) {
			t.Setenv("DNFBRIDGE_MAP_CACHE", tc.raw)
			if got := optionsFromConfig(config.ServiceConfig{}).mapDefinitionCache; got != tc.want {
				t.Fatalf("raw=%q got=%t want=%t", tc.raw, got, tc.want)
			}
		})
	}
}

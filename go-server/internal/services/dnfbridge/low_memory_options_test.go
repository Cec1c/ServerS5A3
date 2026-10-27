package dnfbridge

import (
	"longheng.io/server/internal/platform/config"
	"testing"
)

func TestLowMemoryIsOptInAndIncludesMapCache(t *testing.T) {
	t.Setenv("DNFBRIDGE_MAP_CACHE", "off")
	for _, tc := range []struct {
		raw  string
		want bool
	}{{"", false}, {"off", false}, {"bad", false}, {"on", true}, {"true", true}} {
		t.Run(tc.raw, func(t *testing.T) {
			t.Setenv("DNFBRIDGE_LOW_MEMORY", tc.raw)
			o := optionsFromConfig(config.ServiceConfig{})
			if o.lowMemory != tc.want || o.mapDefinitionCache != tc.want {
				t.Fatalf("raw=%q low=%t cache=%t", tc.raw, o.lowMemory, o.mapDefinitionCache)
			}
		})
	}
}

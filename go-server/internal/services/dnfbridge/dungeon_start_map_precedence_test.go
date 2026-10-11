package dnfbridge

import (
	"testing"

	"longheng.io/server/internal/modules/dnf/worldmap"
)

// TestDungeonStartMapPrecedenceSwitchOnlyAcceptsExplicitOwnership 钉住开关解析口径
// （与 `readHellBlockedSlotsMode` / `readDungeonEntrySelectionMode` 同一约定）：
// **只有显式 `ownership` 才回退**，未设置/空/任何别名都取默认档 `specification`。
// 现场缺陷与真档证据见 dungeon_start_map_precedence.go 文件头。
func TestDungeonStartMapPrecedenceSwitchOnlyAcceptsExplicitOwnership(t *testing.T) {
	if got := readDungeonStartMapPrecedence("ownership"); got != worldmap.StartMapPrecedenceOwnership {
		t.Fatalf("readDungeonStartMapPrecedence(ownership) = %q", got)
	}
	if got := readDungeonStartMapPrecedence(" OWNERSHIP "); got != worldmap.StartMapPrecedenceOwnership {
		t.Fatalf("readDungeonStartMapPrecedence( OWNERSHIP ) = %q", got)
	}
	for _, value := range []string{"", "off", "0", "false", "legacy", "on", "specification", "vendor", "1"} {
		if got := readDungeonStartMapPrecedence(value); got != worldmap.StartMapPrecedenceSpecification {
			t.Fatalf("readDungeonStartMapPrecedence(%q) = %q want %q (默认 = 修复档)",
				value, got, worldmap.StartMapPrecedenceSpecification)
		}
	}
	if dungeonStartMapPrecedenceEnv != "DNFBRIDGE_DUNGEON_START_MAP_PRECEDENCE" {
		t.Fatalf("env name = %q", dungeonStartMapPrecedenceEnv)
	}
}

// TestDungeonResolverOptionsFollowTheSwitch 钉住"开关真的接到 resolver 构造上"：
// 零值 Service（测试/探针路径）与显式档位各自映射到正确的 ResolverOptions。
func TestDungeonResolverOptionsFollowTheSwitch(t *testing.T) {
	var zero *Service
	if got := zero.dungeonResolverOptions().StartMapPrecedence; got != worldmap.StartMapPrecedenceSpecification {
		t.Fatalf("nil Service options = %q want %q", got, worldmap.StartMapPrecedenceSpecification)
	}
	defaultService := &Service{}
	if got := defaultService.dungeonResolverOptions().StartMapPrecedence; got != worldmap.StartMapPrecedenceSpecification {
		t.Fatalf("zero-value Service options = %q want %q", got, worldmap.StartMapPrecedenceSpecification)
	}
	legacyService := &Service{}
	legacyService.options.dungeonStartMapPrecedence = worldmap.StartMapPrecedenceOwnership
	if got := legacyService.dungeonResolverOptions().StartMapPrecedence; got != worldmap.StartMapPrecedenceOwnership {
		t.Fatalf("rollback档 Service options = %q want %q", got, worldmap.StartMapPrecedenceOwnership)
	}
}

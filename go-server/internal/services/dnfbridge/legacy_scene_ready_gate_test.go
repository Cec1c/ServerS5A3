package dnfbridge

import "testing"

// 默认保持旧契约（要求 legacy type1345）；显式 off 时软化为"不等它"。
func TestSceneTailLegacyGateDefaultIsStrict(t *testing.T) {
	t.Setenv(legacySceneReadyGateEnv, "")
	if !currentSceneTailRequiresLegacySceneReady() {
		t.Fatal("默认必须保持旧契约（要求 legacy type1345）")
	}
	for _, off := range []string{"off", "0", "false", "disabled", "NONE"} {
		t.Setenv(legacySceneReadyGateEnv, off)
		if currentSceneTailRequiresLegacySceneReady() {
			t.Fatalf("env=%q 应软化为不等该包", off)
		}
	}
}

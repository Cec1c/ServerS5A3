package main

import "testing"

func TestNormalGMNativeAccountsFollowLocalLauncher(t *testing.T) {
	t.Setenv("DNFBRIDGE_GM_ACCOUNT_TABLE", "unexpected_inherited_table")
	for name, base := range gmRegressFixtures() {
		cfg := gmRegressWithListen(t, base, "127.0.0.1:28211")
		env := gmRegressEnv(cfg)
		if name == "official_native" {
			if env["DNFBRIDGE_GM_ACCOUNT_TABLE"] != localLoginAccountTable {
				t.Fatal("native GM does not use local launcher accounts")
			}
		} else if _, found := env["DNFBRIDGE_GM_ACCOUNT_TABLE"]; found {
			t.Fatal("legacy GM schema changed")
		}
		if _, found := gmRegressEnv(gmRegressWithListen(t, base, ""))["DNFBRIDGE_GM_ACCOUNT_TABLE"]; found {
			t.Fatal("disabled GM emitted an account table")
		}
	}
}

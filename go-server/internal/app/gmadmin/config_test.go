package gmadmin

import (
	"strings"
	"testing"
)

func validConfig() Config {
	return Config{
		Listen: "127.0.0.1:18112", Password: strings.Repeat("x", 32),
		RegistrationDSN: "test:secret@tcp(127.0.0.1:3306)/fixture?parseTime=true",
		RepositoryDSN:   "test:secret@tcp(127.0.0.1:3306)/fixture?parseTime=true",
		AccountTable:    "web_accounts", Databases: []string{"fixture"}, TablePrefix: "dnf",
		PVFPath: "fixture.pvf", PVFSHA256: strings.Repeat("a", 64), ItemIndexPath: "index.json",
		GameAPI: "http://127.0.0.1:17002/api/game/admin", GameToken: strings.Repeat("y", 32), ScanSeconds: 30,
	}
}

func TestConfigRejectsWrongTargetsAndSecretDisclosure(t *testing.T) {
	if err := validConfig().Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Config){
		func(c *Config) { c.Listen = "0.0.0.0:18112" },
		func(c *Config) { c.Listen = "8.8.8.8:18112" },
		func(c *Config) { c.PVFSHA256 = "" },
		func(c *Config) { c.Databases = []string{"fixture;drop"} },
		func(c *Config) { c.AccountTable = "web_accounts`" },
		func(c *Config) { c.GameToken = "short" },
		func(c *Config) { c.GameAPI = "http://8.8.8.8/api/game/admin" },
		func(c *Config) { c.RepositoryDSN += "&multiStatements=true" },
		func(c *Config) { c.RepositoryDSN = "PRIVATE_CREDENTIAL" },
	} {
		c := validConfig()
		change(&c)
		err := c.Validate()
		if err == nil {
			t.Fatal("invalid configuration accepted")
		}
		if strings.Contains(err.Error(), "PRIVATE_CREDENTIAL") || strings.Contains(err.Error(), "test:secret") {
			t.Fatal("credential exposed")
		}
	}
}

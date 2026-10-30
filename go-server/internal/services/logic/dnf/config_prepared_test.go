package dnf

import "testing"

func TestPreparedCacheConfigurationPreservesExplicitDisable(t *testing.T) {
	for _, size := range []int{-1, 0, 1, 64, 1024, 1025} {
		cfg := Config{Repository: RepositoryConfig{Enabled: true, MySQLDSN: "local-test", ShardID: "1", MySQLPreparedCacheSize: &size}}
		cfg.Normalize(nil)
		if *cfg.Repository.MySQLPreparedCacheSize != size {
			t.Fatal("explicit size changed")
		}
		err := cfg.Validate()
		if (err != nil) != (size < 0 || size > 1024) {
			t.Fatalf("size=%d err=%v", size, err)
		}
	}
	cfg := Config{}
	cfg.Normalize(nil)
	if cfg.Repository.MySQLPreparedCacheSize == nil || *cfg.Repository.MySQLPreparedCacheSize != 64 {
		t.Fatal("default cache bound missing")
	}
}

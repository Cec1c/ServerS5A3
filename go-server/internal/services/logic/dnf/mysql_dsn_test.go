package dnf

import (
	driver "github.com/go-sql-driver/mysql"
	"testing"
)

func TestMySQLInterpolationDefaultAndExplicitOverride(t *testing.T) {
	for _, tc := range []struct {
		dsn              string
		enabled, invalid bool
	}{
		{"u:p@tcp(localhost:3306)/dnf?charset=utf8mb4,utf8&parseTime=true", true, false},
		{"u:p@tcp(localhost:3306)/dnf?interpolateParams=false", false, false},
		{"u:p@tcp(localhost:3306)/dnf?interpolateParams=true", true, false},
		{"u:p@tcp(localhost:3306)/dnf?collation=gbk_chinese_ci", false, true},
		{"u:p@tcp(localhost:3306)/dnf?charset=gbk", false, false},
		{"u:p@tcp(localhost:3306)/dnf?charset=gbk&interpolateParams=true", false, true},
		{"u:p@tcp(localhost:3306)/dnf?charset=cp932&interpolateParams=false", false, false},
	} {
		dsn, enabled, err := performanceMySQLDSN(tc.dsn)
		if (err != nil) != tc.invalid {
			t.Fatal("DSN validation result", err)
		}
		if tc.invalid {
			continue
		}
		parsed, err := driver.ParseDSN(dsn)
		if err != nil || enabled != tc.enabled || parsed.InterpolateParams != tc.enabled || parsed.User != "u" || parsed.Passwd != "p" {
			t.Fatal("DSN changed credentials or protocol")
		}
	}
	cfg := Config{Repository: RepositoryConfig{MySQLMaxOpenConns: 12}}
	cfg.Normalize(nil)
	if cfg.Repository.MySQLMaxIdleConns != 12 {
		t.Fatal("idle retention differs from configured pool size")
	}
}

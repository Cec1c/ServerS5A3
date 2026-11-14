package dnf

import (
	"fmt"
	driver "github.com/go-sql-driver/mysql"
	"net/url"
	"strings"
)

// Default to the driver's charset-aware interpolation. Explicit false retains
// the prepared protocol. ParseDSN validates unsafe multibyte collations before
// opening the pool; passwords and query values are never logged here.
func performanceMySQLDSN(dsn string) (string, bool, error) {
	cfg, err := driver.ParseDSN(dsn)
	if err != nil {
		return "", false, err
	}
	params := dsn[strings.LastIndex(dsn, "/")+1:]
	if question := strings.IndexByte(params, '?'); question >= 0 {
		params = params[question+1:]
	} else {
		params = ""
	}
	explicit := false
	for _, param := range strings.Split(params, "&") {
		if strings.HasPrefix(param, "interpolateParams=") {
			explicit = true
		}
	}
	if !explicit {
		cfg.InterpolateParams = true
	}
	// The driver rejects unsafe collations but accepts charset=gbk with its
	// default collation. Interpolating arbitrary bytes under those multibyte
	// encodings is unsafe too. Retain the prepared protocol for legacy DSNs.
	values, err := url.ParseQuery(params)
	if err != nil {
		return "", false, err
	}
	for _, charset := range strings.Split(strings.ToLower(values.Get("charset")), ",") {
		switch strings.TrimSpace(charset) {
		case "big5", "cp932", "gb2312", "gbk", "sjis":
			if explicit && cfg.InterpolateParams {
				return "", false, fmt.Errorf("MySQL interpolation does not support charset %s", charset)
			}
			cfg.InterpolateParams = false
		}
	}
	normalized := cfg.FormatDSN()
	if _, err := driver.ParseDSN(normalized); err != nil {
		return "", false, err
	}
	return normalized, cfg.InterpolateParams, nil
}

package gmadmin

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"regexp"
	"time"

	"github.com/go-sql-driver/mysql"
	"longheng.io/server/internal/modules/dnf/adminapi"
)

// Config belongs to GM, not to game instance generation. Secrets stay in a
// protected config file. No field is echoed by the command or HTTP status.
type Config struct {
	Listen          string   `json:"listen"`
	Password        string   `json:"password"`
	RegistrationDSN string   `json:"registrationDsn"`
	RepositoryDSN   string   `json:"repositoryDsn"`
	AccountTable    string   `json:"accountTable"`
	Databases       []string `json:"databases"`
	TablePrefix     string   `json:"tablePrefix"`
	PVFPath         string   `json:"pvfPath"`
	PVFSHA256       string   `json:"pvfSha256"`
	ItemIndexPath   string   `json:"itemIndexPath"`
	GameAPI         string   `json:"gameApi"`
	GameToken       string   `json:"gameToken"`
	ScanSeconds     int      `json:"scanSeconds"`
}

func ReadConfig(path string) (Config, error) {
	var cfg Config
	f, err := os.Open(path)
	if err != nil {
		return cfg, errors.New("GM config cannot be opened")
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 65537))
	d.DisallowUnknownFields()
	if d.Decode(&cfg) != nil || d.Decode(new(any)) != io.EOF {
		return cfg, errors.New("invalid GM config JSON")
	}
	return cfg, cfg.Validate()
}

func (c Config) Validate() error {
	host, _, err := net.SplitHostPort(c.Listen)
	ip := net.ParseIP(host)
	if err != nil || ip == nil || (!ip.IsLoopback() && !ip.IsPrivate()) {
		return errors.New("GM listener must use an explicit private or loopback IP")
	}
	if len(c.Password) < 32 {
		return errors.New("GM password must have at least 32 characters")
	}
	for _, dsn := range []string{c.RegistrationDSN, c.RepositoryDSN} {
		parsed, err := mysql.ParseDSN(dsn)
		if err != nil || parsed.DBName == "" || !parsed.ParseTime || parsed.MultiStatements {
			return errors.New("GM requires explicit MariaDB databases, parseTime=true and no multiStatements")
		}
	}
	id := regexp.MustCompile(`^[A-Za-z0-9_]{1,40}$`)
	if !id.MatchString(c.AccountTable) || !id.MatchString(c.TablePrefix) || len(c.Databases) == 0 {
		return errors.New("invalid GM database mapping")
	}
	for _, db := range c.Databases {
		if !id.MatchString(db) {
			return errors.New("invalid GM database name")
		}
	}
	if c.PVFPath == "" || !regexp.MustCompile(`^[a-fA-F0-9]{64}$`).MatchString(c.PVFSHA256) {
		return errors.New("GM requires an explicit PVF path and SHA256")
	}
	if c.ItemIndexPath == "" {
		return errors.New("GM requires an independent index path")
	}
	if _, err := adminapi.NewClient(c.GameAPI, c.GameToken); err != nil {
		return err
	}
	if c.ScanSeconds < 5 || c.ScanSeconds > 3600 {
		return errors.New("GM scan interval must be 5..3600 seconds")
	}
	return nil
}

func (c Config) scanInterval() time.Duration { return time.Duration(c.ScanSeconds) * time.Second }

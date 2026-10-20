// Package gmadmin assembles the independent GM process. It deliberately has no
// dependency on dnfbridge, game listeners or the game application's lifecycle.
package gmadmin

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/adminapi"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/mysql"
	"longheng.io/server/internal/modules/dnf/testbenefits"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"longheng.io/server/internal/services/gmtool"
)

func Run(ctx context.Context, cfg Config, initSchema bool, log func(string, ...any)) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if log == nil {
		log = func(string, ...any) {}
	}
	archive, err := platformpvf.Open(cfg.PVFPath)
	if err != nil {
		return errors.New("GM PVF cannot be decoded")
	}
	if !strings.EqualFold(archive.Snapshot().Checksum, cfg.PVFSHA256) {
		return errors.New("GM PVF SHA256 mismatch")
	}
	registration, err := sql.Open("mysql", cfg.RegistrationDSN)
	if err != nil {
		return errors.New("GM registration database configuration invalid")
	}
	defer registration.Close()
	data, err := sql.Open("mysql", cfg.RepositoryDSN)
	if err != nil {
		return errors.New("GM repository database configuration invalid")
	}
	defer data.Close()
	for _, db := range []*sql.DB{registration, data} {
		db.SetMaxOpenConns(8)
		db.SetMaxIdleConns(2)
		db.SetConnMaxLifetime(5 * time.Minute)
		check, cancel := context.WithTimeout(ctx, 10*time.Second)
		err = db.PingContext(check)
		cancel()
		if err != nil {
			return errors.New("GM MariaDB connection unavailable")
		}
	}
	// Reuse authoritative repository transactions without creating/migrating any
	// game tables or booting the game application.
	group, err := mysql.NewMySQLGroupFromDB(data, mysql.MySQLGroupOptions{TablePrefix: cfg.TablePrefix, DatabasePlan: repository.DatabasePlan{WriteDatabases: cfg.Databases, ReadDatabases: cfg.Databases}})
	if err != nil {
		return errors.New("GM repository mapping invalid")
	}
	benefits, err := testbenefits.NewStore(registration, cfg.AccountTable)
	if err != nil {
		return err
	}
	if initSchema {
		if err = benefits.EnsureSchema(ctx); err != nil {
			return errors.New("GM benefit schema initialization failed")
		}
	}
	if _, err = benefits.Campaigns(ctx); err != nil {
		return errors.New("GM benefit schema unavailable; run with --init-schema during installation")
	}
	runtime, err := adminapi.NewClient(cfg.GameAPI, cfg.GameToken)
	if err != nil {
		return err
	}
	deps := gmtool.Deps{Runtime: runtime, Benefits: benefits, Repos: func() (repository.Group, bool) { return group, true }, PVF: func() (dnfpvf.Source, error) { return archive, nil }}
	deps.PremiumTypes = func() ([]int64, error) {
		r, e := runtime.Call(ctx, adminapi.Request{Action: "catalog"})
		return r.PremiumTypes, e
	}
	deps.CrystalCubeItemIDs = func() ([6]int64, error) {
		r, e := runtime.Call(ctx, adminapi.Request{Action: "catalog"})
		return r.CrystalCubeItemIDs, e
	}
	server, err := gmtool.New(gmtool.Config{Listen: cfg.Listen, Password: cfg.Password, DSN: cfg.RegistrationDSN, Table: cfg.AccountTable, PVFPath: cfg.PVFPath, ItemIndexCachePath: cfg.ItemIndexPath, DatabaseLabel: "MariaDB", RequireOffline: true, Logger: log}, deps)
	if err != nil {
		return errors.New("GM service initialization failed")
	}
	defer server.Stop()
	if err = server.Start(ctx); err != nil {
		return errors.New("GM listener or database unavailable")
	}
	log("independent-gm-started")
	var cursor int64
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
			scan, cancel := context.WithTimeout(ctx, 20*time.Second)
			cursor, err = server.ScanNewCharacters(scan, cursor, 50)
			cancel()
			if err != nil {
				log("gm-benefit-scan-retry", "error", "scan incomplete; eligible deliveries will be retried")
			}
			timer.Reset(cfg.scanInterval())
		}
	}
}

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
)

type startOptions struct {
	rebuild            bool
	useRunningDatabase bool
}

// The subsequent normal doctor is the real connectivity/schema barrier.
func configureStartupDatabase(cfg instanceConfig, useRunning bool) (instanceConfig, error) {
	cfg.UseRunningDatabase = useRunning
	if !useRunning {
		return cfg, nil
	}
	if cfg.Protocol.Profile != "native-channel-profile-official-24.9" || cfg.Client.HookCreate {
		return instanceConfig{}, errors.New("--use-running-database requires the audited native profile with hookCreate=false")
	}
	if cfg.Database.Host != "127.0.0.1" && cfg.Database.Host != "localhost" {
		return instanceConfig{}, errors.New("--use-running-database requires the configured loopback database")
	}
	if err := validateInstance(cfg); err != nil {
		return instanceConfig{}, err
	}
	return cfg, nil
}
func rejectOwnedDatabaseState(path string) error {
	_, err := os.Stat(path)
	if err == nil {
		return errors.New("--use-running-database cannot mix with this installation's owned MySQL state; preserve the state and use the portable lifecycle")
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("inspect owned MySQL state before database reuse: %w", err)
	}
	return nil
}
func beginStartupDatabase(ctx context.Context, cfg instanceConfig, startPortable func(context.Context, instanceConfig) (bool, error)) (bool, error) {
	if cfg.UseRunningDatabase {
		return false, nil
	}
	return startPortable(ctx, cfg)
}
func shouldStopStartupDatabase(keepDatabase, _ bool, state processState) bool {
	return !keepDatabase && !state.UsesRunningDatabase
}

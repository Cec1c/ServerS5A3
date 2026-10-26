package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestInstanceMySQLInsertBatchRoundTripAndRuntimeBoundary(t *testing.T) {
	t.Setenv("DNFBRIDGE_MYSQL_INSERT_BATCH", "999")
	for _, size := range []int{0, 1, 200, 1000} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			cfg := validTestInstance()
			cfg.Database.InsertBatchRows = size
			data, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if size == 0 && strings.Contains(string(data), "insertBatchRows") {
				t.Fatal("legacy instance fixture must omit the batch setting")
			}
			var loaded instanceConfig
			if err := json.Unmarshal(data, &loaded); err != nil {
				t.Fatal(err)
			}
			if err := validateInstance(loaded); err != nil {
				t.Fatal(err)
			}
			if loaded.Database.InsertBatchRows != size {
				t.Fatal("batch setting lost on JSON round trip")
			}
			var matches []string
			for _, entry := range runtimeEnvironment(loaded) {
				if strings.HasPrefix(entry, "DNFBRIDGE_MYSQL_INSERT_BATCH=") {
					matches = append(matches, entry)
				}
			}
			want := size
			if want == 0 {
				want = 200
			}
			if len(matches) != 1 || matches[0] != fmt.Sprintf("DNFBRIDGE_MYSQL_INSERT_BATCH=%d", want) {
				t.Fatalf("runtime batch setting = %v, want %d (inherited 999 must be ignored)", matches, want)
			}
		})
	}
}

func TestInstanceMySQLInsertBatchExplicitZeroUsesDefault(t *testing.T) {
	cfg := validTestInstance()
	if err := json.Unmarshal([]byte(`{"database":{"insertBatchRows":0}}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if err := validateInstance(cfg); err != nil {
		t.Fatal(err)
	}
	for _, entry := range runtimeEnvironment(cfg) {
		if entry == "DNFBRIDGE_MYSQL_INSERT_BATCH=200" {
			return
		}
	}
	t.Fatal("explicit zero did not enable the default batch size")
}

func TestInstanceMySQLInsertBatchRejectsInvalidBounds(t *testing.T) {
	for _, size := range []int{-1, 1001} {
		cfg := validTestInstance()
		cfg.Database.InsertBatchRows = size
		if err := validateInstance(cfg); err == nil || !strings.Contains(err.Error(), "database.insertBatchRows") {
			t.Fatalf("batch=%d validation=%v", size, err)
		}
	}
}

package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestInstanceMySQLInsertBatchRoundTripAndRuntimeBoundary(t *testing.T) {
	for _, size := range []int{0, 1, 200, 1000} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			t.Setenv("DNFBRIDGE_MYSQL_INSERT_BATCH", "999")
			t.Setenv("DNF90_MYSQL_INSERT_BATCH", "")
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
	t.Setenv("DNFBRIDGE_MYSQL_INSERT_BATCH", "999")
	t.Setenv("DNF90_MYSQL_INSERT_BATCH", "")
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

func TestDN90MySQLInsertBatchLaunchOverride(t *testing.T) {
	for _, tc := range []struct {
		name      string
		instance  int
		dnf90     string
		wantBatch string
	}{
		{"default", 0, "", "200"},
		{"row-by-row", 1, "", "1"},
		{"custom", 64, "", "64"},
		{"override-row-by-row", 200, "1", "1"},
		{"override-200", 1, "200", "200"},
		{"override-spaces", 1, " 200 ", "200"},
		{"invalid-falls-back", 64, "bad", "64"},
		{"zero-falls-back", 64, "0", "64"},
		{"high-falls-back", 64, "1001", "64"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validTestInstance()
			cfg.Database.InsertBatchRows = tc.instance
			if err := validateInstance(cfg); err != nil {
				t.Fatal(err)
			}
			t.Setenv("DNFBRIDGE_MYSQL_INSERT_BATCH", "999")
			t.Setenv("DNF90_MYSQL_INSERT_BATCH", tc.dnf90)
			for _, entry := range runtimeEnvironment(cfg) {
				if strings.HasPrefix(entry, "DNFBRIDGE_MYSQL_INSERT_BATCH=") {
					if entry != "DNFBRIDGE_MYSQL_INSERT_BATCH="+tc.wantBatch {
						t.Fatalf("batch entry=%q, want %s", entry, tc.wantBatch)
					}
					return
				}
			}
			t.Fatal("runtime environment omitted DNFBRIDGE_MYSQL_INSERT_BATCH")
		})
	}
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

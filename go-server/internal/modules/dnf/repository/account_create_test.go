package repository

import (
	"context"
	"errors"
	"testing"

	"longheng.io/server/internal/platform/db"
)

func TestAccountCreateIfAbsentNeverFallsBackToUnsafeSave(t *testing.T) {
	store := db.NewMemoryStore(AccountKey, CloneAccount)
	created, err := CreateAccountIfAbsent(context.Background(), store, AccountRecord{AccountID: "a"})
	if created || !errors.Is(err, ErrAccountCreationUnavailable) {
		t.Fatal(created, err)
	}
	if _, found, _ := store.Load(context.Background(), "a"); found {
		t.Fatal("不支持原子创建时退化成了不安全的Save")
	}
}

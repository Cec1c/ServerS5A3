package dnfbridge

import (
	"encoding/binary"
	"fmt"
	"longheng.io/server/internal/modules/dnf/premium"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
	"time"
)

// This checks the server relog projection boundary, not acceptance by native EXE.
func TestAdminMembershipRelogProjectionBoundary(t *testing.T) {
	now := time.Unix(1791557436, 0)
	account := repo.AccountRecord{AccountID: "candidate-account", Metadata: map[string]string{}}
	result, err := premium.GrantAdminContract(&account, "candidate-character", 17, 30, "candidate-membership-17", now)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExpireAt != now.Unix()+30*86400 {
		t.Fatal(result)
	}
	body := premium.SelectAckEntries(account, now)
	found := false
	for off := 1; off+9 <= len(body); off += 9 {
		if body[off] == 17 {
			if int64(binary.LittleEndian.Uint64(body[off+1:])) != 30*86400 {
				t.Fatalf("wrong 17 duration: %x", body)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("no 17 in select body: %x", body)
	}
	replay, err := premium.GrantAdminContract(&account, "candidate-character", 17, 30, "candidate-membership-17", now)
	if err != nil || !replay.Replayed || replay.ExpireAt != result.ExpireAt {
		t.Fatalf("replay=%+v %v", replay, err)
	}
	before := fmt.Sprint(account.Metadata)
	if _, err := premium.GrantAdminContract(&account, "candidate-character", 255, 30, "candidate-unknown-255", now); err == nil {
		t.Fatal("unknown type accepted")
	}
	if fmt.Sprint(account.Metadata) != before {
		t.Fatal("unknown rejection mutated metadata")
	}
	t.Logf("server select ACK premium vector: %x", body)
}

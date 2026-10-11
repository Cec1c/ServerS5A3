package premium

import (
	"errors"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// TestActivateContractGrantStacksFromLaterExpiry 钉住"叠加时长"的算术：
// new expire = max(now, current expire) + duration*count（与
// ActivateInventoryContracts / 86JP PremiumService 同一口径）。
func TestActivateContractGrantStacksFromLaterExpiry(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	contract := InventoryContract{ItemID: 30, PremiumType: TypeOverEquip, DurationSeconds: 86400}

	t.Run("已有契约未到期 → 从原到期时间继续叠加", func(t *testing.T) {
		account := dnfrepo.AccountRecord{
			AccountID: "dnf:10005",
			Metadata:  map[string]string{MetadataKey(TypeOverEquip): "1700005000"},
		}
		activation, err := ActivateContractGrant(&account, contract, 1, now)
		if err != nil {
			t.Fatalf("ActivateContractGrant: %v", err)
		}
		if activation.ExpireAt != 1700005000+86400 || activation.DurationSeconds != 86400 ||
			activation.Units != 1 || activation.PremiumType != TypeOverEquip || activation.ItemID != 30 {
			t.Fatalf("activation = %+v", activation)
		}
		if got := ExpireAt(account, TypeOverEquip); got != activation.ExpireAt {
			t.Fatalf("account expire = %d, activation = %d", got, activation.ExpireAt)
		}
	})

	t.Run("无契约/已过期 → 从 now 起算", func(t *testing.T) {
		account := dnfrepo.AccountRecord{
			AccountID: "dnf:10005",
			Metadata:  map[string]string{MetadataKey(TypeOverEquip): "1600000000"},
		}
		activation, err := ActivateContractGrant(&account, contract, 1, now)
		if err != nil {
			t.Fatalf("ActivateContractGrant: %v", err)
		}
		if activation.ExpireAt != now.Unix()+86400 {
			t.Fatalf("expire = %d, want %d", activation.ExpireAt, now.Unix()+86400)
		}
	})

	t.Run("多份同型契约按份数叠加", func(t *testing.T) {
		account := dnfrepo.AccountRecord{AccountID: "dnf:10005"}
		activation, err := ActivateContractGrant(&account, contract, 3, now)
		if err != nil {
			t.Fatalf("ActivateContractGrant: %v", err)
		}
		if activation.Units != 3 || activation.DurationSeconds != 3*86400 ||
			activation.ExpireAt != now.Unix()+3*86400 {
			t.Fatalf("activation = %+v", activation)
		}
	})

	t.Run("非法输入全部拒绝且不写账号", func(t *testing.T) {
		account := dnfrepo.AccountRecord{AccountID: "dnf:10005"}
		if _, err := ActivateContractGrant(nil, contract, 1, now); !errors.Is(err, ErrInventoryActivationAccountRequired) {
			t.Fatalf("nil account error = %v", err)
		}
		if _, err := ActivateContractGrant(&account, InventoryContract{ItemID: 30, PremiumType: 0, DurationSeconds: 86400}, 1, now); !errors.Is(err, ErrInventoryActivationContractInvalid) {
			t.Fatalf("invalid contract error = %v", err)
		}
		if _, err := ActivateContractGrant(&account, contract, 0, now); !errors.Is(err, ErrInventoryActivationCountInvalid) {
			t.Fatalf("zero count error = %v", err)
		}
		if len(account.Metadata) != 0 {
			t.Fatalf("rejected activation wrote metadata: %+v", account.Metadata)
		}
	})
}

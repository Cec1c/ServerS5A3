package premium

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestBlackDiamondActivationUsesNativeMemberTypeWithoutChangingAccount(t *testing.T) {
	now := time.Unix(1700000000, 0)
	account := dnfrepo.AccountRecord{Metadata: map[string]string{MetadataKey(TypeOverEquip): "1700086400"}}
	Upsert(&account, TypeBlackDiamond, 30*86400, 1, now)
	before := map[string]string{}
	for k, v := range account.Metadata {
		before[k] = v
	}
	// Native 56 -> dstr FILE/102 string 71003 (黑钻会员), PVF frames 20/19.
	// Native 17 -> frames 14/13 (霸王之契约), which caused the duplicate banner.
	want := []byte{2, 0, 56, 0, 0x8d, 0x27, 0, 0, 0, 0, 0}
	if got := BuildActivatedBody(TypeBlackDiamond, 30*86400); !bytes.Equal(got, want) {
		t.Fatalf("activation = %x, want native Black Diamond %x", got, want)
	}
	if !Active(account, TypeBlackDiamond, now) || ExpireAt(account, 56) != 0 ||
		!reflect.DeepEqual(account.Metadata, before) {
		t.Fatalf("wire mapping changed persisted entitlements: %v", account.Metadata)
	}
	if got := BuildActivatedBody(TypeOverEquip, 86400); got[2] != 22 {
		t.Fatalf("legitimate over-equip activation changed: %x", got)
	}
}

func TestBlackDiamondSelectKeepsOneNativeBannerAndAllRealContracts(t *testing.T) {
	now := time.Unix(1700000000, 0)
	for _, nativeDays := range []int64{0, 1, 60} {
		t.Run((time.Duration(nativeDays) * 24 * time.Hour).String(), func(t *testing.T) {
			account := dnfrepo.AccountRecord{Metadata: map[string]string{}}
			Upsert(&account, TypeBlackDiamond, 30*86400, 1, now)
			for _, typ := range []int64{22, 27, 84, 97} {
				Upsert(&account, typ, 86400, 1, now)
			}
			if nativeDays > 0 {
				Upsert(&account, 56, nativeDays*86400, 1, now)
			}
			got := SelectAckEntries(account, now)
			if len(got) != 46 || got[0] != 5 {
				t.Fatalf("select length/count = %x, want four real contracts and one member", got)
			}
			wantTypes := []byte{22, 27, 84, 97, 56}
			for i, typ := range wantTypes {
				offset := 1 + 9*i
				remaining := int64(binary.LittleEndian.Uint64(got[offset+1 : offset+9]))
				want := int64(86400)
				if typ == 56 {
					want = 30 * 86400
					if nativeDays > 30 {
						want = nativeDays * 86400
					}
				}
				if got[offset] != typ || remaining != want {
					t.Fatalf("entry %d = %d/%d, want %d/%d", i, got[offset], remaining, typ, want)
				}
			}
			if ExpireAt(account, TypeBlackDiamond) != now.Unix()+30*86400 {
				t.Fatal("select display rewrote the purchased entitlement")
			}
		})
	}
}

func TestExpiredBlackDiamondHasNoNativeBanner(t *testing.T) {
	now := time.Unix(1700000000, 0)
	account := dnfrepo.AccountRecord{Metadata: map[string]string{MetadataKey(TypeBlackDiamond): "1700000000"}}
	if got := SelectAckEntries(account, now); !bytes.Equal(got, []byte{0}) {
		t.Fatalf("expired membership still displayed: %x", got)
	}
}

package localaccount

import (
	"context"
	"errors"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

type credentialFixture struct {
	credential         Credential
	found              bool
	loadErr, recordErr error
	loads, records     int
}

func (f *credentialFixture) LoadCredential(context.Context, string) (Credential, bool, error) {
	f.loads++
	c := f.credential
	c.PasswordHash = append([]byte(nil), c.PasswordHash...)
	return c, f.found, f.loadErr
}
func (f *credentialFixture) RecordLogin(context.Context, Credential) error {
	f.records++
	return f.recordErr
}

func TestNativeAuth42CredentialsRequirePasswordStateAndCanonicalOwner(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("offline-fixture-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal("fixture hash failed")
	}
	for _, kind := range []string{"valid", "wrong_password", "disabled", "missing", "alias", "bad_owner", "lookup_error", "changed_credential", "cancelled", "bad_username", "short_password"} {
		t.Run(kind, func(t *testing.T) {
			f := &credentialFixture{credential: Credential{AccountID: "dnf:10029", Username: "q7a", PasswordHash: hash, State: "active"}, found: true}
			username, password := " Q7A ", "offline-fixture-password"
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch kind {
			case "wrong_password":
				password = "different-fixture-password"
			case "disabled":
				f.credential.State = "disabled"
			case "missing":
				f.found = false
			case "alias":
				f.credential.Username = "q7á"
			case "bad_owner":
				f.credential.AccountID = "bad owner"
			case "lookup_error":
				f.loadErr = errors.New("offline error")
			case "changed_credential":
				f.recordErr = errors.New("credential changed")
			case "cancelled":
				cancel()
			case "bad_username":
				username = "ab"
			case "short_password":
				password = "short"
			}
			account, err := Authenticate(ctx, f, username, password)
			if kind == "valid" {
				if err != nil || account != "dnf:10029" || f.records != 1 {
					t.Fatal("valid credential did not authenticate")
				}
			} else if err == nil || account != "" {
				t.Fatal("invalid credential authenticated")
			}
			if kind != "valid" && kind != "changed_credential" && f.records != 0 {
				t.Fatal("rejected credential wrote login time")
			}
			if len(hash) == 0 || hash[0] == 0 {
				t.Fatal("repository-owned hash was cleared")
			}
		})
	}
	if _, err := Authenticate(context.Background(), nil, "q7a", "offline-fixture-password"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("missing repository authenticated")
	}
}

// Package localaccount authenticates the existing Windows local-account schema.
package localaccount

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

var (
	ErrCredentials   = errors.New("local account authentication failed")
	ErrUnavailable   = errors.New("local account repository unavailable")
	accountIDPattern = regexp.MustCompile(`^[A-Za-z0-9_:.\-]{1,128}$`)
)

// Credential is never returned over HTTP or written to logs.
type Credential struct {
	AccountID    string
	Username     string
	PasswordHash []byte
	State        string
}

// Repository reads the existing dnf_login_accounts and records a successful
// login only if the verified credential and active state still match.
// LoadCredential returns an owned PasswordHash buffer; Authenticate clears it.
type Repository interface {
	LoadCredential(context.Context, string) (Credential, bool, error)
	RecordLogin(context.Context, Credential) error
}

func NormalizeUsername(username string) (string, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	n := utf8.RuneCountInString(username)
	if !utf8.ValidString(username) || n < 3 || n > 32 {
		return "", ErrCredentials
	}
	for _, c := range username {
		if !unicode.IsLetter(c) && !unicode.IsDigit(c) && c != '_' && c != '-' && c != '.' {
			return "", ErrCredentials
		}
	}
	return username, nil
}

func ValidatePassword(password string) error {
	if !utf8.ValidString(password) || len(password) < 8 || len(password) > 72 {
		return ErrCredentials
	}
	for _, c := range password {
		if unicode.IsControl(c) {
			return ErrCredentials
		}
	}
	return nil
}

func ValidAccountID(account string) bool { return accountIDPattern.MatchString(account) }

func Authenticate(ctx context.Context, repo Repository, username, password string) (string, error) {
	username, err := NormalizeUsername(username)
	if err != nil || ValidatePassword(password) != nil {
		return "", ErrCredentials
	}
	if repo == nil {
		return "", ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	credential, found, err := repo.LoadCredential(ctx, username)
	if err != nil {
		return "", ErrUnavailable
	}
	defer clear(credential.PasswordHash)
	if !found || credential.Username != username || credential.State != "active" || !ValidAccountID(credential.AccountID) {
		return "", ErrCredentials
	}
	if bcrypt.CompareHashAndPassword(credential.PasswordHash, []byte(password)) != nil {
		return "", ErrCredentials
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := repo.RecordLogin(ctx, credential); err != nil {
		return "", ErrUnavailable
	}
	return credential.AccountID, nil
}

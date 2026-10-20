package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/localaccount"
)

type localAccountToken struct {
	AccountID string    `json:"account_id"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

type localTokenRequester interface {
	Do(*http.Request) (*http.Response, error)
}

func (c *controller) requestLocalLoginToken(ctx context.Context, cfg instanceConfig, username, password string) (localAccountToken, error) {
	// Credentials must never follow a redirect or an inherited HTTP proxy.
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return requestLocalLoginToken(ctx, cfg, username, password, client)
}

func requestLocalLoginToken(ctx context.Context, cfg instanceConfig, username, password string, client localTokenRequester) (localAccountToken, error) {
	username, err := localaccount.NormalizeUsername(username)
	if err != nil || localaccount.ValidatePassword(password) != nil {
		return localAccountToken{}, localaccount.ErrCredentials
	}
	if err := validateLoopbackListenAddress("server.adminListen", cfg.Server.AdminListen); err != nil {
		return localAccountToken{}, err
	}
	if err := validateSafeValue("server.adminToken", cfg.Server.AdminToken); err != nil {
		return localAccountToken{}, errors.New("local login admin authentication unavailable")
	}
	url, err := clientAccountURL(cfg)
	if err != nil {
		return localAccountToken{}, err
	}
	url = strings.TrimSuffix(url, "/local/client-account") + "/local/account/login"
	body, err := json.Marshal(struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}{username, password})
	if err != nil {
		return localAccountToken{}, localaccount.ErrCredentials
	}
	defer clear(body)
	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return localAccountToken{}, errors.New("local login request failed")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Admin-Token", cfg.Server.AdminToken)
	response, err := client.Do(request)
	if err != nil {
		return localAccountToken{}, errors.New("local login request failed")
	}
	if response == nil || response.Body == nil {
		return localAccountToken{}, errors.New("local login response missing")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return localAccountToken{}, errors.New("local login rejected")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	defer clear(data)
	if err != nil || len(data) > 4096 {
		return localAccountToken{}, errors.New("invalid local login response")
	}
	var result localAccountToken
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil {
		return localAccountToken{}, errors.New("invalid local login response")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return localAccountToken{}, errors.New("invalid local login response")
	}
	if !validLocalAccountToken(result, cfg) {
		return localAccountToken{}, errors.New("local login did not issue a valid token")
	}
	return result, nil
}

func validLocalAccountToken(result localAccountToken, cfg instanceConfig) bool {
	if !localaccount.ValidAccountID(result.AccountID) || len(result.Token) != 32 || len(cfg.Protocol.GameOuterToken) != 32 {
		return false
	}
	if _, err := hex.DecodeString(result.Token); err != nil {
		return false
	}
	if result.Token != strings.ToLower(result.Token) || result.Token[:8] != strings.ToLower(cfg.Protocol.GameOuterToken[:8]) || strings.EqualFold(result.Token, cfg.Protocol.GameOuterToken) {
		return false
	}
	now := time.Now()
	return result.ExpiresAt.After(now) && !result.ExpiresAt.After(now.Add(16*time.Minute))
}

// The start callback is reached only with a current service-issued credential
// token. The profile/config is a value copy and is never persisted or logged.
func launchWithLocalLoginToken(ctx context.Context, cfg instanceConfig, username, password string, authenticate func(context.Context, instanceConfig, string, string) (localAccountToken, error), start func(string, string) error) error {
	if _, err := localaccount.NormalizeUsername(username); err != nil {
		return err
	}
	if err := localaccount.ValidatePassword(password); err != nil {
		return err
	}
	result, err := authenticate(ctx, cfg, username, password)
	if err != nil {
		return err
	}
	if !validLocalAccountToken(result, cfg) {
		return errors.New("local login did not issue a valid token")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	launchCfg := cfg
	launchCfg.Protocol.GameOuterToken = result.Token
	argument, err := clientLaunchArgument(launchCfg)
	if err != nil {
		return err
	}
	return start(result.AccountID, argument)
}

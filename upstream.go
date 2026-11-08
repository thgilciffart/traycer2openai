package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

type Model struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Provider        string   `json:"provider"`
	ContextWindow   int      `json:"contextWindow"`
	MaxOutputTokens int      `json:"maxOutputTokens"`
	SupportsImages  bool     `json:"supportsImages"`
	Efforts         []string `json:"efforts"`
	DefaultEffort   string   `json:"defaultEffort,omitempty"`
}

type Upstream struct {
	Server string
	Authn  string
	HTTP   *http.Client
}

// NewUpstream builds a client with no overall Timeout: long-lived streams are
// bounded by the caller's context, only dial/TLS/response-header phases are
// bounded by timeout.
func NewUpstream(server, authn string, timeout time.Duration) *Upstream {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	transport := &http.Transport{
		// Honour HTTP_PROXY / HTTPS_PROXY / NO_PROXY. Go does not read these on
		// its own, so without this the binary silently bypasses a debugging proxy
		// (mitmproxy) even when the environment asks for one.
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          16,
	}
	return &Upstream{
		Server: server,
		Authn:  authn,
		HTTP: &http.Client{
			Transport: transport,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

type AuthError struct{ Reason string }

func (e *AuthError) Error() string { return "traycer auth error: " + e.Reason }

type wireRetryable struct{ msg string }

func (e *wireRetryable) Error() string { return e.msg }

func IsRetryable(err error) bool {
	var authErr *AuthError
	if errors.As(err, &authErr) {
		return false
	}
	var retryable *wireRetryable
	return errors.As(err, &retryable)
}

func wireTruncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n])
}

func wireDo(u *Upstream, req *http.Request) (*http.Response, error) {
	resp, err := u.HTTP.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, &wireRetryable{msg: err.Error()}
	}
	return resp, nil
}

func wireNewRequest(ctx context.Context, method, url string, body io.Reader, a *Account) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("authorization", "Bearer "+a.Token)
	req.Header.Set("accept", "application/json")
	req.Header.Set("x-traycer-account-context", a.AccountContextHeader())
	if body != nil {
		req.Header.Set("content-type", "application/json")
	}
	return req, nil
}

func (u *Upstream) Responses(ctx context.Context, a *Account, body []byte) (*http.Response, error) {
	req, err := wireNewRequest(ctx, http.MethodPost, u.Server+"/inference/openai/responses", bytes.NewReader(body), a)
	if err != nil {
		return nil, err
	}
	req.Header.Set("accept-encoding", "identity")
	return wireDo(u, req)
}

// GeminiStream posts a native Gemini generateContent body to Traycer's gemini
// route. That route is billed against the account's plan rather than Traycer
// inference credits, so it works when /inference/openai/responses returns 402.
func (u *Upstream) GeminiStream(ctx context.Context, a *Account, model string, body []byte) (*http.Response, error) {
	req, err := wireNewRequest(ctx, http.MethodPost, geminiURL(u.Server, model), bytes.NewReader(body), a)
	if err != nil {
		return nil, err
	}
	req.Header.Set("accept", "*/*")
	req.Header.Set("accept-encoding", "identity")
	return wireDo(u, req)
}

type wireUpstreamModel struct {
	ID                        string `json:"id"`
	Name                      string `json:"name"`
	Provider                  string `json:"provider"`
	ContextWindow             int    `json:"contextWindow"`
	MaxOutputTokens           int    `json:"maxOutputTokens"`
	SupportsImages            bool   `json:"supportsImages"`
	DefaultReasoningEffort    string `json:"defaultReasoningEffort"`
	SupportedReasoningEfforts []struct {
		ID string `json:"id"`
	} `json:"supportedReasoningEfforts"`
}

func (u *Upstream) Models(ctx context.Context, a *Account) ([]Model, error) {
	req, err := wireNewRequest(ctx, http.MethodGet, u.Server+"/api/traycer/models?nativeProviders=gemini", nil, a)
	if err != nil {
		return nil, err
	}
	resp, err := wireDo(u, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, &wireRetryable{msg: "models: read body: " + err.Error()}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("models: %s: %s", resp.Status, wireTruncate(body, 300))
	}
	var raw []wireUpstreamModel
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("models: decode: %w", err)
	}
	models := make([]Model, 0, len(raw))
	for _, m := range raw {
		efforts := make([]string, 0, len(m.SupportedReasoningEfforts))
		for _, e := range m.SupportedReasoningEfforts {
			efforts = append(efforts, e.ID)
		}
		models = append(models, Model{
			ID:              m.ID,
			Name:            m.Name,
			Provider:        m.Provider,
			ContextWindow:   m.ContextWindow,
			MaxOutputTokens: m.MaxOutputTokens,
			SupportsImages:  m.SupportsImages,
			Efforts:         efforts,
			DefaultEffort:   m.DefaultReasoningEffort,
		})
	}
	return models, nil
}

func (u *Upstream) Refresh(ctx context.Context, a *Account) (token, refreshToken string, err error) {
	payload, err := json.Marshal(struct {
		RefreshToken string `json:"refreshToken"`
	}{RefreshToken: a.RefreshToken})
	if err != nil {
		return "", "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.Authn+"/api/v3/auth/refresh", bytes.NewReader(payload))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("authorization", "Bearer "+a.Token)
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "application/json")
	resp, err := wireDo(u, req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return "", "", &wireRetryable{msg: "refresh: read body: " + err.Error()}
	}
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		var out struct {
			Token        string `json:"token"`
			RefreshToken string `json:"refreshToken"`
			FamilyID     string `json:"familyId"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			return "", "", fmt.Errorf("refresh: decode 200 body: %w", err)
		}
		if out.Token == "" {
			return "", "", fmt.Errorf("refresh: %s: 200 body has empty token", resp.Status)
		}
		return out.Token, out.RefreshToken, nil
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		var e struct {
			Reason string `json:"reason"`
		}
		_ = json.Unmarshal(body, &e)
		if e.Reason == "" {
			e.Reason = "unknown"
		}
		return "", "", &AuthError{Reason: e.Reason}
	case resp.StatusCode == 409 || resp.StatusCode == 429 || resp.StatusCode >= 500:
		return "", "", &wireRetryable{msg: fmt.Sprintf("refresh: %s: %s", resp.Status, wireTruncate(body, 300))}
	default:
		return "", "", fmt.Errorf("refresh: unexpected status %s: %s", resp.Status, wireTruncate(body, 300))
	}
}

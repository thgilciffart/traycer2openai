package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Traycer's only interactive login is the OAuth 2.0 Device Authorization Grant
// (RFC 8628) against authn.traycer.ai. It needs no client secret: the desktop
// client id is public and the grant is approved in a browser, so a proxy can
// drive the same flow and hand back a normal token pair.
const deviceClientID = "desktop"

// DeviceAuth is what /device/authorize returns.
type DeviceAuth struct {
	DeviceCode              string
	UserCode                string
	VerificationURI         string
	VerificationURIComplete string
	ExpiresIn               int
	Interval                int
}

func (u *Upstream) deviceURL(endpoint string) string {
	return strings.TrimRight(u.Authn, "/") + "/api/v3/auth/device/" + endpoint
}

func (u *Upstream) DeviceAuthorize(ctx context.Context, hostLabel string) (*DeviceAuth, error) {
	payload, err := json.Marshal(map[string]string{
		"client_id":  deviceClientID,
		"host_label": hostLabel,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.deviceURL("authorize"), bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("accept", "application/json")
	req.Header.Set("content-type", "application/json")
	resp, err := wireDo(u, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, &wireRetryable{msg: "device authorize: read body: " + err.Error()}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("device authorize: %s: %s", resp.Status, wireTruncate(body, 300))
	}
	var out struct {
		DeviceCode              string `json:"device_code"`
		UserCode                string `json:"user_code"`
		VerificationURI         string `json:"verification_uri"`
		VerificationURIComplete string `json:"verification_uri_complete"`
		ExpiresIn               int    `json:"expires_in"`
		Interval                int    `json:"interval"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("device authorize: decode: %w", err)
	}
	if out.DeviceCode == "" || out.UserCode == "" || out.VerificationURI == "" {
		return nil, fmt.Errorf("device authorize: incomplete response")
	}
	if out.Interval <= 0 {
		out.Interval = 5
	}
	if out.ExpiresIn <= 0 {
		out.ExpiresIn = 600
	}
	return &DeviceAuth{
		DeviceCode:              out.DeviceCode,
		UserCode:                out.UserCode,
		VerificationURI:         out.VerificationURI,
		VerificationURIComplete: out.VerificationURIComplete,
		ExpiresIn:               out.ExpiresIn,
		Interval:                out.Interval,
	}, nil
}

// DevicePoll states. The caller keeps polling on pending and slow_down.
const (
	devicePending    = "pending"
	deviceSlowDown   = "slow_down"
	deviceAuthorized = "authorized"
	deviceDenied     = "denied"
	deviceExpired    = "expired"
	deviceInvalid    = "invalid"
)

func (u *Upstream) DevicePoll(ctx context.Context, deviceCode string) (state, token, refresh string, retryAfter int, err error) {
	payload, err := json.Marshal(map[string]string{
		"device_code": deviceCode,
		"client_id":   deviceClientID,
	})
	if err != nil {
		return "", "", "", 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.deviceURL("token"), bytes.NewReader(payload))
	if err != nil {
		return "", "", "", 0, err
	}
	req.Header.Set("accept", "application/json")
	req.Header.Set("content-type", "application/json")
	resp, err := wireDo(u, req)
	if err != nil {
		// A dropped poll is not fatal; the caller simply tries again.
		return devicePending, "", "", 0, nil
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return devicePending, "", "", 0, nil
	}

	switch resp.StatusCode {
	case http.StatusOK:
		var out struct {
			Token        string `json:"token"`
			RefreshToken string `json:"refreshToken"`
		}
		if err := json.Unmarshal(body, &out); err != nil || out.Token == "" || out.RefreshToken == "" {
			return deviceInvalid, "", "", 0, fmt.Errorf("device token: unrecognized 200 body")
		}
		return deviceAuthorized, out.Token, out.RefreshToken, 0, nil
	case http.StatusTooManyRequests:
		secs, _ := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After")))
		return deviceSlowDown, "", "", secs, nil
	}

	var env struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &env)
	switch env.Error {
	case "authorization_pending":
		return devicePending, "", "", 0, nil
	case "slow_down":
		return deviceSlowDown, "", "", 0, nil
	case "access_denied":
		return deviceDenied, "", "", 0, nil
	case "expired":
		return deviceExpired, "", "", 0, nil
	}
	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		return deviceInvalid, "", "", 0, fmt.Errorf("device token: %s: %s", resp.Status, wireTruncate(body, 200))
	}
	return devicePending, "", "", 0, nil
}

// ---------------------------------------------------------------- attempts

type deviceAttempt struct {
	auth      *DeviceAuth
	name      string
	startedAt time.Time
	lastPoll  time.Time
	interval  time.Duration
	state     string
	accountID string
	errMsg    string
}

type deviceAttempts struct {
	mu sync.Mutex
	m  map[string]*deviceAttempt
}

func newDeviceAttempts() *deviceAttempts {
	return &deviceAttempts{m: map[string]*deviceAttempt{}}
}

func (d *deviceAttempts) put(a *deviceAttempt) string {
	id := newID()
	d.mu.Lock()
	// Keep the map from growing without bound if a user opens many panels.
	for k, v := range d.m {
		if time.Since(v.startedAt) > 20*time.Minute {
			delete(d.m, k)
		}
	}
	d.m[id] = a
	d.mu.Unlock()
	return id
}

func (d *deviceAttempts) get(id string) *deviceAttempt {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.m[id]
}

func (d *deviceAttempts) drop(id string) {
	d.mu.Lock()
	delete(d.m, id)
	d.mu.Unlock()
}

func (a *deviceAttempt) terminal() bool {
	switch a.state {
	case deviceAuthorized, deviceDenied, deviceExpired, deviceInvalid:
		return true
	}
	return false
}

// ---------------------------------------------------------------- handlers

func (s *Server) handleDeviceStart(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&in)

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	auth, err := s.up.DeviceAuthorize(ctx, s.cfg.Bind+":"+itoa(s.cfg.Port))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	a := &deviceAttempt{
		auth:      auth,
		name:      strings.TrimSpace(in.Name),
		startedAt: time.Now(),
		interval:  time.Duration(auth.Interval) * time.Second,
		state:     devicePending,
	}
	id := s.devices.put(a)
	writeJSON(w, http.StatusOK, map[string]any{
		"attemptId":               id,
		"userCode":                auth.UserCode,
		"verificationUri":         auth.VerificationURI,
		"verificationUriComplete": auth.VerificationURIComplete,
		"intervalSeconds":         auth.Interval,
		"expiresInSeconds":        auth.ExpiresIn,
	})
}

func (s *Server) handleDeviceStatus(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("attempt")
	a := s.devices.get(id)
	if a == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"state": deviceInvalid, "error": "no such attempt"})
		return
	}

	s.devices.mu.Lock()
	due := time.Since(a.lastPoll) >= a.interval
	state := a.state
	terminal := a.terminal()
	if !terminal && time.Since(a.startedAt) > time.Duration(a.auth.ExpiresIn)*time.Second {
		a.state, state, terminal = deviceExpired, deviceExpired, true
	}
	s.devices.mu.Unlock()

	if !due || terminal {
		writeJSON(w, http.StatusOK, s.deviceResponse(a, state))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	next, token, refresh, retryAfter, err := s.up.DevicePoll(ctx, a.auth.DeviceCode)

	s.devices.mu.Lock()
	a.lastPoll = time.Now()
	switch next {
	case deviceSlowDown:
		step := a.interval + 5*time.Second
		if retryAfter > 0 {
			step = time.Duration(retryAfter) * time.Second
		}
		if step > 60*time.Second {
			step = 60 * time.Second
		}
		a.interval = step
		a.state = devicePending
	case deviceAuthorized:
		a.state = deviceAuthorized
	case devicePending:
		a.interval = time.Duration(a.auth.Interval) * time.Second
		a.state = devicePending
	default:
		a.state = next
	}
	if err != nil && next == deviceInvalid {
		a.errMsg = err.Error()
	}
	s.devices.mu.Unlock()

	if next == deviceAuthorized {
		acc, addErr := s.store.Add(s.deviceAccountName(a.name), token, refresh, "", "")
		s.devices.mu.Lock()
		if addErr != nil {
			a.state = deviceInvalid
			a.errMsg = addErr.Error()
		} else {
			a.accountID = acc.ID
			_ = s.store.SetActive(acc.ID)
			_ = s.store.Save()
		}
		s.devices.mu.Unlock()
	}

	writeJSON(w, http.StatusOK, s.deviceResponse(a, a.state))
}

func (s *Server) deviceResponse(a *deviceAttempt, state string) map[string]any {
	out := map[string]any{"state": state}
	if a.errMsg != "" {
		out["error"] = a.errMsg
	}
	if a.accountID != "" {
		if acc := s.store.ByID(a.accountID); acc != nil {
			out["account"] = map[string]any{"id": acc.ID, "name": acc.Name}
		}
	}
	return out
}

func (s *Server) handleDeviceCancel(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Attempt string `json:"attempt"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&in)
	s.devices.drop(in.Attempt)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// deviceAccountName keeps account names unique without asking the user for one.
func (s *Server) deviceAccountName(want string) string {
	if want == "" {
		want = "traycer"
	}
	taken := map[string]bool{}
	for _, a := range s.store.List() {
		taken[strings.ToLower(a.Name)] = true
	}
	if !taken[strings.ToLower(want)] {
		return want
	}
	for i := 2; i < 100; i++ {
		candidate := fmt.Sprintf("%s-%d", want, i)
		if !taken[strings.ToLower(candidate)] {
			return candidate
		}
	}
	return want + "-" + newID()
}

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func fakeJWT(exp int64) string {
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	pl := base64.RawURLEncoding.EncodeToString([]byte(`{"exp":` + strconv.FormatInt(exp, 10) + `}`))
	return hdr + "." + pl + ".sig"
}

// newFakeAuthn serves the three endpoints the credits path uses, keyed by the
// bearer token so each fake account gets its own promo balance and email.
func newFakeAuthn(t *testing.T, promoByToken map[string]float64) *httptest.Server {
	t.Helper()
	emailByToken := map[string]string{}
	for tok := range promoByToken {
		emailByToken[tok] = "user-of-" + tok + "@example.com"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/traycer/models", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	})
	mux.HandleFunc("/api/v3/auth/refresh", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":        fakeJWT(time.Now().Add(time.Hour).Unix()),
			"refreshToken": "r2",
		})
	})
	mux.HandleFunc("/api/v3/user", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"user": map[string]any{"email": emailByToken[bearer(r)], "providerHandle": "fake"},
			"userSubscription": map[string]any{
				"subscriptionStatus": "FREE", "hasPaymentMethod": false,
				"bundleSummary":   map[string]any{"bundleTotal": 0, "bundleConsumed": 0, "bundleRemaining": 0},
				"totalPlanCredits": 0,
				"credit":          map[string]any{"bonusCredits": 0, "consumedFromPlan": 0, "consumedFromBonus": 0},
			},
		})
	})
	mux.HandleFunc("/api/user/credit-campaigns", func(w http.ResponseWriter, r *http.Request) {
		tok := bearer(r)
		remaining := promoByToken[tok]
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"campaigns": []map[string]any{{
				"id": "c1", "name": "Gemini Weekend", "description": "2 days. $100. Any Gemini models.",
				"grantCredits": 100, "remainingCredits": remaining,
				"startsAt": "2026-11-07T16:00:00.000Z", "endsAt": "2026-11-09T08:00:00.000Z", "claim": "active",
			}},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newTestServer(t *testing.T, authnURL string) *Server {
	t.Helper()
	cfg := &Config{
		AccountsFile:    filepath.Join(t.TempDir(), "accounts.json"),
		TraycerServer:   authnURL,
		TraycerAuthn:    authnURL,
		CreditsCache:    0, // always fetch
		AutoRotate:      true,
		RotateThreshold: 5,
		RotateCheck:     time.Second,
	}
	up := NewUpstream(cfg.TraycerServer, cfg.TraycerAuthn, 5*time.Second)
	store, err := OpenStore(cfg, up)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	return &Server{
		cfg: cfg, store: store, up: up,
		credits: map[string]*creditsState{},
		logger:  log.New(io.Discard, "", 0),
	}
}

func addFakeAccount(t *testing.T, s *Server, name, token string) *Account {
	t.Helper()
	a, err := s.store.Add(name, token, "refresh-"+name, "", "")
	if err != nil {
		t.Fatalf("add %s: %v", name, err)
	}
	return a
}

func TestRotateWhenPromoBelowThreshold(t *testing.T) {
	tokA, tokB := "token-a", "token-b"
	authn := newFakeAuthn(t, map[string]float64{tokA: 4.2, tokB: 50})
	s := newTestServer(t, authn.URL)
	a := addFakeAccount(t, s, "a", tokA)
	b := addFakeAccount(t, s, "b", tokB)
	if err := s.store.SetActive(a.ID); err != nil {
		t.Fatal(err)
	}

	s.rotateIfPromoLow(context.Background())

	if got := s.store.ActiveID(); got != b.ID {
		t.Fatalf("active = %s, want %s (b should win with $50 promo vs $4.20)", got, b.ID)
	}
}

func TestNoRotateWhenPromoAboveThreshold(t *testing.T) {
	tokA, tokB := "token-a", "token-b"
	authn := newFakeAuthn(t, map[string]float64{tokA: 99, tokB: 50})
	s := newTestServer(t, authn.URL)
	a := addFakeAccount(t, s, "a", tokA)
	addFakeAccount(t, s, "b", tokB)
	if err := s.store.SetActive(a.ID); err != nil {
		t.Fatal(err)
	}

	s.rotateIfPromoLow(context.Background())

	if got := s.store.ActiveID(); got != a.ID {
		t.Fatalf("active = %s, want %s (promo $99 is above threshold, no rotation)", got, a.ID)
	}
}

func TestIdentityLearnedFromUserV3(t *testing.T) {
	tokA := "token-a"
	authn := newFakeAuthn(t, map[string]float64{tokA: 50})
	s := newTestServer(t, authn.URL)
	a := addFakeAccount(t, s, "a", tokA)
	if a.Email != "" || a.Handle != "" {
		t.Fatalf("identity should start empty, got email=%q handle=%q", a.Email, a.Handle)
	}

	c := s.creditsForAccount(context.Background(), a)

	if c.Email != "user-of-token-a@example.com" || c.Handle != "fake" {
		t.Fatalf("credits identity = %q / %q", c.Email, c.Handle)
	}
	persisted := s.store.ByID(a.ID)
	if persisted.Email != "user-of-token-a@example.com" || persisted.Handle != "fake" {
		t.Fatalf("account identity = %q / %q, want it learned from /api/v3/user and persisted", persisted.Email, persisted.Handle)
	}
}

func TestNoRotateWhenNobodyHasMorePromo(t *testing.T) {
	tokA, tokB := "token-a", "token-b"
	authn := newFakeAuthn(t, map[string]float64{tokA: 2, tokB: 1})
	s := newTestServer(t, authn.URL)
	a := addFakeAccount(t, s, "a", tokA)
	addFakeAccount(t, s, "b", tokB)
	if err := s.store.SetActive(a.ID); err != nil {
		t.Fatal(err)
	}

	s.rotateIfPromoLow(context.Background())

	if got := s.store.ActiveID(); got != a.ID {
		t.Fatalf("active = %s, want %s (b has less promo; rotating would flap)", got, a.ID)
	}
}

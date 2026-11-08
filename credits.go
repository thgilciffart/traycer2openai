package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// CreditCampaign is one promotional credit grant — traycer.ai/billing's "Credit
// offers" card. Source: GET {authn}/api/user/credit-campaigns, confirmed live
// against authn.traycer.ai with a host Bearer token (CONTRACT.md).
type CreditCampaign struct {
	ID               string  `json:"id"`
	Name             string  `json:"name"`
	Description      string  `json:"description,omitempty"`
	GrantCredits     float64 `json:"grantCredits"`
	RemainingCredits float64 `json:"remainingCredits"`
	StartsAt         string  `json:"startsAt,omitempty"`
	EndsAt           string  `json:"endsAt,omitempty"`
	Claim            string  `json:"claim,omitempty"` // "active" once claimed, "none" before
}

// Credits is the per-account billing snapshot shown in the dashboard's Credits
// block. Campaigns come from /api/user/credit-campaigns; the totals come from
// /api/v3/user (same userSubscription shape the web app reads from /api/v2/user,
// which rejects host tokens with 401 — see CONTRACT.md). totalRemaining mirrors
// traycer.ai's own billing page: plan + bundle + bonus remaining, campaigns are
// shown separately and are NOT part of that sum (that is why the Traycer billing
// page can show $0.000 next to a $99.938 promo).
type Credits struct {
	Plan             string           `json:"plan"` // subscriptionStatus, e.g. "FREE"
	Email            string           `json:"email,omitempty"`  // user.email, shown in the dashboard
	Handle           string           `json:"handle,omitempty"` // user.providerHandle, shown in the dashboard
	HasPaymentMethod bool             `json:"hasPaymentMethod"`
	PlanTotal        float64          `json:"planTotal"`
	PlanRemaining    float64          `json:"planRemaining"`
	BundleTotal      float64          `json:"bundleTotal"`
	BundleRemaining  float64          `json:"bundleRemaining"`
	BonusTotal       float64          `json:"bonusTotal"`
	BonusRemaining   float64          `json:"bonusRemaining"`
	TotalRemaining   float64          `json:"totalRemaining"`
	Campaigns        []CreditCampaign `json:"campaigns"`
	FetchedAt        string           `json:"fetchedAt,omitempty"`
	Error            string           `json:"error,omitempty"`
}

// PromoRemaining is the spendable promotional credit: the remaining balance of
// every campaign that has been claimed (claim != "none"). This is the number the
// auto-rotation watches.
func (c *Credits) PromoRemaining() float64 {
	if c == nil {
		return 0
	}
	total := 0.0
	for _, camp := range c.Campaigns {
		if camp.Claim == "none" {
			continue
		}
		total += camp.RemainingCredits
	}
	return total
}

// userV3 is the subset of GET {authn}/api/v3/user that the dashboard needs.
type userV3 struct {
	User struct {
		Email          string `json:"email"`
		ProviderHandle string `json:"providerHandle"`
	} `json:"user"`
	UserSubscription struct {
		SubscriptionStatus string `json:"subscriptionStatus"`
		HasPaymentMethod   bool   `json:"hasPaymentMethod"`
		BundleSummary      struct {
			BundleTotal     float64 `json:"bundleTotal"`
			BundleConsumed  float64 `json:"bundleConsumed"`
			BundleRemaining float64 `json:"bundleRemaining"`
		} `json:"bundleSummary"`
		TotalPlanCredits float64 `json:"totalPlanCredits"`
		Credit           struct {
			BonusCredits      float64 `json:"bonusCredits"`
			ConsumedFromPlan  float64 `json:"consumedFromPlan"`
			ConsumedFromBonus float64 `json:"consumedFromBonus"`
		} `json:"credit"`
	} `json:"userSubscription"`
}

func (u *Upstream) getJSON(ctx context.Context, a *Account, path string, out any) error {
	req, err := wireNewRequest(ctx, http.MethodGet, u.Authn+path, nil, a)
	if err != nil {
		return err
	}
	resp, err := wireDo(u, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return &wireRetryable{msg: path + ": read body: " + err.Error()}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s: %s: %s", path, resp.Status, wireTruncate(body, 300))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("%s: decode: %w", path, err)
	}
	return nil
}

// CreditCampaigns fetches the promotional credit offers (billing page's
// "Credit offers" card).
func (u *Upstream) CreditCampaigns(ctx context.Context, a *Account) ([]CreditCampaign, error) {
	var out struct {
		Campaigns []CreditCampaign `json:"campaigns"`
	}
	if err := u.getJSON(ctx, a, "/api/user/credit-campaigns", &out); err != nil {
		return nil, err
	}
	if out.Campaigns == nil {
		out.Campaigns = []CreditCampaign{}
	}
	return out.Campaigns, nil
}

// Credits combines /api/v3/user and /api/user/credit-campaigns into the
// dashboard's per-account billing snapshot. A failure of one endpoint does not
// sink the other; err carries the joined failure only if both fail.
func (u *Upstream) Credits(ctx context.Context, a *Account) (*Credits, error) {
	c := &Credits{Campaigns: []CreditCampaign{}}
	var v3 userV3
	v3Err := u.getJSON(ctx, a, "/api/v3/user", &v3)
	if v3Err == nil {
		sub := v3.UserSubscription
		c.Plan = sub.SubscriptionStatus
		c.Email = v3.User.Email
		c.Handle = v3.User.ProviderHandle
		c.HasPaymentMethod = sub.HasPaymentMethod
		c.PlanTotal = sub.TotalPlanCredits
		c.PlanRemaining = clampNonNegative(sub.TotalPlanCredits - sub.Credit.ConsumedFromPlan)
		c.BundleTotal = sub.BundleSummary.BundleTotal
		c.BundleRemaining = clampNonNegative(sub.BundleSummary.BundleRemaining)
		c.BonusTotal = sub.Credit.BonusCredits
		c.BonusRemaining = clampNonNegative(sub.Credit.BonusCredits - sub.Credit.ConsumedFromBonus)
		// Same formula as traycer.ai's billing page (pricing-utils): the
		// "TOTAL CREDITS REMAINING" card excludes campaign credits.
		c.TotalRemaining = totalRemaining(sub.TotalPlanCredits, sub.Credit.ConsumedFromPlan,
			sub.BundleSummary.BundleRemaining, sub.Credit.BonusCredits, sub.Credit.ConsumedFromBonus)
	}
	campaigns, campErr := u.CreditCampaigns(ctx, a)
	if campErr == nil {
		c.Campaigns = campaigns
	}
	switch {
	case v3Err != nil && campErr != nil:
		return c, fmt.Errorf("credits: %v; %v", v3Err, campErr)
	case v3Err != nil:
		return c, fmt.Errorf("credits: %w", v3Err)
	case campErr != nil:
		return c, fmt.Errorf("credit-campaigns: %w", campErr)
	}
	return c, nil
}

func clampNonNegative(v float64) float64 {
	if v < 0 {
		return 0
	}
	return v
}

// totalRemaining mirrors traycer.ai's billing page: plan + bundle + bonus
// remaining, with consumption clamped at zero. Campaign (promo) credits are
// deliberately excluded, which is why the billing page can show $0.000 next to
// a $99.938 promo offer.
func totalRemaining(planTotal, consumedFromPlan, bundleRemaining, bonusCredits, consumedFromBonus float64) float64 {
	return clampNonNegative(planTotal-consumedFromPlan) +
		clampNonNegative(bundleRemaining) +
		clampNonNegative(bonusCredits-consumedFromBonus)
}

// ---------------------------------------------------------------- server side

type creditsState struct {
	mu sync.Mutex
	at time.Time
	c  *Credits
}

// creditsFor returns the cached billing snapshot for an account, refreshing it
// at most once per cfg.CreditsCache. The per-account mutex both single-flights
// concurrent refreshers (dashboard poll + rotation loop) and bounds the upstream
// call rate.
func (s *Server) creditsFor(ctx context.Context, a *Account) *Credits {
	s.creditsMu.Lock()
	st, ok := s.credits[a.ID]
	if !ok {
		st = &creditsState{}
		s.credits[a.ID] = st
	}
	s.creditsMu.Unlock()

	st.mu.Lock()
	defer st.mu.Unlock()
	if st.c != nil && time.Since(st.at) < s.cfg.CreditsCache {
		return st.c
	}
	fctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	c, err := s.up.Credits(fctx, a)
	if err != nil {
		// Keep serving the last good snapshot alongside the error so the
		// dashboard can show "stale" data instead of nothing.
		if st.c != nil {
			stale := *st.c
			stale.Error = err.Error()
			st.c = &stale
			st.at = time.Now()
			return st.c
		}
		c = &Credits{Campaigns: []CreditCampaign{}, Error: err.Error()}
	}
	c.FetchedAt = time.Now().UTC().Format(time.RFC3339)
	st.c = c
	st.at = time.Now()
	// Accounts added by pasting a token have no email/handle stored; learn them
	// from /api/v3/user so the dashboard can show a distinguishable identity.
	s.store.NoteIdentity(a, c.Email, c.Handle)
	return c
}

// creditsForAccount guarantees a fresh token before reading credits. Burned
// accounts (status error) are not refreshed just to render the dashboard.
func (s *Server) creditsForAccount(ctx context.Context, a *Account) *Credits {
	if a.LastError != "" && statusFor(a) == statusError {
		return &Credits{Campaigns: []CreditCampaign{}, Error: a.LastError}
	}
	if err := s.store.EnsureToken(ctx, a); err != nil {
		return &Credits{Campaigns: []CreditCampaign{}, Error: err.Error()}
	}
	return s.creditsFor(ctx, a)
}

// rotationLoop auto-rotates the active account once its promotional credits
// drop below cfg.RotateThreshold ($ by default): the active account moves to
// whichever other healthy account has the most promo remaining. Promo only
// drains, so picking the strict maximum converges instead of flapping.
func (s *Server) rotationLoop(ctx context.Context) {
	if !s.cfg.AutoRotate {
		return
	}
	interval := s.cfg.RotateCheck
	if interval <= 0 {
		interval = 30 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.rotateIfPromoLow(ctx)
	}
}

func (s *Server) rotateIfPromoLow(ctx context.Context) {
	accs := s.store.List()
	if len(accs) < 2 {
		return
	}
	active := s.store.pick("")
	if active == nil {
		return
	}
	activeCredits := s.creditsForAccount(ctx, active)
	if activeCredits.Error != "" {
		return
	}
	promo := activeCredits.PromoRemaining()
	if promo >= s.cfg.RotateThreshold {
		return
	}
	var best *Account
	var bestCredits *Credits
	bestPromo := promo
	for _, a := range accs {
		if a.ID == active.ID || statusFor(a) != statusOK {
			continue
		}
		c := s.creditsForAccount(ctx, a)
		if c.Error != "" {
			continue
		}
		if p := c.PromoRemaining(); p > bestPromo {
			best, bestCredits, bestPromo = a, c, p
		}
	}
	if best == nil {
		return // nobody has more promo than the active account; stay put
	}
	if err := s.store.SetActive(best.ID); err != nil {
		s.logger.Printf("auto-rotate: %v", err)
		return
	}
	if err := s.store.Save(); err != nil {
		s.logger.Printf("auto-rotate: saved active=%s but persist failed: %v", best.ID, err)
	}
	s.logger.Printf("auto-rotate: %s promo $%.3f < $%.2f threshold, active -> %s (promo $%.3f, total $%.3f)",
		active.Name, promo, s.cfg.RotateThreshold, best.Name, bestPromo, bestCredits.TotalRemaining)
}

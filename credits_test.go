package main

import "testing"

func TestTotalRemainingMatchesTraycerBillingFormula(t *testing.T) {
	// HAR capture of a FREE account with only the Gemini Weekend promo active:
	// the billing page showed $0.000 total next to a $99.938 campaign.
	got := totalRemaining(0, 0, 0, 0, 0)
	if got != 0 {
		t.Fatalf("free account: got %v, want 0", got)
	}
	// PRO plan with consumption and a bonus, no bundle.
	got = totalRemaining(37.5, 12.5, 0, 10, 4)
	if got != 25+0+6 {
		t.Fatalf("got %v, want 31", got)
	}
	// Consumption clamped: never negative.
	got = totalRemaining(5, 10, 0, 2, 9)
	if got != 0 {
		t.Fatalf("clamped: got %v, want 0", got)
	}
}

func TestPromoRemainingSkipsUnclaimedCampaigns(t *testing.T) {
	c := &Credits{Campaigns: []CreditCampaign{
		{Name: "Gemini Weekend", GrantCredits: 100, RemainingCredits: 99.938, Claim: "active"},
		{Name: "Unclaimed", GrantCredits: 50, RemainingCredits: 50, Claim: "none"},
	}}
	if got := c.PromoRemaining(); got != 99.938 {
		t.Fatalf("got %v, want 99.938", got)
	}
	var nilCredits *Credits
	if got := nilCredits.PromoRemaining(); got != 0 {
		t.Fatalf("nil credits: got %v, want 0", got)
	}
}

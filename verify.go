package main

import "context"

// VerifyToken is the cheap "is this token real" probe used when an account is
// added: the model catalog is the only unauthenticated-inference endpoint that
// answers 200 for a live token and 401 for a dead one.
func (u *Upstream) VerifyToken(ctx context.Context, a *Account) error {
	_, err := u.Models(ctx, a)
	return err
}

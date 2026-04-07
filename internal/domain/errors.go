package domain

import "errors"

var (
	ErrTelegramAccessRequestNotFound     = errors.New("telegram access request not found")
	ErrTelegramAccessRequestNotPending   = errors.New("telegram access request is not pending")
	ErrTelegramAccessRequestAlreadyFinal = errors.New("telegram access request is already final")
	ErrRetentionLockBusy                 = errors.New("retention lock busy")
	ErrWebAuthRateLimited                = errors.New("web auth request rate limited")
	ErrWebAuthChallengeNotFound          = errors.New("web auth challenge not found")
	ErrWebAuthChallengeExpired           = errors.New("web auth challenge expired")
	ErrWebAuthSessionNotFound            = errors.New("web auth session not found")
)

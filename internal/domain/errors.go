package domain

import "errors"

var (
	ErrTelegramAccessRequestNotFound     = errors.New("telegram access request not found")
	ErrTelegramAccessRequestNotPending   = errors.New("telegram access request is not pending")
	ErrTelegramAccessRequestAlreadyFinal = errors.New("telegram access request is already final")
	ErrRetentionLockBusy                 = errors.New("retention lock busy")
)

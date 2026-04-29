package domain

import "errors"

var (
	ErrArtifactNotFound                     = errors.New("artifact not found")
	ErrTelegramAccessRequestNotFound        = errors.New("telegram access request not found")
	ErrTelegramAccessRequestNotPending      = errors.New("telegram access request is not pending")
	ErrTelegramAccessRequestAlreadyFinal    = errors.New("telegram access request is already final")
	ErrRetentionLockBusy                    = errors.New("retention lock busy")
	ErrWebAuthRateLimited                   = errors.New("web auth request rate limited")
	ErrWebAuthChallengeNotFound             = errors.New("web auth challenge not found")
	ErrWebAuthChallengeExpired              = errors.New("web auth challenge expired")
	ErrWebAuthSessionNotFound               = errors.New("web auth session not found")
	ErrIdentityUserNotFound                 = errors.New("identity user not found")
	ErrLinkedIdentityNotFound               = errors.New("linked identity not found")
	ErrStepUpChallengeNotFound              = errors.New("step-up challenge not found")
	ErrStepUpChallengeExpired               = errors.New("step-up challenge expired")
	ErrTrustPolicyNotFound                  = errors.New("trust policy not found")
	ErrRecentStepUpRequired                 = errors.New("recent step-up required")
	ErrLinkedIdentityRequired               = errors.New("linked identity required")
	ErrApprovalChannelNotAllowed            = errors.New("approval channel not allowed")
	ErrWhatsAppContactPolicyNotFound        = errors.New("whatsapp contact policy not found")
	ErrWhatsAppPolicyOptedOut               = errors.New("whatsapp_policy_opted_out")
	ErrWhatsAppPolicyWindowClosedNoTemplate = errors.New("whatsapp_policy_window_closed_no_template")
)

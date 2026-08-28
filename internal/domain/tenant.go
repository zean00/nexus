package domain

import (
	"errors"
	"strings"
	"time"
)

var ErrTenantNotFound = errors.New("tenant not found")

// TenantRecord describes one laju instance served by this nexus. The
// env-configured default tenant (single-tenant deployments) is represented by
// the same shape without a registry row.
type TenantRecord struct {
	TenantID          string
	DisplayName       string
	LajuBaseURL       string
	LajuBearerToken   string
	InboundWebhookURL string
	AdminTokenHash    string
	WebChatAccountKey string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// InboundWebhookEndpoint returns the URL nexus delivers inbound-forward
// events to for this tenant.
func (r TenantRecord) InboundWebhookEndpoint() string {
	if r.InboundWebhookURL != "" {
		return r.InboundWebhookURL
	}
	if r.LajuBaseURL == "" {
		return ""
	}
	return strings.TrimRight(r.LajuBaseURL, "/") + "/api/integrations/nexus/inbound"
}

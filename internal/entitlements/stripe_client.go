package entitlements

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const stripeAPIBaseURL = "https://api.stripe.com/v1"

type CheckoutRequest struct {
	WorkspaceID, Plan, PriceID, CustomerID string
	SuccessURL, CancelURL, IdempotencyKey  string
}

type PortalRequest struct {
	CustomerID, ReturnURL, IdempotencyKey string
}

type BillingSession struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

type SessionProvider interface {
	CreateCheckout(context.Context, CheckoutRequest) (BillingSession, error)
	CreatePortal(context.Context, PortalRequest) (BillingSession, error)
}

type StripeClient struct {
	SecretKey string
	BaseURL   string
	Client    *http.Client
}

func (c *StripeClient) CreateCheckout(ctx context.Context, req CheckoutRequest) (BillingSession, error) {
	if strings.TrimSpace(req.WorkspaceID) == "" || strings.TrimSpace(req.PriceID) == "" {
		return BillingSession{}, fmt.Errorf("workspace and Stripe price are required")
	}
	form := url.Values{
		"mode":                                      {"subscription"},
		"line_items[0][price]":                      {req.PriceID},
		"line_items[0][quantity]":                   {"1"},
		"client_reference_id":                       {req.WorkspaceID},
		"metadata[workspace_id]":                    {req.WorkspaceID},
		"metadata[plan]":                            {req.Plan},
		"subscription_data[metadata][workspace_id]": {req.WorkspaceID},
		"subscription_data[metadata][plan]":         {req.Plan},
		"success_url":                               {req.SuccessURL},
		"cancel_url":                                {req.CancelURL},
	}
	if strings.TrimSpace(req.CustomerID) != "" {
		form.Set("customer", req.CustomerID)
	}
	return c.post(ctx, "/checkout/sessions", form, req.IdempotencyKey)
}

func (c *StripeClient) CreatePortal(ctx context.Context, req PortalRequest) (BillingSession, error) {
	if strings.TrimSpace(req.CustomerID) == "" {
		return BillingSession{}, fmt.Errorf("Stripe customer is required")
	}
	return c.post(ctx, "/billing_portal/sessions", url.Values{
		"customer":   {req.CustomerID},
		"return_url": {req.ReturnURL},
	}, req.IdempotencyKey)
}

func (c *StripeClient) post(ctx context.Context, path string, form url.Values, idempotencyKey string) (BillingSession, error) {
	if strings.TrimSpace(c.SecretKey) == "" {
		return BillingSession{}, fmt.Errorf("Stripe secret key is not configured")
	}
	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if base == "" {
		base = stripeAPIBaseURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, strings.NewReader(form.Encode()))
	if err != nil {
		return BillingSession{}, err
	}
	req.SetBasicAuth(c.SecretKey, "")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return BillingSession{}, fmt.Errorf("Stripe request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return BillingSession{}, fmt.Errorf("Stripe response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var envelope struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(body, &envelope)
		message := strings.TrimSpace(envelope.Error.Message)
		if message == "" {
			message = http.StatusText(resp.StatusCode)
		}
		return BillingSession{}, fmt.Errorf("Stripe returned %d: %s", resp.StatusCode, message)
	}
	var session BillingSession
	if err := json.Unmarshal(body, &session); err != nil {
		return BillingSession{}, fmt.Errorf("decode Stripe session: %w", err)
	}
	if strings.TrimSpace(session.URL) == "" {
		return BillingSession{}, fmt.Errorf("Stripe session response did not include a URL")
	}
	return session, nil
}

// control/vast/client.go
package vast

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"
)

const (
	defaultBaseURL   = "https://console.vast.ai/api/v0"
	defaultHTTPTimeout = 10 * time.Second
)

// Client is a minimal Vast API client for read-only marketplace queries.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// NewFromEnv builds a Vast client using environment variables:
//   - VAST_API_KEY (required)
//   - VAST_API_URL (optional, defaults to https://console.vast.ai/api/v0)
func NewFromEnv() (*Client, error) {
	apiKey := os.Getenv("VAST_API_KEY")
	if apiKey == "" {
		return nil, errors.New("VAST_API_KEY not set")
	}

	baseURL := os.Getenv("VAST_API_URL")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}

	return &Client{
		baseURL: baseURL,
		apiKey:  apiKey,
		httpClient: &http.Client{
			Timeout: defaultHTTPTimeout,
		},
	}, nil
}

// searchOffersRequest is a trimmed request body for /bundles/ (search offers).
// Docs: POST https://console.vast.ai/api/v0/bundles/ :contentReference[oaicite:1]{index=1}
type searchOffersRequest struct {
	Limit    int                    `json:"limit"`
	Type     string                 `json:"type"` // "on-demand", "reserved", "bid"
	Verified map[string]bool        `json:"verified,omitempty"`
	Rentable map[string]bool        `json:"rentable,omitempty"`
	Rented   map[string]bool        `json:"rented,omitempty"`
}

// Offer is a trimmed subset of Vast's offer fields with things we actually care about.
type Offer struct {
	ID           int64   `json:"id"`
	MachineID    int64   `json:"machine_id"`
	GPUName      string  `json:"gpu_name"`
	NumGPUs      int     `json:"num_gpus"`
	GPURAM       int64   `json:"gpu_ram"`       // MB
	GPUTotalRAM  int64   `json:"gpu_total_ram"` // MB
	DPHTotal     float64 `json:"dph_total"`     // $/hour
	Geolocation  string  `json:"geolocation"`
	Reliability  float64 `json:"reliability"`
	Verification string  `json:"verification"`
	ResourceType string  `json:"resource_type"`
}

// searchOffersResponse matches Vast's response shape: { "offers": [ ... ] }
type searchOffersResponse struct {
	Offers []Offer `json:"offers"`
}

// SearchOffers hits the Vast search offers endpoint (POST /bundles/)
// and returns a trimmed slice of Offer.
//
// This is read-only and safe; it does not start, stop, or bid on anything.
func (c *Client) SearchOffers(ctx context.Context, limit int) ([]Offer, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	reqBody := searchOffersRequest{
		Limit: limit,
		Type:  "on-demand",
		Verified: map[string]bool{
			"eq": true,
		},
		Rentable: map[string]bool{
			"eq": true,
		},
		Rented: map[string]bool{
			"eq": false,
		},
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal search offers request: %w", err)
	}

	url := fmt.Sprintf("%s/bundles/", c.baseURL)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Best-effort body read for debugging.
		var errBody map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&errBody)
		return nil, fmt.Errorf("vast search offers: status %d, body=%v", resp.StatusCode, errBody)
	}

	var out searchOffersResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return out.Offers, nil
}

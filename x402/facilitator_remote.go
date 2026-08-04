package x402

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// RemoteFacilitator implements Facilitator by calling a facilitator HTTP
// service (cmd/facilitator). A gateway configured with one needs neither an RPC
// connection nor a private key: verification and settlement happen remotely.
type RemoteFacilitator struct {
	BaseURL string
	HTTP    *http.Client
}

func (f *RemoteFacilitator) client() *http.Client {
	if f.HTTP != nil {
		return f.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// Verify posts to /verify. A payment judged invalid comes back as a 200 with
// IsValid=false; only transport or server (5xx) failures return an error.
func (f *RemoteFacilitator) Verify(ctx context.Context, p PaymentPayload, reqs PaymentRequirements) (*VerifyResult, error) {
	var out VerifyResult
	if err := f.post(ctx, "/verify", p, reqs, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Settle posts to /settle. A reverted settlement comes back as a 200 with
// Success=false; only transport or server (5xx) failures return an error.
func (f *RemoteFacilitator) Settle(ctx context.Context, p PaymentPayload, reqs PaymentRequirements) (*SettlementResponse, error) {
	var out SettlementResponse
	if err := f.post(ctx, "/settle", p, reqs, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (f *RemoteFacilitator) post(ctx context.Context, path string, p PaymentPayload, reqs PaymentRequirements, out interface{}) error {
	body, err := json.Marshal(FacilitatorRequest{
		X402Version:         Version,
		PaymentPayload:      p,
		PaymentRequirements: reqs,
	})
	if err != nil {
		return fmt.Errorf("facilitator %s: encode request: %w", path, err)
	}

	url := strings.TrimRight(f.BaseURL, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("facilitator %s: build request: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := f.client().Do(req)
	if err != nil {
		return fmt.Errorf("facilitator %s: request: %w", path, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("facilitator %s: read response: %w", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("facilitator %s: status %d: %s", path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("facilitator %s: decode response: %w", path, err)
	}
	return nil
}

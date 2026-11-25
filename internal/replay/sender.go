package replay

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kauavnogueira/hookbox/internal/model"
)

// ReplayOptions defines optional modifications to apply when replaying a webhook.
type ReplayOptions struct {
	// Re-signing options
	StripeSecret string // If set, re-signs payload with Stripe-Signature format (current timestamp + HMAC-SHA256)
	GitHubSecret string // If set, re-signs payload with X-Hub-Signature-256
	HMACSecret   string // If set, signs payload with generic HMAC-SHA256
	HMACHeader   string // Header name to use for generic HMAC (default: "X-Signature-256")

	// Routing options
	PreservePath  bool // Appends original webhook path to target URL
	PreserveQuery bool // Appends original webhook query string to target URL

	// Customization options
	CustomBody    string            // If set, replaces the webhook body
	CustomHeaders map[string]string // Custom headers to inject or override
}

// Result holds the details and outcome of a replayed webhook.
type Result struct {
	WebhookID    string
	TargetURL    string
	Method       string
	StatusCode   int
	StatusText   string
	Duration     time.Duration
	ResponseBody string
	ResponseHead http.Header
}

// Sender executes replay requests to target endpoints.
type Sender struct {
	client *http.Client
}

// NewSender creates a new replay sender with default timeout.
func NewSender(timeout time.Duration) *Sender {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Sender{
		client: &http.Client{
			Timeout: timeout,
		},
	}
}

// ResolveTargetURL calculates the final destination URL considering routing options.
func ResolveTargetURL(targetURL string, wh *model.Webhook, opts ReplayOptions) (string, error) {
	if targetURL == "" {
		return "", fmt.Errorf("target URL cannot be empty")
	}

	u, err := url.Parse(targetURL)
	if err != nil {
		return "", fmt.Errorf("invalid target URL %q: %w", targetURL, err)
	}

	if opts.PreservePath && wh.Path != "" {
		if u.Path == "" || u.Path == "/" {
			u.Path = wh.Path
		} else {
			u.Path = strings.TrimSuffix(u.Path, "/") + "/" + strings.TrimPrefix(wh.Path, "/")
		}
	}

	if opts.PreserveQuery && wh.Query != "" {
		if u.RawQuery == "" {
			u.RawQuery = wh.Query
		} else {
			u.RawQuery = u.RawQuery + "&" + wh.Query
		}
	}

	return u.String(), nil
}

// Send dispatches an exact replica of the webhook to the specified target URL.
func (s *Sender) Send(wh *model.Webhook, targetURL string) (*Result, error) {
	return s.SendWithOptions(wh, targetURL, ReplayOptions{})
}

// SendWithOptions dispatches a webhook applying custom replay options (re-signing, path/query preservation, custom body/headers).
func (s *Sender) SendWithOptions(wh *model.Webhook, targetURL string, opts ReplayOptions) (*Result, error) {
	finalURL, err := ResolveTargetURL(targetURL, wh, opts)
	if err != nil {
		return nil, err
	}

	bodyStr := wh.Body
	if opts.CustomBody != "" {
		bodyStr = opts.CustomBody
	}

	var bodyReader io.Reader
	if len(bodyStr) > 0 {
		bodyReader = strings.NewReader(bodyStr)
	}

	req, err := http.NewRequest(wh.Method, finalURL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Copy original headers
	for key, values := range wh.Headers {
		// Skip hop-by-hop headers and Host header as Go manages Host separately
		if strings.EqualFold(key, "Host") {
			continue
		}
		for _, v := range values {
			req.Header.Add(key, v)
		}
	}

	// Apply custom header overrides
	for k, v := range opts.CustomHeaders {
		req.Header.Set(k, v)
	}

	// Apply re-signing if configured (uses the effective bodyStr)
	if opts.StripeSecret != "" {
		sig, _ := SignStripe(bodyStr, opts.StripeSecret, 0)
		req.Header.Set("Stripe-Signature", sig)
	}

	if opts.GitHubSecret != "" {
		sig := SignGitHub(bodyStr, opts.GitHubSecret)
		req.Header.Set("X-Hub-Signature-256", sig)
	}

	if opts.HMACSecret != "" {
		headerName := opts.HMACHeader
		if headerName == "" {
			headerName = "X-Signature-256"
		}
		sig := SignHMACSHA256(bodyStr, opts.HMACSecret)
		req.Header.Set(headerName, sig)
	}

	// Set Hookbox tracking headers
	req.Header.Set("X-Hookbox-Replay", "true")
	req.Header.Set("X-Hookbox-Original-ID", wh.ID)

	startTime := time.Now()
	resp, err := s.client.Do(req)
	duration := time.Since(startTime)
	if err != nil {
		return nil, fmt.Errorf("error sending replay request to %s: %w", finalURL, err)
	}
	defer resp.Body.Close()

	respBodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("error reading response body: %w", err)
	}

	return &Result{
		WebhookID:    wh.ID,
		TargetURL:    finalURL,
		Method:       wh.Method,
		StatusCode:   resp.StatusCode,
		StatusText:   resp.Status,
		Duration:     duration,
		ResponseBody: string(respBodyBytes),
		ResponseHead: resp.Header,
	}, nil
}

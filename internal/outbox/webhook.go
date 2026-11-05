package outbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"example.com/oracle-bmi/workflow-engine/internal/domain"
)

// Webhook creates an HTTP event consumer. Redirects are refused so one attempt
// has one destination. Receivers must honor the stable Idempotency-Key header.
func Webhook(endpoint string) (Handler, error) {
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return nil, fmt.Errorf("EVENT_WEBHOOK_URL must be an absolute HTTP(S) URL without credentials")
	}
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return func(ctx context.Context, ev domain.Event) error {
		body, err := json.Marshal(ev)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", ev.QuoteID+":"+strconv.FormatInt(ev.Version, 10))
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("send event webhook: %w", err)
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("event webhook returned HTTP %d", resp.StatusCode)
		}
		return nil
	}, nil
}

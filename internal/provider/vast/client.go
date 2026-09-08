package vast

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultBaseURL is Vast's REST v0 root.
const DefaultBaseURL = "https://console.vast.ai/api/v0"

// maxRetries is how many times a 429 is retried before it is reported
// (the Vast CLI's precedent is three tries).
const maxRetries = 3

// client is the thin HTTP layer under the driver: Bearer auth, JSON in and
// out, and a short exponential backoff on 429, which Vast rate-limits per
// endpoint.
type client struct {
	base      string
	key       string
	http      *http.Client
	retryWait time.Duration // first 429 backoff; doubles per retry
}

// apiError is a non-2xx answer. Its text names the call and keeps Vast's
// own message so a CLI user can tell "not found" from "forbidden".
type apiError struct {
	Method string
	Path   string
	Status int
	Code   string // Vast's {"error": ...} when the body was JSON
	Msg    string // Vast's {"msg": ...} or the trimmed body text
}

func (e *apiError) Error() string {
	s := fmt.Sprintf("vast: %s %s: HTTP %d %s", e.Method, e.Path, e.Status, http.StatusText(e.Status))
	switch {
	case e.Msg != "" && e.Code != "":
		s += ": " + e.Code + ": " + e.Msg
	case e.Msg != "":
		s += ": " + e.Msg
	case e.Code != "":
		s += ": " + e.Code
	}
	return s
}

// text is the error's own words — code and message — for pattern matching
// independent of the HTTP framing.
func (e *apiError) text() string { return e.Code + " " + e.Msg }

func newAPIError(method, path string, status int, body []byte) *apiError {
	e := &apiError{Method: method, Path: path, Status: status}
	var v struct {
		Error  string `json:"error"`
		Msg    string `json:"msg"`
		Detail string `json:"detail"`
	}
	if json.Unmarshal(body, &v) == nil && (v.Error != "" || v.Msg != "" || v.Detail != "") {
		e.Code = v.Error
		e.Msg = v.Msg
		if e.Msg == "" {
			e.Msg = v.Detail
		}
		return e
	}
	s := strings.TrimSpace(string(body))
	if strings.HasPrefix(s, "<") { // an HTML error page says nothing useful
		return e
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	e.Msg = s
	return e
}

// do performs one API call. body (when non-nil) is sent as JSON; the
// response is decoded into out (when non-nil). A 429 is retried
// maxRetries times with retryWait, 2×retryWait, 4×retryWait between tries.
func (c *client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return fmt.Errorf("vast: %s %s: encoding request: %w", method, path, err)
		}
	}
	u := c.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(payload))
		if err != nil {
			return fmt.Errorf("vast: %s %s: %w", method, path, err)
		}
		req.Header.Set("Authorization", "Bearer "+c.key)
		req.Header.Set("Accept", "application/json")
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return fmt.Errorf("vast: %s %s: %w", method, path, err)
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return fmt.Errorf("vast: %s %s: reading response: %w", method, path, err)
		}
		if resp.StatusCode == http.StatusTooManyRequests && attempt < maxRetries {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(c.retryWait << attempt):
			}
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			e := newAPIError(method, path, resp.StatusCode, data)
			if resp.StatusCode == http.StatusTooManyRequests {
				e.Msg = fmt.Sprintf("rate limited after %d attempts", attempt+1)
			}
			return e
		}
		if out == nil || len(bytes.TrimSpace(data)) == 0 {
			return nil
		}
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("vast: %s %s: decoding response: %w", method, path, err)
		}
		return nil
	}
}

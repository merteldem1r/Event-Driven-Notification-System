// Package loadtest sends many notifications through the API, waits until they are delivered,
// and measures how fast each channel went, both as recorded by the workers and as the fake
// provider received them, to check the shared rate limit (IMP-1, IMP-9, D17).
package loadtest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/merteldem1r/Event-Driven-Notification-System/internal/domain"
	"github.com/merteldem1r/Event-Driven-Notification-System/internal/fakeprovider"
)

// maxBatch is the API's batch size limit.
const maxBatch = 1000

// pollInterval is how often the batch status is checked while waiting.
const pollInterval = 500 * time.Millisecond

// Config controls one load test run.
type Config struct {
	APIURL     string        // e.g. http://localhost:8080
	FakeURL    string        // the fake provider the workers send to, e.g. http://localhost:9090
	PerChannel int           // notifications per channel
	Timeout    time.Duration // how long to wait for all of them to finish
}

// Result is what happened on one channel.
type Result struct {
	Channel    domain.Channel
	Sent       int
	Failed     int
	Busiest    int     // most sends recorded by the workers in any one-second window
	AtProvider int     // most arrivals at the provider in any one-second window: what the limit is about
	Rate       float64 // average sends per second from the first to the last
	PerSecond  []int   // sends in each second, counted from the channel's first send
}

// Run creates cfg.PerChannel notifications on every channel at once, waits until none is
// pending or processing, and measures each channel's sends.
func Run(ctx context.Context, client *http.Client, cfg Config) ([]Result, error) {
	// Start the provider's measurements from zero, so only this run counts.
	if err := call(ctx, client, http.MethodDelete, cfg.FakeURL+fakeprovider.StatsPath, nil, http.StatusNoContent, nil); err != nil {
		return nil, fmt.Errorf("reset the fake provider's stats (is it running at %s?): %w", cfg.FakeURL, err)
	}

	var batches []string
	for _, ch := range domain.Channels() {
		for start := 0; start < cfg.PerChannel; start += maxBatch {
			id, err := createBatch(ctx, client, cfg.APIURL, ch, start, min(maxBatch, cfg.PerChannel-start))
			if err != nil {
				return nil, err
			}
			batches = append(batches, id)
		}
	}

	waitCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	for _, id := range batches {
		if err := waitForBatch(waitCtx, client, cfg.APIURL, id); err != nil {
			return nil, err
		}
	}

	sentAt := map[domain.Channel][]time.Time{}
	failed := map[domain.Channel]int{}
	for _, id := range batches {
		if err := collect(ctx, client, cfg.APIURL, id, sentAt, failed); err != nil {
			return nil, err
		}
	}

	var stats fakeprovider.Stats
	if err := call(ctx, client, http.MethodGet, cfg.FakeURL+fakeprovider.StatsPath, nil, http.StatusOK, &stats); err != nil {
		return nil, fmt.Errorf("read the fake provider's stats: %w", err)
	}

	var results []Result
	for _, ch := range domain.Channels() {
		r := Measure(sentAt[ch])
		r.Channel, r.Failed, r.AtProvider = ch, failed[ch], stats.Busiest[string(ch)]
		results = append(results, r)
	}
	return results, nil
}

// Measure computes a channel's numbers from the times its notifications were sent.
func Measure(times []time.Time) Result {
	if len(times) == 0 {
		return Result{}
	}
	ts := slices.Clone(times)
	slices.SortFunc(ts, func(a, b time.Time) int { return a.Compare(b) })

	r := Result{Sent: len(ts)}
	// Busiest window: for each send, how many sends fall in the second that starts with it.
	end := 0
	for start := range ts {
		for end < len(ts) && ts[end].Sub(ts[start]) < time.Second {
			end++
		}
		r.Busiest = max(r.Busiest, end-start)
	}
	if span := ts[len(ts)-1].Sub(ts[0]).Seconds(); span > 0 {
		r.Rate = float64(len(ts)-1) / span
	}
	r.PerSecond = make([]int, int(ts[len(ts)-1].Sub(ts[0])/time.Second)+1)
	for _, t := range ts {
		r.PerSecond[int(t.Sub(ts[0])/time.Second)]++
	}
	return r
}

// createBatch creates count notifications of channel ch; i numbers them for unique content.
func createBatch(ctx context.Context, client *http.Client, api string, ch domain.Channel, first, count int) (string, error) {
	type item struct {
		Channel   domain.Channel `json:"channel"`
		Recipient string         `json:"recipient"`
		Content   string         `json:"content"`
	}
	items := make([]item, count)
	for i := range items {
		n := first + i
		items[i] = item{Channel: ch, Recipient: recipient(ch, n), Content: fmt.Sprintf("load test %s %d", ch, n)}
	}
	body, err := json.Marshal(map[string]any{"notifications": items})
	if err != nil {
		return "", fmt.Errorf("encode batch: %w", err)
	}

	var resp struct {
		BatchID string `json:"batch_id"`
	}
	if err := call(ctx, client, http.MethodPost, api+"/api/v1/notifications/batch", body, http.StatusAccepted, &resp); err != nil {
		return "", fmt.Errorf("create %s batch: %w", ch, err)
	}
	return resp.BatchID, nil
}

// recipient returns a valid recipient for channel ch.
func recipient(ch domain.Channel, n int) string {
	switch ch {
	case domain.ChannelEmail:
		return fmt.Sprintf("load%d@example.com", n)
	case domain.ChannelPush:
		return fmt.Sprintf("load-device-token-%d", n)
	default:
		return "+905551234567"
	}
}

// waitForBatch polls the batch status until nothing in it is pending or processing.
func waitForBatch(ctx context.Context, client *http.Client, api, id string) error {
	for {
		var status struct {
			Counts struct {
				Pending    int `json:"pending"`
				Processing int `json:"processing"`
			} `json:"counts"`
		}
		if err := call(ctx, client, http.MethodGet, api+"/api/v1/batches/"+id, nil, http.StatusOK, &status); err != nil {
			return fmt.Errorf("batch %s status: %w", id, err)
		}
		if status.Counts.Pending+status.Counts.Processing == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("batch %s: %d pending, %d processing when the wait ended: %w",
				id, status.Counts.Pending, status.Counts.Processing, ctx.Err())
		case <-time.After(pollInterval):
		}
	}
}

// collect reads every notification of the batch: the send time of the sent ones (updated_at,
// when the worker recorded the result) and a count of the failed ones.
func collect(ctx context.Context, client *http.Client, api, id string,
	sentAt map[domain.Channel][]time.Time, failed map[domain.Channel]int,
) error {
	cursor := ""
	for {
		q := url.Values{"batch_id": {id}, "limit": {"100"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var page struct {
			Items []struct {
				Channel   domain.Channel `json:"channel"`
				Status    string         `json:"status"`
				UpdatedAt time.Time      `json:"updated_at"`
			} `json:"items"`
			NextCursor string `json:"next_cursor"`
		}
		if err := call(ctx, client, http.MethodGet, api+"/api/v1/notifications?"+q.Encode(), nil, http.StatusOK, &page); err != nil {
			return fmt.Errorf("list batch %s: %w", id, err)
		}
		for _, n := range page.Items {
			switch n.Status {
			case string(domain.StatusSent):
				sentAt[n.Channel] = append(sentAt[n.Channel], n.UpdatedAt)
			case string(domain.StatusFailed):
				failed[n.Channel]++
			}
		}
		if page.NextCursor == "" {
			return nil
		}
		cursor = page.NextCursor
	}
}

// call sends one API request and decodes the JSON answer into out.
func call(ctx context.Context, client *http.Client, method, target string, body []byte, want int, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req) //nolint:gosec // the URL is the API address the operator passed in
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }() // nothing useful to do if closing fails
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != want {
		return fmt.Errorf("status %d, want %d: %s", resp.StatusCode, want, raw)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

package threadsapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// MediaLikeCount returns the current `likes` insight value for a published
// Threads media id.
//
// The insights endpoint returns a `data` array — one entry per requested
// metric — each entry carries a `values` array of `{value, end_time}`
// records. For `likes` the value is a lifetime count, so we read the first
// value of the first matching metric.
func MediaLikeCount(mediaID, accessToken string) (int, error) {
	if mediaID == "" {
		return 0, fmt.Errorf("threads: empty media id")
	}
	endpoint := fmt.Sprintf("%s%s/insights", GraphURL, mediaID)
	params := url.Values{}
	params.Set("metric", "likes")
	params.Set("access_token", accessToken)

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, err
	}
	req.URL.RawQuery = params.Encode()
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return 0, ErrRateLimited
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("threads insights status %d body: %s", resp.StatusCode, string(body))
	}

	var res struct {
		Data []struct {
			Name   string `json:"name"`
			Values []struct {
				Value int `json:"value"`
			} `json:"values"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return 0, fmt.Errorf("threads decode insights: %w", err)
	}

	for _, m := range res.Data {
		if m.Name == "likes" && len(m.Values) > 0 {
			return m.Values[0].Value, nil
		}
	}
	// No `likes` metric in response — newly-published posts may not yet have
	// insights populated; surface as zero rather than an error so the
	// scheduler can keep retrying on its normal cadence.
	return 0, nil
}

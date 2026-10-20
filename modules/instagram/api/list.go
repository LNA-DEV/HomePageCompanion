package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/lib/redact"
)

// Media is one of the account's own posts.
type Media struct {
	ID        string    `json:"id"`
	MediaURL  string    `json:"media_url"`
	Permalink string    `json:"permalink"`
	Caption   string    `json:"caption"`
	Timestamp time.Time `json:"-"`
	RawTime   string    `json:"timestamp"`
}

// ListMedia returns every post of the account, newest first.
func ListMedia(accountID, accessToken string) ([]Media, error) {
	var all []Media
	next := fmt.Sprintf("%s%s/media?fields=id,media_url,permalink,caption,timestamp&access_token=%s",
		GraphURL, accountID, url.QueryEscape(accessToken))
	for next != "" {
		resp, err := http.Get(next)
		if err != nil {
			return all, redact.URLError(err)
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			resp.Body.Close()
			return all, ErrRateLimited
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return all, &StatusError{Op: "list media", StatusCode: resp.StatusCode}
		}
		var page struct {
			Data   []Media `json:"data"`
			Paging struct {
				Next string `json:"next"`
			} `json:"paging"`
		}
		err = json.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		if err != nil {
			return all, err
		}
		for _, m := range page.Data {
			m.Timestamp, _ = time.Parse("2006-01-02T15:04:05-0700", m.RawTime)
			all = append(all, m)
		}
		next = page.Paging.Next
	}
	return all, nil
}

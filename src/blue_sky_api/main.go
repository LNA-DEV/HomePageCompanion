package blueskyapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

var ErrRateLimited = errors.New("rate limited")

func BlueskyLogin(username, password string) (*BlueskySession, error) {
	payload := map[string]string{
		"identifier": username,
		"password":   password,
	}
	data, _ := json.Marshal(payload)
	resp, err := http.Post("https://bsky.social/xrpc/com.atproto.server.createSession", "application/json", bytes.NewBuffer(data))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, ErrRateLimited
	}

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("failed to authenticate, status code: %d", resp.StatusCode)
	}

	var session BlueskySession
	if err := json.NewDecoder(resp.Body).Decode(&session); err != nil {
		return nil, err
	}
	return &session, nil
}

type BlueskySession struct {
	AccessJwt string `json:"accessJwt"`
	Did       string `json:"did"`
	Handle    string `json:"handle"`
}
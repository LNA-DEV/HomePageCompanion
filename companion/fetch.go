package companion

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
)

// MaxFetchBytes caps Fetch, so one huge file cannot fill the memory.
const MaxFetchBytes = 64 << 20

// LocalFetcher resolves URLs the core itself serves (temp files, signed blob
// URLs) without a network round trip. The core sets it.
type LocalFetcher func(ctx context.Context, url string) ([]byte, string, bool, error)

var (
	localMu    sync.RWMutex
	localFetch LocalFetcher
)

// SetLocalFetcher is used by the core.
func SetLocalFetcher(f LocalFetcher) {
	localMu.Lock()
	localFetch = f
	localMu.Unlock()
}

// Fetch downloads a URL handed over by another building block: a temp or
// blob URL of the core (resolved in-process) or any http(s) URL. Only status
// 200 counts.
func Fetch(ctx context.Context, client *http.Client, url string) ([]byte, string, error) {
	if url == "" {
		return nil, "", errors.New("fetch: empty URL")
	}
	localMu.RLock()
	f := localFetch
	localMu.RUnlock()
	if f != nil {
		if b, ct, ok, err := f(ctx, url); ok {
			return b, ct, err
		}
	}
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("fetch %s: status %d", url, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, MaxFetchBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(b) > MaxFetchBytes {
		return nil, "", fmt.Errorf("fetch %s: larger than %d bytes", url, MaxFetchBytes)
	}
	return b, resp.Header.Get("Content-Type"), nil
}

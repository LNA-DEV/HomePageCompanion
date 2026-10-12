// Package redact keeps credentials that travel in a URL's query string out of
// error messages. Meta's Graph APIs (Instagram, Threads) take access_token
// there, and net/http puts the whole request URL into every transport error
// (docs/concepts/meta-token-refresh.md §3.1).
package redact

import (
	"errors"
	"net/url"
	"strings"
)

// URLError strips the query from the URL inside a *url.Error. net/http wraps
// every failed request in one, and url.Parse every unparseable URL; Go only
// redacts a password in the userinfo part, never the query. Other errors,
// and nil, pass through unchanged.
func URLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		if i := strings.IndexByte(ue.URL, '?'); i >= 0 {
			ue.URL = ue.URL[:i]
		}
	}
	return err
}

package wire

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Headers of the service protocol.
const (
	// SignatureHeader carries the core's signature on every request to a
	// service: "t=<unix seconds>,v1=<hex HMAC-SHA256>".
	SignatureHeader = "Companion-Signature"
	// InstanceHeader names the instance a request is for (§5.4).
	InstanceHeader = "Companion-Instance"
	// TargetHeader names the connected instance a service calls through
	// one of its slots, when several are connected.
	TargetHeader = "Companion-Target"
)

// MaxSkew is how far a signature's time may be off.
const MaxSkew = 5 * time.Minute

// signed is what the signature covers: time, method, path with query, the
// instance header and the body.
func signed(r *http.Request, body []byte, t int64) []byte {
	sum := sha256.Sum256(body)
	return fmt.Appendf(nil, "%d\n%s\n%s\n%s\n%s", t, r.Method, r.URL.RequestURI(), r.Header.Get(InstanceHeader), hex.EncodeToString(sum[:]))
}

func mac(key, msg []byte) string {
	m := hmac.New(sha256.New, key)
	m.Write(msg)
	return hex.EncodeToString(m.Sum(nil))
}

// Sign signs a request with the service's key. Set the instance header
// before.
func Sign(r *http.Request, body, key []byte, now time.Time) {
	t := now.Unix()
	r.Header.Set(SignatureHeader, fmt.Sprintf("t=%d,v1=%s", t, mac(key, signed(r, body, t))))
}

// ErrBadSignature is returned by Verify.
var ErrBadSignature = errors.New("missing or invalid " + SignatureHeader)

// Verify checks a request's signature: made with key, at most MaxSkew away
// from now.
func Verify(r *http.Request, body, key []byte, now time.Time) error {
	var t int64 = -1
	var sig string
	for _, part := range strings.Split(r.Header.Get(SignatureHeader), ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
		switch k {
		case "t":
			t, _ = strconv.ParseInt(v, 10, 64)
		case "v1":
			sig = v
		}
	}
	if t < 0 || sig == "" {
		return ErrBadSignature
	}
	if d := now.Sub(time.Unix(t, 0)); d > MaxSkew || d < -MaxSkew {
		return fmt.Errorf("%w: time off by %s", ErrBadSignature, d.Round(time.Second))
	}
	if !hmac.Equal([]byte(sig), []byte(mac(key, signed(r, body, t)))) {
		return ErrBadSignature
	}
	return nil
}

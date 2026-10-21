// Package wire carries capability calls over HTTP, between the core and the
// services (docs/concepts/modular-architecture.md §5.4). The adapters hpc gen
// writes into each capability package (wire_gen.go) use it: a client that
// turns a Go call into a request, and a server that turns a request into a
// call of an implementation.
//
// An operation is POST <base>/<capability>/v<major>/<operation>. Its JSON body
// is the operation's only struct argument itself, or else an object of its
// arguments by name. An argument of type []byte travels as the raw body
// instead, the other arguments in the "args" query parameter; a []byte
// result comes back as the raw body. Errors come back as
// {"error": {"code", "message", "cause", "status"}}.
package wire

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"

	"github.com/LNA-DEV/HomePageCompanion/companion"
)

// Request is one call's input: Args as JSON, or Raw as the body with Args
// beside it.
type Request struct {
	Args any
	Raw  []byte
}

// Caller sends one operation to a provider. out is a pointer the JSON result
// is decoded into, a *[]byte for a raw result, or nil.
type Caller interface {
	Call(ctx context.Context, op string, req Request, out any) error
}

// Raw is a result an operation returns as the raw body.
type Raw []byte

// Error is a failed operation as it travels. Code names the error (a
// capability's sentinel, such as "rate_limited", or a generic one); Cause
// is the provider's own classification (account, content, transient);
// Status is the HTTP status the platform behind the provider answered.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Cause   string `json:"cause,omitempty"`
	Status  int    `json:"status,omitempty"`

	sentinel error
}

func (e *Error) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return e.Message
}

// Unwrap returns the capability's sentinel error for Code, so errors.Is
// works across the wire.
func (e *Error) Unwrap() error { return e.sentinel }

// HTTPStatus is the platform's status, for error classification.
func (e *Error) HTTPStatus() int { return e.Status }

// FailureCause is the provider's classification of the failure.
func (e *Error) FailureCause() string { return e.Cause }

// Generic error codes.
const (
	CodeBadRequest  = "bad_request" // the body did not decode
	CodeUnsupported = "unsupported" // the operation is not offered
	CodeUnavailable = "unavailable" // the provider did not answer properly
	CodeInternal    = "internal"    // anything else
)

// Errors maps codes to a capability package's sentinel errors.
type Errors map[string]error

// Map gives a wire error its sentinel, so the caller sees the capability's
// own error.
func (m Errors) Map(err error) error {
	var we *Error
	if errors.As(err, &we) && we.sentinel == nil {
		if s, ok := m[we.Code]; ok {
			we.sentinel = s
		}
	}
	return err
}

// Encode turns an implementation's error into a wire error.
func (m Errors) Encode(err error) *Error {
	var we *Error
	if errors.As(err, &we) {
		return &Error{Code: we.Code, Message: err.Error(), Cause: we.Cause, Status: we.Status}
	}
	out := &Error{Code: CodeInternal, Message: err.Error()}
	codes := make([]string, 0, len(m))
	for c := range m {
		codes = append(codes, c)
	}
	sort.Strings(codes)
	for _, c := range codes {
		if errors.Is(err, m[c]) {
			out.Code = c
			break
		}
	}
	var st interface{ HTTPStatus() int }
	if errors.As(err, &st) {
		out.Status = st.HTTPStatus()
	}
	var fc interface{ FailureCause() string }
	if errors.As(err, &fc) {
		out.Cause = fc.FailureCause()
	}
	return out
}

// statusOf is the HTTP status a wire error answers with.
func statusOf(code string) int {
	switch code {
	case CodeBadRequest:
		return http.StatusBadRequest
	case CodeUnsupported:
		return http.StatusNotImplemented
	case CodeInternal:
		return http.StatusInternalServerError
	case "not_found":
		return http.StatusNotFound
	case "rate_limited":
		return http.StatusTooManyRequests
	}
	return http.StatusUnprocessableEntity
}

// In is a received call.
type In struct {
	body []byte
	args []byte
	raw  bool
}

// Decode decodes the JSON arguments into v.
func (in *In) Decode(v any) error {
	b := in.body
	if in.raw {
		b = in.args
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return nil
	}
	if err := json.Unmarshal(b, v); err != nil {
		return &Error{Code: CodeBadRequest, Message: err.Error()}
	}
	return nil
}

// Body is the raw body of an operation with a []byte argument.
func (in *In) Body() []byte { return in.body }

// Op is one operation of a server.
type Op func(ctx context.Context, in *In) (any, error)

// Server answers the operations of one capability for one implementation.
type Server struct {
	Capability string // "social.publisher/v1"
	Ops        map[string]Op
	Errors     Errors
}

// Operations lists the offered operations, sorted.
func (s *Server) Operations() []string {
	out := make([]string, 0, len(s.Ops))
	for name := range s.Ops {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// MaxBody caps a request or response body.
const MaxBody = 64 << 20

// Serve answers one operation.
func (s *Server) Serve(w http.ResponseWriter, r *http.Request, op string) {
	f, ok := s.Ops[op]
	if !ok {
		WriteError(w, &Error{Code: CodeUnsupported, Message: fmt.Sprintf("%s does not offer %s", s.Capability, op)})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxBody+1))
	if err != nil || len(body) > MaxBody {
		WriteError(w, &Error{Code: CodeBadRequest, Message: "body unreadable or too large"})
		return
	}
	in := &In{body: body}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/octet-stream") {
		in.raw, in.args = true, []byte(r.URL.Query().Get("args"))
	}
	out, err := f(r.Context(), in)
	if err != nil {
		WriteError(w, s.Errors.Encode(err))
		return
	}
	if raw, ok := out.(Raw); ok {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(raw)
		return
	}
	if out == nil {
		out = struct{}{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// WriteError answers with a wire error.
func WriteError(w http.ResponseWriter, e *Error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusOf(e.Code))
	json.NewEncoder(w).Encode(map[string]*Error{"error": e})
}

// ReadError reads a failed response into a wire error.
func ReadError(resp *http.Response) *Error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var env struct {
		Error *Error `json:"error"`
	}
	if json.Unmarshal(b, &env) == nil && env.Error != nil && env.Error.Code != "" {
		return env.Error
	}
	msg := strings.TrimSpace(string(b))
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return &Error{Code: CodeUnavailable, Message: fmt.Sprintf("provider answered %d %s", resp.StatusCode, msg)}
}

// HTTP is a Caller that posts to URL + "/" + op.
type HTTP struct {
	Client *http.Client
	URL    string // up to the capability: "http://lemmy:8080/capabilities/social.publisher/v1"
	// Prepare sets headers before sending, such as the instance, a
	// signature or a token; body is what will be sent.
	Prepare func(r *http.Request, body []byte)
}

// Call implements Caller.
func (h *HTTP) Call(ctx context.Context, op string, req Request, out any) error {
	u := strings.TrimSuffix(h.URL, "/") + "/" + op
	var body []byte
	ct := "application/json"
	if req.Raw != nil {
		body, ct = req.Raw, "application/octet-stream"
		if req.Args != nil {
			a, err := json.Marshal(req.Args)
			if err != nil {
				return err
			}
			u += "?args=" + url.QueryEscape(string(a))
		}
	} else {
		args := req.Args
		if args == nil {
			args = struct{}{}
		}
		var err error
		if body, err = json.Marshal(args); err != nil {
			return err
		}
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	r.Header.Set("Content-Type", ct)
	if h.Prepare != nil {
		h.Prepare(r, body)
	}
	client := h.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return ReadError(resp)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxBody+1))
	if err != nil {
		return err
	}
	if len(data) > MaxBody {
		return &Error{Code: CodeUnavailable, Message: "response too large"}
	}
	switch o := out.(type) {
	case nil:
		return nil
	case *[]byte:
		*o = data
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return &Error{Code: CodeUnavailable, Message: "response does not decode: " + err.Error()}
	}
	return nil
}

// Adapter is what hpc gen writes for one capability.
type Adapter struct {
	Capability companion.Capability
	// Required are the operations every provider offers; Optional the
	// others, by the optional interface's type name.
	Required []string
	Optional []string
	// Client returns an implementation of the capability (and of the
	// optional interfaces whose operations are in ops) that calls c.
	Client func(c Caller, ops []string) any
	// Server returns the server for impl, nil if impl does not implement
	// the capability.
	Server func(impl any) *Server
}

var (
	mu       sync.RWMutex
	adapters = map[string]Adapter{}
)

// Register is called by the generated adapters.
func Register(a Adapter) {
	mu.Lock()
	defer mu.Unlock()
	adapters[a.Capability.String()] = a
}

// Lookup returns the adapter of a capability by its full name
// ("social.publisher/v1").
func Lookup(name string) (Adapter, bool) {
	mu.RLock()
	defer mu.RUnlock()
	a, ok := adapters[name]
	return a, ok
}

// Adapters lists every registered adapter, by name.
func Adapters() []Adapter {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]Adapter, 0, len(adapters))
	for _, a := range adapters {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Capability.String() < out[j].Capability.String() })
	return out
}

// Has reports whether ops contains op.
func Has(ops []string, op string) bool {
	for _, o := range ops {
		if o == op {
			return true
		}
	}
	return false
}

package core

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// The egress proxy (docs/concepts/modular-architecture.md §5.8): a service's
// only way to the internet. Its containers sit in internal networks with
// HTTP_PROXY pointing here; the proxy lets through what the service's
// egress grant lists and nothing else. It tells services apart by their
// address in their own network, or by Proxy-Authorization
// (Basic <service>:<token>) where that is not possible (development).

// egressAllowLoopback lets tests reach their local servers.
var egressAllowLoopback = false

const egressDialTimeout = 10 * time.Second

// egress handles CONNECT and absolute-URL requests on the services'
// listener.
func (m *serviceManager) egress(w http.ResponseWriter, r *http.Request) {
	b := m.egressClient(r)
	if b == nil {
		w.Header().Set("Proxy-Authenticate", `Basic realm="companion"`)
		http.Error(w, "egress: unknown service (not on a service network, no valid Proxy-Authorization)", http.StatusProxyAuthRequired)
		return
	}
	host, port := r.URL.Hostname(), r.URL.Port()
	if r.Method == http.MethodConnect {
		host, port, _ = net.SplitHostPort(r.Host)
	}
	if port == "" {
		port = map[string]string{"https": "443"}[r.URL.Scheme]
		if port == "" {
			port = "80"
		}
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if !egressGranted(b.currentGrants().Egress, host, port) {
		m.denyEgress(w, b, host, port, "not granted")
		return
	}
	ips, err := m.egressResolve(r.Context(), host)
	if err != nil {
		m.c.metrics.add("companion_egress_requests_total", 1, "service", b.id, "host", host, "result", "error")
		http.Error(w, fmt.Sprintf("egress: %s: %v", host, err), http.StatusBadGateway)
		return
	}
	m.c.metrics.add("companion_egress_requests_total", 1, "service", b.id, "host", host, "result", "allowed")
	addr := net.JoinHostPort(ips[0].String(), port)
	if r.Method == http.MethodConnect {
		m.tunnel(w, addr)
		return
	}
	m.forwardHTTP(w, r, addr)
}

func (m *serviceManager) denyEgress(w http.ResponseWriter, b *svcBlock, host, port, why string) {
	m.c.metrics.add("companion_egress_requests_total", 1, "service", b.id, "host", host, "result", "denied")
	m.c.log.Printf("services: %s: egress to %s:%s refused (%s)", b.id, host, port, why)
	http.Error(w, fmt.Sprintf("egress to %s:%s is %s for service %s", host, port, why, b.id), http.StatusForbidden)
}

// egressGranted matches a destination against the grant: "host" (ports 80
// and 443), "host:port", "*.example.com" (subdomains, not example.com).
func egressGranted(grant []string, host, port string) bool {
	for _, g := range grant {
		g = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(g)), ".")
		gh, gp := g, ""
		if h, p, err := net.SplitHostPort(g); err == nil {
			gh, gp = h, p
		}
		if gp == "" && port != "80" && port != "443" {
			continue
		}
		if gp != "" && gp != port {
			continue
		}
		if suffix, ok := strings.CutPrefix(gh, "*."); ok {
			if strings.HasSuffix(host, "."+suffix) {
				return true
			}
			continue
		}
		if gh == host {
			return true
		}
	}
	return false
}

// egressResolve resolves the destination once, so the address checked is
// the address dialled, and refuses loopback, link-local (the cloud metadata
// service), multicast and unspecified addresses.
func (m *serviceManager) egressResolve(ctx context.Context, host string) ([]net.IP, error) {
	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		for _, a := range addrs {
			ips = append(ips, a.IP)
		}
	}
	var ok []net.IP
	for _, ip := range ips {
		if (ip.IsLoopback() && !egressAllowLoopback) || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
			continue
		}
		ok = append(ok, ip)
	}
	if len(ok) == 0 {
		return nil, errors.New("resolves only to addresses services may not reach")
	}
	return ok, nil
}

// tunnel answers CONNECT: a TCP connection the service speaks TLS over.
func (m *serviceManager) tunnel(w http.ResponseWriter, addr string) {
	dst, err := net.DialTimeout("tcp", addr, egressDialTimeout)
	if err != nil {
		http.Error(w, "egress: "+err.Error(), http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		dst.Close()
		http.Error(w, "egress: no tunnel", http.StatusInternalServerError)
		return
	}
	src, buf, err := hj.Hijack()
	if err != nil {
		dst.Close()
		return
	}
	src.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if buf.Reader.Buffered() > 0 {
			io.CopyN(dst, buf, int64(buf.Reader.Buffered()))
		}
		io.Copy(dst, src)
		closeWrite(dst)
	}()
	go func() {
		defer wg.Done()
		io.Copy(src, dst)
		closeWrite(src)
	}()
	wg.Wait()
	src.Close()
	dst.Close()
}

func closeWrite(c net.Conn) {
	if tc, ok := c.(interface{ CloseWrite() error }); ok {
		tc.CloseWrite()
	}
}

// forwardHTTP passes a plain-HTTP request on to the checked address.
func (m *serviceManager) forwardHTTP(w http.ResponseWriter, r *http.Request, addr string) {
	tr := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: egressDialTimeout}).DialContext(ctx, "tcp", addr)
		},
		ResponseHeaderTimeout: 2 * time.Minute,
	}
	defer tr.CloseIdleConnections()
	out := r.Clone(r.Context())
	out.RequestURI = ""
	for _, h := range []string{"Proxy-Authorization", "Proxy-Connection", "Connection", "Keep-Alive", "Te", "Trailer", "Upgrade"} {
		out.Header.Del(h)
	}
	resp, err := tr.RoundTrip(out)
	if err != nil {
		http.Error(w, "egress: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// egressClient finds the service a proxy request comes from.
func (m *serviceManager) egressClient(r *http.Request) *svcBlock {
	if auth := r.Header.Get("Proxy-Authorization"); auth != "" {
		raw, ok := strings.CutPrefix(auth, "Basic ")
		if !ok {
			return nil
		}
		dec, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return nil
		}
		user, pass, _ := strings.Cut(string(dec), ":")
		b := m.byToken(pass)
		if b == nil || b.id != user {
			return nil
		}
		return b
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return nil
	}
	return m.byAddress(r.Context(), net.ParseIP(host))
}

// byAddress maps a source address to the service whose container has it:
// next to the companion, each service's name resolves to its address in
// its own network, where nothing but the service and the companion lives.
func (m *serviceManager) byAddress(ctx context.Context, ip net.IP) *svcBlock {
	if ip == nil {
		return nil
	}
	for _, b := range m.active() {
		for _, a := range m.addressesOf(ctx, b) {
			if a.Equal(ip) {
				return b
			}
		}
	}
	return nil
}

// addressesOf resolves a service's name, cached for a while; a miss is
// what a restarted container with a new address looks like, so it is
// resolved again.
func (m *serviceManager) addressesOf(ctx context.Context, b *svcBlock) []net.IP {
	b.mu.Lock()
	cached, at := b.addrs, b.addrsAt
	b.mu.Unlock()
	if time.Since(at) < 10*time.Second {
		return cached
	}
	host := b.id
	if u := b.baseURL(); u != "" {
		if i := strings.Index(u, "://"); i >= 0 {
			hp := u[i+3:]
			if j := strings.IndexAny(hp, "/?"); j >= 0 {
				hp = hp[:j]
			}
			if h, _, err := net.SplitHostPort(hp); err == nil {
				host = h
			} else {
				host = hp
			}
		}
	}
	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else if addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host); err == nil {
		for _, a := range addrs {
			ips = append(ips, a.IP)
		}
	}
	// A loopback address is no container's: anything local has it.
	var out []net.IP
	for _, ip := range ips {
		if !ip.IsLoopback() {
			out = append(out, ip)
		}
	}
	b.mu.Lock()
	b.addrs, b.addrsAt = out, time.Now()
	b.mu.Unlock()
	return out
}

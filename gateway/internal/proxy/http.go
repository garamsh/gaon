package proxy

import (
	"crypto/tls"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
)

// newTransport returns the transport to the backends: HTTP/1.1 over plain TCP,
// with the body left as the backend sent it.
func newTransport() *http.Transport {
	return &http.Transport{
		DialContext:         (&net.Dialer{Timeout: handshakeTimeout}).DialContext,
		DisableCompression:  true,
		MaxIdleConnsPerHost: 16,
		IdleConnTimeout:     DefaultKeepAlive,
	}
}

// httpHandler answers the requests of one HTTP or HTTPS port.
type httpHandler struct {
	p     *Proxy
	port  int32
	https bool
}

// ServeHTTP recovers a panic of the handler, which net/http would answer by
// dropping the connection, and answers 500. http.ErrAbortHandler is passed on.
func (h httpHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() {
		v := recover()
		if v == nil {
			return
		}
		if v == http.ErrAbortHandler {
			panic(v)
		}
		h.p.log.Error("handler panicked", "panic", v)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}()
	h.route(w, r)
}

func (h httpHandler) route(w http.ResponseWriter, r *http.Request) {
	pt := h.p.table.Load().ports[h.port]
	if pt == nil {
		http.Error(w, "no route", http.StatusNotFound)
		return
	}
	host := hostOf(r.Host)
	serverName := host
	if h.https {
		serverName = ""
		if r.TLS != nil {
			serverName = strings.ToLower(r.TLS.ServerName)
		}
	}
	m, ok := pt.lookup(serverName, host, r.URL.Path)
	if !ok {
		http.Error(w, "no route", http.StatusNotFound)
		return
	}
	addr, ok := m.rule.backend.pick()
	if !ok {
		http.Error(w, "no ready backend", http.StatusServiceUnavailable)
		return
	}
	proto := "http"
	if h.https {
		proto = "https"
	}
	rp := &httputil.ReverseProxy{
		// Rewrite runs after the hop-by-hop headers are removed, so a client
		// cannot strip a header the proxy sets by naming it in Connection.
		Rewrite: func(pr *httputil.ProxyRequest) {
			out := pr.Out
			out.URL.Scheme, out.URL.Host = "http", addr
			// Rewrite mode drops the client's Forwarded and X-Forwarded-Host
			// and re-encodes a query it cannot parse; the proxy passes all
			// three on as sent.
			out.URL.RawQuery = pr.In.URL.RawQuery
			for _, k := range []string{"Forwarded", "X-Forwarded-Host"} {
				if v, ok := pr.In.Header[k]; ok {
					out.Header[k] = append([]string(nil), v...)
				}
			}
			forwardedFor(pr)
			out.Header.Set("X-Forwarded-Proto", proto)
			if m.rule.hasRewrite {
				out.URL.Path = replacePrefix(out.URL.Path, m.prefix, m.rule.rewrite)
				out.URL.RawPath = ""
			}
		},
		Transport:     h.p.trans,
		FlushInterval: -1,
		ErrorLog:      slog.NewLogLogger(h.p.log.Handler(), slog.LevelDebug),
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			h.p.log.Warn("backend failed", "backend", addr, "error", err)
			http.Error(w, "bad gateway", http.StatusBadGateway)
		},
	}
	rp.ServeHTTP(w, r)
}

// forwardedFor appends the address of the peer to the X-Forwarded-For the
// request carries, or sets it where there is none.
func forwardedFor(pr *httputil.ProxyRequest) {
	peer, _, err := net.SplitHostPort(pr.In.RemoteAddr)
	if err != nil {
		peer = pr.In.RemoteAddr
	}
	if prior := pr.In.Header["X-Forwarded-For"]; len(prior) > 0 {
		peer = strings.Join(prior, ", ") + ", " + peer
	}
	pr.Out.Header.Set("X-Forwarded-For", peer)
}

// newHTTPServer returns the server of an HTTP or HTTPS port.
func (p *Proxy) newHTTPServer(port int32, https bool) *http.Server {
	return &http.Server{
		Handler:           httpHandler{p: p, port: port, https: https},
		ReadHeaderTimeout: handshakeTimeout,
		IdleTimeout:       p.cfg.KeepAlive,
		TLSNextProto:      map[string]func(*http.Server, *tls.Conn, http.Handler){},
		ErrorLog:          slog.NewLogLogger(p.log.Handler(), slog.LevelDebug),
	}
}

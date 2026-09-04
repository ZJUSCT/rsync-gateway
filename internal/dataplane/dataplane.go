// Package dataplane embeds the rsync-proxy data plane (pkg/server) into the
// gateway process. It owns the listeners (which are static for the lifetime
// of the process, mirroring the pkg/server design) and exposes a
// hot-reloadable Apply used by the controller to push new routing tables.
package dataplane

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/ustclug/rsync-proxy/pkg/server"
)

// Options configures a Dataplane.
type Options struct {
	// BindAddrs are the rsync listener addresses. At least one is
	// required. The addresses are bound once at startup; the set of ports
	// is static for the lifetime of the process.
	BindAddrs []string
	// AdminAddr is the address of the HTTP admin server exposing the
	// upstream /metrics, /status and /upstream-modules endpoints. It is
	// served by the first rsync server instance.
	AdminAddr string
	// ReadTimeout is the per-read timeout applied to client and upstream
	// connections. Zero means one minute, matching the upstream
	// rsync-proxy CLI.
	ReadTimeout time.Duration
	// WriteTimeout is the per-write timeout. Zero means one minute.
	WriteTimeout time.Duration
}

// Dataplane wraps the embedded rsync-proxy servers. There is one underlying
// server.Server per bind address; each of them holds an identical copy of
// the routing table, and the first one additionally serves the admin HTTP
// endpoints.
type Dataplane struct {
	servers []*server.Server
	errC    chan error
}

// emptyTableConfig is applied at startup so that the data plane accepts
// connections (and refuses every module) before the controller produces the
// first table.
func emptyTableConfig() *server.Config {
	return &server.Config{Upstreams: map[string]*server.Upstream{}}
}

// New creates the listeners and applies an initial, empty routing table.
// The returned Dataplane is not serving yet; call Start to begin serving.
// On error, every listener created so far is closed.
func New(opts Options) (*Dataplane, error) {
	if len(opts.BindAddrs) == 0 {
		return nil, fmt.Errorf("dataplane: at least one bind address is required")
	}
	if opts.AdminAddr == "" {
		return nil, fmt.Errorf("dataplane: admin address is required")
	}
	readTimeout, writeTimeout := opts.ReadTimeout, opts.WriteTimeout
	if readTimeout == 0 {
		readTimeout = time.Minute
	}
	if writeTimeout == 0 {
		writeTimeout = time.Minute
	}

	d := &Dataplane{errC: make(chan error, 1)}
	closeAll := func() { d.Close() }
	for i, addr := range opts.BindAddrs {
		srv := server.New()
		srv.ReadTimeout = readTimeout
		srv.WriteTimeout = writeTimeout
		if err := srv.ApplyConfig(emptyTableConfig(), false); err != nil {
			closeAll()
			return nil, fmt.Errorf("dataplane: apply initial config: %w", err)
		}
		if i == 0 {
			// The first server owns the admin HTTP listener; Listen
			// binds both the rsync and the admin socket.
			srv.ListenAddr = addr
			srv.HTTPListenAddr = opts.AdminAddr
			if err := srv.Listen(); err != nil {
				closeAll()
				return nil, fmt.Errorf("dataplane: listen on %s: %w", addr, err)
			}
		} else {
			// Additional servers: pkg/server always creates an admin
			// listener inside Listen, so instead of Listen we bind the
			// rsync socket manually and hand Run a pre-closed dummy
			// HTTP listener (its http.Serve returns net.ErrClosed
			// immediately, which Run filters out).
			l, err := bindAddr(addr)
			if err != nil {
				closeAll()
				return nil, fmt.Errorf("dataplane: listen on %s: %w", addr, err)
			}
			srv.TCPListener = l
			srv.ListenAddr = l.Addr().String()
			dummy, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				_ = l.Close()
				closeAll()
				return nil, fmt.Errorf("dataplane: create dummy http listener: %w", err)
			}
			dummy.Close()
			srv.HTTPListener = dummy
		}
		d.servers = append(d.servers, srv)
	}
	return d, nil
}

// bindAddr mirrors pkg/server's listenTCPOrUnix: addresses starting with "/"
// are UNIX domain sockets, everything else is TCP.
func bindAddr(addr string) (net.Listener, error) {
	if strings.HasPrefix(addr, "/") {
		_ = os.Remove(addr)
		l, err := net.Listen("unix", addr)
		if err != nil {
			return nil, err
		}
		if err := os.Chmod(addr, 0o660); err != nil {
			_ = l.Close()
			return nil, err
		}
		return l, nil
	}
	return net.Listen("tcp", addr)
}

// Apply hot-swaps the routing table (and the proxy-wide settings) on every
// underlying server via Server.ApplyConfig with openLog=false: the embedded
// data plane performs no module discovery and writes no log files.
func (d *Dataplane) Apply(cfg *server.Config) error {
	for _, srv := range d.servers {
		if err := srv.ApplyConfig(cfg, false); err != nil {
			return fmt.Errorf("dataplane: apply config: %w", err)
		}
	}
	return nil
}

// Start starts serving on all listeners in the background. The first
// asynchronous error is surfaced on Err.
func (d *Dataplane) Start() {
	for _, srv := range d.servers {
		go d.runOne(srv)
	}
}

func (d *Dataplane) runOne(srv *server.Server) {
	err := srv.Run()
	if err != nil {
		select {
		case d.errC <- err:
		default:
		}
	}
}

// Err exposes a channel carrying the first data-plane error, if any.
func (d *Dataplane) Err() <-chan error {
	return d.errC
}

// Drain closes all listeners and waits for every active relay connection to
// finish or ctx to expire. It returns the number of connections that were
// still active when it returned (the maximum across all underlying
// servers).
func (d *Dataplane) Drain(ctx context.Context) int {
	var remaining int
	for _, srv := range d.servers {
		if n := srv.Drain(ctx); n > remaining {
			remaining = n
		}
	}
	return remaining
}

// Close closes all listeners without waiting for connections. It is
// idempotent: server.Server.Close tolerates double closes.
func (d *Dataplane) Close() {
	for _, srv := range d.servers {
		srv.Close()
	}
}

// Addrs returns the resolved rsync listener addresses.
func (d *Dataplane) Addrs() []string {
	addrs := make([]string, 0, len(d.servers))
	for _, srv := range d.servers {
		addrs = append(addrs, srv.ListenAddr)
	}
	return addrs
}

// AdminAddr returns the resolved address of the admin HTTP listener.
func (d *Dataplane) AdminAddr() string {
	if len(d.servers) == 0 {
		return ""
	}
	return d.servers[0].HTTPListenAddr
}

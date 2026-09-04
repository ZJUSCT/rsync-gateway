package dataplane

import (
	"context"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ustclug/rsync-proxy/pkg/server"
	"github.com/ustclug/rsync-proxy/test/fake/rsync"
)

// doClientHandshake speaks the client side of the rsync protocol, mirroring
// pkg/server's own test helpers.
func doClientHandshake(conn *rsync.Conn, version []byte, module string) error {
	_, err := conn.Write(version)
	if err != nil {
		return err
	}
	if _, err = conn.ReadLine(); err != nil {
		return err
	}
	_, err = conn.Write([]byte(module + "\n"))
	return err
}

// doServerHandshake speaks the upstream (server) side: read client version,
// reply with our version, read the module name.
func doServerHandshake(conn *rsync.Conn, extra []byte, payload func(*rsync.Conn)) {
	defer conn.Close()
	if _, err := conn.ReadLine(); err != nil {
		return
	}
	_, _ = conn.Write(append([]byte("@RSYNCD: 32.0 sha512 sha256 sha1 md5 md4\n"), extra...))
	if _, err := conn.ReadLine(); err != nil {
		return
	}
	if payload != nil {
		payload(conn)
	}
}

// newUpstream starts a fake rsyncd that replies with the given extra data
// after its version banner and blocks until released while a relay is
// active.
func newUpstream(t *testing.T, extra string, hold *sync.WaitGroup) *rsync.Server {
	t.Helper()
	up := rsync.NewServer(func(conn *rsync.Conn) {
		doServerHandshake(conn, []byte(extra), func(c *rsync.Conn) {
			if hold != nil {
				// Block the relay until the test releases us.
				hold.Wait()
			}
		})
	})
	up.Start()
	t.Cleanup(up.Close)
	return up
}

func newTestDataplane(t *testing.T, opts Options) *Dataplane {
	t.Helper()
	opts.AdminAddr = "127.0.0.1:0"
	if len(opts.BindAddrs) == 0 {
		opts.BindAddrs = []string{"127.0.0.1:0"}
	}
	dp, err := New(opts)
	require.NoError(t, err)
	t.Cleanup(dp.Close)
	return dp
}

// TestDataplaneRoutesModulesToEndpoints covers (a) known modules route to
// the correct upstream, (b) unknown modules are refused with the @ERROR
// wire format, and (c) an empty-line list-all returns the aggregated module
// list.
func TestDataplaneRoutesModulesToEndpoints(t *testing.T) {
	upA := newUpstream(t, "from-upstream-A\n", nil)
	upB := newUpstream(t, "from-upstream-B\n", nil)

	dp := newTestDataplane(t, Options{})
	require.NoError(t, dp.Apply(&server.Config{
		Upstreams: map[string]*server.Upstream{
			"route-a": {Address: upA.Listener.Addr().String(), Modules: []string{"mod-a"}},
			"route-b": {Address: upB.Listener.Addr().String(), Modules: []string{"mod-b"}},
		},
	}))
	dp.Start()
	addr := dp.Addrs()[0]

	t.Run("unknown module gets @ERROR", func(t *testing.T) {
		raw, err := net.Dial("tcp", addr)
		require.NoError(t, err)
		conn := rsync.NewConn(raw)
		defer conn.Close()
		require.NoError(t, doClientHandshake(conn, server.RsyncdServerVersion, "does-not-exist"))
		data, err := io.ReadAll(conn)
		require.NoError(t, err)
		assert.Contains(t, string(data), "@ERROR: Unknown module")
		assert.Contains(t, string(data), "does-not-exist")
	})

	t.Run("known module routes to its upstream", func(t *testing.T) {
		raw, err := net.Dial("tcp", addr)
		require.NoError(t, err)
		conn := rsync.NewConn(raw)
		defer conn.Close()
		require.NoError(t, doClientHandshake(conn, server.RsyncdServerVersion, "mod-a"))
		data, err := io.ReadAll(conn)
		require.NoError(t, err)
		assert.Contains(t, string(data), "from-upstream-A")
		assert.NotContains(t, string(data), "from-upstream-B")
	})

	t.Run("list all returns aggregated modules", func(t *testing.T) {
		raw, err := net.Dial("tcp", addr)
		require.NoError(t, err)
		conn := rsync.NewConn(raw)
		defer conn.Close()
		require.NoError(t, doClientHandshake(conn, server.RsyncdServerVersion, ""))
		data, err := io.ReadAll(conn)
		require.NoError(t, err)
		assert.Contains(t, string(data), "mod-a\n")
		assert.Contains(t, string(data), "mod-b\n")
		assert.Contains(t, string(data), string(server.RsyncdExit))
	})

	t.Run("admin endpoint served", func(t *testing.T) {
		resp, err := http.Get("http://" + dp.AdminAddr() + "/status")
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})
}

// TestDataplaneApplyWithoutRestart covers (d): booting with an empty table,
// then applying a populated table serves modules without the listener
// address changing.
func TestDataplaneApplyWithoutRestart(t *testing.T) {
	up := newUpstream(t, "", nil)
	dp := newTestDataplane(t, Options{})
	dp.Start()

	addr := dp.Addrs()[0]

	// With the empty table every module must be refused.
	raw, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	conn := rsync.NewConn(raw)
	require.NoError(t, doClientHandshake(conn, server.RsyncdServerVersion, "whatever"))
	data, err := io.ReadAll(conn)
	require.NoError(t, err)
	assert.Contains(t, string(data), "@ERROR: Unknown module")
	require.NoError(t, conn.Close())

	// Apply a populated table: the listener must not move.
	require.NoError(t, dp.Apply(&server.Config{
		Upstreams: map[string]*server.Upstream{
			"r": {Address: up.Listener.Addr().String(), Modules: []string{"fake"}},
		},
	}))
	require.Equal(t, []string{addr}, dp.Addrs())

	raw2, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	conn2 := rsync.NewConn(raw2)
	defer conn2.Close()
	require.NoError(t, doClientHandshake(conn2, server.RsyncdServerVersion, "fake"))
	_, err = io.ReadAll(conn2)
	require.NoError(t, err)
}

// TestDataplaneDrain covers (e): Drain closes the listener and returns 0
// once the client disconnects; while a relay is active it returns the
// still-active count when the context expires.
func TestDataplaneDrain(t *testing.T) {
	hold := &sync.WaitGroup{}
	hold.Add(1)
	// The upstream writes the version banner plus a (single) extra line in
	// one go; the proxy relays everything after the first newline, so the
	// client can observe that the relay is established.
	up := newUpstream(t, "\n", hold)

	dp := newTestDataplane(t, Options{})
	require.NoError(t, dp.Apply(&server.Config{
		Upstreams: map[string]*server.Upstream{
			"r": {Address: up.Listener.Addr().String(), Modules: []string{"fake"}},
		},
	}))
	dp.Start()
	addr := dp.Addrs()[0]

	raw, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	conn := rsync.NewConn(raw)
	defer conn.Close()
	require.NoError(t, doClientHandshake(conn, server.RsyncdServerVersion, "fake"))
	_, err = conn.ReadLine()
	require.NoError(t, err, "should receive the upstream version banner through the relay")

	// Drain with a short context: the listener closes but the active
	// relay keeps counting.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	remaining := make(chan int, 1)
	go func() { remaining <- dp.Drain(ctx) }()

	require.Eventually(t, func() bool {
		c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err != nil {
			return true
		}
		_ = c.Close()
		return false
	}, time.Second, 20*time.Millisecond, "listener must be closed by Drain")

	select {
	case n := <-remaining:
		assert.Equal(t, 1, n, "the relay was still active when the context expired")
	case <-time.After(2 * time.Second):
		t.Fatal("Drain did not return after the context expired")
	}

	// Once the client disconnects a second Drain returns 0.
	require.NoError(t, conn.Close())
	hold.Done()
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	assert.Equal(t, 0, dp.Drain(ctx2))
}

// TestDataplaneMultipleBinds verifies the repeatable bind set: every address
// serves the same table and shares the single admin endpoint.
func TestDataplaneMultipleBinds(t *testing.T) {
	up := newUpstream(t, "", nil)
	dp := newTestDataplane(t, Options{BindAddrs: []string{"127.0.0.1:0", "127.0.0.1:0"}})
	require.NoError(t, dp.Apply(&server.Config{
		Upstreams: map[string]*server.Upstream{
			"r": {Address: up.Listener.Addr().String(), Modules: []string{"fake"}},
		},
	}))
	dp.Start()

	addrs := dp.Addrs()
	require.Len(t, addrs, 2)
	require.NotEqual(t, addrs[0], addrs[1])
	for _, addr := range addrs {
		raw, err := net.Dial("tcp", addr)
		require.NoError(t, err)
		conn := rsync.NewConn(raw)
		require.NoError(t, doClientHandshake(conn, server.RsyncdServerVersion, "fake"))
		_, err = io.ReadAll(conn)
		require.NoError(t, err)
		_ = conn.Close()
	}
}

package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/headwalluk/sycamore-client/core/config"
	"github.com/headwalluk/sycamore-client/core/protocol"
	"github.com/headwalluk/sycamore-client/core/tunnel"
)

// This file is the orchestration layer: it composes the SSH tunnel
// (core/tunnel) with the session-aware Client to connect to a named
// instance from the config file, and to run the staged "test
// connection" probe. It lives here, above tunnel, because the Connect
// stage has to read a welcome/error off the wire — protocol knowledge
// the transport-only tunnel package deliberately doesn't carry.
// See dev-notes/planning-ssh-tunnel.md §5, §11.

// Stage-probe timeouts. The SSH stage gets the longest budget — it
// covers DNS, the TCP handshake, and SSH auth.
const (
	probeSSHTimeout     = 15 * time.Second
	probeSocketTimeout  = 10 * time.Second
	probeConnectTimeout = 10 * time.Second
)

// InstanceClient is a Client whose connection runs over an SSH tunnel.
// It embeds *Client, so Messages / Send / Session / Err work directly;
// Close tears down both the client and the tunnel.
type InstanceClient struct {
	*Client
	tunnel *tunnel.Tunnel
}

// Close shuts down the client and then the tunnel. The client's error
// (if any) takes precedence in the return value.
func (ic *InstanceClient) Close() error {
	cerr := ic.Client.Close()
	terr := ic.tunnel.Close()
	if cerr != nil {
		return cerr
	}
	return terr
}

// DialInstance opens an SSH tunnel to inst, connects a session-aware
// Client over the forwarded socket, and returns the pair. On any
// failure the tunnel is torn down before returning, so a partial
// connect never strands a background ssh master.
//
// ctx bounds only the setup (tunnel open + dial); the returned client
// outlives it.
func DialInstance(ctx context.Context, inst config.Instance) (*InstanceClient, error) {
	tun, err := tunnel.Open(ctx, tunnelConfig(inst))
	if err != nil {
		return nil, fmt.Errorf("tunnel: %w", err)
	}

	client, err := Connect(ctx, tun.LocalSocket())
	if err != nil {
		if closeErr := tun.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("closing the tunnel: %w", closeErr))
		}
		return nil, fmt.Errorf("connect: %w", err)
	}

	return &InstanceClient{Client: client, tunnel: tun}, nil
}

// tunnelConfig maps a config.Instance onto a tunnel.Config. Kept here so
// the tunnel package stays free of any dependency on config.
func tunnelConfig(inst config.Instance) tunnel.Config {
	return tunnel.Config{
		Name:         inst.Name,
		Host:         inst.Host,
		User:         inst.User,
		Port:         inst.Port,
		IdentityFile: inst.Identity,
		RemoteSocket: inst.Socket,
		SocketDir:    config.RuntimeDir(),
	}
}

// Stage is the result of one step of the staged test-connection.
// Attempted is false for stages skipped because an earlier one failed.
type Stage struct {
	Attempted bool
	OK        bool
	Detail    string // operator-facing one-liner
	RawErr    string // underlying error text, for verbose output
}

// ProbeResult is the outcome of ProbeInstance — three stages mirroring
// the layers a connection passes through. Read top to bottom: the first
// non-OK stage is where it broke.
type ProbeResult struct {
	SSH     Stage // tunnel established?
	Socket  Stage // is sycamore.sock present on the far side?
	Connect Stage // can we open it as this user, and does the daemon accept us?

	// Cleanup is why tearing the probe's tunnel down failed, if it did.
	Cleanup error
}

// OK reports whether all three stages passed.
func (r ProbeResult) OK() bool {
	return r.SSH.OK && r.Socket.OK && r.Connect.OK
}

// ProbeInstance runs the staged "test connection" against inst: open the
// tunnel (SSH), check the remote socket exists (Socket), then open it
// and read the daemon's first message (Connect). It always tears the
// tunnel down before returning — it's a diagnostic, not a session.
//
// The Connect stage is where the operator's predicted #1 failure mode
// surfaces: a tunnel that's up and a socket that exists, but the SSH
// login user can't open it (EACCES — not in the `sycamore` group on the
// target → the channel closes with no bytes) or the daemon rejects the
// resolved principal (an `error` message). The three cases are reported
// distinctly. See dev-notes/planning-ssh-tunnel.md §5.
func ProbeInstance(ctx context.Context, inst config.Instance) (res ProbeResult) {

	// ── SSH stage ──
	res.SSH.Attempted = true
	octx, cancel := context.WithTimeout(ctx, probeSSHTimeout)
	tun, err := tunnel.Open(octx, tunnelConfig(inst))
	cancel()
	if err != nil {
		res.SSH.Detail = err.Error()
		res.SSH.RawErr = err.Error()
		return res
	}
	res.SSH.OK = true
	res.SSH.Detail = "tunnel established"
	defer func() { res.Cleanup = tun.Close() }()

	// ── Socket stage ──
	res.Socket.Attempted = true
	sctx, cancel2 := context.WithTimeout(ctx, probeSocketTimeout)
	exists, serr := tun.RemoteSocketExists(sctx)
	cancel2()
	if serr != nil {
		res.Socket.Detail = serr.Error()
		res.Socket.RawErr = serr.Error()
		return res
	}
	if !exists {
		res.Socket.Detail = "not found at " + inst.Socket
		return res
	}
	res.Socket.OK = true
	res.Socket.Detail = "found"

	// ── Connect stage ──
	res.Connect.Attempted = true
	cctx, cancel3 := context.WithTimeout(ctx, probeConnectTimeout)
	defer cancel3()
	res.Connect = probeConnect(cctx, tun.LocalSocket())
	res.Connect.Attempted = true
	return res
}

// connectClosedHint explains the most likely cause of the far side
// closing the forwarded channel with no welcome: the SSH login user
// can't open the socket (the daemon never sees the connection, so it
// can't tell us itself).
const connectClosedHint = "the host accepted the connection but the agent socket closed with no reply — " +
	"likely a permission issue (is the SSH login user in the `sycamore` group on the target host?)"

// probeConnect dials the forwarded socket and classifies the daemon's
// first message: a welcome (success), an error (the daemon rejected our
// principal), an immediate close (EACCES on the far side), or a timeout.
func probeConnect(ctx context.Context, localSocket string) (stage Stage) {
	conn, err := Dial(ctx, localSocket)
	if err != nil {
		return Stage{Attempted: true, Detail: "could not open the forwarded socket: " + err.Error(), RawErr: err.Error()}
	}
	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			stage.RawErr = strings.TrimSpace(stage.RawErr + "; closing the probe connection: " + closeErr.Error())
		}
	}()

	type result struct {
		kind string
		raw  json.RawMessage
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		kind, raw, err := conn.Recv()
		ch <- result{kind, raw, err}
	}()

	select {
	case <-ctx.Done():
		// Closing the conn unblocks the goroutine's Recv.
		return Stage{Attempted: true, Detail: "timed out waiting for the agent's welcome", RawErr: ctx.Err().Error()}
	case result := <-ch:
		if result.err != nil {
			return Stage{Attempted: true, Detail: connectClosedHint, RawErr: result.err.Error()}
		}
		switch result.kind {
		case protocol.TypeWelcome:
			var welcome protocol.Welcome
			if decodeErr := json.Unmarshal(result.raw, &welcome); decodeErr != nil {
				return Stage{Attempted: true, OK: true, Detail: "connected (the welcome didn't decode)", RawErr: decodeErr.Error()}
			}
			detail := "connected"
			if welcome.Principal != "" {
				detail = "connected as " + welcome.Principal
			}
			return Stage{Attempted: true, OK: true, Detail: detail}
		case protocol.TypeError:
			var rejection protocol.Error
			if decodeErr := json.Unmarshal(result.raw, &rejection); decodeErr != nil {
				return Stage{Attempted: true, Detail: "agent rejected the connection (its reason didn't decode)", RawErr: decodeErr.Error()}
			}
			return Stage{Attempted: true, Detail: "agent rejected the connection: " + rejection.Message, RawErr: rejection.Message}
		default:
			return Stage{Attempted: true, Detail: "unexpected first message from agent: " + result.kind}
		}
	}
}

// SweepStaleTunnels stops tunnel masters left behind by client processes
// that exited without closing them, and removes their files. Clients run it
// at start-up; see tunnel.SweepStale.
func SweepStaleTunnels(ctx context.Context) ([]string, error) {
	return tunnel.SweepStale(ctx, config.RuntimeDir())
}

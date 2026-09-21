package routeros

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jmrplens/mikroscope/internal/rosapi/proto"
)

// newClientAndLogin is what every Dial* funnels into once it has a connection,
// so it is the one place the "connected but not logged in" state is closed.
// It takes an io.ReadWriteCloser, which means it needs no socket to test: the
// pipe pair the protocol tests already use is a connection as far as it is
// concerned.
func pipePair(t *testing.T) (io.ReadWriteCloser, *fakeServer) {
	t.Helper()
	ar, aw := io.Pipe()
	br, bw := io.Pipe()
	return &conn{ar, bw}, &fakeServer{proto.NewReader(br), proto.NewWriter(aw), &conn{br, aw}}
}

func TestNewClientAndLoginReturnsALoggedInClient(t *testing.T) {
	rwc, s := pipePair(t)
	go func() {
		defer deferCloser(t, s)
		s.readSentence(t, "/login @ [{`name` `admin`} {`password` `secret`}]")
		s.writeSentence(t, "!done")
	}()

	c, err := newClientAndLogin(context.Background(), rwc, "admin", "secret")
	require.NoError(t, err)
	require.NotNil(t, c)
	deferCloser(t, c)
}

// A refused login has to close the connection on the way out: a client that
// returned an error and left the socket open would leak one per attempt, and
// the router counts sessions.
func TestNewClientAndLoginClosesTheConnectionWhenLoginFails(t *testing.T) {
	rwc, s := pipePair(t)
	go func() {
		defer deferCloser(t, s)
		s.readSentence(t, "/login @ [{`name` `admin`} {`password` `wrong`}]")
		// A trap is how RouterOS refuses credentials.
		s.writeSentence(t, "!trap", "=message=cannot log in")
		s.writeSentence(t, "!done")
	}()

	c, err := newClientAndLogin(context.Background(), rwc, "admin", "wrong")
	require.Error(t, err)
	require.Nil(t, c)
	if !strings.Contains(err.Error(), "could not login") {
		t.Errorf("error = %q, want it to say which half failed", err)
	}
	// The error carries the close's own outcome, so a close that also failed
	// is not swallowed.
	if !strings.Contains(err.Error(), "close") {
		t.Errorf("error = %q, want the close result in it", err)
	}
}

// SetLogHandler is how a caller redirects the client's protocol logging; it
// takes the mutex because a running client logs from its read loop.
func TestSetLogHandlerRedirectsTheClientsLogging(t *testing.T) {
	rwc, s := pipePair(t)
	go func() {
		defer deferCloser(t, s)
		s.readSentence(t, "/login @ [{`name` `a`} {`password` `b`}]")
		s.writeSentence(t, "!done")
	}()
	c, err := newClientAndLogin(context.Background(), rwc, "a", "b")
	require.NoError(t, err)
	defer deferCloser(t, c)

	var sb strings.Builder
	c.SetLogHandler(slog.NewTextHandler(&sb, &slog.HandlerOptions{Level: slog.LevelDebug}))
	c.logger().Debug("a line from the read loop")
	if !strings.Contains(sb.String(), "a line from the read loop") {
		t.Errorf("the handler did not receive the line: %q", sb.String())
	}
}

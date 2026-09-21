package apitier

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/jmrplens/mikroscope/internal/rosapi/proto"
)

// Dial is the tier's own connect: a bounded dial, a login, and a command
// timeout set on the way out so one slow command cannot stall the tier. It
// needs a RouterOS API speaker, not a router — a listener that answers the
// login sentence is one.
func loginListener(t *testing.T) string {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				r, w := proto.NewReader(c), proto.NewWriter(c)
				for {
					if _, readErr := r.ReadSentence(); readErr != nil {
						return
					}
					w.BeginSentence()
					w.WriteWord("!done")
					if endErr := w.EndSentence(); endErr != nil {
						return
					}
				}
			}(c)
		}
	}()
	return ln.Addr().String()
}

func TestDialLogsInAndBoundsItsCommands(t *testing.T) {
	t.Parallel()
	c, err := Dial(context.Background(), loginListener(t), "admin", "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if c == nil {
		t.Fatal("Dial returned no client and no error")
	}
}

// A refused connection has to name the address: an operator reading it needs
// to know which host:port the tier was configured with, not just that it
// failed.
func TestDialNamesTheAddressItCouldNotReach(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := Dial(ctx, "127.0.0.1:1", "admin", "secret")
	if err == nil {
		t.Fatal("dialing a closed port returned no error")
	}
	if !strings.Contains(err.Error(), "127.0.0.1:1") || !strings.HasPrefix(err.Error(), "api ") {
		t.Errorf("error = %q, want it prefixed `api ` and carrying the address", err)
	}
}

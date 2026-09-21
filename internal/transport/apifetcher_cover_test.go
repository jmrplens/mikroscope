package transport

import (
	"context"
	"io"
	"strings"
	"testing"

	rosapi "github.com/jmrplens/mikroscope/internal/rosapi"
	"github.com/jmrplens/mikroscope/internal/rosapi/proto"
)

// APIFetcher is the relay's router-side half: it asks RouterOS to make the
// HTTP request with `/tool fetch output=user` and reads the body back out of
// the reply. It needs a RouterOS API session, not a router — the client takes
// an io.ReadWriteCloser, so a pipe pair is a session as far as it is
// concerned.
type pipeConn struct {
	io.Reader
	io.WriteCloser
}

func (p *pipeConn) Close() error { return p.WriteCloser.Close() }

// fetchRouter answers a login and then one /tool/fetch, writing the sentences
// reply says. It returns the client the fetcher will use.
func fetchRouter(t *testing.T, reply func(w proto.Writer)) *rosapi.Client {
	t.Helper()
	ar, aw := io.Pipe()
	br, bw := io.Pipe()
	client, err := rosapi.NewClient(&pipeConn{ar, bw})
	if err != nil {
		t.Fatal(err)
	}
	r, w := proto.NewReader(br), proto.NewWriter(aw)
	go func() {
		for {
			if _, readErr := r.ReadSentence(); readErr != nil {
				return
			}
			reply(w)
		}
	}()
	return client
}

func sentence(w proto.Writer, words ...string) {
	w.BeginSentence()
	for _, word := range words {
		w.WriteWord(word)
	}
	_ = w.EndSentence()
}

func TestAPIFetcherReadsTheBodyOutOfTheReply(t *testing.T) {
	t.Parallel()
	body := `{"ok":true,"seq":7}`
	c := fetchRouter(t, func(w proto.Writer) {
		// RouterOS answers /tool fetch with one or more !re sentences and a
		// !done; the body is the `data` field of the last one that has it.
		sentence(w, "!re", "=status=finished")
		sentence(w, "!re", "=data="+body)
		sentence(w, "!done")
	})
	defer func() { _ = c.Close() }()

	got, err := APIFetcher{Client: c}.Fetch(context.Background(), "http://172.30.10.2:9123/healthz")
	if err != nil {
		t.Fatal(err)
	}
	if got != body {
		t.Errorf("Fetch = %q, want %q", got, body)
	}
}

// A reply with no data field at all is the shape RouterOS returns when the
// user's policy is short of `test`: the call succeeds and carries nothing.
// Reading that as an empty body would make the collector see an agent that
// answers with silence rather than a permission problem.
func TestAPIFetcherRefusesAReplyWithNoData(t *testing.T) {
	t.Parallel()
	c := fetchRouter(t, func(w proto.Writer) {
		sentence(w, "!re", "=status=finished")
		sentence(w, "!done")
	})
	defer func() { _ = c.Close() }()

	_, err := APIFetcher{Client: c}.Fetch(context.Background(), "http://172.30.10.2:9123/healthz")
	if err == nil || !strings.Contains(err.Error(), "no data") {
		t.Errorf("Fetch on a reply with no data = %v, want an error saying so", err)
	}
}

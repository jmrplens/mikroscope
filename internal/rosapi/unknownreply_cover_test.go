package routeros

import (
	"testing"

	"github.com/jmrplens/mikroscope/internal/rosapi/proto"
)

// The reply word the client could not place travels in the error, because
// without it the message says only that something unexpected arrived.
func TestUnknownReplyErrorNamesTheWord(t *testing.T) {
	t.Parallel()
	err := &UnknownReplyError{Sentence: &proto.Sentence{Word: "!bogus"}}
	if got, want := err.Error(), "unknown RouterOS reply word: !bogus"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

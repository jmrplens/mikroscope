package router

import (
	"errors"
	"regexp"
	"strings"
	"testing"
)

// keyedPut finds the key a keyed query prints under.
var keyedPut = regexp.MustCompile(`\("@@([^"=]+)=" \. `)

// answerLine is what the router prints for query q when its value is v: the
// key and the value for a keyed query, the bare value for anything else.
// Every fake router in these tests answers through it.
func answerLine(q, v string) string {
	if m := keyedPut.FindStringSubmatch(q); m != nil {
		return keyPrefix + m[1] + "=" + v
	}
	return v
}

func TestKeyedWrapsEveryPut(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		`:put [:len [/interface/veth/find name="veth-mikroscope"]]`:        `:put ("@@k=" . [:len [/interface/veth/find name="veth-mikroscope"]])`,
		`:put ([:len [/container/find]] + [:len [/file/find name="a]b"]])`: `:put ("@@k=" . ([:len [/container/find]] + [:len [/file/find name="a]b"]]))`,
		`:put [/system/resource/get version]`:                              `:put ("@@k=" . [/system/resource/get version])`,
		`:put "literal"`:                                                   `:put ("@@k=" . "literal")`,
		`:put $x`:                                                          `:put ("@@k=" . $x)`,
		`:local a 1; :put $a; :put 2`:                                      `:local a 1; :put ("@@k=" . $a); :put ("@@k=" . 2)`,
		// The container step's Owned: two :put, one of which runs.
		`:if ([:len [find comment="x (managed by mikroscope)"]] > 0) do={ :put ([:len [/container/find]] + 1) } else={ :put [:len [/container/find]] }`: `:if ([:len [find comment="x (managed by mikroscope)"]] > 0) do={ :put ("@@k=" . ([:len [/container/find]] + 1)) } else={ :put ("@@k=" . [:len [/container/find]]) }`,
		// Nothing to key, or keyed already.
		`/interface/veth/remove [find]`:        `/interface/veth/remove [find]`,
		`:put ("@@other=" . 1)`:                `:put ("@@other=" . 1)`,
		`:put "a :put b"`:                      `:put ("@@k=" . "a :put b")`,
		`:put [:len [find comment="\":put "]]`: `:put ("@@k=" . [:len [find comment="\":put "]])`,
	} {
		if got := keyed("k", in); got != want {
			t.Errorf("keyed(%s)\n got %s\nwant %s", in, got, want)
		}
	}
}

func TestParseKeyedReadsByKeyAndKeepsTheRest(t *testing.T) {
	t.Parallel()
	// What the lab's CHR printed for five keyed :put, the second and the
	// fourth reading a menu and a property that do not exist (RouterOS
	// 7.24.4, 2026-09-26), plus a stray warning and a duplicate key.
	out := "@@a=7.24.4 (stable)\r\nsyntax error (line 1 column 29)\n@@c=3\r\n" +
		"input does not match any value of value-name (/system/resource/get (value-name); line 1)\n" +
		"@@e=x86_64\r\nwarning: something\n@@c=4\n@@=nokey\n\n"
	a, stray := parseKeyed(out)
	if a.get("a") != "7.24.4 (stable)" || a.get("c") != "3" || a.get("e") != "x86_64" {
		t.Errorf("answers = %v", a)
	}
	if a.has("b") || a.get("b") != unread || a.get("d") != unread {
		t.Errorf("a key with no line reads %q, want %q", a.get("b"), unread)
	}
	if len(stray) != 4 || !strings.HasPrefix(stray[0], "syntax error") || stray[3] != "@@=nokey" {
		t.Errorf("stray = %q", stray)
	}
}

// batch keys each query by its position, so an extra line does not shift
// the answers after it, and a query the router did not answer is an error
// that names it and quotes what the router said instead.
func TestBatchReadsByKeyNotByPosition(t *testing.T) {
	t.Parallel()
	r := runFunc(func(cmd string) (string, error) {
		qs := strings.Split(cmd, "\n")
		return "a warning first\n" + answerLine(qs[1], "b") + "\n" + answerLine(qs[0], "a") + "\n", nil
	})
	got, err := batch(r, []string{":put 1", ":put 2"})
	if err != nil || len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("batch = %q, %v", got, err)
	}
	r = runFunc(func(cmd string) (string, error) {
		qs := strings.Split(cmd, "\n")
		return answerLine(qs[0], "a") + "\nbad command name nosuch (line 1 column 2)\n", nil
	})
	if _, err = batch(r, []string{":put 1", ":put [/nosuch/get x]"}); err == nil ||
		!strings.Contains(err.Error(), "/nosuch/get x") || !strings.Contains(err.Error(), "bad command name") {
		t.Errorf("a query without an answer: %v", err)
	}
	failed := errors.New("ssh: connection refused")
	if _, err = batch(runFunc(func(string) (string, error) { return "", failed }), []string{":put 1"}); !errors.Is(err, failed) {
		t.Errorf("a failed connect: %v", err)
	}
}

// runFunc is a Runner made of one function.
type runFunc func(string) (string, error)

func (f runFunc) Run(cmd string) (string, error) { return f(cmd) }
func (runFunc) Upload([]byte, string) error      { return nil }

// RouterOS's ssh exits 1 when the session's last command failed, and a
// keyed batch ends with a line that cannot fail (endLine): an exit status
// with that line's answer in the output is a batch that ran through, and its
// answers are read; one without it (a refused connect, a session cut short)
// is the error it is.
func TestReadKeyedReadsThroughAFailedExitStatus(t *testing.T) {
	t.Parallel()
	exit1 := errors.New("ssh: exit status 1")
	r := runFunc(func(cmd string) (string, error) {
		qs := strings.Split(cmd, "\n")
		if qs[len(qs)-1] != endLine {
			t.Errorf("the batch does not end with %q: %q", endLine, cmd)
		}
		return answerLine(qs[0], "a") + "\nno such item (/disk/get; line 1)\n" + keyPrefix + batchEnd + "=1\n", exit1
	})
	a, stray, err := readKeyed(r, []query{{key: "x", text: ":put 1"}, {key: "y", text: `:put [/disk/get [find slot="nosuch"] free]`}})
	if err != nil || a.get("x") != "a" || a.has("y") || a.has(batchEnd) || len(stray) != 1 {
		t.Errorf("readKeyed = %v, %q, %v", a, stray, err)
	}
	r = runFunc(func(cmd string) (string, error) {
		return answerLine(strings.Split(cmd, "\n")[0], "a") + "\n", exit1
	})
	if _, _, err = readKeyed(r, []query{{key: "x", text: ":put 1"}, {key: "y", text: ":put 2"}}); !errors.Is(err, exit1) {
		t.Errorf("a batch cut short: %v", err)
	}
}

package router

import (
	"bytes"
	"strings"
	"testing"
)

func TestGOARCHFor(t *testing.T) {
	t.Parallel()
	for arch, want := range map[string]string{"arm64": "arm64", "arm": "arm", "x86_64": "amd64", "mipsbe": "", "tile": "", "": ""} {
		got, ok := GOARCHFor(arch)
		if got != want || ok != (want != "") {
			t.Errorf("GOARCHFor(%q) = %q, %v", arch, got, ok)
		}
	}
}

func TestReadArchIsOneKeyedRead(t *testing.T) {
	t.Parallel()
	r := &answeringRunner{answers: [][2]string{{"architecture-name", "x86_64"}}}
	arch, err := ReadArch(r)
	if err != nil || arch != "x86_64" || len(r.ran) != 1 || strings.Count(r.ran[0], "\n") != 1 || !strings.HasSuffix(r.ran[0], "\n"+endLine) {
		t.Errorf("ReadArch = %q, %v after %q", arch, err, r.ran)
	}
	r = &answeringRunner{answers: [][2]string{{"architecture-name", "!bad command name"}}}
	if _, err = ReadArch(r); err == nil || !strings.Contains(err.Error(), "bad command name") {
		t.Errorf("an unanswered read: %v", err)
	}
}

// upgrade's one connect: whether the install is there, the router's
// architecture, and the credential check for a pull.
func TestUpgradeReadAsksEverythingInOneConnect(t *testing.T) {
	t.Parallel()
	o := defaults(t, func(o *Options) { o.RemoteImage = "jmrplens/mikroscope-agent:1.3.1" })
	r := &upgradeRouter{registryURL: "https://docker.1ms.run", userSet: "true"}
	var out bytes.Buffer
	st, err := UpgradeRead(archAnswering{r, "x86_64"}, o, &out)
	if err != nil || !st.Installed || st.Arch != "x86_64" {
		t.Fatalf("UpgradeRead = %+v, %v", st, err)
	}
	if len(r.ran) != 1 || !strings.Contains(out.String(), "WARN") || !strings.Contains(out.String(), "note") {
		t.Errorf("one connect and the credential check: %d connects\n%s", len(r.ran), out.String())
	}
	r = &upgradeRouter{absent: true}
	out.Reset()
	if st, err = UpgradeRead(archAnswering{r, "arm64"}, o, &out); err != nil || st.Installed || st.Arch != "arm64" || out.Len() != 0 {
		t.Errorf("nothing installed: %+v, %v, printed %q", st, err, out.String())
	}
	// A plan step the router did not answer is an error, not "installed".
	if _, err = UpgradeRead(runFunc(func(string) (string, error) { return "syntax error\n", nil }), o, &out); err == nil {
		t.Error("an unanswered read passed")
	}
}

// archAnswering answers the architecture read and hands every other line to
// the router it wraps.
type archAnswering struct {
	*upgradeRouter
	arch string
}

func (a archAnswering) Run(command string) (string, error) {
	var lines, rest []string
	for q := range strings.SplitSeq(command, "\n") {
		if strings.Contains(q, "architecture-name") {
			lines = append(lines, answerLine(q, a.arch))
			continue
		}
		rest = append(rest, q)
	}
	out, err := a.upgradeRouter.Run(strings.Join(rest, "\n"))
	return strings.Join(lines, "\n") + "\n" + out, err
}

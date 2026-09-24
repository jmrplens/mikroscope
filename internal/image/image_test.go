package image

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

// TestTarShape pins the docker-save layout RouterOS accepts (a
// `podman save --format docker-archive` tar was accepted by the reference
// RB5009 on RouterOS 7.24.2, 2026-09-11): a manifest naming a config and one
// layer, the layer carrying /mikroscope-agent, and the config declaring the
// architecture. Built twice from the same binary it must
// be byte-identical — determinism is what lets an operator diff two installs.
func TestTarShape(t *testing.T) {
	binary := []byte("fake-elf")
	img1, err := Tar(binary, "arm64", "")
	if err != nil {
		t.Fatal(err)
	}
	img2, _ := Tar(binary, "arm64", "")
	if !bytes.Equal(img1, img2) {
		t.Fatal("image tar is not deterministic")
	}

	files := map[string][]byte{}
	tr := tar.NewReader(bytes.NewReader(img1))
	for {
		hdr, nextErr := tr.Next()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			t.Fatal(nextErr)
		}
		data, _ := io.ReadAll(tr)
		files[hdr.Name] = data
	}
	manifestRaw, ok := files["manifest.json"]
	if !ok {
		t.Fatal("no manifest.json")
	}
	var manifest []struct {
		Config   string
		RepoTags []string
		Layers   []string
	}
	if unmarshalErr := json.Unmarshal(manifestRaw, &manifest); unmarshalErr != nil {
		t.Fatal(unmarshalErr)
	}
	if len(manifest) != 1 || len(manifest[0].Layers) != 1 || manifest[0].RepoTags[0] != RepoTag {
		t.Fatalf("manifest shape: %+v", manifest)
	}
	var config struct {
		Architecture string `json:"architecture"`
		Config       struct {
			Entrypoint []string
		} `json:"config"`
	}
	if cfgErr := json.Unmarshal(files[manifest[0].Config], &config); cfgErr != nil {
		t.Fatal(cfgErr)
	}
	if config.Architecture != "arm64" || len(config.Config.Entrypoint) != 1 || config.Config.Entrypoint[0] != "/"+BinaryName {
		t.Fatalf("config: %+v", config)
	}
	layer := tar.NewReader(bytes.NewReader(files[manifest[0].Layers[0]]))
	lhdr, layerErr := layer.Next()
	if layerErr != nil || lhdr.Name != BinaryName || lhdr.Mode != 0o755 {
		t.Fatalf("layer content: %v %v", lhdr, layerErr)
	}
}

// TestArmVariant pins that a 32-bit ARM image declares the ARM level it was
// built for, and that no other architecture declares one at all.
//
// The level matters on RouterOS: MikroTik's container documentation states
// that the package exists for arm, arm64 and x86 only, and that "for devices
// with EN7562CT CPU like the hEX Refresh, only arm32v5 container images are
// supported".
// An OCI consumer matches `linux/arm` on this variant, so an image that says
// v7 is not one of those boards will run. Read from MikroTik's documentation,
// not measured: this project has run on one device, the reference RB5009,
// which is arm64.
func TestArmVariant(t *testing.T) {
	t.Parallel()
	for goarm, want := range map[string]string{"5": `"variant":"v5"`, "7": `"variant":"v7"`, "": `"variant":"v7"`} {
		img, err := Tar([]byte("x"), "arm", goarm)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(img, []byte(want)) {
			t.Errorf("arm image built with GOARM=%q does not declare %s", goarm, want)
		}
	}
	if img64, _ := Tar([]byte("x"), "arm64", ""); bytes.Contains(img64, []byte(`"variant"`)) {
		t.Fatal("arm64 image declares a variant")
	}
}

// TestVariantNote pins which image gets a word and which does not: only the
// 32-bit ARMv7 one, because it is the only image a reader can download that
// will not run on a board MikroTik ships.
func TestVariantNote(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		info Info
		want bool
	}{
		{Info{Arch: "arm", Variant: "v7"}, true},
		{Info{Arch: "arm", Variant: "v5"}, false},
		{Info{Arch: "arm64"}, false},
		{Info{Arch: "amd64"}, false},
	} {
		note := VariantNote(tc.info)
		if (note != "") != tc.want {
			t.Errorf("VariantNote(%+v) = %q, wanted a note: %v", tc.info, note, tc.want)
		}
		if tc.want && !strings.Contains(note, "armv5") {
			t.Errorf("the note does not name the image to use instead: %q", note)
		}
	}
}

// TestArmVariantString pins the mapping GOARM → OCI variant, including the
// empty string, which is what the toolchain's own default means here.
func TestArmVariantString(t *testing.T) {
	t.Parallel()
	for goarm, want := range map[string]string{"": "v7", "5": "v5", "6": "v6", "7": "v7"} {
		if got := ArmVariant(goarm); got != want {
			t.Errorf("ArmVariant(%q) = %q, want %q", goarm, got, want)
		}
	}
}

// TestStampLdflags pins what reaches the linker. A release tar's agent has to
// report the release, so all three variables are stamped when known; an empty
// one is left out, because `-X pkg.Commit=` would stamp an empty string over
// the VCS fallback internal/version resolves for itself.
func TestStampLdflags(t *testing.T) {
	const pkg = "github.com/jmrplens/mikroscope/internal/version."
	full := Stamp{Version: "0.1.0", Commit: "abc1234", BuildDate: "2026-09-15T10:00:00Z"}.ldflags()
	if want := "-s -w -X " + pkg + "Version=0.1.0 -X " + pkg + "Commit=abc1234 -X " + pkg + "BuildDate=2026-09-15T10:00:00Z"; full != want {
		t.Errorf("full stamp:\n got %q\nwant %q", full, want)
	}
	if got, want := (Stamp{Version: "0.1.0"}).ldflags(), "-s -w -X "+pkg+"Version=0.1.0"; got != want {
		t.Errorf("version only: got %q, want %q", got, want)
	}
	if got := (Stamp{}).ldflags(); got != "-s -w" {
		t.Errorf("empty stamp: got %q, want %q", got, "-s -w")
	}
}

// TestInspectReadsBackWhatTarWrote closes the loop --agent-tar depends on: a
// tar this package produced must be recognized as this agent, for the
// architecture it was built for, and anything else must be refused before it
// reaches a router.
func TestInspectReadsBackWhatTarWrote(t *testing.T) {
	t.Parallel()
	for _, arch := range []string{"arm64", "arm", "amd64"} {
		data, err := Tar([]byte("ELF-ish agent bytes"), arch, "")
		if err != nil {
			t.Fatal(err)
		}
		info, err := Inspect(data)
		if err != nil {
			t.Fatalf("%s: %v", arch, err)
		}
		wantVariant := ""
		if arch == "arm" {
			wantVariant = "v7"
		}
		if info.Arch != arch || info.Variant != wantVariant || info.Size != len("ELF-ish agent bytes") {
			t.Errorf("%s: %+v", arch, info)
		}
	}
}

// TestInspectRefusesWhatIsNotThisImage: the point of the check is that the
// operator learns here, not from `exec format error` in the container log.
func TestInspectRefusesWhatIsNotThisImage(t *testing.T) {
	t.Parallel()
	if _, err := Inspect([]byte("not a tar at all")); err == nil {
		t.Error("random bytes were accepted as an image")
	}
	empty, tarErr := tarOf(map[string][]byte{"hello.txt": []byte("hi")})
	if tarErr != nil {
		t.Fatal(tarErr)
	}
	if _, err := Inspect(empty); err == nil {
		t.Error("a tar with no manifest was accepted as an image")
	}
	// A well-formed docker-save tar of somebody else's image: right shape,
	// wrong entrypoint.
	foreign, foreignErr := tarOf(map[string][]byte{
		"manifest.json": []byte(`[{"Config":"c.json","Layers":["l/layer.tar"]}]`),
		"c.json":        []byte(`{"architecture":"arm64","config":{"Entrypoint":["/bin/sh"]}}`),
		"l/layer.tar":   []byte("x"),
	})
	if foreignErr != nil {
		t.Fatal(foreignErr)
	}
	if _, err := Inspect(foreign); err == nil {
		t.Error("an image whose entrypoint is not the agent was accepted")
	}
}

// tarOf builds a tar with the given entries, for the negative cases above.
func tarOf(files map[string][]byte) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))}); err != nil {
			return nil, err
		}
		if _, err := tw.Write(body); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), tw.Close()
}

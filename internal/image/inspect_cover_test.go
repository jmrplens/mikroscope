package image

import (
	"testing"
)

// Inspect is what stands between a downloaded tar and an install. Every
// refusal below is a way an install would otherwise fail on the router
// instead of here — at best with `exec format error` in the container log,
// which is a slow way to learn the file was wrong, and at worst with a
// container that starts and reports nothing.
func TestInspectRefusesEveryShapeOfWrongTar(t *testing.T) {
	t.Parallel()
	good, err := Tar([]byte("#!/bin/true\n"), "arm64", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Inspect(good); err != nil {
		t.Fatalf("a tar this package just built was refused: %v", err)
	}

	mustTar := func(files map[string][]byte) []byte {
		t.Helper()
		b, tarErr := tarOf(files)
		if tarErr != nil {
			t.Fatal(tarErr)
		}
		return b
	}

	for name, tc := range map[string]struct {
		body []byte
		says string
	}{
		"not a tar at all": {[]byte("hello"), ""},
		"a truncated tar":  {good[:len(good)/2], ""},
		"a tar of nothing": {mustTar(nil), "manifest.json"},
		"a tar with no manifest": {
			mustTar(map[string][]byte{"hello.txt": []byte("hi")}), "manifest.json",
		},
		"a manifest that is not JSON": {
			mustTar(map[string][]byte{"manifest.json": []byte("{{{")}), "not one image",
		},
		"a manifest that is an empty list": {
			mustTar(map[string][]byte{"manifest.json": []byte(`[]`)}), "not one image",
		},
		"a manifest naming a config the tar does not carry": {
			mustTar(map[string][]byte{"manifest.json": []byte(`[{"Config":"absent.json","Layers":["l.tar"]}]`)}),
			"does not carry",
		},
		"a config with the wrong entrypoint": {
			mustTar(map[string][]byte{
				"manifest.json": []byte(`[{"Config":"c.json","Layers":["l.tar"]}]`),
				"c.json":        []byte(`{"architecture":"arm64","config":{"Entrypoint":["/bin/sh"]}}`),
			}), "not a mikroscope agent image",
		},
		"a config that is not JSON": {
			mustTar(map[string][]byte{
				"manifest.json": []byte(`[{"Config":"c.json","Layers":["l.tar"]}]`),
				"c.json":        []byte("{{{"),
			}), "image config",
		},
		"more than one layer": {
			mustTar(map[string][]byte{
				"manifest.json": []byte(`[{"Config":"c.json","Layers":["a.tar","b.tar"]}]`),
				"c.json":        agentConfig,
			}), "want 1",
		},
		"a manifest naming a layer the tar does not carry": {
			mustTar(map[string][]byte{
				"manifest.json": []byte(`[{"Config":"c.json","Layers":["absent.tar"]}]`),
				"c.json":        agentConfig,
			}), "does not carry",
		},
		"a layer that is not a tar": {
			mustTar(map[string][]byte{
				"manifest.json": []byte(`[{"Config":"c.json","Layers":["l.tar"]}]`),
				"c.json":        agentConfig,
				"l.tar":         []byte("not a tar"),
			}), "layer",
		},
		"a layer carrying something else": {
			mustTar(map[string][]byte{
				"manifest.json": []byte(`[{"Config":"c.json","Layers":["l.tar"]}]`),
				"c.json":        agentConfig,
				"l.tar":         mustTar(map[string][]byte{"somethingelse": []byte("x")}),
			}), BinaryName,
		},
	} {
		_, err = Inspect(tc.body)
		if err == nil {
			t.Errorf("%s was accepted as an agent image", name)
			continue
		}
		if tc.says != "" && !containsFold(err.Error(), tc.says) {
			t.Errorf("%s: error = %q, want it to mention %q", name, err, tc.says)
		}
	}
}

// agentConfig is the image config an agent image carries: the entrypoint is
// checked before the layers are, so a config without it never reaches the
// cases below.
var agentConfig = []byte(`{"architecture":"arm64","config":{"Entrypoint":["/` + BinaryName + `"]}}`)

func containsFold(hay, needle string) bool {
	return len(needle) == 0 || len(hay) >= len(needle) && indexFold(hay, needle) >= 0
}

func indexFold(hay, needle string) int {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if equalFold(hay[i:i+len(needle)], needle) {
			return i
		}
	}
	return -1
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range len(a) {
		x, y := a[i], b[i]
		if 'A' <= x && x <= 'Z' {
			x += 'a' - 'A'
		}
		if 'A' <= y && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}

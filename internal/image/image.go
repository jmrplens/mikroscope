// Package image builds the agent's container image without Docker: a
// cross-compiled static binary packed into a docker-save-format tar that
// RouterOS's `/container/add file=` accepts. Lineage: cs-routeros-bouncer
// cmd/perfmon/image.go (MIT), reworked for the agent's name, the ARM variant
// and a build that can be reused by goreleaser.
package image

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	// BinaryName is the agent binary inside the image, at the root.
	BinaryName = "mikroscope-agent"
	// RepoTag is the name the image carries in its manifest; RouterOS shows
	// it as the container's `tag`.
	RepoTag = "mikroscope/agent:local"
)

// Stamp is the build identity BuildAgent writes into the agent through the
// linker, the same three internal/version variables a release stamps.
type Stamp struct {
	Version   string
	Commit    string
	BuildDate string
}

// ldflags renders the stamp as the -ldflags value. An empty field is left out
// rather than stamped empty, so the agent falls back to what internal/version
// resolves on its own instead of reporting nothing.
func (s Stamp) ldflags() string {
	const pkg = "github.com/jmrplens/mikroscope/internal/version."
	var flags strings.Builder
	flags.WriteString("-s -w")
	for _, kv := range [][2]string{{"Version", s.Version}, {"Commit", s.Commit}, {"BuildDate", s.BuildDate}} {
		if kv[1] != "" {
			flags.WriteString(" -X " + pkg + kv[0] + "=" + kv[1])
		}
	}
	return flags.String()
}

// BuildAgent cross-compiles cmd/mikroscope-agent for linux/goarch with the
// Go toolchain this tool runs with. CGO is off so the binary is static and a
// FROM-scratch image can carry it; -trimpath and -s -w keep it small and
// reproducible. goarm applies to arm only (7 for the hEX refresh line). The
// stamp is the calling CLI's own identity, so an agent installed by a release
// CLI reports the release it came with.
func BuildAgent(goarch, goarm string, stamp Stamp) ([]byte, error) {
	dir, err := os.MkdirTemp("", "mikroscope-agent-*")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()

	out := filepath.Join(dir, BinaryName)
	ldflags := stamp.ldflags()
	// #nosec G204 -- the arguments are fixed except the temp output path this
	// function just created; the developer's own go toolchain is the point.
	cmd := exec.CommandContext(context.Background(), "go", "build", "-trimpath", "-ldflags="+ldflags, "-o", out, "./cmd/mikroscope-agent")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+goarch)
	if goarch == "arm" && goarm != "" {
		cmd.Env = append(cmd.Env, "GOARM="+goarm)
	}
	if outBytes, buildErr := cmd.CombinedOutput(); buildErr != nil {
		return nil, fmt.Errorf("go build %s: %w\n%s", BinaryName, buildErr, outBytes)
	}
	return os.ReadFile(out) // #nosec G304 -- reading back the binary written two lines up
}

// Info is what Inspect could read back out of an image tar.
type Info struct {
	Arch    string // the config's `architecture`: arm64, arm, amd64
	Variant string // `variant` where the config carries one (v7 for arm)
	Size    int    // the agent binary's size in bytes
}

// Inspect reads a docker-save tar and reports what image it is, so a tar
// handed to `--agent-tar` is checked before it reaches the router rather than
// after: an amd64 image on an arm64 board installs, starts, and fails with
// `exec format error` in the container log, which is a slow way to learn that
// the wrong asset was downloaded.
//
// It reads the manifest, the config it names and the layer the config names,
// and it fails when any of the three is missing, when the layer holds
// something other than the agent binary, or when the entrypoint is not the
// agent — the three ways a tar can be a tar and still not be this image.
func Inspect(data []byte) (Info, error) {
	files, err := tarFiles(data)
	if err != nil {
		return Info{}, err
	}
	manifestRaw, ok := files["manifest.json"]
	if !ok {
		return Info{}, errors.New("not a docker-save image: no manifest.json")
	}
	var manifest []struct {
		Config string   `json:"Config"`
		Layers []string `json:"Layers"`
	}
	if jsonErr := json.Unmarshal(manifestRaw, &manifest); jsonErr != nil || len(manifest) != 1 {
		return Info{}, fmt.Errorf("manifest.json is not one image: %w", jsonErr)
	}
	configRaw, ok := files[manifest[0].Config]
	if !ok {
		return Info{}, fmt.Errorf("manifest names config %q, which the tar does not carry", manifest[0].Config)
	}
	var config struct {
		Architecture string `json:"architecture"`
		Variant      string `json:"variant"`
		Config       struct {
			Entrypoint []string `json:"Entrypoint"`
		} `json:"config"`
	}
	if jsonErr := json.Unmarshal(configRaw, &config); jsonErr != nil {
		return Info{}, fmt.Errorf("image config: %w", jsonErr)
	}
	if len(config.Config.Entrypoint) != 1 || config.Config.Entrypoint[0] != "/"+BinaryName {
		return Info{}, fmt.Errorf("entrypoint is %v, not /%s: this is not a mikroscope agent image", config.Config.Entrypoint, BinaryName)
	}
	if len(manifest[0].Layers) != 1 {
		return Info{}, fmt.Errorf("image has %d layers, want 1", len(manifest[0].Layers))
	}
	layer, ok := files[manifest[0].Layers[0]]
	if !ok {
		return Info{}, fmt.Errorf("manifest names layer %q, which the tar does not carry", manifest[0].Layers[0])
	}
	inner, err := tarFiles(layer)
	if err != nil {
		return Info{}, fmt.Errorf("layer: %w", err)
	}
	binary, ok := inner[BinaryName]
	if !ok {
		return Info{}, fmt.Errorf("the layer does not carry %s", BinaryName)
	}
	return Info{Arch: config.Architecture, Variant: config.Variant, Size: len(binary)}, nil
}

// tarFiles reads a tar into memory, keyed by entry name. The images this
// package handles are a few MiB, and the alternative — two passes over a
// stream — buys nothing at that size.
func tarFiles(data []byte) (map[string][]byte, error) {
	out := map[string][]byte{}
	tr := tar.NewReader(bytes.NewReader(data))
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("read tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", hdr.Name, err)
		}
		out[hdr.Name] = body
	}
}

// Tar packs the agent binary into a docker-save-format tar. Hand-crafted on
// purpose: needing a Docker daemon to install a monitoring probe would be the
// tool's heaviest dependency by far, and the format is three JSON files and
// one layer. Every timestamp is the epoch, so the same binary always yields
// the same bytes.
func Tar(binary []byte, arch string) ([]byte, error) {
	var layer bytes.Buffer
	lw := tar.NewWriter(&layer)
	if err := lw.WriteHeader(&tar.Header{
		Name: BinaryName, Mode: 0o755, Size: int64(len(binary)),
		ModTime: time.Unix(0, 0),
	}); err != nil {
		return nil, err
	}
	if _, err := lw.Write(binary); err != nil {
		return nil, err
	}
	if err := lw.Close(); err != nil {
		return nil, err
	}
	layerDigest := sha256.Sum256(layer.Bytes())
	layerID := hex.EncodeToString(layerDigest[:])

	configMap := map[string]any{
		"architecture": arch,
		"os":           "linux",
		"config":       map[string]any{"Entrypoint": []string{"/" + BinaryName}},
		"rootfs": map[string]any{
			"type":     "layers",
			"diff_ids": []string{"sha256:" + layerID},
		},
		"history": []map[string]any{{"created": "1970-01-01T00:00:00Z", "created_by": "mikroscope"}},
		"created": "1970-01-01T00:00:00Z",
	}
	if arch == "arm" {
		// 32-bit RouterOS on the hEX refresh line runs armv7 userland; the
		// variant is what an OCI consumer matches on for linux/arm.
		configMap["variant"] = "v7"
	}
	config, err := json.Marshal(configMap)
	if err != nil {
		return nil, err
	}
	configDigest := sha256.Sum256(config)
	configID := hex.EncodeToString(configDigest[:])

	manifest, err := json.Marshal([]map[string]any{{
		"Config":   configID + ".json",
		"RepoTags": []string{RepoTag},
		"Layers":   []string{layerID + "/layer.tar"},
	}})
	if err != nil {
		return nil, err
	}

	var out bytes.Buffer
	tw := tar.NewWriter(&out)
	files := []struct {
		name string
		data []byte
	}{
		{configID + ".json", config},
		{layerID + "/layer.tar", layer.Bytes()},
		{"manifest.json", manifest},
	}
	for _, f := range files {
		if hdrErr := tw.WriteHeader(&tar.Header{
			Name: f.name, Mode: 0o644, Size: int64(len(f.data)), ModTime: time.Unix(0, 0),
		}); hdrErr != nil {
			return nil, hdrErr
		}
		if _, writeErr := tw.Write(f.data); writeErr != nil {
			return nil, writeErr
		}
	}
	if closeErr := tw.Close(); closeErr != nil {
		return nil, closeErr
	}
	return out.Bytes(), nil
}

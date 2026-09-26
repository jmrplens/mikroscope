//go:build linux

package lab

import (
	"archive/zip"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Disk makes base.qcow2 and the container package. For CHR, base.qcow2 is
// MikroTik's raw image converted and grown to LAB_DISK_SIZE, and
// container-<v>.npk comes out of the extra-packages archive. For the ISO lab
// it is MikroTik's installer run onto an empty disk, with the container
// package picked in its menu.
func (l *Lab) Disk(ctx context.Context) error {
	if err := l.Fetch(ctx); err != nil {
		return err
	}
	if err := os.MkdirAll(l.cfg.VM, 0o750); err != nil {
		return fmt.Errorf("the disks' directory: %w", err)
	}
	if l.cfg.Kind == "iso" {
		return l.isoInstall(ctx)
	}
	if !exists(filepath.Join(l.cfg.VM, "base.qcow2")) {
		if err := l.convertBase(ctx); err != nil {
			return err
		}
	}
	return l.extractPackage()
}

// convertBase unpacks MikroTik's raw image into a directory of its own under
// the disk directory, and qemu-img, in the lab's image, converts it to
// base.qcow2 and grows it; the raw image goes when it is done.
func (l *Lab) convertBase(ctx context.Context) error {
	if err := l.ensureImage(ctx); err != nil {
		return err
	}
	l.sayf("converting %s to base.qcow2 (%s)", l.cfg.Downloads[0], l.cfg.DiskSize)
	tmp, err := os.MkdirTemp(l.cfg.VM, ".convert-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	img, err := ExtractOne(filepath.Join(l.cfg.DLDir, l.cfg.Downloads[0]), func(name string) bool { return strings.HasSuffix(name, ".img") }, tmp)
	if err != nil {
		return die("%s: %v", l.cfg.Downloads[0], err)
	}
	raw := filepath.Base(tmp) + "/" + filepath.Base(img)
	err = l.qemuImg(ctx, l.cfg.VMRel, "convert", "-O", "qcow2", raw, "base.qcow2")
	if err != nil {
		return err
	}
	err = l.qemuImg(ctx, l.cfg.VMRel, "resize", "-q", "base.qcow2", l.cfg.DiskSize)
	if err != nil {
		// A base.qcow2 of the image's own size would pass for a finished
		// one on the next run.
		_ = os.Remove(filepath.Join(l.cfg.VM, "base.qcow2"))
		return err
	}
	return readOnly(filepath.Join(l.cfg.VM, "base.qcow2"))
}

// extractPackage takes the container package out of the extra-packages
// archive, as container-<v>.npk whatever the archive calls it (the arm64
// one says container-<v>-arm64.npk).
func (l *Lab) extractPackage() error {
	npk := filepath.Join(l.cfg.VM, "container-"+l.cfg.ROS+".npk")
	if exists(npk) {
		return nil
	}
	pattern := "container-" + l.cfg.ROS + "*.npk"
	got, err := ExtractOne(filepath.Join(l.cfg.DLDir, l.cfg.Downloads[1]), func(name string) bool {
		ok, _ := path.Match(pattern, path.Base(name))
		return ok
	}, l.cfg.VM)
	if err != nil {
		return die("no container package in %s: %v", l.cfg.Downloads[1], err)
	}
	if got == npk {
		return nil
	}
	return os.Rename(got, npk)
}

// readOnly takes every write bit off a file: a disk the chain above it
// depends on must not change under it.
func readOnly(p string) error {
	st, err := os.Stat(p)
	if err != nil {
		return err
	}
	return os.Chmod(p, st.Mode().Perm()&^0o222)
}

// ExtractOne writes the first entry of a zip archive that match accepts,
// by its base name, into dir, and returns where it wrote it. unzip -j did
// this for lab.sh.
func ExtractOne(archive string, match func(string) bool, dir string) (string, error) {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return "", err
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || !match(f.Name) {
			continue
		}
		base := path.Base(f.Name)
		if base == "." || base == "/" || base == ".." || strings.ContainsAny(base, `/\`) {
			continue
		}
		dst := filepath.Join(dir, base)
		err = extract(f, dst)
		if err != nil {
			return "", err
		}
		return dst, nil
	}
	return "", errors.New("no matching file in the archive")
}

// maxEntry bounds what one archive entry may expand to: CHR's image is
// 128 MiB, and no RouterOS package is near this.
const maxEntry = 1 << 30

func extract(f *zip.File, dst string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644) // #nosec G302 G304 -- a base name, in the lab's cache
	if err != nil {
		return err
	}
	n, err := io.Copy(out, io.LimitReader(rc, maxEntry+1))
	if err == nil && n > maxEntry {
		err = fmt.Errorf("%s expands past %d bytes", f.Name, maxEntry)
	}
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	return err
}

// overlay makes name a copy-on-write layer over backing, both in the lab's
// disk directory, by a relative backing path so the chain reads the same
// inside the container and out.
func (l *Lab) overlay(ctx context.Context, backing, name string) error {
	// A run canceled before this point leaves the disk it would replace.
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(l.cfg.VM, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return l.qemuImg(ctx, l.cfg.VMRel, "create", "-q", "-f", "qcow2", "-b", backing, "-F", "qcow2", name)
}

// ─── The snapshot chain ────────────────────────────────────────────────────

// The disks are a qcow2 chain under .cache/vm/<id>-<version>/: base.qcow2 is
// MikroTik's image converted (or the ISO's install), clean.qcow2 the
// provisioned snapshot over it, and run.qcow2 the live layer over that. A
// reset replaces run.qcow2 with an empty layer over clean.qcow2.

// Backing is the backing file a qcow2 image names in its header, empty for
// an image with none. It reads the header only (QEMU's docs/interop/qcow2):
// the magic "QFI\xfb", the version, then the backing file's offset (u64) and
// length (u32), big-endian.
func Backing(p string) (string, error) {
	f, err := os.Open(p) // #nosec G304 -- a disk under the lab's cache
	if err != nil {
		return "", err
	}
	defer f.Close()
	var hdr [20]byte
	if _, err = io.ReadFull(f, hdr[:]); err != nil {
		return "", fmt.Errorf("%s: not a qcow2 image: %w", filepath.Base(p), err)
	}
	if string(hdr[:4]) != "QFI\xfb" {
		return "", fmt.Errorf("%s: not a qcow2 image", filepath.Base(p))
	}
	if v := binary.BigEndian.Uint32(hdr[4:8]); v != 2 && v != 3 {
		return "", fmt.Errorf("%s: qcow2 version %d", filepath.Base(p), v)
	}
	off := binary.BigEndian.Uint64(hdr[8:16])
	size := binary.BigEndian.Uint32(hdr[16:20])
	if off == 0 {
		return "", nil
	}
	if size == 0 || size > 1023 || off > 1<<20 {
		return "", fmt.Errorf("%s: a backing file name of %d bytes at %d", filepath.Base(p), size, off)
	}
	name := make([]byte, size)
	if _, err = f.ReadAt(name, int64(off)); err != nil { // #nosec G115 -- checked above
		return "", fmt.Errorf("%s: %w", filepath.Base(p), err)
	}
	return string(name), nil
}

// CheckChain verifies that top leads down to base.qcow2 through the chain
// the lab makes (run.qcow2 over clean.qcow2 over base.qcow2), every link a
// file of dir named by a relative path, and base.qcow2 over nothing.
func CheckChain(dir, top string) error {
	want := map[string]string{"run.qcow2": "clean.qcow2", "clean.qcow2": "base.qcow2", "provision.qcow2": "base.qcow2", "base.qcow2": ""}
	for name, seen := top, 0; ; seen++ {
		expected, known := want[name]
		if !known || seen > 3 {
			return fmt.Errorf("%s is not a disk of the lab's chain", name)
		}
		back, err := Backing(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		if back != expected {
			if expected == "" {
				return fmt.Errorf("%s has a backing file (%s) and should have none", name, back)
			}
			return fmt.Errorf("%s is a layer over %q, not over %s", name, back, expected)
		}
		if expected == "" {
			return nil
		}
		name = expected
	}
}

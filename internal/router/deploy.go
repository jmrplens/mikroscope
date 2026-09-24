package router

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

// Listing prints every write install would perform, in order, with the
// exact RouterOS command — the `--dry-run` output and the text the operator
// confirms before a real install. imageSize is the tar the upload step will
// send. The token never appears: the envlist line is printed with it masked.
func Listing(o Options, imageSize int, w io.Writer) {
	fmt.Fprintf(w, "mikroscope install plan for %s\n", o.Name)
	fmt.Fprintf(w, "  options: %s\n", o.String())
	fmt.Fprintf(w, "  tag:     %q on every object; removals select by tag + identity\n", o.Tag())
	for i, s := range Plan(o) {
		if strings.HasPrefix(s.Name, "container ") {
			switch {
			case o.UsesRemoteImage():
				fmt.Fprintf(w, "  %2d. the router pulls %s (nothing is uploaded)\n", i+1, o.RemoteRef())
			default:
				fmt.Fprintf(w, "  %2d. upload %s (%d KiB) with scp\n", i+1, o.ImageFile(), imageSize/1024)
			}
		}
		fmt.Fprintf(w, "  %2d. %s\n      %s\n", i+1, s.Name, mask(s.Create, o.Token))
	}
	fmt.Fprintln(w, "nothing above has been written yet")
}

// UpgradeListing prints the writes `upgrade` performs, and only those: it
// replaces the container and leaves the veth, the router address and the two
// list memberships alone. Install's Listing is the wrong text here — it names
// four objects upgrade will not touch, which invites an operator to expect
// writes that never come.
//
// It exists because until 1.1.0 `upgrade --dry-run` printed NOTHING and then
// asked for confirmation: the flag documented as "print the plan and write
// nothing" did neither half, so `upgrade --dry-run --yes` replaced the
// container on a live router while promising it would not.
func UpgradeListing(o Options, imageSize int, w io.Writer) {
	plan := Plan(o)
	c := plan[len(plan)-1]
	fmt.Fprintf(w, "mikroscope upgrade plan for %s\n", o.Name)
	fmt.Fprintf(w, "  options: %s\n", o.String())
	fmt.Fprintf(w, "  keeps:   the veth, the router address and the list memberships are not touched\n")
	fmt.Fprintf(w, "   1. remove %s\n      %s\n", c.Name, c.Remove)
	if o.UsesRemoteImage() {
		fmt.Fprintf(w, "   2. the router pulls %s (nothing is uploaded)\n", o.RemoteRef())
	} else {
		fmt.Fprintf(w, "   2. upload %s (%d KiB) with scp\n", o.ImageFile(), imageSize/1024)
	}
	fmt.Fprintf(w, "   3. %s\n      %s\n", c.Name, mask(c.Create, o.Token))
	fmt.Fprintln(w, "nothing above has been written yet")
}

// Script renders the install as a RouterOS script the operator runs ON the
// router — the path that needs neither this CLI nor ssh from another machine:
// paste it into a terminal, or upload it and `/import`.
//
// It is the same Create commands Install runs, in the same order, so the two
// paths cannot drift. Two things differ, and both are stated in the script
// itself rather than left to be discovered:
//
//   - The image. A script on the router cannot upload a tar, so it needs
//     either --remote-image (the router pulls it) or a tar already on the
//     device under the name the container step expects. A pull names its
//     registry inside `remote-image=` (RemoteRef), so the script neither
//     reads nor writes /container/config.
//   - The token. The envlist line carries it in clear, because the router
//     needs it; anyone who can read the script can read the token.
func Script(o Options, w io.Writer) {
	fmt.Fprintf(w, "# mikroscope — install script for RouterOS. Container name: %s\n", o.Name)
	fmt.Fprintf(w, "# Every object it creates carries the comment %q, which is how\n", o.Tag())
	fmt.Fprintln(w, "# `mikroscope status` and `uninstall` recognize them later.")
	fmt.Fprintln(w, "#")
	switch {
	case o.UsesRemoteImage():
		fmt.Fprintf(w, "# The router pulls %s itself.\n", o.RemoteRef())
		fmt.Fprintln(w, "# The registry host is part of remote-image= (RouterOS 7.18 and later take it")
		fmt.Fprintln(w, "# there), so this script neither reads nor changes the device-wide registry-url.")
		fmt.Fprintf(w, "# A registry username set on the device for a registry other than %s\n", o.RegistryHost())
		fmt.Fprintln(w, "# can make the pull end in `auth error`; `mikroscope doctor` warns about it.")
	default:
		fmt.Fprintf(w, "# BEFORE RUNNING: put the agent image tar on the device as %s\n", o.ImageFile())
		fmt.Fprintln(w, "# (upload it over WinBox/WebFig Files, or /tool/fetch it), or regenerate this")
		fmt.Fprintln(w, "# script with --remote-image so the router pulls the image instead.")
	}
	if o.Token != "" {
		fmt.Fprintln(w, "#")
		fmt.Fprintln(w, "# The agent's bearer token is in clear below: treat this file as a credential.")
	}
	fmt.Fprintln(w, "")
	for _, s := range Plan(o) {
		fmt.Fprintf(w, "# %s\n%s\n\n", s.Name, s.Create)
	}
	fmt.Fprintf(w, "# When it is done: /container/print where name~\"%s\"\n", o.Name)
	fmt.Fprintf(w, "# The agent answers on http://%s:%d/healthz from the router's LAN.\n", o.ContainerIP, o.Port)
}

func mask(cmd, token string) string {
	if token == "" {
		return cmd
	}
	return strings.ReplaceAll(cmd, `value="`+token+`"`, `value="(token)"`)
}

// Install walks the plan, creating what is missing and refusing what is
// present but not ours. All the questions go out in one connect; only the
// writes take one each. The container step uploads the image first. It
// returns how many steps it created.
func Install(r Runner, o Options, image []byte, w io.Writer) (int, error) {
	plan := Plan(o)
	st, err := states(r, plan)
	if err != nil {
		return 0, err
	}
	created := 0
	for i, s := range plan {
		switch st[i] {
		case stateOwned:
			fmt.Fprintf(w, "  ok    %s (already present)\n", s.Name)
			continue
		case stateForeign:
			return created, fmt.Errorf("%s exists on the router and was not created by mikroscope (no ownership tag); "+
				"pick another --name/--veth/--subnet, or remove it by hand if it is yours", s.Name)
		case stateAbsent:
		}
		if createErr := createStep(r, o, s, image, w); createErr != nil {
			return created, createErr
		}
		fmt.Fprintf(w, "  new   %s\n", s.Name)
		created++
	}
	return created, nil
}

type state int

const (
	stateAbsent state = iota
	stateOwned
	stateForeign
)

// createStep uploads the image for the container step, runs Create and
// treats anything the router printed as a failure: a RouterOS write prints
// nothing on success and reports errors as text with exit status 0 over
// ssh, and the rest of a `;`-joined line does not run after the error.
func createStep(r Runner, o Options, s Step, image []byte, w io.Writer) error {
	isContainer := strings.HasPrefix(s.Name, "container ")
	uploads := isContainer && !o.UsesRemoteImage()
	if uploads {
		fmt.Fprintf(w, "  up    uploading %s (%d KiB)\n", o.ImageFile(), len(image)/1024)
		if upErr := r.Upload(image, o.ImageFile()); upErr != nil {
			return upErr
		}
	}
	if isContainer && o.UsesRemoteImage() {
		fmt.Fprintf(w, "  pull  the router pulls %s itself\n", o.RemoteRef())
	}
	out, err := r.Run(s.Create)
	if msg := strings.TrimSpace(out); err == nil && msg != "" {
		err = fmt.Errorf("router said %q", msg)
	}
	if err == nil {
		return nil
	}
	if uploads {
		// The tar went up before the marker was written, so it would
		// otherwise count as foreign forever; take it back.
		if _, rmErr := r.Run(`/file/remove [find name="` + o.ImageFile() + `"]`); rmErr == nil {
			fmt.Fprintf(w, "  undo  removed the uploaded %s\n", o.ImageFile())
		}
	}
	return fmt.Errorf("create %s: %w", s.Name, err)
}

// Uninstall removes everything install created, newest first, ignoring what
// is already gone — then asks the router, step by step, whether anything
// install created is still there, and returns an error naming what is. A
// removal that printed nothing is not evidence; the count is.
// RemovalListing prints what Uninstall would take off the router, in the order
// it would take it — the install plan backwards, because an object is removed
// after whatever depends on it.
func RemovalListing(o Options, w io.Writer) {
	plan := Plan(o)
	fmt.Fprintf(w, "  %d router object(s) tagged %q:\n", len(plan), o.Tag())
	for _, s := range slices.Backward(plan) {
		fmt.Fprintf(w, "    %s\n", s.Name)
	}
}

func Uninstall(r Runner, o Options, w io.Writer) error {
	plan := Plan(o)
	for _, s := range slices.Backward(plan) {
		out, err := r.Run(s.Remove)
		if err != nil {
			fmt.Fprintf(w, "  skip  %s (%s)\n", s.Name, firstLine(err))
			continue
		}
		if msg := strings.TrimSpace(out); msg != "" {
			fmt.Fprintf(w, "  skip  %s (router said %q)\n", s.Name, firstLine(errors.New(msg)))
			continue
		}
		fmt.Fprintf(w, "  gone  %s\n", s.Name)
	}
	if err := Verify(r, o, w); err != nil {
		return fmt.Errorf("uninstall left objects behind: %w", err)
	}
	return nil
}

// Verify asks Owned for every step in one connect and fails naming the ones
// still present. `status` uses it on its own; uninstall uses it as its last
// word. It prints one line per step with the count.
func Verify(r Runner, o Options, w io.Writer) error {
	plan := Plan(o)
	got, err := counts(r, plan)
	if err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	var left []string
	for i, s := range plan {
		fmt.Fprintf(w, "  %-5s %s\n", got[i], s.Name)
		if got[i] != "0" {
			left = append(left, s.Name)
		}
	}
	if len(left) > 0 {
		return fmt.Errorf("%d step(s) present: %s", len(left), strings.Join(left, "; "))
	}
	fmt.Fprintln(w, "verified: nothing mikroscope created remains on the router")
	return nil
}

// Installed reports whether every step is owned, in one connect.
func Installed(r Runner, o Options) (bool, error) {
	plan := Plan(o)
	got, err := counts(r, plan)
	if err != nil {
		return false, err
	}
	return !slices.Contains(got, "0"), nil
}

// UpgradePreflight is what upgrade asks the router before it writes anything,
// in one connect: whether every step of this install is owned — the question
// Installed answers — and, with --remote-image, the two /container/config
// answers doctor's credential check reads. upgrade runs no doctor, and it
// removes the old container before the router pulls the new image, so a pull
// that fails leaves the router without an agent. When the install is there,
// this prints that credential check and, when registry-url names a host
// other than the one the pull goes to, a note with the reference that keeps
// that host (registryURLNote), so the operator reads both before the
// confirmation. It prints nothing for a tar upgrade or when the install is
// not there, and writes nothing.
func UpgradePreflight(r Runner, o Options, w io.Writer) (bool, error) {
	plan := Plan(o)
	queries := make([]string, 0, len(plan)+2)
	for _, s := range plan {
		queries = append(queries, s.Owned)
	}
	if o.UsesRemoteImage() {
		queries = append(queries, registryURLQuery, registryUserQuery)
	}
	lines, err := batch(r, queries)
	if err != nil {
		return false, err
	}
	if slices.Contains(lines[:len(plan)], "0") {
		return false, nil
	}
	if !o.UsesRemoteImage() {
		return true, nil
	}
	registryURL, userSet := lines[len(plan)], isYes(lines[len(plan)+1])
	var rep Report
	addRegistryCredential(&rep, o, registryURL, userSet)
	for _, it := range rep.Items {
		it.print(w)
	}
	if note := registryURLNote(o, registryURL); note != "" {
		fmt.Fprintf(w, "  %-7s %s\n", "note", note)
	}
	return true, nil
}

func firstLine(err error) string {
	msg := err.Error()
	if before, _, ok := strings.Cut(msg, "\n"); ok {
		return before
	}
	return msg
}

func trimSpace(s string) string { return strings.TrimSpace(s) }

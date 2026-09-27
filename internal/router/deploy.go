package router

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/jmrplens/mikroscope/internal/version"
)

// Listing prints every write install would perform, in order, with the
// exact RouterOS command — the `--dry-run` output and the text the operator
// confirms before a real install. imageSize is the tar the upload step will
// send. The token never appears: the envlist line is printed with it masked.
// Each step has one number; where the image comes from is a line of the
// container step, because the upload, or the pull, is part of it.
func Listing(o Options, imageSize int, w io.Writer) {
	fmt.Fprintf(w, "mikroscope install plan for %s\n", o.Name)
	fmt.Fprintf(w, "  options: %s\n", o.String())
	fmt.Fprintf(w, "  tag:     %q on every object; removals select by tag + identity\n", o.Tag())
	for i, s := range Plan(o) {
		fmt.Fprintf(w, "  %2d. %s\n", i+1, s.Name)
		if s.id == "container" {
			imageLine(o, imageSize, w)
		}
		fmt.Fprintf(w, "      %s\n", mask(s.Create, o.Token))
	}
	fmt.Fprintln(w, "nothing above has been written yet")
}

// imageLine says where the container step's image comes from.
func imageLine(o Options, imageSize int, w io.Writer) {
	if o.UsesRemoteImage() {
		fmt.Fprintf(w, "      the router pulls %s (nothing is uploaded)\n", o.RemoteRef())
		return
	}
	fmt.Fprintf(w, "      upload %s (%d KiB) with scp\n", o.ImageFile(), imageSize/1024)
}

// UpgradeListing prints the writes `upgrade` performs, and only those: it
// rewrites the install manifest, replaces the container and leaves every
// other object of the plan alone, and says which. Install's Listing is the
// wrong text here — it names objects upgrade will not touch, which invites an
// operator to expect writes that never come.
//
// It exists because until 1.1.0 `upgrade --dry-run` printed NOTHING and then
// asked for confirmation: the flag documented as "print the plan and write
// nothing" did neither half, so `upgrade --dry-run --yes` replaced the
// container on a live router while promising it would not.
func UpgradeListing(o Options, imageSize int, w io.Writer) {
	plan := Plan(o)
	m, c := plan[0], plan[len(plan)-1]
	var kept []string
	for _, s := range plan[1 : len(plan)-1] {
		kept = append(kept, s.Name)
	}
	fmt.Fprintf(w, "mikroscope upgrade plan for %s\n", o.Name)
	fmt.Fprintf(w, "  options: %s\n", o.String())
	fmt.Fprintf(w, "  keeps:   %s: not touched\n", strings.Join(kept, ", "))
	fmt.Fprintf(w, "   1. %s\n      %s\n", m.Name, m.Create)
	fmt.Fprintf(w, "   2. remove %s\n      %s\n", c.Name, c.Remove)
	fmt.Fprintf(w, "   3. %s\n", c.Name)
	imageLine(o, imageSize, w)
	fmt.Fprintf(w, "      %s\n", mask(c.Create, o.Token))
	fmt.Fprintln(w, "nothing above has been written yet")
}

// Script renders the install as a RouterOS script the operator runs ON the
// router — the path that needs neither this CLI nor ssh from another machine:
// paste it into a terminal, or upload it and `/import`.
//
// It is the same Create commands Install runs, in the same order, inside one
// `{ … }` block behind guards that stop it before the first write on a
// router that cannot take it (renderScript, from the steps spec). Two things
// differ from Install, and both are stated in the script itself rather than
// left to be discovered:
//
//   - The image. A script on the router cannot upload a tar, so it needs
//     either --remote-image (the router pulls it) or a tar already on the
//     device under the name the container step expects, which a guard
//     checks. A pull names its registry inside `remote-image=` (RemoteRef),
//     so the script neither reads nor writes /container/config.
//   - The token. The envlist line carries it in clear, because the router
//     needs it; anyone who can read the script can read the token.
func Script(o Options, w io.Writer) {
	o.mustBeFinished()
	_, _ = io.WriteString(w, renderScript(&o, version.Version))
}

func mask(cmd, token string) string {
	if token == "" {
		return cmd
	}
	return strings.ReplaceAll(cmd, `value="`+token+`"`, `value="(token)"`)
}

// Install walks the plan, creating what is missing and refusing what is
// present but not ours — before anything is written. All the questions go
// out in one connect; only the writes take one each. The first write is the
// install manifest, and the container step uploads the image first. It
// returns how many steps it created.
func Install(r Runner, o Options, image []byte, w io.Writer) (int, error) {
	plan := Plan(o)
	st, err := states(r, plan)
	if err != nil {
		return 0, err
	}
	// Every collision is refused before the first write: the answers are
	// all in, so an install that cannot finish writes nothing, where it used
	// to create every step before the foreign one.
	for i, s := range plan {
		if st[i] == stateForeign {
			return 0, fmt.Errorf("%s exists on the router and was not created by mikroscope (no ownership tag); "+
				"pick another --name/--veth/--subnet, or remove it by hand if it is yours; nothing was written", s.Name)
		}
	}
	created := 0
	for i, s := range plan {
		if st[i] == stateOwned {
			fmt.Fprintf(w, "  ok    %s (already present)\n", s.Name)
			continue
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
// nothing on success and reports an error as text, and the rest of a
// `;`-joined line does not run after the error. Over ssh the session then
// exits 0 or 1 — about half each way over 20 runs in the virtual lab (CHR
// x86_64, RouterOS 7.24.4, 2026-09-26) — so the words, not the status, are
// what says a step failed.
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
		undoUpload(r, o, w)
	}
	return fmt.Errorf("create %s: %w", s.Name, err)
}

// undoUpload takes back a tar the container step uploaded and could not use:
// it went up before the marker was written, so it would otherwise count as
// foreign forever. It stays while a container of this install exists, which
// is when the step stopped at the extraction wait: RouterOS may still be
// extracting that tar, the step's own error says it stays, and the
// container step's removal takes it with the container.
func undoUpload(r Runner, o Options, w io.Writer) {
	out, err := r.Run(`:if ([:len [/container/find comment="` + o.Tag() + `"]] = 0) do={ /file/remove [find name="` + o.ImageFile() + `"]; :put "removed" } else={ :put "kept" }`)
	switch strings.TrimSpace(out) {
	case "removed":
		fmt.Fprintf(w, "  undo  removed the uploaded %s\n", o.ImageFile())
	case "kept":
		fmt.Fprintf(w, "  keep  %s: this install's container holds it; uninstall removes both\n", o.ImageFile())
	default:
		if err == nil {
			err = fmt.Errorf("router said %q", strings.TrimSpace(out))
		}
		fmt.Fprintf(w, "  skip  removing the uploaded %s (%s)\n", o.ImageFile(), firstLine(err))
	}
}

// RemovalListing prints what Uninstall would take off the router, in the order
// it would take it — the install plan backwards, because an object is removed
// after whatever depends on it, with the install manifest last. It reads
// nothing from the router: the manifest there, when there is one, decides
// the plan Uninstall removes.
func RemovalListing(o Options, w io.Writer) {
	plan := Plan(o)
	fmt.Fprintf(w, "  %d router object(s) tagged %q:\n", len(plan)-1, o.Tag())
	for _, s := range slices.Backward(plan[1:]) {
		fmt.Fprintf(w, "    %s\n", s.Name)
	}
	fmt.Fprintf(w, "  then any other object tagged %q, in %s\n", o.Tag(), strings.Join(sweepMenus, ", "))
	fmt.Fprintf(w, "  and last the %s with %s, and %s when nothing else is in it\n", plan[0].Name, o.RootDir(), manifestDir(&o))
	fmt.Fprintf(w, "  (the manifest on the router, when there is one, says which objects the plan holds)\n")
}

// Uninstall removes everything install created, and only that. It reads the
// install manifest first and removes the plan the manifest records — so an
// install made with --expose or other lists is removed whole by an uninstall
// given neither — or, without one, the plan the flags give. The steps go
// newest first, ignoring what is already gone; the container step takes the
// container root with the container. Then the tag sweep removes any other
// object that carries this install's exact tag, in its menus and in any
// other menu the manifest lists; then the manifest step removes the
// manifest, and the directory it shares with the root when nothing else is
// in it, and refuses while anything tagged remains, so a failed removal
// keeps the record for the next attempt. Last it asks the router whether
// anything is left — every step, the sweep, the paths and every path the
// manifest lists — and returns an error naming what is. A removal that
// printed nothing is not evidence; the count is.
func Uninstall(r Runner, o Options, w io.Writer) error {
	m, found, err := ReadManifest(r, o)
	return uninstallFrom(r, o, m, found, err, w)
}

// UninstallShape is Uninstall with the manifest the shape read already
// brought (ReadShape, whose one connect finds it with the objects), so it
// is not read a second time: one connect fewer. o is the options the shape
// resolved.
func UninstallShape(r Runner, o Options, s Shape, w io.Writer) error {
	m, found, err := s.manifestAt(ManifestFile(o))
	return uninstallFrom(r, o, m, found, err, w)
}

// uninstallFrom is Uninstall once the manifest has been read.
func uninstallFrom(r Runner, o Options, m Manifest, found bool, readErr error, w io.Writer) error {
	o, note, x := fromManifest(o, m, found, readErr)
	fmt.Fprintf(w, "  %s\n", note)
	plan := Plan(o)
	for _, s := range slices.Backward(plan[1:]) {
		removeStep(r, s, w)
	}
	sweep(r, o, x.menus, w)
	removeStep(r, plan[0], w)
	if verifyErr := verify(r, o, x, w); verifyErr != nil {
		return fmt.Errorf("uninstall left objects behind: %w", verifyErr)
	}
	return nil
}

// removeStep runs one step's Remove and says how it went.
func removeStep(r Runner, s Step, w io.Writer) {
	out, err := r.Run(s.Remove)
	if err != nil {
		fmt.Fprintf(w, "  skip  %s (%s)\n", s.Name, firstLine(err))
		return
	}
	if msg := strings.TrimSpace(out); msg != "" {
		fmt.Fprintf(w, "  skip  %s (router said %q)\n", s.Name, firstLine(errors.New(msg)))
		return
	}
	fmt.Fprintf(w, "  gone  %s\n", s.Name)
}

// sweepPrefix starts each line the sweep prints for what it removed.
const sweepPrefix = "@@swept="

// sweep removes, in one connect, every object in the sweep menus, and in
// the extra menus a manifest lists, that carries this install's exact tag:
// what an install with other options left, what a manifest from a newer
// mikroscope listed, what a 1.3.x install left that the flags given to
// uninstall do not describe. The tag is exact, never a pattern, and it is
// this install's alone, so nothing another install or the operator made is
// selected. Like a keyed batch it ends with a line that cannot fail, so a
// menu that failed (one this RouterOS does not have) costs its own line and
// not the ones after it.
func sweep(r Runner, o Options, extraMenus []string, w io.Writer) {
	v := values(&o, "")
	menus := append(slices.Clone(sweepMenus), extraMenus...)
	cmds := make([]string, 0, len(menus)+1)
	for _, m := range menus {
		cmds = append(cmds, sweepFor(sweepRemove, m, v))
	}
	out, err := r.Run(strings.Join(append(cmds, endLine), "\n"))
	if err != nil && !strings.Contains(out, keyPrefix+batchEnd+"=") {
		fmt.Fprintf(w, "  skip  tag sweep (%s)\n", firstLine(err))
		return
	}
	for line := range strings.SplitSeq(strings.ReplaceAll(out, "\r", ""), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, keyPrefix+batchEnd+"=") {
			continue
		}
		rest, ok := strings.CutPrefix(line, sweepPrefix)
		menu, n, found := strings.Cut(rest, " ")
		if !ok || !found {
			fmt.Fprintf(w, "  skip  tag sweep (router said %q)\n", line)
			continue
		}
		fmt.Fprintf(w, "  swept %s object(s) tagged %q in %s\n", n, o.Tag(), menu)
	}
}

// Verify asks the router whether anything the install created is there, and
// fails naming what is: the Owned count of every step of the plan — the one
// its manifest records, when the router has one that reads, else the flags'
// — then, in the sweep menus and any other menu the manifest lists, objects
// that carry the tag beyond what the plan's steps hold, every other path the
// manifest lists, and the container root or the directory when they are left
// with nothing in them. uninstall uses it as its last word. It prints one
// line per step with the count, and one per leftover.
//
// One connect asks for the manifest and for the counts the flags' plan
// needs. Only when the manifest records another plan does a second connect
// ask for that plan's counts.
func Verify(r Runner, o Options, w io.Writer) error {
	queries := append([]string{manifestQuery(o)}, verifyQueries(o, extra{})...)
	got, err := batch(r, queries)
	if err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	m, found, mErr := decodeManifest(got[0], ManifestFile(o))
	resolved, note, x := fromManifest(o, m, found, mErr)
	fmt.Fprintf(w, "  %s\n", note)
	if !slices.Equal(verifyQueries(resolved, x), queries[1:]) {
		return verify(r, resolved, x, w)
	}
	return verifyReport(resolved, x, got[1:], w)
}

// Status is `status`'s read of the router, in one connect when it can be:
// the install's shape (ReadShape, the manifest with it) and Verify's counts
// for the plan the options give. adopt turns a shape the router holds into
// the options status goes on with (the CLI fills the flags that were not
// given and refuses the ones that contradict it); only when that changes
// what the plan asks does a second connect ask again, as UpgradeRead does.
// It prints Verify's report and returns whether anything of the install is
// on the router; the error is a read that failed, or adopt's.
func Status(r Runner, o Options, adopt func(Shape) (Options, error), w io.Writer) (present bool, err error) {
	qs := shapeQueries(o)
	asked := verifyQueries(o, extra{})
	for i, q := range asked {
		qs = append(qs, query{key: "verify." + strconv.Itoa(i), text: q})
	}
	a, stray, err := readKeyed(r, qs)
	if err != nil {
		return false, fmt.Errorf("status: %w", err)
	}
	s := parseShape(a, o.Name)
	if adopt != nil && s.Found {
		if o, err = adopt(s); err != nil {
			return false, err
		}
	}
	m, found, mErr := s.manifestAt(ManifestFile(o))
	resolved, note, x := fromManifest(o, m, found, mErr)
	fmt.Fprintf(w, "  %s\n", note)
	var got []string
	if next := verifyQueries(resolved, x); slices.Equal(next, asked) {
		got = make([]string, len(asked))
		for i := range asked {
			v, ok := a["verify."+strconv.Itoa(i)]
			if !ok {
				return false, fmt.Errorf("status: the router gave no answer to %q; it printed %q", asked[i], strings.Join(stray, " / "))
			}
			got[i] = v
		}
	} else if got, err = batch(r, next); err != nil {
		return false, fmt.Errorf("status: %w", err)
	}
	return verifyReport(resolved, x, got, w) != nil, nil
}

// verify is Verify for options already resolved: one connect.
func verify(r Runner, o Options, x extra, w io.Writer) error {
	got, err := batch(r, verifyQueries(o, x))
	if err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	return verifyReport(o, x, got, w)
}

// verifyQueries are Verify's questions for o's plan, one answer each: every
// step's Owned, the tag count in each sweep menu and each extra menu, the
// two paths, and each extra path.
func verifyQueries(o Options, x extra) []string {
	plan := Plan(o)
	v := values(&o, "")
	queries := make([]string, 0, len(plan)+len(sweepMenus)+len(x.menus)+2+len(x.paths))
	for _, s := range plan {
		queries = append(queries, s.Owned)
	}
	for _, m := range append(slices.Clone(sweepMenus), x.menus...) {
		queries = append(queries, sweepFor(sweepCount, m, v))
	}
	queries = append(queries, substitute(rootDirLeftQuery, v), substitute(dirLeftQuery, v))
	for _, p := range x.paths {
		queries = append(queries, `:put [:len [/file/find name="`+p+`"]]`)
	}
	return queries
}

// verifyReport reads verifyQueries' answers. In each menu the plan's steps
// account for the objects their own counts found there, and no more: a
// tagged object beyond those is left over, whether or not a step of the plan
// writes to that menu.
func verifyReport(o Options, x extra, got []string, w io.Writer) error {
	plan := Plan(o)
	expected := map[string]int{}
	var left []string
	for i, s := range plan {
		if n, err := strconv.Atoi(got[i]); err == nil && s.menu != "" {
			expected[s.menu] += n
		}
		fmt.Fprintf(w, "  %-5s %s\n", got[i], s.Name)
		if got[i] != "0" {
			left = append(left, s.Name)
		}
	}
	at := len(plan)
	for _, m := range append(slices.Clone(sweepMenus), x.menus...) {
		n, convErr := strconv.Atoi(got[at])
		if convErr != nil || n > expected[m] {
			what := "object(s) in " + m + " tagged " + strconv.Quote(o.Tag()) + " that the plan does not select"
			fmt.Fprintf(w, "  %-5s %s\n", got[at], what)
			left = append(left, what)
		}
		at++
	}
	paths := []string{"container root " + o.RootDir() + " that no container holds", "empty directory " + manifestDir(&o)}
	for _, p := range x.paths {
		paths = append(paths, p+", which the manifest lists")
	}
	for _, what := range paths {
		if n := got[at]; n != "0" {
			fmt.Fprintf(w, "  %-5s %s\n", n, what)
			left = append(left, what)
		}
		at++
	}
	if len(left) > 0 {
		return fmt.Errorf("%d present: %s", len(left), strings.Join(left, "; "))
	}
	fmt.Fprintln(w, "verified: nothing mikroscope created remains on the router")
	return nil
}

// rootDirLeftQuery prints 1 when the container root is on /file and no
// container holds it: RouterOS deletes it with the container, and this is
// for when it did not. RouterOS stores root-dir with a leading slash the
// plan never wrote (/mikroscope/mikroscope, read in the virtual lab), so both
// spellings are asked.
const rootDirLeftQuery = `:if ([:len [/file/find name="{{rootDir}}"]] > 0 && ` + rootUnheld + `) do={ :put 1 } else={ :put 0 }`

// dirLeftQuery prints 1 when the directory the manifest and the container
// root share is on /file with nothing in it: what an uninstall that deleted
// everything else leaves. A directory that holds anything, another install's
// root or a file of the operator's, is not left over.
const dirLeftQuery = `:if ([:len [/file/find name="{{manifestDir}}" type="directory"]] > 0 && ` + dirEmpty + `) do={ :put 1 } else={ :put 0 }`

// Installed reports whether every object step is owned, in one connect. The
// manifest is not asked for: an install made before it existed has none and
// is installed all the same.
func Installed(r Runner, o Options) (bool, error) {
	plan := Plan(o)
	got, err := counts(r, plan[1:])
	if err != nil {
		return false, err
	}
	return !slices.Contains(got, "0"), nil
}

func firstLine(err error) string {
	msg := err.Error()
	if before, _, ok := strings.Cut(msg, "\n"); ok {
		return before
	}
	return msg
}

func trimSpace(s string) string { return strings.TrimSpace(s) }

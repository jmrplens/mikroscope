package router

import "github.com/jmrplens/mikroscope/internal/version"

// Step is one install action with three questions and two verbs. Install
// asks Owned first: ours already → skip. Then Check: exists but not ours →
// refuse, naming the object, because mikroscope does not build on objects it
// does not own and will not later remove. Neither → Create. Uninstall runs
// Remove in reverse order — the install manifest, which is the first step,
// last, after the tag sweep — then asks Owned for every step and refuses to
// report success while any count is non-zero.
//
// Owned selects only what install itself created, by the exact comment tag
// it wrote, and Remove uses that same selector — which is what keeps
// uninstall from touching a hand-made setup, or anything else that happens
// to share a substring with the container name. Check asks the broader
// question — does the EFFECT exist, however it got there — and its only job
// is to detect a collision with something that is not ours.
//
// Every find quotes address and port attributes: unquoted, RouterOS parses
// them as typed values and the comparison with the stored one comes back
// empty. Measured on the reference RB5009, 2026-09-11, for addresses on
// RouterOS 7.24.1 and for ports on 7.24.2: against a rule that carries
// `dst-port=9123`, `find chain=dstnat dst-port=9123 comment=…` matches 0
// and the same find with `dst-port="9123"` matches 1.
type Step struct {
	Name   string
	Check  string // RouterOS expression printing a count; "0" means the effect is absent
	Create string
	Owned  string // RouterOS expression printing how many objects install created still exist
	Remove string
	// Present, when set, answers install's "is our object there?" instead
	// of Owned: the container step owns derived resources (envlist, image)
	// that can outlive the container, and their presence alone must not
	// make install skip re-creating it; the manifest step is present only
	// when the file on the router already says what this plan would write.
	Present string

	id   string // the step spec's ID: manifest, veth, address, …, container
	menu string // the RouterOS menu of the tagged object it creates, "" for none
}

// MarkerName is the environment entry (RouterOS 7.24: `key=`) that signs the
// envlist. A /file entry carries no comment, and the envlist entries no
// install has ever written one on (RouterOS 7.24.4's /container/envs/add
// takes a comment, read in the virtual lab, CHR x86_64; installs made before
// that was read wrote none, and uninstall has to recognize them). So
// ownership of the two resources the container step derives — the envlist
// and the uploaded image — is established by this entry holding the exact
// tag, and the image is considered ours only while the marker exists. The
// agent ignores it.
const MarkerName = "MIKROSCOPE_TAG"

// Plan returns the install steps for o, in creation order: the steps spec
// (stepspec.go) rendered for these options. Every object carries o.Tag() in
// its comment, verbatim, and every Remove selects by that exact comment
// together with the identity its Check used. The first step is always the
// install manifest and the last the container.
func Plan(o Options) []Step {
	o.mustBeFinished()
	v := values(&o, version.Version)
	p := predicateValues(&o)
	steps := make([]Step, 0, len(stepSpecs))
	for _, s := range stepSpecs {
		if holds(s.When, p) {
			steps = append(steps, s.render(v, p))
		}
	}
	return steps
}

// The container step (containerStepSpec in stepspec.go) derives the envlist,
// the image file and the container. The image file is uploaded by install
// right before this step runs and removed by the step itself once the
// container is extracted; should that removal not happen, the file stays part
// of what uninstall owes the device. The marker is written first and removed
// last, so a removal that fails half-way leaves the envlist and the image
// counted as ours, and uninstall says so instead of reporting clean.
//
// Settings measured on the reference RB5009 (RouterOS 7.24.2, kernel 5.6.3
// arm64, 2026-09-11): a bounded on-failure restart so a broken image cannot
// loop at boot — with restart-max-count=3 and restart-interval=5s an
// entrypoint that exits 1 is retried at +5 s, +10 s and +15 s and then stops
// for good; memory-max, which RouterOS stores in bytes and installs as the
// cgroup `memory.max`; logging to the router log; and the default 10 s
// stop-time, which is a grace only for a process that has a SIGTERM handler —
// without one the log reads `signal 15 (Terminated) not caught, kill
// immediately` and the container exits on signal 9, so the agent catches
// SIGTERM.
//
// Where the image comes from: a tar this CLI uploaded, or a registry the
// router pulls from itself. `remote-image=` is the whole difference — with it
// there is no file to wait for, none to remove, and none for uninstall to
// account for. It carries the full reference, registry host included
// (RemoteRef), so the pull does not depend on the device-wide
// /container/config registry-url, which nothing here reads or writes.
//
// privileged=yes drops the container's user namespace, which is what makes
// /dev/kmsg, /proc/slabinfo and /proc/pagetypeinfo readable; it does not
// widen the network or PID namespace. Measured on the reference RB5009
// (RouterOS 7.24.2, 2026-09-12): uid_map becomes the identity map and /dev the
// host devtmpfs, while /proc/net/dev and /sys/class/net still show only lo and
// the veth and only our own PIDs are visible.
//
// ignore-remote-image-change=yes: with the default (no) RouterOS watches the
// image and, when the tar is removed, stops and removes the container and
// re-extracts it minutes later — measured on the reference RB5009 (RouterOS
// 7.24.2, 2026-09-11): 3 s after the tar was deleted the router stopped and
// removed the container on its own, and repulled and started it about 4 min
// later.
//
// The tar is deleted once the image is extracted, before start: a tar left on
// the device is what uninstall would later have to find in a /file index that
// lags (after a 30 min container was removed, `/file/find` did not list the
// tar at all and it reappeared minutes later; RB5009, 7.24.2, 2026-09-11).
// Extraction is over when the container carries the `stopped` flag, and the
// step waits for that, up to --extract-timeout (120 s by default), instead of
// guessing with a fixed delay. Measured in the virtual lab (CHR x86_64,
// RouterOS 7.24.4): a tar /container/add returned with the container already
// `stopped` (`extracting tar archived image` and `download/extract done` in
// the same second of the log), and a remote-image add carried the flag
// `downloading/extracting` for 2 s and then `stopped`. A container that is
// not `stopped` by the deadline stops the step with :error before the tar is
// deleted and before the start. The CLI's install reports the error and takes
// its upload back (createStep); a script run leaves the tar, which the
// marker makes uninstall's to remove.
//
// The stop is guarded, because stop errors on a container that is not
// running and RouterOS abandons the rest of a `;`-joined line at the first
// error; remove is not, so a failure there is reported. Between the two the
// removal waits, up to 30 s, while the container is `running` or `stopping`:
// a stop returns at once, the container stays `stopping` until the agent has
// exited, and /container/remove refuses it meanwhile with `failure: cannot
// remove running`. Measured in the virtual lab (CHR x86_64, RouterOS 7.24.4):
// with a client reading /stream the container was `stopping`, with `running`
// already clear, for 6 s, and a remove 0.3 s after the stop was refused so;
// without a client it was `stopped` within a second. Waiting on `running`
// alone would have gone straight to the refused remove. The agent's HTTP
// shutdown takes at most 5 s and RouterOS's stop-time is 10 s. The fixed
// `:delay 4s` this replaces lost the race on 5 of 15 first attempts on the
// lab's x86_64 router, and on every attempt while a client held /stream.
//
// /container/remove returns before the container is gone (RouterOS logs
// `removing files … remove done` seconds later) and a /file/remove of the
// image issued meanwhile did nothing, silently (RB5009 7.24.2, 2026-09-11) —
// so the removal waits for the container to vanish, retries the file removal
// for up to 15 s, and drops the marker only once the file is gone: if it is
// not, the marker stays, Owned keeps counting the file and the envlist, and
// uninstall says so.
//
// Check selects the container that belongs to THIS install by its veth, not
// by the image it was created from. A remote-image install used to be
// identified by its registry reference, which is the same string for every
// mikroscope install on earth: a second one on the same router — its own
// --name, --veth and --subnet, its own everything — found the first one and
// refused with "exists on the router and was not created by mikroscope (no
// ownership tag); pick another --name/--veth/--subnet", which is what had just
// been done. Measured on the reference RB5009 on 2026-09-21 while testing the
// GHCR route beside the running install. The veth is one-to-one with the
// install by construction: doctor checks the name is free or ours before
// anything is written, and RouterOS stores it verbatim, which root-dir is not
// — it comes back with a leading slash the plan never wrote.
//
// A tar install counts the tar in its Check and its Owned; a remote-image
// install has no file, so counting one would make every count short by one
// and read as a partial removal.
//
// --container-name writes `name=` (RouterOS 7.24.4 takes it on
// /container/add: its argument list, read in the virtual lab on CHR x86_64,
// 2026-09-26), and without it RouterOS names the container itself, as every
// install before the flag did. Check then also counts a container that
// already holds the name, so that install refuses to write a second
// container under a name something else is using, as it refuses a veth that
// is not its own. Whether RouterOS itself would refuse the duplicate was not
// measured.

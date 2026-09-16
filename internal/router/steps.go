package router

import "strconv"

// Step is one install action with three questions and two verbs. Install
// asks Owned first: ours already → skip. Then Check: exists but not ours →
// refuse, naming the object, because mikroscope does not build on objects it
// does not own and will not later remove. Neither → Create. Uninstall runs
// Remove in reverse order, then asks Owned for every step and refuses to
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
	// make install skip re-creating it.
	Present string
}

// MarkerName is the environment entry (RouterOS 7.24: `key=`) that signs the envlist. Neither
// /container/envs nor /file carries a comment, so ownership of the two
// resources the container step derives — the envlist and the uploaded image
// — is established by this entry holding the exact tag, and the image is
// considered ours only while the marker exists. The agent ignores it.
const MarkerName = "MIKROSCOPE_TAG"

// Plan returns the install steps for o, in creation order. Every object
// carries o.Tag() in its comment, verbatim, and every Remove selects by that
// exact comment together with the identity its Check used.
func Plan(o Options) []Step {
	o.mustBeFinished()
	tag := o.Tag()
	byTag := ` comment="` + tag + `"`
	port := strconv.Itoa(o.Port)
	steps := []Step{
		{
			Name:   "veth interface " + o.Veth,
			Check:  `:put [:len [/interface/veth/find name="` + o.Veth + `"]]`,
			Create: `/interface/veth/add name="` + o.Veth + `" address=` + o.ContainerIP + `/30 gateway=` + o.GatewayIP + byTag,
			Owned:  `:put [:len [/interface/veth/find name="` + o.Veth + `"` + byTag + `]]`,
			Remove: `/interface/veth/remove [find name="` + o.Veth + `"` + byTag + `]`,
		},
		{
			Name:   "router address " + o.GatewayIP,
			Check:  `:put [:len [/ip/address/find interface="` + o.Veth + `"]]`,
			Create: `/ip/address/add address=` + o.GatewayIP + `/30 interface="` + o.Veth + `"` + byTag,
			Owned:  `:put [:len [/ip/address/find interface="` + o.Veth + `"` + byTag + `]]`,
			Remove: `/ip/address/remove [find interface="` + o.Veth + `"` + byTag + `]`,
		},
		{
			// Without this, the defconf raw rule `drop the rest
			// (in-interface-list=!LAN)` silently eats every packet the
			// container sends: the veth has to be a member of the LAN
			// interface list for that rule not to match it (measured on
			// the reference RB5009, 2026-08-26, and still the shape of the
			// raw chain in the 2026-09-11 inventory).
			Name:   "interface-list membership " + o.IfaceList,
			Check:  `:put [:len [/interface/list/member/find interface="` + o.Veth + `" list="` + o.IfaceList + `"]]`,
			Create: `/interface/list/member/add list="` + o.IfaceList + `" interface="` + o.Veth + `"` + byTag,
			Owned:  `:put [:len [/interface/list/member/find interface="` + o.Veth + `" list="` + o.IfaceList + `"` + byTag + `]]`,
			Remove: `/interface/list/member/remove [find interface="` + o.Veth + `" list="` + o.IfaceList + `"` + byTag + `]`,
		},
		{
			// The sibling trap: the defconf raw rule `drop local if not
			// from default IP range` (in-interface-list=LAN,
			// src-address-list=!LANs) matches any source outside the LANs
			// address list, so the /30 has to join it (measured on the
			// reference RB5009, 2026-08-26).
			Name:   "address-list membership " + o.AddrList,
			Check:  `:put [:len [/ip/firewall/address-list/find list="` + o.AddrList + `" address="` + o.Subnet + `"]]`,
			Create: `/ip/firewall/address-list/add list="` + o.AddrList + `" address=` + o.Subnet + byTag,
			Owned:  `:put [:len [/ip/firewall/address-list/find list="` + o.AddrList + `" address="` + o.Subnet + `"` + byTag + `]]`,
			Remove: `/ip/firewall/address-list/remove [find list="` + o.AddrList + `" address="` + o.Subnet + `"` + byTag + `]`,
		},
	}
	if o.Expose {
		natSel := `chain=dstnat dst-address="` + o.LANAddress + `" dst-port="` + port + `" protocol=tcp`
		acceptSel := `chain=forward dst-address="` + o.ContainerIP + `" dst-port="` + port + `" protocol=tcp`
		acceptRule := `/ip/firewall/filter/add chain=forward dst-address=` + o.ContainerIP + ` protocol=tcp dst-port=` + port +
			` connection-nat-state=dstnat action=accept` + byTag
		steps = append(steps,
			Step{
				// Measured on the reference RB5009 (RouterOS 7.24.2,
				// 2026-09-11): with this pair in place a LAN host reaches
				// the agent through the router's own address — HTTP 200 in
				// 1.3 ms, body intact — and the token becomes mandatory
				// because the veth is no longer link-local only.
				Name:   "expose dst-nat " + o.LANAddress + ":" + port,
				Check:  `:put [:len [/ip/firewall/nat/find ` + natSel + `]]`,
				Create: `/ip/firewall/nat/add chain=dstnat dst-address=` + o.LANAddress + ` protocol=tcp dst-port=` + port + ` action=dst-nat to-addresses=` + o.ContainerIP + ` to-ports=` + port + byTag,
				Owned:  `:put [:len [/ip/firewall/nat/find ` + natSel + byTag + `]]`,
				Remove: `/ip/firewall/nat/remove [find ` + natSel + byTag + `]`,
			},
			Step{
				// The accept must land before the first forward drop; on a
				// router whose forward chain has no drop it is appended. The
				// :local shares the line with its use because over ssh every
				// line is its own console command.
				Name:  "expose forward accept",
				Check: `:put [:len [/ip/firewall/filter/find ` + acceptSel + `]]`,
				Create: `:local d [/ip/firewall/filter/find chain=forward action=drop]; ` +
					`:if ([:len $d] > 0) do={ ` + acceptRule + ` place-before=($d->0) } else={ ` + acceptRule + ` }`,
				Owned:  `:put [:len [/ip/firewall/filter/find ` + acceptSel + byTag + `]]`,
				Remove: `/ip/firewall/filter/remove [find ` + acceptSel + byTag + `]`,
			},
		)
	}
	return append(steps, containerStep(&o, tag, byTag))
}

// containerStep derives the envlist, the image file and the container. The
// image file is uploaded by install right before this step runs and removed
// by the step itself once the container is extracted; should that removal
// not happen, the file stays part of what uninstall owes the device. The
// marker is written first and removed last, so a removal that fails
// half-way leaves the envlist and the image counted as ours, and uninstall
// says so instead of reporting clean.
func containerStep(o *Options, tag, byTag string) Step {
	envList := o.EnvList()
	imageFile := o.ImageFile()
	marker := `[/container/envs/find list="` + envList + `" key="` + MarkerName + `" value="` + tag + `"]`
	env := func(name, value string) string {
		return `/container/envs/add list="` + envList + `" key=` + name + ` value="` + value + `"; `
	}
	// A previous install's envlist under our marker is ours to replace.
	create := `:if ([:len ` + marker + `] > 0) do={ /container/envs/remove [find list="` + envList + `"] }; ` +
		env(MarkerName, tag) +
		env("RATE_HZ", strconv.Itoa(o.RateHz)) +
		env("BUFFER_S", strconv.Itoa(o.BufferS)) +
		env("PORT", strconv.Itoa(o.Port)) +
		env("ADDR", o.ContainerIP) +
		env("MEM_LIMIT_MB", strconv.Itoa(o.MemLimitMB))
	if o.FloorHz > 0 {
		create += env("FLOOR_HZ", strconv.Itoa(o.FloorHz))
	}
	create += env("CAPTURE_MB", strconv.Itoa(o.CaptureMB))
	if o.Triggers != "" {
		create += env("TRIGGERS", o.Triggers)
	}
	if o.Token != "" {
		create += env("TOKEN", o.Token)
	}
	// Settings measured on the reference RB5009 (RouterOS 7.24.2, kernel
	// 5.6.3 arm64, 2026-09-11): a bounded on-failure restart so a broken
	// image cannot loop at boot — with restart-max-count=3 and
	// restart-interval=5s an entrypoint that exits 1 is retried at +5 s,
	// +10 s and +15 s and then stops for good; memory-max, which RouterOS
	// stores in bytes and installs as the cgroup `memory.max`; logging to
	// the router log; and the default 10 s stop-time, which is a grace
	// only for a process that has a SIGTERM handler — without one the log
	// reads `signal 15 (Terminated) not caught, kill immediately` and the
	// container exits on signal 9, so the agent catches SIGTERM.
	// Where the image comes from: a tar this CLI uploaded, or a registry the
	// router pulls from itself. `remote-image=` is the whole difference —
	// with it there is no file to wait for, none to remove, and none for
	// uninstall to account for.
	source := `file=` + imageFile
	if o.UsesRemoteImage() {
		source = `remote-image="` + o.RemoteRef() + `"`
	}
	create += `/container/add ` + source + ` interface="` + o.Veth + `" root-dir=` + o.RootDir() +
		` envlist="` + envList + `" logging=yes start-on-boot=` + o.StartOnBoot() +
		` restart-policy=on-failure restart-max-count=` + strconv.Itoa(o.RestartMaxCount) +
		` restart-interval=` + o.RestartInterval + ` memory-max=` + o.MemoryMax +
		` privileged=` + yesNo(o.Privileged) + ` ignore-remote-image-change=yes` + byTag + `; ` +
		// privileged=yes drops the container's user namespace, which is what
		// makes /dev/kmsg, /proc/slabinfo and /proc/pagetypeinfo readable; it
		// does not widen the network or PID namespace. Measured on the
		// reference RB5009 (RouterOS 7.24.2, 2026-09-12): uid_map becomes the
		// identity map and /dev the host devtmpfs, while /proc/net/dev and
		// /sys/class/net still show only lo and the veth and only our own
		// PIDs are visible.
		// ignore-remote-image-change=yes: with the default (no) RouterOS watches
		// the image and, when the tar below is removed, stops and removes the
		// container and re-extracts it minutes later — measured on the
		// reference RB5009 (RouterOS 7.24.2, 2026-09-11): 3 s after the tar
		// was deleted the router stopped and removed the container on its
		// own, and repulled and started it about 4 min later.
		// RouterOS extracts the image at add time (same device and date: a
		// 1.8 MiB tar goes from `extracting tar archived image` to
		// `download/extract done` within the same second), so once the
		// container exists the tar has no further use. A tar left on the
		// device is what uninstall would later have to find in a /file index
		// that lags: after a 30 min container was removed, `/file/find` did
		// not list the tar at all and it reappeared minutes later. So the tar
		// goes right here, before start.
		waitAndDropTar(o, imageFile) +
		`/container/start [find` + byTag + `]`
	return Step{
		Name:    "container " + o.Name,
		Present: `:put [:len [/container/find` + byTag + `]]`,
		Check: `:put ([:len [/container/find` + containerBySource(o, imageFile) + `]] + ` +
			`[:len [/container/envs/find list="` + envList + `"]]` + plusImageFile(o, imageFile) + `)`,
		Create: create,
		Owned: `:if ([:len ` + marker + `] > 0) do={ ` +
			`:put ([:len [/container/find` + byTag + `]] + ` +
			`[:len [/container/envs/find list="` + envList + `"]]` + plusImageFile(o, imageFile) + `) ` +
			`} else={ :put [:len [/container/find` + byTag + `]] }`,
		// stop errors on a container that is not running, and RouterOS
		// abandons the rest of a `;`-joined line at the first error, so the
		// stop is guarded; remove is not, so a failure there is reported.
		// /container/remove returns before the container is gone (RouterOS
		// logs `removing files … remove done` seconds later) and a
		// /file/remove of the image issued meanwhile did nothing, silently
		// (RB5009 7.24.2, 2026-09-11) — so wait for the container to vanish,
		// retry the file removal for up to 15 s, and drop the marker only
		// once the file is gone: if it is not, the marker stays, Owned keeps
		// counting the file and the envlist, and uninstall says so.
		Remove: `:do { /container/stop [find` + byTag + `]; :delay 4s } on-error={}; ` +
			`/container/remove [find` + byTag + `]; ` +
			`:local i 0; :while ([:len [/container/find` + byTag + `]] > 0 && $i < 20) do={ :delay 1s; :set i ($i + 1) }; ` +
			`:if ([:len ` + marker + `] > 0) do={ ` +
			removeTarThenEnvs(o, imageFile, envList, marker) + ` }`,
	}
}

// waitAndDropTar is the wait for RouterOS to finish extracting the image and
// the removal of the tar that follows it. With remote-image there is no tar:
// RouterOS pulled the layers itself, there is nothing on /file to wait for or
// to delete, and the step goes straight to start.
func waitAndDropTar(o *Options, imageFile string) string {
	if o.UsesRemoteImage() {
		return ""
	}
	return `:local w 0; :while ([:len [/container/find file="` + imageFile + `"]] = 0 && $w < 15) do={ :delay 1s; :set w ($w + 1) }; :delay 3s; ` +
		`/file/remove [find name="` + imageFile + `"]; `
}

// containerBySource selects the container by what it was created from, for
// the Check count: the tar's path, or the registry reference.
func containerBySource(o *Options, imageFile string) string {
	if o.UsesRemoteImage() {
		return ` remote-image="` + o.RemoteRef() + `"`
	}
	return ` file="` + imageFile + `"`
}

// plusImageFile adds the tar to an ownership count, for the install that
// uploaded one. A remote-image install has no file, so counting one would
// make every count short by one and read as a partial removal.
func plusImageFile(o *Options, imageFile string) string {
	if o.UsesRemoteImage() {
		return ""
	}
	return ` + [:len [/file/find name="` + imageFile + `"]]`
}

// removeTarThenEnvs drops the envlist once the tar is gone — or immediately,
// when there was no tar. The order matters either way: the marker is the last
// thing to go, so a removal that fails half-way still counts as ours.
func removeTarThenEnvs(o *Options, imageFile, envList, marker string) string {
	drop := `/container/envs/remove [find list="` + envList + `" key!="` + MarkerName + `"]; ` +
		`/container/envs/remove ` + marker
	if o.UsesRemoteImage() {
		return drop
	}
	return `:local j 0; :while ([:len [/file/find name="` + imageFile + `"]] > 0 && $j < 15) do={ /file/remove [find name="` + imageFile + `"]; :delay 1s; :set j ($j + 1) }; ` +
		`:if ([:len [/file/find name="` + imageFile + `"]] = 0) do={ ` + drop + ` }`
}

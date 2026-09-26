package router

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// The steps spec: every RouterOS command an install, an upgrade, an uninstall
// and a status send, and every line of the `plan --rsc` script, written once
// as data. Plan renders it for the CLI, Script renders it for the router's
// own terminal, and SpecJSON exports it for the site's script generator and
// the manual install pages, which render it with the same substitution in
// JavaScript. A command written here cannot drift between the four.
//
// Rendering is deliberately trivial: a fragment is kept when its predicate
// holds, and each {{key}} is replaced by its value. Nothing is escaped, because
// every value has passed its bound in Options.Finish (no quote, space or
// semicolon reaches a value, the tag aside, which only ever sits inside
// quotes).

// fragment is one piece of text: kept when When holds, with {{key}}
// placeholders. When is "" (always), a predicate name, or "!" and a name.
type fragment struct {
	When string `json:"when,omitempty"`
	Text string `json:"text"`
}

// stepSpec is one install step as data. The command fields concatenate their
// fragments into one RouterOS line; Manifest holds one manifest line per
// fragment. Menu is the RouterOS menu of the tagged object the step creates,
// which is what the tag sweep compares its counts with; the manifest step
// creates a file and has none.
type stepSpec struct {
	ID       string     `json:"id"`
	When     string     `json:"when,omitempty"`
	Menu     string     `json:"menu,omitempty"`
	Name     []fragment `json:"name"`
	Check    []fragment `json:"check"`
	Create   []fragment `json:"create"`
	Owned    []fragment `json:"owned"`
	Remove   []fragment `json:"remove"`
	Present  []fragment `json:"present,omitempty"`
	Manifest []fragment `json:"manifest"`
}

// t is a fragment that is always kept; w one kept when the predicate holds.
func t(text string) fragment       { return fragment{Text: text} }
func w(when, text string) fragment { return fragment{When: when, Text: text} }

// ts is a field made of one fragment.
func ts(text string) []fragment { return []fragment{t(text)} }

// The predicates a fragment or a step may name. Each is one line here and one
// line in the site's renderer.
var predicates = map[string]func(o *Options) bool{
	"expose":        func(o *Options) bool { return o.Expose },
	"remote":        func(o *Options) bool { return o.UsesRemoteImage() },
	"tar":           func(o *Options) bool { return !o.UsesRemoteImage() },
	"token":         func(o *Options) bool { return o.Token != "" },
	"triggers":      func(o *Options) bool { return o.Triggers != "" },
	"floorHz":       func(o *Options) bool { return o.FloorHz > 0 },
	"ifaceList":     func(o *Options) bool { return o.JoinsIfaceList() },
	"addrList":      func(o *Options) bool { return o.JoinsAddrList() },
	"containerName": func(o *Options) bool { return o.ContainerName != "" },
	"ephemeral":     func(o *Options) bool { return o.Ephemeral },
	"disk":          func(o *Options) bool { return o.Disk != "" },
}

// manifestDir is the directory the install's manifest and its container root
// share: `mikroscope` on the chosen disk, the parent of RootDir.
func manifestDir(o *Options) string { return o.onDisk("mikroscope") }

// ManifestFile is where the install manifest lives on the router, beside the
// container root. It is derived from --name and --disk, which uninstall takes
// too, so finding it needs nothing the operator does not already pass.
func ManifestFile(o Options) string { return o.onDisk("mikroscope/" + o.Name + ".manifest.txt") }

// manifestFormat is the manifest's first line. A reader refuses any other.
const manifestFormat = "mikroscope-manifest=1"

// derivedTemplates are the derived values that are a plain template of other
// values, exported so that the site computes them from the same text. The
// rest (the /30 ends, the memory limit, the registry reference, start-on-boot,
// the extraction timeout in seconds, the manifest) take arithmetic or parsing
// and are described in SpecJSON's derive section.
var derivedTemplates = []struct {
	Key  string     `json:"key"`
	Text []fragment `json:"text"`
}{
	{"tag", ts("mikroscope:{{name}} (managed by mikroscope)")},
	{"envList", ts("{{name}}-env")},
	{"imageFile", []fragment{w("disk", "{{disk}}/"), t("{{name}}.tar")}},
	{"rootDir", []fragment{w("disk", "{{disk}}/"), t("mikroscope/{{name}}")}},
	{"manifestDir", []fragment{w("disk", "{{disk}}/"), t("mikroscope")}},
	{"manifestFile", []fragment{w("disk", "{{disk}}/"), t("mikroscope/{{name}}.manifest.txt")}},
}

// markerFind selects the envlist's marker entry: the one entry that says the
// envlist, and the image beside it, are this install's (MarkerName).
const markerFind = `[/container/envs/find list="{{envList}}" key="` + MarkerName + `" value="{{tag}}"]`

// manifestIsOurs is the RouterOS condition that the file at manifestFile is
// this install's manifest: it holds this install's tag line. A file has no
// comment, so its content is what carries the ownership.
const manifestIsOurs = `[:typeof [:find [/file/get [find name="{{manifestFile}}"] contents] "\ntag={{tag}}\n"]] = "num"`

// manifestHeader is the start of the manifest: the format, the install's
// identity, and its shape — the options every object below was created from,
// under the CLI's flag names, so that uninstall, status and upgrade can
// rebuild the plan from the router instead of from flags. Nothing secret
// goes in it: a `read` user can read files. token= says only whether one is
// set.
var manifestHeader = []fragment{
	t(manifestFormat),
	t("name={{name}}"),
	t("tag={{tag}}"),
	t("disk={{disk}}"),
	t("veth={{veth}}"),
	t("subnet={{subnet}}"),
	t("port={{port}}"),
	t("iface-list={{ifaceList}}"),
	t("addr-list={{addrList}}"),
	w("expose", "expose={{lanAddress}}"),
	w("!expose", "expose="),
	t("container-name={{containerName}}"),
	w("remote", "remote-image={{remoteRef}}"),
	w("tar", "remote-image="),
	w("token", "token=yes"),
	w("!token", "token=no"),
}

// sweepMenus are the menus whose every entry carries a comment, in the order
// the sweep removes what it finds there: whatever refers to an interface or
// an address first, the veth after, lists and disks last. /container is
// counted and not swept: its removal needs the stop and the waits of the
// container step, whose selector is the tag alone anyway.
var sweepMenus = []string{
	"/ip/firewall/address-list", "/interface/list/member", "/ip/firewall/nat", "/ip/firewall/filter",
	"/ip/firewall/raw", "/ip/firewall/mangle", "/ip/route", "/container/mounts", "/ip/address",
	"/interface/veth", "/interface/list", "/disk",
}

// sweepCount and sweepRemove are the sweep's two commands for one menu, with
// {{menu}} standing for it. The removal prints how many it took, keyed, so
// that uninstall can say so.
const (
	sweepCount  = `:put [:len [{{menu}}/find comment="{{tag}}"]]`
	sweepRemove = `:local n [:len [{{menu}}/find comment="{{tag}}"]]; :if ($n > 0) do={ {{menu}}/remove [find comment="{{tag}}"]; :put ("@@swept={{menu}} " . $n) }`
)

// taggedCount is the RouterOS expression that counts every object the
// install's tag or marker still selects: the sweep menus, the container and
// the envlist marker. The manifest step's removal refuses while it is not 0.
func taggedCount() string {
	var b strings.Builder
	b.WriteString("(")
	for _, m := range append(slices.Clone(sweepMenus), "/container") {
		b.WriteString(`[:len [` + m + `/find comment="{{tag}}"]] + `)
	}
	b.WriteString(`[:len ` + markerFind + `])`)
	return b.String()
}

// rootUnheld is the RouterOS condition that no container holds the
// container root. RouterOS stores root-dir with a leading slash the plan
// never wrote (/mikroscope/mikroscope, read in the virtual lab), so both
// spellings are asked.
const rootUnheld = `([:len [/container/find root-dir="/{{rootDir}}"]] + [:len [/container/find root-dir="{{rootDir}}"]]) = 0`

// dirEmpty is the RouterOS condition that nothing is under the directory the
// manifest and the container root share. It asks /file for names under it by
// a pattern rather than walking every file on the router, which on a router
// with other containers is every file of their roots too; the disk name and
// `mikroscope` hold no pattern character (validDisk). Measured in the
// virtual lab (CHR x86_64, RouterOS 7.24.4, 2026-09-27): with
// mikroscope/a.txt, mikroscope/sub/b.txt and mikroscopex/c.txt on the
// router, `find name~"^mikroscope/"` counted 3, the directory sub among them,
// and not mikroscopex's file.
const dirEmpty = `[:len [/file/find name~"^{{manifestDir}}/"]] = 0`

// rootCleanup removes the container root when it is on /file and no
// container holds it: RouterOS deletes it with the container, a moment after
// the container is gone, and this is for when it did not; guarded, since
// RouterOS may take it in between. /file/remove on a directory takes its
// whole content.
const rootCleanup = `:if ([:len [/file/find name="{{rootDir}}"]] > 0 && ` + rootUnheld + `) do={ :do { /file/remove [find name="{{rootDir}}"] } on-error={} }`

// manifestStep is the first step: it writes the manifest before any object it
// lists exists, so that an install which stops half-way still leaves the
// record of everything it meant to create, and uninstall, which runs the
// steps backwards, deletes it last.
//
// Present reads the file back and compares it with what this plan would
// write, so a repeat install with the same options leaves it alone and one
// with other options rewrites it. Owned is the tag line. Check is the path:
// a file there that is not this install's manifest stops the install.
//
// Its removal refuses while anything the tag or the marker selects remains,
// so a removal that failed half-way keeps the record for the next attempt.
// Then, when the manifest is this install's, it removes the container root
// it lists if no container holds it — the container step takes the root with
// the container, and this is for a root an earlier attempt left — and then
// the manifest, so an attempt cut short between the two still has the
// record of the root. Last it removes the directory the two share when
// nothing else is in it. A file and a directory carry no comment, so these
// are selected by their exact paths, which --name and --disk derive, and the
// root goes only on the manifest's word (or, in the container step, the
// tagged container's); /file/remove on a directory takes its whole content,
// which is why the directory goes only when it is empty (measured in the
// virtual lab, CHR x86_64, RouterOS 7.24.4: /file/add creates the missing
// parent directory, and /file/remove of a directory removed the file inside
// it). That directory is removed when empty even if it was there before the
// install: it carries nothing that says whose it is, and an empty one holds
// nothing of anybody's.
var manifestStep = stepSpec{
	ID:      "manifest",
	Name:    ts("install manifest {{manifestFile}}"),
	Check:   ts(`:put [:len [/file/find name="{{manifestFile}}"]]`),
	Create:  ts(`:local m "{{manifest}}"; :if ([:len [/file/find name="{{manifestFile}}"]] > 0) do={ /file/set [find name="{{manifestFile}}"] contents=$m } else={ /file/add name="{{manifestFile}}" contents=$m }`),
	Owned:   ts(`:if ([:len [/file/find name="{{manifestFile}}"]] > 0) do={ :if (` + manifestIsOurs + `) do={ :put 1 } else={ :put 0 } } else={ :put 0 }`),
	Present: ts(`:if ([:len [/file/find name="{{manifestFile}}"]] > 0) do={ :if ([/file/get [find name="{{manifestFile}}"] contents] = "{{manifest}}") do={ :put 1 } else={ :put 0 } } else={ :put 0 }`),
	Remove: ts(`:if (` + taggedCount() + ` > 0) do={ :error "mikroscope: objects tagged {{tag}} remain, so {{manifestFile}} stays" }; ` +
		`:if ([:len [/file/find name="{{manifestFile}}"]] > 0) do={ :if (` + manifestIsOurs + `) do={ ` + rootCleanup + `; /file/remove [find name="{{manifestFile}}"] } }; ` +
		`:if (` + dirEmpty + `) do={ /file/remove [find name="{{manifestDir}}" type="directory"] }`),
	Manifest: []fragment{t("dir={{manifestDir}}"), t("file={{manifestFile}}")},
}

// stepSpecs is the plan: the manifest, then every object in creation order.
// Every object carries {{tag}} in its comment, verbatim, and every Remove
// selects by that exact comment together with the identity its Check used.
// Step's comment in steps.go says what each command field is for, and why
// every find quotes its address and port attributes.
var stepSpecs = []stepSpec{
	manifestStep,
	{
		ID:       "veth",
		Menu:     "/interface/veth",
		Name:     ts("veth interface {{veth}}"),
		Check:    ts(`:put [:len [/interface/veth/find name="{{veth}}"]]`),
		Create:   ts(`/interface/veth/add name="{{veth}}" address={{containerIP}}/30 gateway={{gatewayIP}} comment="{{tag}}"`),
		Owned:    ts(`:put [:len [/interface/veth/find name="{{veth}}" comment="{{tag}}"]]`),
		Remove:   ts(`/interface/veth/remove [find name="{{veth}}" comment="{{tag}}"]`),
		Manifest: ts("object=/interface/veth name={{veth}}"),
	},
	{
		ID:       "address",
		Menu:     "/ip/address",
		Name:     ts("router address {{gatewayIP}}"),
		Check:    ts(`:put [:len [/ip/address/find interface="{{veth}}"]]`),
		Create:   ts(`/ip/address/add address={{gatewayIP}}/30 interface="{{veth}}" comment="{{tag}}"`),
		Owned:    ts(`:put [:len [/ip/address/find interface="{{veth}}" comment="{{tag}}"]]`),
		Remove:   ts(`/ip/address/remove [find interface="{{veth}}" comment="{{tag}}"]`),
		Manifest: ts("object=/ip/address interface={{veth}}"),
	},
	// The two list memberships exist for the firewall traps. A router whose
	// firewall drops nothing by list needs neither, and ListNone
	// (--iface-list none, --addr-list none) leaves each one out.
	//
	// Without this one, the defconf raw rule `drop the rest
	// (in-interface-list=!LAN)` silently eats every packet the container
	// sends: the veth has to be a member of the LAN interface list for that
	// rule not to match it (measured on the reference RB5009, 2026-08-26, and
	// still the shape of the raw chain in the 2026-09-11 inventory).
	{
		ID:       "iface-member",
		When:     "ifaceList",
		Menu:     "/interface/list/member",
		Name:     ts("interface-list membership {{ifaceList}}"),
		Check:    ts(`:put [:len [/interface/list/member/find interface="{{veth}}" list="{{ifaceList}}"]]`),
		Create:   ts(`/interface/list/member/add list="{{ifaceList}}" interface="{{veth}}" comment="{{tag}}"`),
		Owned:    ts(`:put [:len [/interface/list/member/find interface="{{veth}}" list="{{ifaceList}}" comment="{{tag}}"]]`),
		Remove:   ts(`/interface/list/member/remove [find interface="{{veth}}" list="{{ifaceList}}" comment="{{tag}}"]`),
		Manifest: ts("object=/interface/list/member interface={{veth}} list={{ifaceList}}"),
	},
	// The sibling trap: the defconf raw rule `drop local if not from default
	// IP range` (in-interface-list=LAN, src-address-list=!LANs) matches any
	// source outside the LANs address list, so the /30 has to join it
	// (measured on the reference RB5009, 2026-08-26).
	{
		ID:       "addr-member",
		When:     "addrList",
		Menu:     "/ip/firewall/address-list",
		Name:     ts("address-list membership {{addrList}}"),
		Check:    ts(`:put [:len [/ip/firewall/address-list/find list="{{addrList}}" address="{{subnet}}"]]`),
		Create:   ts(`/ip/firewall/address-list/add list="{{addrList}}" address={{subnet}} comment="{{tag}}"`),
		Owned:    ts(`:put [:len [/ip/firewall/address-list/find list="{{addrList}}" address="{{subnet}}" comment="{{tag}}"]]`),
		Remove:   ts(`/ip/firewall/address-list/remove [find list="{{addrList}}" address="{{subnet}}" comment="{{tag}}"]`),
		Manifest: ts("object=/ip/firewall/address-list list={{addrList}} address={{subnet}}"),
	},
	// protocol="tcp", QUOTED. In a RouterOS `find`, a bare word is read as a
	// variable name and an unset variable is the empty value, so
	// `protocol=tcp` matches nothing at all while `protocol="tcp"` matches —
	// measured on the reference RB5009 (7.24.4, 2026-09-21): against the same
	// 15 dstnat rules, `find chain=dstnat protocol=tcp` returned 0 and `find
	// chain=dstnat protocol="tcp"` returned 10. Every other field here was
	// already quoted; this one was not, and because the same selector is the
	// Check, the Owned and the Remove, the effect was silent in both
	// directions: `uninstall --expose` removed neither rule and then verified
	// zero of them, printing "verified: nothing mikroscope created remains on
	// the router" over a live dst-nat, and `install` never saw its own rule,
	// so a repeat install added a second copy. `add protocol=tcp` is
	// unaffected: creation takes a bare word.
	//
	// Measured on the reference RB5009 (RouterOS 7.24.2, 2026-09-11): with
	// this pair in place a LAN host reaches the agent through the router's own
	// address — HTTP 200 in 1.3 ms, body intact — and the token becomes
	// mandatory because the veth is no longer link-local only.
	{
		ID:       "expose-nat",
		When:     "expose",
		Menu:     "/ip/firewall/nat",
		Name:     ts("expose dst-nat {{lanAddress}}:{{port}}"),
		Check:    ts(`:put [:len [/ip/firewall/nat/find chain=dstnat dst-address="{{lanAddress}}" dst-port="{{port}}" protocol="tcp"]]`),
		Create:   ts(`/ip/firewall/nat/add chain=dstnat dst-address={{lanAddress}} protocol=tcp dst-port={{port}} action=dst-nat to-addresses={{containerIP}} to-ports={{port}} comment="{{tag}}"`),
		Owned:    ts(`:put [:len [/ip/firewall/nat/find chain=dstnat dst-address="{{lanAddress}}" dst-port="{{port}}" protocol="tcp" comment="{{tag}}"]]`),
		Remove:   ts(`/ip/firewall/nat/remove [find chain=dstnat dst-address="{{lanAddress}}" dst-port="{{port}}" protocol="tcp" comment="{{tag}}"]`),
		Manifest: ts("object=/ip/firewall/nat chain=dstnat dst-address={{lanAddress}} dst-port={{port}} protocol=tcp"),
	},
	// The accept must land before the first forward drop; on a router whose
	// forward chain has no drop it is appended. The :local shares the line
	// with its use because over ssh every line is its own console command.
	{
		ID:    "expose-accept",
		When:  "expose",
		Menu:  "/ip/firewall/filter",
		Name:  ts("expose forward accept"),
		Check: ts(`:put [:len [/ip/firewall/filter/find chain=forward dst-address="{{containerIP}}" dst-port="{{port}}" protocol="tcp"]]`),
		Create: ts(`:local d [/ip/firewall/filter/find chain=forward action=drop]; ` +
			`:if ([:len $d] > 0) do={ /ip/firewall/filter/add chain=forward dst-address={{containerIP}} protocol=tcp dst-port={{port}} connection-nat-state=dstnat action=accept comment="{{tag}}" place-before=($d->0) } ` +
			`else={ /ip/firewall/filter/add chain=forward dst-address={{containerIP}} protocol=tcp dst-port={{port}} connection-nat-state=dstnat action=accept comment="{{tag}}" }`),
		Owned:    ts(`:put [:len [/ip/firewall/filter/find chain=forward dst-address="{{containerIP}}" dst-port="{{port}}" protocol="tcp" comment="{{tag}}"]]`),
		Remove:   ts(`/ip/firewall/filter/remove [find chain=forward dst-address="{{containerIP}}" dst-port="{{port}}" protocol="tcp" comment="{{tag}}"]`),
		Manifest: ts("object=/ip/firewall/filter chain=forward dst-address={{containerIP}} dst-port={{port}} protocol=tcp"),
	},
	containerStepSpec,
}

// containerStepSpec derives the envlist, the image file and the container.
// containerStep in steps.go says why each part is the way it is.
var containerStepSpec = stepSpec{
	ID:   "container",
	Menu: "/container",
	Name: ts("container {{name}}"),
	Check: []fragment{
		t(`:put ([:len [/container/find interface="{{veth}}"]] + [:len [/container/envs/find list="{{envList}}"]]`),
		w("tar", ` + [:len [/file/find name="{{imageFile}}"]]`),
		w("containerName", ` + [:len [/container/find name="{{containerName}}"]]`),
		t(`)`),
	},
	Create: []fragment{
		t(`:if ([:len ` + markerFind + `] > 0) do={ /container/envs/remove [find list="{{envList}}"] }; `),
		t(`/container/envs/add list="{{envList}}" key=` + MarkerName + ` value="{{tag}}"; `),
		t(`/container/envs/add list="{{envList}}" key=RATE_HZ value="{{rateHz}}"; `),
		t(`/container/envs/add list="{{envList}}" key=BUFFER_S value="{{bufferS}}"; `),
		t(`/container/envs/add list="{{envList}}" key=PORT value="{{port}}"; `),
		t(`/container/envs/add list="{{envList}}" key=ADDR value="{{containerIP}}"; `),
		t(`/container/envs/add list="{{envList}}" key=MEM_LIMIT_MB value="{{memLimitMB}}"; `),
		w("floorHz", `/container/envs/add list="{{envList}}" key=FLOOR_HZ value="{{floorHz}}"; `),
		t(`/container/envs/add list="{{envList}}" key=CAPTURE_MB value="{{captureMB}}"; `),
		w("triggers", `/container/envs/add list="{{envList}}" key=TRIGGERS value="{{triggers}}"; `),
		w("token", `/container/envs/add list="{{envList}}" key=TOKEN value="{{token}}"; `),
		t(`/container/add `),
		w("containerName", `name="{{containerName}}" `),
		w("tar", `file={{imageFile}}`),
		w("remote", `remote-image="{{remoteRef}}"`),
		t(` interface="{{veth}}" root-dir={{rootDir}} envlist="{{envList}}" logging=yes start-on-boot={{startOnBoot}}` +
			` restart-policy=on-failure restart-max-count={{restartMaxCount}} restart-interval={{restartInterval}} memory-max={{memoryMax}}` +
			` privileged={{privileged}} ignore-remote-image-change=yes comment="{{tag}}"; `),
		w("tar", `:local w 0; :while ([:len [/container/find comment="{{tag}}" stopped]] = 0 && $w < {{extractTimeoutS}}) do={ :delay 1s; :set w ($w + 1) }; `+
			`:if ([:len [/container/find comment="{{tag}}" stopped]] = 0) do={ :error "mikroscope: the image was not extracted within {{extractTimeoutS}} s; {{imageFile}} stays" }; `+
			`/file/remove [find name="{{imageFile}}"]; `),
		t(`/container/start [find comment="{{tag}}"]`),
	},
	Owned: []fragment{
		t(`:if ([:len ` + markerFind + `] > 0) do={ :put ([:len [/container/find comment="{{tag}}"]] + [:len [/container/envs/find list="{{envList}}"]]`),
		w("tar", ` + [:len [/file/find name="{{imageFile}}"]]`),
		t(`) } else={ :put [:len [/container/find comment="{{tag}}"]] }`),
	},
	Present: ts(`:put [:len [/container/find comment="{{tag}}"]]`),
	// The removal stops the container and waits for it to stop (F1), removes
	// it and waits for it to go; then, when it removed one, it waits up to
	// 10 s for RouterOS to delete the container root and removes what is
	// left (rootCleanup): the tagged container is what says the root is this
	// install's. Last, when the marker says the envlist is ours, the tar and
	// the envlist.
	Remove: []fragment{
		t(`:local had [:len [/container/find comment="{{tag}}"]]; ` +
			`:do { /container/stop [find comment="{{tag}}"] } on-error={}; ` +
			`:local s 0; :while (([:len [/container/find comment="{{tag}}" running]] + [:len [/container/find comment="{{tag}}" stopping]]) > 0 && $s < 30) do={ :delay 1s; :set s ($s + 1) }; ` +
			`/container/remove [find comment="{{tag}}"]; ` +
			`:local i 0; :while ([:len [/container/find comment="{{tag}}"]] > 0 && $i < 20) do={ :delay 1s; :set i ($i + 1) }; ` +
			`:if ($had > 0) do={ :local r 0; :while ([:len [/file/find name="{{rootDir}}"]] > 0 && ` + rootUnheld + ` && $r < 10) do={ :delay 1s; :set r ($r + 1) }; ` + rootCleanup + ` }; ` +
			`:if ([:len ` + markerFind + `] > 0) do={ `),
		w("tar", `:local j 0; :while ([:len [/file/find name="{{imageFile}}"]] > 0 && $j < 15) do={ /file/remove [find name="{{imageFile}}"]; :delay 1s; :set j ($j + 1) }; `+
			`:if ([:len [/file/find name="{{imageFile}}"]] = 0) do={ `),
		t(`/container/envs/remove [find list="{{envList}}" key!="` + MarkerName + `"]; /container/envs/remove ` + markerFind),
		w("tar", ` }`),
		t(` }`),
	},
	Manifest: []fragment{
		w("tar", "file={{imageFile}}"),
		t("object=/container/envs list={{envList}}"),
		t("object=/container interface={{veth}}"),
		t("dir={{rootDir}}"),
	},
}

// The script's own text, around the steps' Create lines (Script): the
// header comments and the opening of the block, the guards, and the wait for
// the agent, the end of the block and the closing comments. Each fragment is
// a line.
//
// The block runs as one script, pasted into a terminal or /imported: a guard
// stops it with :error before the first write, and a failure at any step
// stops the rest instead of leaving the steps before it behind with the
// rest missing. Measured in the virtual lab (CHR x86_64, RouterOS 7.24.4):
// an /import of a `{ … }` block whose :error fires before a write ends with
// `Script Error: <message>` and writes nothing, a `#` line inside the block
// is a comment, and declaring a :local twice in one block is accepted.
var (
	scriptHeader = []fragment{
		t("# mikroscope {{version}}: install script for RouterOS 7.24 or later. Container name: {{name}}"),
		t(`# Every object it creates carries the comment "{{tag}}", which is how`),
		t("# `mikroscope status` and `uninstall` recognize them later. It lists them in"),
		t("# {{manifestFile}} on the router, which `mikroscope uninstall` reads and deletes last."),
		t("#"),
		w("remote", "# The router pulls {{remoteRef}} itself."),
		w("remote", "# The registry host is part of remote-image= (RouterOS 7.18 and later take it"),
		w("remote", "# there), so this script neither reads nor changes the device-wide registry-url."),
		w("remote", "# A registry username set on the device for a registry other than {{registryHost}}"),
		w("remote", "# can make the pull end in `auth error`; `mikroscope doctor` warns about it."),
		w("tar", "# BEFORE RUNNING: put the agent image tar on the device as {{imageFile}}"),
		w("tar", "# (upload it over WinBox/WebFig Files, or /tool/fetch it), or regenerate this"),
		w("tar", "# script with --remote-image so the router pulls the image instead."),
		w("token", "#"),
		w("token", "# The agent's bearer token is in clear below: treat this file as a credential."),
		t("#"),
		t("# It runs as one block: a check that fails stops it before anything is written,"),
		t("# and a step that fails stops the steps after it."),
		t(""),
		t("{"),
	}
	scriptGuards = []fragment{
		// 7.24 or later, as a pattern on the version string: 7.24.4 (stable),
		// 7.24 (stable), which has no second dot, 7.25rc1 (testing), 7.100,
		// 8.0. Measured in the lab against those strings, and against
		// 7.23.1, 7.20.1, 7.3, 7.2 and 6.49.10, which it refuses.
		t(`:if (!([/system/resource/get version] ~ "^(7[.](2[4-9]|[3-9][0-9]|[1-9][0-9][0-9])|([89]|[1-9][0-9]+)[.])")) do={ :error "mikroscope: needs RouterOS 7.24 or later" }`),
		t(`:if ([:len [/system/package/find name="container" disabled=no]] = 0) do={ :error "mikroscope: the container package is not installed" }`),
		t(`:local dm [:tostr [/system/device-mode/get container]]; :if ($dm != "yes" && $dm != "true") do={ :error "mikroscope: device-mode container is not enabled" }`),
		t(`:if ([:len [/interface/veth/find name="{{veth}}"]] > 0 && [:len [/interface/veth/find name="{{veth}}" comment="{{tag}}"]] = 0) do={ :error "mikroscope: veth {{veth}} exists and is not mikroscope's" }`),
		// An envlist of the same name without the marker is somebody
		// else's: the container step would add the marker and the agent's
		// entries to it, and uninstall, finding the marker, would then
		// remove the owner's entries with them. The CLI refuses it at the
		// container step's Check; this is the script's refusal.
		t(`:if ([:len [/container/envs/find list="{{envList}}"]] > 0 && [:len ` + markerFind + `] = 0) do={ :error "mikroscope: envlist {{envList}} exists and is not mikroscope's" }`),
		w("containerName", `:if ([:len [/container/find name="{{containerName}}"]] > 0 && [:len [/container/find name="{{containerName}}" comment="{{tag}}"]] = 0) do={ :error "mikroscope: a container named {{containerName}} exists and is not mikroscope's" }`),
		w("ifaceList", `:if ([:len [/interface/list/find name="{{ifaceList}}"]] = 0) do={ :error "mikroscope: interface list {{ifaceList}} does not exist" }`),
		w("disk", `:if ([:len [/disk/find slot="{{disk}}"]] = 0) do={ :error "mikroscope: disk {{disk}} does not exist" }`),
		w("tar", `:if ([:len [/file/find name="{{imageFile}}"]] = 0) do={ :error "mikroscope: upload {{imageFile}} first" }`),
		t(`:if ([:len [/file/find name="{{manifestFile}}"]] > 0) do={ :if (!(` + manifestIsOurs + `)) do={ :error "mikroscope: {{manifestFile}} exists and is not this install's manifest" } }`),
	}
	scriptFooter = []fragment{
		t(`:local k 0; :while ([:len [/container/find comment="{{tag}}" running]] = 0 && $k < 120) do={ :delay 1s; :set k ($k + 1) }`),
		t(`:if ([:len [/container/find comment="{{tag}}" running]] > 0) do={ :put "mikroscope: agent running, http://{{containerIP}}:{{port}}/healthz" } else={ :put "mikroscope: not running yet; see /log/print where topics~\"container\"" }`),
		t("}"),
		t(`# When it is done: /container/print where comment="{{tag}}"`),
		t("# The agent answers on http://{{containerIP}}:{{port}}/healthz from the router's LAN."),
	}
)

// holds says whether a fragment or a step is kept for these predicate values.
func holds(when string, p map[string]bool) bool {
	if when == "" {
		return true
	}
	if name, negated := strings.CutPrefix(when, "!"); negated {
		return !mustPredicate(p, name)
	}
	return mustPredicate(p, when)
}

func mustPredicate(p map[string]bool, name string) bool {
	v, ok := p[name]
	if !ok {
		panic("router: steps spec names an unknown predicate " + strconv.Quote(name))
	}
	return v
}

var placeholder = regexp.MustCompile(`\{\{([A-Za-z]+)\}\}`)

// substitute replaces every {{key}} with its value. An unknown key is a
// programming error in the spec, caught by the tests.
func substitute(text string, v map[string]string) string {
	return placeholder.ReplaceAllStringFunc(text, func(m string) string {
		key := m[2 : len(m)-2]
		val, ok := v[key]
		if !ok {
			panic("router: steps spec names an unknown value " + strconv.Quote(key))
		}
		return val
	})
}

// renderText concatenates the fragments that hold into one string.
func renderText(frags []fragment, v map[string]string, p map[string]bool) string {
	var b strings.Builder
	for _, f := range frags {
		if holds(f.When, p) {
			b.WriteString(substitute(f.Text, v))
		}
	}
	return b.String()
}

// renderLines is the fragments that hold, one line each.
func renderLines(frags []fragment, v map[string]string, p map[string]bool) []string {
	var out []string
	for _, f := range frags {
		if holds(f.When, p) {
			out = append(out, substitute(f.Text, v))
		}
	}
	return out
}

// renderScript is the `plan --rsc` script: the header, the guards, each
// step's name as a comment and its Create, the footer, one line each.
func renderScript(o *Options, version string) string {
	v, p := values(o, version), predicateValues(o)
	lines := renderLines(scriptHeader, v, p)
	lines = append(lines, renderLines(scriptGuards, v, p)...)
	for _, s := range stepSpecs {
		if holds(s.When, p) {
			r := s.render(v, p)
			lines = append(lines, "# "+r.Name, r.Create)
		}
	}
	lines = append(lines, renderLines(scriptFooter, v, p)...)
	return strings.Join(lines, "\n") + "\n"
}

// predicateValues evaluates every predicate for o.
func predicateValues(o *Options) map[string]bool {
	p := make(map[string]bool, len(predicates))
	for name, f := range predicates {
		p[name] = f(o)
	}
	return p
}

// values is every placeholder the spec may name, for o: the options as
// Finish left them and what derives from them. manifest comes last because
// it is rendered from the others.
func values(o *Options, version string) map[string]string {
	v := map[string]string{
		"name":            o.Name,
		"veth":            o.Veth,
		"subnet":          o.Subnet,
		"ifaceList":       o.IfaceList,
		"addrList":        o.AddrList,
		"disk":            o.Disk,
		"port":            strconv.Itoa(o.Port),
		"rateHz":          strconv.Itoa(o.RateHz),
		"bufferS":         strconv.Itoa(o.BufferS),
		"floorHz":         strconv.Itoa(o.FloorHz),
		"captureMB":       strconv.Itoa(o.CaptureMB),
		"triggers":        o.Triggers,
		"token":           o.Token,
		"restartMaxCount": strconv.Itoa(o.RestartMaxCount),
		"restartInterval": o.RestartInterval,
		"memoryMax":       o.MemoryMax,
		"lanAddress":      o.LANAddress,
		"containerName":   o.ContainerName,
		"extractTimeoutS": strconv.Itoa(o.ExtractTimeoutS()),
		"version":         version,
		"gatewayIP":       o.GatewayIP,
		"containerIP":     o.ContainerIP,
		"memLimitMB":      strconv.Itoa(o.MemLimitMB),
		"tag":             o.Tag(),
		"envList":         o.EnvList(),
		"imageFile":       o.ImageFile(),
		"rootDir":         o.RootDir(),
		"manifestDir":     manifestDir(o),
		"manifestFile":    ManifestFile(*o),
		"startOnBoot":     o.StartOnBoot(),
		"privileged":      yesNo(o.Privileged),
		"remoteRef":       o.RemoteRef(),
		"registryHost":    o.RegistryHost(),
		"source":          "tar",
	}
	if o.UsesRemoteImage() {
		v["source"] = "remote"
	}
	v["manifest"] = strings.Join(manifestLines(v, predicateValues(o)), `\n`) + `\n`
	return v
}

// manifestLines is the manifest: the header, then each kept step's lines in
// plan order. It is written with `\n` between lines inside a RouterOS
// string, which RouterOS stores as newlines (/file/get … contents gives
// them back as written).
func manifestLines(v map[string]string, p map[string]bool) []string {
	lines := renderLines(manifestHeader, v, p)
	for _, s := range stepSpecs {
		if holds(s.When, p) {
			lines = append(lines, renderLines(s.Manifest, v, p)...)
		}
	}
	return lines
}

// render turns one step spec into a Step for these values.
func (s stepSpec) render(v map[string]string, p map[string]bool) Step {
	return Step{
		id:      s.ID,
		menu:    s.Menu,
		Name:    renderText(s.Name, v, p),
		Check:   renderText(s.Check, v, p),
		Create:  renderText(s.Create, v, p),
		Owned:   renderText(s.Owned, v, p),
		Remove:  renderText(s.Remove, v, p),
		Present: renderText(s.Present, v, p),
	}
}

// sweepFor renders one of the sweep's commands for one menu.
func sweepFor(cmd, menu string, v map[string]string) string {
	return substitute(strings.ReplaceAll(cmd, "{{menu}}", menu), v)
}

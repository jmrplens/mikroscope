/**
 * Yes-or-no facts about RouterOS that the installer and the security pages
 * rest on, each checked once on the reference device, for `<Verified>`.
 *
 * They are not figures, so they are not in measurements.ts and `<Provenance>`
 * refuses them: "Measured on" beside "a read user can list every envlist" would
 * be the wrong verb. Each one is cited from several pages in both locales, in
 * the middle of a sentence ("verified on …"), with the device, the version
 * and the date written once here.
 *
 * `fact` and `source` are for the reviewer and never rendered: the sentence
 * around the component says what was verified, in the page's language.
 */
import { RB5009 } from "./measurements";

export interface Verification {
	device: string;
	routeros: string;
	/** ISO date the source gives. */
	date: string;
	fact: string;
	source: string;
}

const onRB5009 = { device: RB5009.device, routeros: RB5009.routeros } as const;

export const verifications = {
	"read-user-envlist": {
		...onRB5009,
		date: "2026-09-11",
		fact: "/container/print over the binary API returns every container's cmd and envlist to a read,api user",
		source: "site/src/content/docs/security/api-user.mdx",
	},
	"test-policy": {
		...onRB5009,
		date: "2026-09-11",
		fact: "/tool fetch and /tool profile both require the test policy",
		source:
			"site/src/content/docs/security/api-user.mdx (the fact); site/src/content/docs/install/reaching-the-agent.mdx (the version, for /tool fetch); date checked on the reference device, no published document records it",
	},
	"find-quoting": {
		...onRB5009,
		date: "2026-09-11",
		fact: "in a RouterOS find, address and port attributes match only when quoted",
		source:
			"site/src/content/docs/security/installer.mdx (the fact); internal/router/router_test.go, TestFindsQuoteAddressesAndPorts (the fact, the version and the date)",
	},
	"expose-rules": {
		...onRB5009,
		date: "2026-09-11",
		fact: "dst-nat plus forward accept reaches the agent from the LAN; both rules are removable by tag",
		source:
			"site/src/content/docs/security/expose.mdx (both rules, removed by uninstall); internal/router/router_test.go, the comment above fakeRunner (the date)",
	},
	"direct-lists": {
		...onRB5009,
		date: "2026-09-11",
		fact: "the LAN reaches the veth once the veth joins LAN and the /30 joins LANs",
		source:
			"site/src/content/docs/install/firewall.mdx (the fact and the date); internal/router/steps.go, Plan",
	},
	"remove-race": {
		...onRB5009,
		date: "2026-09-11",
		fact: "/container/remove returns before the container is gone; a /file/remove issued meanwhile does nothing, silently",
		source:
			"site/src/content/docs/security/installer.mdx; internal/router/steps.go, containerStep",
	},
	"remote-image-change": {
		...onRB5009,
		date: "2026-09-11",
		fact: "with ignore-remote-image-change=no, removing the tar makes RouterOS stop, remove and re-extract the container",
		source:
			"site/src/content/docs/security/index.mdx; internal/router/steps.go, containerStep",
	},
	"export-identical": {
		...onRB5009,
		date: "2026-09-12",
		fact: "doctor, install, status, upgrade, uninstall leaves /export byte-identical",
		source:
			"site/src/content/docs/install/index.mdx (the full round trip, the device and the version)",
	},
	"tmpfs-zero-writes": {
		...onRB5009,
		date: "2026-09-11",
		fact: "a tmpfs disk hosts the image tar and the container root with zero NAND writes: write-sect-since-reboot stayed at 58 279 across install, run and removal",
		source:
			"site/src/content/docs/install/layout.mdx (tmpfs adds nothing to the flash counters, and write-sect-since-reboot read 58 279 before the install and after the removal); site/src/content/docs/start/walkthrough.mdx (58 279 across install, run and removal)",
	},
	"privileged-namespaces": {
		...onRB5009,
		date: "2026-09-12",
		fact: "privileged=yes drops the container's user namespace but not its network or PID namespace",
		source:
			"site/src/content/docs/limits/privileged.mdx (user, network and PID); internal/router/steps.go, containerStep",
	},
} as const satisfies Record<string, Verification>;

export type VerificationId = keyof typeof verifications;

export const isVerificationId = (id: string): id is VerificationId =>
	Object.hasOwn(verifications, id);

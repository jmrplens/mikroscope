// @ts-check
// The script generator in the browser: reads the form, renders with
// src/lib/rsc.mjs, and shows the script, the commands around it and every
// field's error.
//
// What it must never do, and does not: make a request (there is no fetch,
// XHR or beacon here, and the page's policy has no connect source for one),
// store anything (no localStorage, sessionStorage or cookie, and every text
// field asks the browser not to restore it), put the token in the URL or in
// the command line, or log it. The token is generated here, from
// crypto.getRandomValues, and lives in one read-only input until the page is
// left.
//
// Bundled by Astro (ScriptGenerator.astro's processed <script>), so the
// meta CSP allows it as 'self'; it adds no inline handler.

import spec from "../data/rsc/spec.json";
import {
	AGENT_TARS,
	cliArgs,
	render,
	renderUninstall,
	resolve,
	tarCommands,
	uninstallCommand,
	verifyCommands,
} from "./rsc.mjs";
import { highlightInto } from "./rsc-highlight.mjs";

/** @typedef {import("./rsc.mjs").Spec} Spec */
/** @typedef {import("./rsc.mjs").GenOptions} GenOptions */
/** @typedef {import("./rsc.mjs").FieldError} FieldError */

const SPEC = /** @type {Spec} */ (/** @type {unknown} */ (spec));

/** Each option's first control, where an error link takes the reader. */
const PRIMARY = {
	remoteImage: "gen-registry",
	arch: "gen-arch",
	disk: "gen-disk",
	ephemeral: "gen-disk",
	name: "gen-name",
	veth: "gen-veth",
	subnet: "gen-subnet",
	port: "gen-port",
	ifaceList: "gen-iface-list",
	addrList: "gen-addr-list",
	rateHz: "gen-rate",
	bufferS: "gen-buffer",
	memoryMax: "gen-memory-max",
	memLimitMB: "gen-mem-limit",
	captureMB: "gen-capture",
	floorHz: "gen-floor",
	triggers: "gen-triggers-first",
	privileged: "gen-privileged",
	token: "gen-token",
	expose: "gen-expose",
	lanAddress: "gen-lan-address",
	restartMaxCount: "gen-restart-max",
	restartInterval: "gen-restart-interval",
	startOnBoot: "gen-start-on-boot",
	containerName: "gen-container-name",
	extractTimeout: "gen-extract-timeout",
};

/**
 * The options whose select has an "Other…" choice, and the text field that
 * choice opens. With "Other…" chosen, an error is about what the field holds,
 * so the error link goes to the field and only the field is marked invalid;
 * the select's value is a valid choice either way.
 */
const OTHER = {
	remoteImage: { select: "gen-registry", field: "gen-remote-other" },
	ifaceList: { select: "gen-iface-list", field: "gen-iface-other" },
	addrList: { select: "gen-addr-list", field: "gen-addr-other" },
};

/**
 * The control an error of `key` is about: the "Other…" text field when that
 * choice is made, else the option's first control.
 * @param {string} key
 */
function errorTarget(key) {
	const other = OTHER[/** @type {keyof typeof OTHER} */ (key)];
	if (other) {
		const select = /** @type {HTMLSelectElement | null} */ (
			document.getElementById(other.select)
		);
		if (select?.value === "") return other.field;
	}
	return PRIMARY[/** @type {keyof typeof PRIMARY} */ (key)] ?? "gen-name";
}

/** The 62 characters a token is drawn from. */
const TOKEN_ALPHABET =
	"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789";
const TOKEN_LENGTH = 32;

/**
 * A token of TOKEN_LENGTH characters, uniform over the alphabet: bytes of
 * 248 or more are drawn again, so no character is likelier than another
 * (248 is the largest multiple of 62 a byte holds).
 */
export function newToken() {
	const limit = 256 - (256 % TOKEN_ALPHABET.length);
	let out = "";
	const buf = new Uint8Array(64);
	while (out.length < TOKEN_LENGTH) {
		crypto.getRandomValues(buf);
		for (const b of buf) {
			if (b < limit && out.length < TOKEN_LENGTH)
				out += TOKEN_ALPHABET[b % TOKEN_ALPHABET.length];
		}
	}
	return out;
}

/**
 * A text field read as the whole number an option holds: digits, with a
 * sign, or NaN, which resolve() reports as "a whole number".
 * @param {string} text
 */
function wholeNumber(text) {
	const t = text.trim();
	return /^-?\d{1,9}$/.test(t) ? Number(t) : NaN;
}

/**
 * @param {string} template
 * @param {Record<string, string | number>} params
 */
function fill(template, params) {
	return template.replace(/\{(\w+)\}/g, (m, k) =>
		k in params ? String(params[k]) : m,
	);
}

export function start() {
	const form = /** @type {HTMLElement | null} */ (
		document.querySelector("[data-ms-gen]")
	);
	if (!form) return;
	/** @type {Record<string, string>} */
	const str = JSON.parse(form.dataset.strings ?? "{}");
	const router = form.dataset.router ?? "admin@192.168.88.1";

	const byId = (/** @type {string} */ id) =>
		/** @type {HTMLInputElement} */ (document.getElementById(id));
	const radio = (/** @type {string} */ name) =>
		/** @type {HTMLInputElement | null} */ (
			form.querySelector(`input[name="${name}"]:checked`)
		)?.value ?? "";
	const all = (/** @type {string} */ sel) =>
		/** @type {HTMLElement[]} */ ([...document.querySelectorAll(sel)]);

	// The page works without JavaScript on the defaults; with it, the form and
	// the buttons appear and the note that says they need it goes.
	for (const el of all("[data-gen-nojs]")) el.hidden = true;
	form.hidden = false;
	for (const el of all("[data-gen-js]:not([data-gen-show])")) el.hidden = false;
	const firstTrigger = form.querySelector("[data-trigger]");
	if (firstTrigger) firstTrigger.id = "gen-triggers-first";

	/** What the form holds, as extra state the options do not carry. */
	const ui = () => {
		const source = radio("gen-source");
		const registry = byId("gen-registry").value;
		const storage = radio("gen-storage");
		const iface = byId("gen-iface-list").value;
		const addr = byId("gen-addr-list").value;
		const custom = radio("gen-triggers-mode") === "custom";
		return {
			source,
			registry,
			storage,
			iface,
			addr,
			custom,
			asset: byId("gen-arch").value,
		};
	};

	/**
	 * The option object the form describes, and the errors only the form can
	 * make (no trigger chosen).
	 * @returns {{ options: GenOptions, formErrors: FieldError[] }}
	 */
	const read = () => {
		const u = ui();
		/** @type {FieldError[]} */
		const formErrors = [];
		let triggers = "";
		if (u.custom) {
			const parts = [];
			for (const g of SPEC.triggers) {
				const box = /** @type {HTMLInputElement | null} */ (
					form.querySelector(`[data-trigger="${g.name}"]`)
				);
				if (!box?.checked) continue;
				const th = /** @type {HTMLInputElement | null} */ (
					form.querySelector(`[data-threshold="${g.name}"]`)
				);
				parts.push(
					g.op === "" ? g.name : `${g.name}${g.op}${(th?.value ?? "").trim()}`,
				);
			}
			triggers = parts.join(",");
			if (parts.length === 0)
				formErrors.push({ field: "triggers", code: "triggersEmpty" });
		}
		const tar = AGENT_TARS.find((a) => a.asset === u.asset) ?? AGENT_TARS[0];
		const memLimit = byId("gen-mem-limit").value.trim();
		/** @type {GenOptions} */
		const options = {
			name: byId("gen-name").value.trim(),
			veth: byId("gen-veth").value.trim(),
			subnet: byId("gen-subnet").value.trim(),
			ifaceList:
				u.iface === "" ? byId("gen-iface-other").value.trim() : u.iface,
			addrList: u.addr === "" ? byId("gen-addr-other").value.trim() : u.addr,
			disk: u.storage === "other" ? byId("gen-disk").value.trim() : "",
			ephemeral: u.storage === "tmpfs",
			arch: u.source === "tar" ? tar.arch : "auto",
			port: wholeNumber(byId("gen-port").value),
			rateHz: wholeNumber(byId("gen-rate").value),
			bufferS: wholeNumber(byId("gen-buffer").value),
			memoryMax: byId("gen-memory-max").value.trim(),
			memLimitMB: memLimit === "" ? 0 : wholeNumber(memLimit),
			floorHz: wholeNumber(byId("gen-floor").value),
			captureMB: wholeNumber(byId("gen-capture").value),
			triggers,
			token: byId("gen-token").value,
			remoteImage:
				u.source === "tar"
					? ""
					: u.registry === ""
						? byId("gen-remote-other").value.trim()
						: u.registry,
			expose: byId("gen-expose").checked,
			lanAddress: byId("gen-expose").checked
				? byId("gen-lan-address").value.trim()
				: "",
			privileged: byId("gen-privileged").checked,
			restartMaxCount: wholeNumber(byId("gen-restart-max").value),
			restartInterval: byId("gen-restart-interval").value.trim(),
			startOnBoot: byId("gen-start-on-boot").value,
			containerName: byId("gen-container-name").value.trim(),
			extractTimeout: byId("gen-extract-timeout").value.trim(),
		};
		// An "other" registry left empty is not a tar install.
		if (u.source === "remote" && options.remoteImage === "") {
			formErrors.push({
				field: "remoteImage",
				code: "pattern",
				params: { pattern: "imageRef" },
			});
		}
		if (u.storage === "other" && options.disk === "") {
			formErrors.push({
				field: "disk",
				code: "pattern",
				params: { pattern: "disk" },
			});
		}
		return { options, formErrors };
	};

	/** @param {FieldError} e */
	const message = (e) => {
		const key =
			e.code === "pattern"
				? `ms.gen.err.pattern.${e.params?.pattern}`
				: `ms.gen.err.${e.code}`;
		return fill(str[key] ?? str["ms.gen.err.other"] ?? e.code, e.params ?? {});
	};

	/** Shows or hides every element that depends on the form's state. */
	const showState = (/** @type {Record<string, boolean>} */ state) => {
		for (const el of all("[data-gen-show]")) {
			const name = el.dataset.genShow ?? "";
			el.hidden = !state[name];
		}
	};

	let lastErrors = "";

	/** @type {{ install: string, uninstall: string, cli: string, tar: string, "uninstall-cli": string, token: string }} */
	const current = {
		install: "",
		uninstall: "",
		cli: "",
		tar: "",
		"uninstall-cli": "",
		token: "",
	};

	// An output marked data-gen-hl runs on the router and is coloured as RouterOS
	// (src/lib/rsc-highlight.mjs); its text is the same either way. Should the
	// colouring throw (a browser whose RegExp cannot compile the grammar), the
	// output is still written, plain: `current` already holds the new script,
	// and what copy and download hand out must be what the page shows.
	const setOut = (/** @type {string} */ name, /** @type {string} */ text) => {
		for (const el of all(`[data-gen-out="${name}"]`)) {
			if (!el.hasAttribute("data-gen-hl")) {
				el.textContent = text;
				continue;
			}
			try {
				highlightInto(el, text);
			} catch {
				el.textContent = text;
			}
		}
	};

	const update = () => {
		const u = ui();
		const { options, formErrors } = read();
		const r = resolve(options, SPEC);
		/** @type {FieldError[]} */
		const errors = [...formErrors];
		for (const e of r.errors) {
			if (!errors.some((x) => x.field === e.field)) errors.push(e);
		}
		const token = String(options.token);
		showState({
			remote: u.source === "remote",
			tar: u.source === "tar",
			"remote-other": u.source === "remote" && u.registry === "",
			"disk-other": u.storage === "other",
			tmpfs: u.storage === "tmpfs",
			"iface-other": u.iface === "",
			"addr-other": u.addr === "",
			"triggers-custom": u.custom,
			expose: options.expose === true,
			token: token !== "",
		});

		// Each field: its error, or none.
		for (const key of Object.keys(PRIMARY)) {
			const err = document.getElementById(`gen-err-${key}`);
			const e = errors.find((x) => x.field === key);
			if (err) {
				err.hidden = !e;
				err.textContent = e ? message(e) : "";
			}
			const wrap = form.querySelector(`[data-opt="${key}"]`);
			const other = OTHER[/** @type {keyof typeof OTHER} */ (key)];
			for (const c of wrap
				? wrap.querySelectorAll(
						"input:not([type=radio]):not([type=checkbox]), select",
					)
				: []) {
				// Of an option with "Other…", only the field it opens can be wrong.
				const blamed = !other || c.id === errorTarget(key);
				if (e && blamed) c.setAttribute("aria-invalid", "true");
				else c.removeAttribute("aria-invalid");
			}
		}

		// Live hints: the /30's ends and the derived memory limit.
		const subnetHint = document.querySelector('[data-gen-live="subnet"]');
		if (subnetHint && r.values) {
			subnetHint.textContent = fill(str["ms.gen.subnet.ends"], {
				router: r.values.gatewayIP,
				agent: r.values.containerIP,
			});
		}
		const memHint = document.querySelector('[data-gen-live="memLimitMB"]');
		if (memHint && r.values && Number(options.memLimitMB) === 0) {
			memHint.textContent = fill(str["ms.gen.memLimitMB.hint"], {
				n: r.values.memLimitMB,
			});
		}

		const valid =
			errors.length === 0 && r.values !== null && r.predicates !== null;
		for (const el of all("[data-gen-valid]")) el.hidden = !valid;
		for (const el of all("[data-gen-invalid]")) el.hidden = valid;
		for (const b of /** @type {HTMLButtonElement[]} */ (
			all("[data-gen-copy]:not([data-gen-copy=token]), [data-gen-download]")
		)) {
			b.disabled = !valid;
		}
		const box = /** @type {HTMLElement | null} */ (
			document.querySelector("[data-gen-errors]")
		);
		const list = document.querySelector("[data-gen-errors-list]");
		const title = document.querySelector("[data-gen-errors-title]");
		// Rebuilt only when the errors change: a blur that fires `change`
		// while the reader clicks one of these links would otherwise swap the
		// link out from under the click.
		const signature = JSON.stringify(errors);
		if (box && list && title && signature !== lastErrors) {
			lastErrors = signature;
			box.hidden = valid;
			list.replaceChildren(
				...errors.map((e) => {
					const li = document.createElement("li");
					const a = document.createElement("a");
					a.href = `#${errorTarget(e.field)}`;
					a.dataset.genField = e.field;
					a.textContent = `${str[`ms.gen.${e.field}`] ?? e.field}: ${message(e)}`;
					li.append(a);
					return li;
				}),
			);
			title.textContent =
				errors.length === 1
					? str["ms.gen.summaryOne"]
					: fill(str["ms.gen.summary"], { n: errors.length });
		}
		announceValidity(valid, errors.length);

		if (!valid || !r.values || !r.predicates) {
			for (const k of /** @type {const} */ ([
				"install",
				"uninstall",
				"cli",
				"tar",
				"uninstall-cli",
			]))
				current[k] = "";
			return;
		}
		current.install = render(options, SPEC);
		current.uninstall = renderUninstall(options, SPEC);
		current.cli = cliArgs(options, SPEC);
		current["uninstall-cli"] = uninstallCommand(options, SPEC, router);
		current.tar =
			u.source === "tar"
				? tarCommands(r.values, u.asset, router).join("\n")
				: "";
		const verify = verifyCommands(r.values, r.predicates);
		setOut("install", current.install);
		setOut("uninstall", current.uninstall);
		setOut("cli", current.cli);
		setOut("uninstall-cli", current["uninstall-cli"]);
		setOut("tar", current.tar);
		setOut("verify-router", verify.router.join("\n"));
		setOut("verify-lan", verify.lan.join("\n"));
	};

	// One polite announcement when the count of fields to fix changes, not on
	// every keystroke.
	let lastAnnounced = 0;
	const validity = /** @type {HTMLElement | null} */ (
		document.querySelector('[data-gen-status="validity"]')
	);
	const announceValidity = (
		/** @type {boolean} */ valid,
		/** @type {number} */ n,
	) => {
		if (!validity || n === lastAnnounced) return;
		lastAnnounced = n;
		validity.textContent = valid
			? str["ms.gen.valid"]
			: n === 1
				? str["ms.gen.summaryOne"]
				: fill(str["ms.gen.summary"], { n });
	};

	let timer = 0;
	const later = () => {
		window.clearTimeout(timer);
		timer = window.setTimeout(update, 150);
	};
	form.addEventListener("input", later);
	form.addEventListener("change", () => {
		window.clearTimeout(timer);
		update();
	});

	// Error links: open the Advanced group when the field is in it, then focus.
	document.addEventListener("click", (ev) => {
		const a = /** @type {HTMLElement | null} */ (
			ev.target instanceof Element
				? ev.target.closest("a[data-gen-field]")
				: null
		);
		if (!a) return;
		const id = (a.getAttribute("href") ?? "").slice(1);
		const target = document.getElementById(id);
		if (!target) return;
		ev.preventDefault();
		const details = target.closest("details");
		if (details) details.open = true;
		target.focus();
		target.scrollIntoView({ block: "center" });
	});

	/** @param {string} which @param {string} text */
	const status = (which, text) => {
		const el = document.querySelector(`[data-gen-status="${which}"]`);
		if (!el) return;
		el.textContent = "";
		window.setTimeout(() => (el.textContent = text), 50);
	};

	const tokenInput = byId("gen-token");
	const tokenButtons = /** @type {HTMLButtonElement[]} */ (
		all('[data-gen-copy="token"], [data-gen-token="clear"]')
	);
	for (const b of /** @type {HTMLButtonElement[]} */ (
		all("[data-gen-token]")
	)) {
		b.addEventListener("click", () => {
			const generate = b.dataset.genToken === "generate";
			tokenInput.value = generate ? newToken() : "";
			for (const x of tokenButtons) x.disabled = !generate;
			status(
				"token",
				generate ? str["ms.gen.tokenGenerated"] : str["ms.gen.tokenCleared"],
			);
			update();
		});
	}

	for (const b of /** @type {HTMLButtonElement[]} */ (all("[data-gen-copy]"))) {
		b.addEventListener("click", async () => {
			const which = b.dataset.genCopy ?? "";
			const text =
				which === "token"
					? tokenInput.value
					: current[/** @type {keyof typeof current} */ (which)];
			if (!text) return;
			try {
				await navigator.clipboard.writeText(text);
				status(which, str["ms.gen.copied"]);
			} catch {
				status(which, str["ms.gen.copyFailed"]);
			}
		});
	}

	for (const b of /** @type {HTMLButtonElement[]} */ (
		all("[data-gen-download]")
	)) {
		b.addEventListener("click", () => {
			const which = /** @type {"install" | "uninstall"} */ (
				b.dataset.genDownload
			);
			const text = current[which];
			if (!text) return;
			const file = b.dataset.file ?? "mikroscope.rsc";
			const url = URL.createObjectURL(new Blob([text], { type: "text/plain" }));
			const a = document.createElement("a");
			a.href = url;
			a.download = file;
			a.hidden = true;
			document.body.append(a);
			a.click();
			a.remove();
			window.setTimeout(() => URL.revokeObjectURL(url), 1000);
			status(which, fill(str["ms.gen.downloaded"], { file }));
		});
	}

	// A browser that restores form fields on reload or back (autocomplete=off
	// asks it not to) could hand back a token; start from the page's own state.
	tokenInput.value = "";
	update();
}

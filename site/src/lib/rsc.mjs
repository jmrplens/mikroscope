// @ts-check
// The RouterOS install script, rendered in JavaScript from the steps spec.
//
// `mikroscope plan --rsc` renders the script in Go from a table of steps
// (internal/router/stepspec.go), and cmd/gen_rsc exports that table as
// src/data/rsc/spec.json. This module renders the same table with the same
// two rules, so the script generator and the manual install pages print the
// commands the CLI prints, byte for byte, without a copy of them:
//
// - a fragment is kept when its predicate holds: `when` is empty, a predicate
//   name, or `!` and a name;
// - each `{{key}}` is replaced by its value. Nothing is escaped, because every
//   value has passed its bound first, as Options.Finish bounds it in Go.
//
// What is not a template of other values is derived here the way options.go
// derives it, and spec.derive.rules says each rule in words: the /30 ends, the
// Go memory limit, the registry reference, start-on-boot, the extraction
// timeout in seconds and the manifest. scripts/check-rsc.mjs holds all of it
// to Go: for every case of the golden matrix it compares the values with the
// ones Go computed (cases.json), the script with `plan --rsc`'s output
// (cases/<id>.rsc), every step's commands with the plan golden, and the
// command line with Go's CLIArgs.
//
// Pure: no DOM, no network, no storage. The component renders the default
// script with it at build time, src/lib/rsc-ui.mjs renders every change in
// the browser, and the check runs it under Node.

/**
 * @typedef {{ when?: string, text: string }} Fragment
 * @typedef {{
 *   id: string, when?: string, menu?: string, name: Fragment[], check: Fragment[],
 *   create: Fragment[], owned: Fragment[], remove: Fragment[], present?: Fragment[],
 *   manifest: Fragment[],
 * }} StepSpec
 * @typedef {{
 *   version: string,
 *   defaults: Record<string, string | number | boolean>,
 *   flags: { key: string, flag: string }[],
 *   cli: { command: string, plain: string, tokenEnv: string },
 *   bounds: {
 *     regex: Record<string, string>,
 *     ranges: Record<string, [number, number]>,
 *     listNone: string,
 *     builtinIfaceLists: string[],
 *     arch: string[],
 *     startOnBoot: string[],
 *     subnetPrefix: number,
 *   },
 *   derive: {
 *     approxLineBytes: number, memLimitRingFactorHalves: number, minMemLimitMB: number,
 *     memoryCapNumerator: number, memoryCapDenominator: number,
 *     dockerHubHost: string, dockerHubAliases: string[],
 *   },
 *   triggers: { name: string, op: string, min: number, max: number }[],
 *   predicates: string[],
 *   placeholders: string[],
 *   derived: { key: string, text: Fragment[] }[],
 *   manifest: { header: Fragment[] },
 *   sweep: { menus: string[], count: string, remove: string },
 *   steps: StepSpec[],
 *   scriptHeader: Fragment[], scriptGuards: Fragment[], scriptFooter: Fragment[],
 * }} Spec
 * @typedef {Record<string, string | number | boolean>} GenOptions
 * @typedef {{ field: string, code: string, params?: Record<string, string | number> }} FieldError
 * @typedef {{ id: string, name: string, check: string, create: string, owned: string, remove: string, present: string }} Step
 */

/**
 * The registries the release publishes the agent image to, as the generator
 * offers them: `ref` is what goes after --remote-image, `published` the name
 * in .goreleaser.yaml's `images:`, which scripts/check-rsc.mjs reads, so a
 * registry the release stops publishing to cannot stay on offer.
 */
export const AGENT_IMAGES = /** @type {const} */ ([
	{
		id: "dockerhub",
		ref: "jmrplens/mikroscope-agent",
		published: "docker.io/jmrplens/mikroscope-agent",
	},
	{
		id: "ghcr",
		ref: "ghcr.io/jmrplens/mikroscope-agent",
		published: "ghcr.io/jmrplens/mikroscope-agent",
	},
]);

/**
 * The agent image tars a release carries, by the architecture a router
 * reports (scripts/agent-tars.sh writes `mikroscope-agent-<asset>.tar`), and
 * the --arch each is installed with. The architecture changes which tar to
 * upload, never a command in the script.
 */
export const AGENT_TARS = /** @type {const} */ ([
	{ asset: "arm64", arch: "arm64" },
	{ asset: "armv7", arch: "arm" },
	{ asset: "armv5", arch: "arm" },
	{ asset: "amd64", arch: "amd64" },
]);

/** The architecture an offline render resolves `auto` to, as Finish does. */
const OFFLINE_ARCH = "arm64";

/** The GOARCH values an agent image is built for (spec.bounds.arch without auto). */
const archOf = (/** @type {Spec} */ spec) =>
	spec.bounds.arch.filter((a) => a !== "auto");

const MIB = 1024 * 1024;

/**
 * The predicates a fragment or a step may name, as stepspec.go's
 * `predicates`: one line each. `o` is the finished option object.
 * @type {Record<string, (o: GenOptions, spec: Spec) => boolean>}
 */
export const PREDICATES = {
	expose: (o) => o.expose === true,
	remote: (o) => o.remoteImage !== "",
	tar: (o) => o.remoteImage === "",
	token: (o) => o.token !== "",
	triggers: (o) => o.triggers !== "",
	floorHz: (o) => Number(o.floorHz) > 0,
	ifaceList: (o, spec) => o.ifaceList !== spec.bounds.listNone,
	addrList: (o, spec) => o.addrList !== spec.bounds.listNone,
	containerName: (o) => o.containerName !== "",
	ephemeral: (o) => o.ephemeral === true,
	disk: (o) => o.disk !== "",
};

/**
 * Whether a fragment or a step is kept. An unknown predicate is an error in
 * the spec, not in the input, and throws, as Go panics on it.
 * @param {string | undefined} when
 * @param {Record<string, boolean>} p
 */
export function holds(when, p) {
	if (!when) return true;
	const negated = when.startsWith("!");
	const name = negated ? when.slice(1) : when;
	if (!(name in p))
		throw new Error(`rsc: the steps spec names an unknown predicate "${name}"`);
	return negated ? !p[name] : p[name];
}

/**
 * Every `{{key}}` replaced by its value. An unknown key throws.
 * @param {string} text
 * @param {Record<string, string>} v
 */
export function substitute(text, v) {
	return text.replace(/\{\{([A-Za-z]+)\}\}/g, (_, key) => {
		if (!Object.hasOwn(v, key))
			throw new Error(`rsc: the steps spec names an unknown value "${key}"`);
		return v[key];
	});
}

/**
 * The fragments that hold, concatenated.
 * @param {Fragment[] | undefined} frags
 * @param {Record<string, string>} v
 * @param {Record<string, boolean>} p
 */
export function renderText(frags, v, p) {
	return (frags ?? [])
		.filter((f) => holds(f.when, p))
		.map((f) => substitute(f.text, v))
		.join("");
}

/**
 * The fragments that hold, one line each.
 * @param {Fragment[] | undefined} frags
 * @param {Record<string, string>} v
 * @param {Record<string, boolean>} p
 */
export function renderLines(frags, v, p) {
	return (frags ?? [])
		.filter((f) => holds(f.when, p))
		.map((f) => substitute(f.text, v));
}

/* ------------------------------------------------------------ derivations */

/**
 * A dotted-quad IPv4 address as four octets, or null. Stricter than Go's
 * net.ParseIP, which also takes an IPv4-mapped IPv6 address: a value this
 * refuses is one the generator never offers, and nothing it accepts is one
 * Go refuses (leading zeros included, which Go refuses too).
 * @param {string} s
 * @returns {number[] | null}
 */
export function parseIPv4(s) {
	const m =
		/^(0|[1-9]\d{0,2})\.(0|[1-9]\d{0,2})\.(0|[1-9]\d{0,2})\.(0|[1-9]\d{0,2})$/.exec(
			s,
		);
	if (!m) return null;
	const octets = m.slice(1).map(Number);
	return octets.every((n) => n <= 255) ? octets : null;
}

/**
 * The /30's two ends, as deriveEndpoints computes them, or the error code.
 * @param {string} subnet
 * @param {number} prefix
 * @returns {{ subnet: string, gatewayIP: string, containerIP: string } | { code: string, params?: Record<string, string> }}
 */
export function endpoints(subnet, prefix) {
	const m = /^([^/]+)\/(0|[1-9]\d?)$/.exec(subnet);
	const ip = m ? parseIPv4(m[1]) : null;
	if (!m || !ip || Number(m[2]) > 32) return { code: "subnet" };
	const bits = Number(m[2]);
	if (bits !== prefix) return { code: "subnetPrefix" };
	const mask = bits === 0 ? 0 : (0xffffffff << (32 - bits)) >>> 0;
	const addr = ((ip[0] << 24) | (ip[1] << 16) | (ip[2] << 8) | ip[3]) >>> 0;
	const net = (addr & mask) >>> 0;
	const quad = (/** @type {number} */ n) =>
		[n >>> 24, (n >>> 16) & 255, (n >>> 8) & 255, n & 255].join(".");
	if (net !== addr)
		return {
			code: "subnetNetwork",
			params: { network: `${quad(net)}/${bits}` },
		};
	return {
		subnet: `${quad(net)}/${bits}`,
		gatewayIP: quad(net + 1),
		containerIP: quad(net + 2),
	};
}

/**
 * memory-max in bytes, as doctor.go's memoryMaxBytes: 64 MiB for anything it
 * cannot read.
 * @param {string} spec
 */
export function memoryMaxBytes(spec) {
	const digits = spec.replace(/[KMG]+$/, "");
	const n = /^[+-]?\d+$/.test(digits) ? Number(digits) : NaN;
	if (!Number.isSafeInteger(n) || n <= 0) return 64 * MIB;
	const unit = spec.slice(digits.length);
	if (unit === "G") return n * 1024 * MIB;
	if (unit === "M") return n * MIB;
	if (unit === "K") return n * 1024;
	return n;
}

/**
 * The Go memory limit the agent gets when none is given, as deriveMemLimit:
 * the ring times the factor, rounded up to MiB, at least the minimum, capped
 * at three quarters of memory-max while that still leaves the ring room.
 * @param {GenOptions} o
 * @param {Spec} spec
 */
export function deriveMemLimit(o, spec) {
	if (Number(o.memLimitMB) !== 0) return Number(o.memLimitMB);
	const d = spec.derive;
	const ringBytes = Number(o.rateHz) * Number(o.bufferS) * d.approxLineBytes;
	let limit = Math.floor(
		(Math.floor((ringBytes * d.memLimitRingFactorHalves) / 2) + MIB - 1) / MIB,
	);
	limit = Math.max(limit, d.minMemLimitMB);
	const maxBytes = memoryMaxBytes(String(o.memoryMax));
	if (maxBytes > 0) {
		const ceiling = Math.floor(
			Math.floor((maxBytes * d.memoryCapNumerator) / d.memoryCapDenominator) /
				MIB,
		);
		if (ceiling > Math.floor(ringBytes / MIB) && limit > ceiling)
			limit = ceiling;
	}
	return limit;
}

/**
 * A reference split into its registry host and the rest, by Docker's rule, as
 * options.go's splitImageRef: every Docker Hub alias becomes the API host,
 * and a Docker Hub name with no namespace gains `library/`.
 * @param {string} ref
 * @param {Spec} spec
 * @returns {[string, string]}
 */
export function splitImageRef(ref, spec) {
	const slash = ref.indexOf("/");
	let host = spec.derive.dockerHubHost;
	let rest = ref;
	if (slash >= 0) {
		const first = ref.slice(0, slash);
		if (/[.:]/.test(first) || first === "localhost") {
			host = first;
			rest = ref.slice(slash + 1);
		}
	}
	if (
		host !== spec.derive.dockerHubHost &&
		!spec.derive.dockerHubAliases.includes(host)
	) {
		return [host, rest];
	}
	if (!rest.includes("/")) rest = `library/${rest}`;
	return [spec.derive.dockerHubHost, rest];
}

/**
 * A duration validDuration accepts, in seconds; 0 for anything else.
 * @param {string} d
 */
export function durationSeconds(d) {
	const m = /^(\d{1,4})([smh])$/.exec(d);
	if (!m) return 0;
	return Number(m[1]) * (m[2] === "h" ? 3600 : m[2] === "m" ? 60 : 1);
}

/**
 * Why a TRIGGERS value is refused, as the agent's ParseTriggers says it, or
 * null when it parses. A threshold is a plain decimal here, which is
 * narrower than Go's ParseFloat and never wider.
 * @param {string} spec
 * @param {Spec["triggers"]} grammar
 * @returns {{ code: string, params: Record<string, string | number> } | null}
 */
export function triggersError(spec, grammar) {
	for (const part of spec.split(",")) {
		const raw = part.trim();
		if (raw === "") continue;
		const at = raw.search(/[<>]/);
		const name = at >= 0 ? raw.slice(0, at) : raw;
		const arg = at >= 0 ? raw.slice(at) : "";
		const rule = grammar.find((g) => g.name === name);
		if (!rule) return { code: "triggerUnknown", params: { trigger: raw } };
		if (rule.op === "") {
			if (arg !== "")
				return { code: "triggerNoThreshold", params: { trigger: raw } };
			continue;
		}
		const value = arg.startsWith(rule.op) ? arg.slice(rule.op.length) : null;
		const n =
			value !== null && /^\d+(\.\d+)?$/.test(value) ? Number(value) : NaN;
		if (!(n >= rule.min && n <= rule.max)) {
			return {
				code: "triggerRange",
				params: { trigger: raw, min: rule.min, max: rule.max },
			};
		}
	}
	return null;
}

/* ------------------------------------------------------------- the options */

/**
 * The option object with every key the spec's defaults name, the given value
 * where there is one.
 * @param {GenOptions} options
 * @param {Spec} spec
 * @returns {GenOptions}
 */
export function withDefaults(options, spec) {
	/** @type {GenOptions} */
	const o = { ...spec.defaults };
	for (const key of Object.keys(spec.defaults)) {
		if (options[key] !== undefined) o[key] = options[key];
	}
	return o;
}

/**
 * The option object finished as Options.FinishFor("plan") finishes it: every
 * bound checked, the /30's ends and the memory limit derived. Unlike Go it
 * does not stop at the first error: it collects one per field, so a form can
 * mark each.
 * @param {GenOptions} options
 * @param {Spec} spec
 * @returns {{ o: GenOptions, errors: FieldError[] }}
 */
export function finish(options, spec) {
	const o = withDefaults(options, spec);
	/** @type {FieldError[]} */
	const errors = [];
	const re = (/** @type {string} */ name) =>
		new RegExp(spec.bounds.regex[name]);
	/**
	 * @param {string} field
	 * @param {string} code
	 * @param {Record<string, string | number>} [params]
	 */
	const fail = (field, code, params) => {
		errors.push(params ? { field, code, params } : { field, code });
	};
	const integer = (/** @type {string} */ key) => {
		const n = o[key];
		if (typeof n !== "number" || !Number.isInteger(n)) {
			fail(key, "integer");
			return false;
		}
		return true;
	};
	/**
	 * @param {string} key
	 * @param {string} [rangeKey]
	 */
	const inRange = (key, rangeKey) => {
		if (!integer(key)) return;
		const [min, max] = spec.bounds.ranges[rangeKey ?? key];
		const n = Number(o[key]);
		if (n < min || n > max) fail(key, "range", { min, max });
	};
	const text = (/** @type {string} */ key) => {
		if (typeof o[key] !== "string") {
			fail(key, "text");
			return false;
		}
		return true;
	};
	for (const key of ["ephemeral", "expose", "privileged"]) {
		if (typeof o[key] !== "boolean") fail(key, "boolean");
	}
	for (const key of [
		"name",
		"veth",
		"subnet",
		"ifaceList",
		"addrList",
		"disk",
		"arch",
		"memoryMax",
		"triggers",
		"token",
		"remoteImage",
		"lanAddress",
		"restartInterval",
		"startOnBoot",
		"containerName",
		"extractTimeout",
	]) {
		text(key);
	}
	if (errors.length > 0) return { o, errors };

	// validateNames
	if (!re("name").test(String(o.name)))
		fail("name", "pattern", { pattern: "name" });
	for (const key of ["veth", "ifaceList", "addrList"]) {
		if (!re("objectName").test(String(o[key])))
			fail(key, "pattern", { pattern: "objectName" });
	}
	if (spec.bounds.builtinIfaceLists.includes(String(o.ifaceList))) {
		fail("ifaceList", "builtinList", {
			list: String(o.ifaceList),
			none: spec.bounds.listNone,
		});
	}
	if (o.containerName !== "" && !re("name").test(String(o.containerName))) {
		fail("containerName", "pattern", { pattern: "name" });
	}
	if (o.ephemeral) o.disk = "tmpfs";
	if (o.disk !== "" && !re("disk").test(String(o.disk)))
		fail("disk", "pattern", { pattern: "disk" });
	if (o.arch === "auto") o.arch = OFFLINE_ARCH;
	if (!archOf(spec).includes(String(o.arch))) fail("arch", "arch");
	if (!re("token").test(String(o.token)))
		fail("token", "pattern", { pattern: "token" });

	// validateLimits, with the memory limit derived first, as FinishFor does
	for (const key of ["port", "rateHz", "bufferS", "floorHz", "captureMB"])
		inRange(key);
	if (!re("memory").test(String(o.memoryMax)))
		fail("memoryMax", "pattern", { pattern: "memory" });
	if (integer("memLimitMB")) {
		const ringOK = !errors.some((e) => ["rateHz", "bufferS"].includes(e.field));
		if (ringOK) {
			const derived = Number(o.memLimitMB) === 0;
			o.memLimitMB = deriveMemLimit(o, spec);
			const [min, max] = spec.bounds.ranges.memLimitMB;
			if (Number(o.memLimitMB) < min || Number(o.memLimitMB) > max) {
				fail("memLimitMB", derived ? "memLimitDerived" : "range", {
					min,
					max,
					got: Number(o.memLimitMB),
				});
			}
		}
	}
	if (o.triggers !== "") {
		const t = triggersError(String(o.triggers), spec.triggers);
		if (t) fail("triggers", t.code, t.params);
	}

	// validateContainerSettings
	inRange("restartMaxCount");
	if (!re("duration").test(String(o.restartInterval))) {
		fail("restartInterval", "pattern", { pattern: "duration" });
	}
	if (!spec.bounds.startOnBoot.includes(String(o.startOnBoot)))
		fail("startOnBoot", "startOnBoot");
	if (!re("duration").test(String(o.extractTimeout))) {
		fail("extractTimeout", "pattern", { pattern: "duration" });
	} else {
		const [min, max] = spec.bounds.ranges.extractTimeoutS;
		const s = durationSeconds(String(o.extractTimeout));
		if (s < min || s > max)
			fail("extractTimeout", "durationRange", { min, max });
	}

	// validateExpose: plan renders the envlist, so an exposed agent needs a token
	if (o.expose) {
		const ip = parseIPv4(String(o.lanAddress));
		if (!ip) fail("lanAddress", "lanAddress");
		else o.lanAddress = ip.join(".");
		if (o.token === "") fail("token", "exposeToken");
	}
	if (o.remoteImage !== "" && !re("imageRef").test(String(o.remoteImage))) {
		fail("remoteImage", "pattern", { pattern: "imageRef" });
	}

	// deriveEndpoints
	const ends = endpoints(String(o.subnet), spec.bounds.subnetPrefix);
	if ("code" in ends) fail("subnet", ends.code, ends.params);
	else Object.assign(o, ends);
	return { o, errors };
}

/**
 * Every predicate, for a finished option object.
 * @param {GenOptions} o
 * @param {Spec} spec
 */
export function predicateValues(o, spec) {
	/** @type {Record<string, boolean>} */
	const p = {};
	for (const name of spec.predicates) {
		const f = PREDICATES[name];
		if (!f) throw new Error(`rsc: no renderer for the predicate "${name}"`);
		p[name] = f(o, spec);
	}
	return p;
}

/**
 * Every placeholder the spec may name, for a finished option object, as
 * stepspec.go's `values`. The manifest comes last because it is rendered
 * from the others.
 * @param {GenOptions} o
 * @param {Spec} spec
 * @param {Record<string, boolean>} p
 * @returns {Record<string, string>}
 */
export function valuesOf(o, spec, p) {
	const remote = o.remoteImage !== "";
	const [host, rest] = remote
		? splitImageRef(String(o.remoteImage), spec)
		: ["", ""];
	/** @type {Record<string, string>} */
	const v = {
		name: String(o.name),
		veth: String(o.veth),
		subnet: String(o.subnet),
		ifaceList: String(o.ifaceList),
		addrList: String(o.addrList),
		disk: String(o.disk),
		port: String(o.port),
		rateHz: String(o.rateHz),
		bufferS: String(o.bufferS),
		floorHz: String(o.floorHz),
		captureMB: String(o.captureMB),
		triggers: String(o.triggers),
		token: String(o.token),
		restartMaxCount: String(o.restartMaxCount),
		restartInterval: String(o.restartInterval),
		memoryMax: String(o.memoryMax),
		lanAddress: String(o.lanAddress),
		containerName: String(o.containerName),
		extractTimeoutS: String(durationSeconds(String(o.extractTimeout))),
		version: spec.version,
		gatewayIP: String(o.gatewayIP),
		containerIP: String(o.containerIP),
		memLimitMB: String(o.memLimitMB),
		startOnBoot:
			o.startOnBoot === "yes" || o.startOnBoot === "no"
				? String(o.startOnBoot)
				: o.ephemeral
					? "no"
					: "yes",
		privileged: o.privileged ? "yes" : "no",
		remoteRef: remote ? `${host}/${rest}` : "",
		registryHost: host,
		source: remote ? "remote" : "tar",
	};
	for (const d of spec.derived) v[d.key] = renderText(d.text, v, p);
	v.manifest = manifestLines(v, p, spec).join("\\n") + "\\n";
	return v;
}

/**
 * The manifest's lines: its header, then each kept step's lines in plan
 * order (spec.manifest.rule).
 * @param {Record<string, string>} v
 * @param {Record<string, boolean>} p
 * @param {Spec} spec
 */
export function manifestLines(v, p, spec) {
	const lines = renderLines(spec.manifest.header, v, p);
	for (const s of spec.steps) {
		if (holds(s.when, p)) lines.push(...renderLines(s.manifest, v, p));
	}
	return lines;
}

/**
 * The option object resolved: finished, with its predicates and values, or
 * the errors that stop it. `values` is null whenever `errors` is not empty,
 * so a partial script cannot be rendered by mistake.
 * @param {GenOptions} options
 * @param {Spec} spec
 * @returns {{ options: GenOptions, predicates: Record<string, boolean> | null, values: Record<string, string> | null, errors: FieldError[] }}
 */
export function resolve(options, spec) {
	const { o, errors } = finish(options, spec);
	if (errors.length > 0)
		return { options: o, predicates: null, values: null, errors };
	const predicates = predicateValues(o, spec);
	return {
		options: o,
		predicates,
		values: valuesOf(o, spec, predicates),
		errors,
	};
}

/** Thrown by the renderers for an option object that does not resolve. */
export class OptionsError extends Error {
	/** @param {FieldError[]} errors */
	constructor(errors) {
		super(`rsc: ${errors.map((e) => `${e.field}: ${e.code}`).join("; ")}`);
		this.errors = errors;
	}
}

/**
 * @param {GenOptions} options
 * @param {Spec} spec
 */
function mustResolve(options, spec) {
	const r = resolve(options, spec);
	if (!r.values || !r.predicates) throw new OptionsError(r.errors);
	return { v: r.values, p: r.predicates, o: r.options };
}

/**
 * The steps the plan holds for these values, rendered.
 * @param {Record<string, string>} v
 * @param {Record<string, boolean>} p
 * @param {Spec} spec
 * @returns {Step[]}
 */
export function planSteps(v, p, spec) {
	return spec.steps
		.filter((s) => holds(s.when, p))
		.map((s) => ({
			id: s.id,
			name: renderText(s.name, v, p),
			check: renderText(s.check, v, p),
			create: renderText(s.create, v, p),
			owned: renderText(s.owned, v, p),
			remove: renderText(s.remove, v, p),
			present: renderText(s.present, v, p),
		}));
}

/**
 * The `plan --rsc` script: the header, the guards, each step's name as a
 * comment and its create command, the footer, one line each (spec.script).
 * Byte for byte what `mikroscope plan --rsc <cliArgs>` prints.
 * @param {GenOptions} options
 * @param {Spec} spec
 */
export function render(options, spec) {
	const { v, p } = mustResolve(options, spec);
	const lines = [
		...renderLines(spec.scriptHeader, v, p),
		...renderLines(spec.scriptGuards, v, p),
	];
	for (const s of planSteps(v, p, spec)) lines.push(`# ${s.name}`, s.create);
	lines.push(...renderLines(spec.scriptFooter, v, p));
	return `${lines.join("\n")}\n`;
}

/**
 * The commands `mikroscope uninstall` sends for these options, in its order
 * (deploy.go's uninstallFrom): each step's removal but the manifest's, newest
 * step first; then the tag sweep, one command per menu; then the manifest
 * step's removal, which refuses while anything tagged remains.
 * @param {Record<string, string>} v
 * @param {Record<string, boolean>} p
 * @param {Spec} spec
 * @returns {{ name: string, command: string, kind: "step" | "sweep" | "manifest" }[]}
 */
export function removalCommands(v, p, spec) {
	const steps = planSteps(v, p, spec);
	const [manifest, ...rest] = steps;
	if (!manifest || manifest.id !== "manifest") {
		throw new Error(
			"rsc: the plan's first step is not the manifest; uninstall's order no longer holds",
		);
	}
	/** @type {{ name: string, command: string, kind: "step" | "sweep" | "manifest" }[]} */
	const out = rest
		.reverse()
		.map((s) => ({ name: s.name, command: s.remove, kind: "step" }));
	for (const menu of spec.sweep.menus) {
		out.push({
			name: `tag sweep ${menu}`,
			command: substitute(spec.sweep.remove.replaceAll("{{menu}}", menu), v),
			kind: "sweep",
		});
	}
	out.push({ name: manifest.name, command: manifest.remove, kind: "manifest" });
	return out;
}

/**
 * The uninstall script: the commands removalCommands lists, as one block to
 * paste or /import. Each removal but the manifest's runs inside its own
 * `:do … on-error=`, so one that fails is reported and the next still runs,
 * as `uninstall` carries on past a step that fails; the manifest's runs last
 * and bare, so its refusal ends the script with the reason.
 * @param {GenOptions} options
 * @param {Spec} spec
 */
export function renderUninstall(options, spec) {
	const { v, p } = mustResolve(options, spec);
	const lines = [
		`# mikroscope ${v.version}: uninstall script for RouterOS 7.24 or later. Container name: ${v.name}`,
		`# It removes what the install script with the same settings created: every`,
		`# object tagged "${v.tag}", the container root ${v.rootDir},`,
		`# and ${v.manifestFile} last. It sends the commands \`mikroscope uninstall\` sends,`,
		`# in its order, and selects nothing by pattern.`,
		"",
		"{",
	];
	for (const c of removalCommands(v, p, spec)) {
		if (c.kind === "manifest") {
			lines.push(`# ${c.name}`, c.command);
		} else if (c.kind === "sweep") {
			lines.push(
				`:do { ${c.command} } on-error={ :put "mikroscope: could not sweep ${c.name.slice("tag sweep ".length)}" }`,
			);
		} else {
			lines.push(
				`# ${c.name}`,
				`:do { ${c.command} } on-error={ :put "mikroscope: could not remove ${c.name}" }`,
			);
		}
	}
	lines.push(
		`:put "mikroscope: removed; /container/print where comment=\\"${v.tag}\\" lists nothing"`,
		"}",
	);
	return `${lines.join("\n")}\n`;
}

/**
 * The `mikroscope plan --rsc` command that renders the same script, as Go's
 * CLIArgs: the flags whose value differs from the default, in the spec's
 * flag order; a true bool as the flag alone, a false one as flag=false; a
 * value in single quotes unless it is plain. Never the token: the command
 * reads it from spec.cli.tokenEnv (cliEnv).
 * @param {GenOptions} options
 * @param {Spec} spec
 */
export function cliArgs(options, spec) {
	const cur = withDefaults(options, spec);
	const plain = new RegExp(spec.cli.plain);
	const parts = spec.cli.command.split(" ");
	for (const { key, flag } of spec.flags) {
		const value = cur[key];
		if (value === spec.defaults[key] || key === "token") continue;
		if (typeof value === "boolean") parts.push(value ? flag : `${flag}=false`);
		else if (typeof value === "string")
			parts.push(flag, plain.test(value) ? value : `'${value}'`);
		else parts.push(flag, String(value));
	}
	return parts.join(" ");
}

/**
 * The variables cliArgs' command needs set before it runs: the token's,
 * when there is a token.
 * @param {GenOptions} options
 * @param {Spec} spec
 */
export function cliEnv(options, spec) {
	return withDefaults(options, spec).token === "" ? [] : [spec.cli.tokenEnv];
}

/* ------------------------------------------------ the manual install pages */

/**
 * The placeholder each value stands under on the manual install pages. The
 * pages explain each one in a table; a value missing here fails the check.
 */
export const PLACEHOLDERS = /** @type {const} */ ({
	name: "NAME",
	veth: "VETH",
	subnet: "NET/30",
	ifaceList: "IFACE_LIST",
	addrList: "ADDR_LIST",
	disk: "DISK",
	port: "PORT",
	rateHz: "RATE_HZ",
	bufferS: "BUFFER_S",
	floorHz: "FLOOR_HZ",
	captureMB: "CAPTURE_MB",
	triggers: "TRIGGERS",
	token: "TOKEN",
	restartMaxCount: "RESTART_MAX_COUNT",
	restartInterval: "RESTART_INTERVAL",
	memoryMax: "MEMORY_MAX",
	lanAddress: "LAN_IP",
	containerName: "CONTAINER_NAME",
	extractTimeoutS: "EXTRACT_TIMEOUT_S",
	gatewayIP: "GW_IP",
	containerIP: "AGENT_IP",
	memLimitMB: "MEM_LIMIT_MB",
	startOnBoot: "START_ON_BOOT",
	privileged: "PRIVILEGED",
	remoteRef: "IMAGE_REF",
	registryHost: "REGISTRY_HOST",
	tag: "TAG",
	envList: "ENVLIST",
	imageFile: "IMAGE_FILE",
	rootDir: "ROOT_DIR",
	manifestDir: "MANIFEST_DIR",
	manifestFile: "MANIFEST_FILE",
});

/**
 * Values and predicates for the manual pages: every value its placeholder
 * (the version and the source as they are), the manifest rendered from them,
 * and the predicates as given.
 * @param {Spec} spec
 * @param {Record<string, boolean>} predicates every predicate the spec names
 */
export function placeholderValues(spec, predicates) {
	for (const name of spec.predicates) {
		if (typeof predicates[name] !== "boolean")
			throw new Error(`rsc: predicate "${name}" not given`);
	}
	/** @type {Record<string, string>} */
	const v = {
		version: spec.version,
		source: predicates.remote ? "remote" : "tar",
	};
	for (const key of spec.placeholders) {
		if (key === "version" || key === "source" || key === "manifest") continue;
		const ph = /** @type {Record<string, string>} */ (PLACEHOLDERS)[key];
		if (!ph) throw new Error(`rsc: no placeholder for the value "${key}"`);
		v[key] = ph;
	}
	v.manifest = manifestLines(v, predicates, spec).join("\\n") + "\\n";
	return v;
}

/**
 * One step's create command split into the commands it chains, each kept
 * whole: a command ends at a fragment that ends with `; `. Each carries the
 * predicate of its first fragment, so an optional envlist entry can be told
 * from a fixed one, and a kind read from its first word. A command whose
 * kind this does not know throws, so a new command in the container step
 * cannot slip past the manual pages unnoticed.
 * @param {StepSpec} step
 * @param {Record<string, string>} v
 * @param {Record<string, boolean>} p
 * @returns {{ kind: string, when: string, command: string }[]}
 */
export function splitCreate(step, v, p) {
	/** @type {{ kind: string, when: string, command: string }[]} */
	const out = [];
	let text = "";
	let when = "";
	let started = false;
	for (const f of step.create) {
		if (!holds(f.when, p)) continue;
		if (!started) {
			when = f.when ?? "";
			started = true;
		}
		text += substitute(f.text, v);
		if (f.text.endsWith("; ")) {
			out.push({ kind: "", when, command: text.slice(0, -2) });
			text = "";
			started = false;
		}
	}
	if (started) out.push({ kind: "", when, command: text });
	for (const c of out) c.kind = commandKind(step.id, c.command);
	return out;
}

/**
 * What a command of a create is, by its first words.
 * @param {string} stepId
 * @param {string} command
 */
function commandKind(stepId, command) {
	if (stepId !== "container") return stepId;
	if (command.startsWith(":if ([:len [/container/envs/find"))
		return "env-reset";
	if (command.startsWith("/container/envs/add ")) return "env";
	if (command.startsWith("/container/add ")) return "container-add";
	if (command.startsWith(":local w ")) return "extract-wait";
	if (command.startsWith("/container/start ")) return "start";
	throw new Error(
		`rsc: the container step has a command the manual pages do not know: ${command.slice(0, 60)}`,
	);
}

/* ------------------------------------------- around the script: the page */

/**
 * The `mikroscope uninstall` command for an install made with these options:
 * the same flags as cliArgs, which uninstall takes as install does. Without
 * --yes it lists what it would remove.
 * @param {GenOptions} options
 * @param {Spec} spec
 * @param {string} router the --router value, user@address
 */
export function uninstallCommand(options, spec, router) {
	const flags = cliArgs(options, spec).slice(spec.cli.command.length);
	return `mikroscope uninstall --router ${router}${flags}`;
}

/**
 * What proves the install worked, for resolved values: on the router, the
 * container and the agent's /healthz fetched by the router itself; from a
 * host on the LAN, /healthz over the router, and over the exposed address
 * when there is one. /healthz needs no token.
 * @param {Record<string, string>} v
 * @param {Record<string, boolean>} p
 */
export function verifyCommands(v, p) {
	const url = `http://${v.containerIP}:${v.port}/healthz`;
	const router = [
		`/container/print where comment="${v.tag}"`,
		`:put ([/tool/fetch url="${url}" output=user as-value]->"data")`,
	];
	const lan = [`curl ${url}`];
	if (p.expose) lan.push(`curl http://${v.lanAddress}:${v.port}/healthz`);
	return { router, lan };
}

/**
 * The shell commands that fetch a release's agent image tar, check it
 * against the release's checksums and put it on the router under the name
 * the script expects.
 * @param {Record<string, string>} v
 * @param {string} asset one of AGENT_TARS' assets
 * @param {string} router the scp target, user@address
 */
export function tarCommands(v, asset, router) {
	if (!AGENT_TARS.some((a) => a.asset === asset))
		throw new Error(`rsc: no agent tar for "${asset}"`);
	const base = `https://github.com/jmrplens/mikroscope/releases/download/v${v.version}`;
	const file = `mikroscope-agent-${asset}.tar`;
	return [
		`curl -fsSLO ${base}/${file}`,
		`curl -fsSLO ${base}/checksums.txt`,
		`sha256sum --ignore-missing -c checksums.txt`,
		`scp ${file} ${router}:${v.imageFile}`,
	];
}

/** Where the page's example commands address the router: RouterOS's default LAN address. */
export const EXAMPLE_ROUTER = "admin@192.168.88.1";

/**
 * Everything the generator page shows before anyone touches the form: the
 * defaults with a Docker Hub pull, which is what the page shows without
 * JavaScript and what its Markdown twin says.
 * @param {Spec} spec
 */
export function generatorDefaults(spec) {
	/** @type {GenOptions} */
	const options = {
		...spec.defaults,
		remoteImage: `${AGENT_IMAGES[0].ref}:${spec.version}`,
	};
	const { v, p } = mustResolve(options, spec);
	const tar = mustResolve({ ...spec.defaults }, spec);
	return {
		options,
		values: v,
		script: render(options, spec),
		cli: cliArgs(options, spec),
		uninstall: renderUninstall(options, spec),
		uninstallCli: uninstallCommand(options, spec, EXAMPLE_ROUTER),
		tar: tarCommands(tar.v, AGENT_TARS[0].asset, EXAMPLE_ROUTER),
		verify: verifyCommands(v, p),
	};
}

/**
 * The parts of the container step's create, by the name a manual page asks
 * for: `container` is the /container/add command itself.
 */
export const CONTAINER_PARTS = [
	"env-reset",
	"env",
	"env-optional",
	"container",
	"extract-wait",
	"start",
];

/**
 * The commands a `<ManualSteps>` tag shows, with the values as placeholders
 * (placeholderValues): one step's command, a part of the container step, the
 * removals in uninstall's order, the tag sweep, or a golden case's whole
 * script. ManualSteps.astro renders it and src/lib/page-markdown.mjs reduces
 * the tag with it, so the page and its Markdown twin show the same lines.
 * Anything it cannot render throws, naming what it can.
 * @param {{ step?: string, field?: string, variant?: string, source?: string, with?: string, without?: string, case?: string }} props
 * @param {Spec} spec
 * @param {{ id: string, options: GenOptions }[]} cases the golden matrix (src/data/rsc/cases.json)
 */
export function manualCode(props, spec, cases) {
	const { step, field = "create", variant, source = "remote" } = props;
	const optional = [
		"token",
		"triggers",
		"floorHz",
		"containerName",
		"disk",
		"ephemeral",
		"expose",
	];
	const predicatesFor = (/** @type {string[]} */ extra) => {
		/** @type {Record<string, boolean>} */
		const p = Object.fromEntries(spec.predicates.map((n) => [n, false]));
		if (source !== "remote" && source !== "tar")
			throw new Error(`ManualSteps: source="${source}" is remote or tar`);
		p.remote = source === "remote";
		p.tar = source === "tar";
		p.ifaceList = true;
		p.addrList = true;
		for (const name of [...(props.with ?? "").split(/\s+/), ...extra].filter(
			Boolean,
		)) {
			if (!optional.includes(name)) {
				throw new Error(
					`ManualSteps: with="${name}" is not an optional part; use ${optional.join(", ")}`,
				);
			}
			p[name] = true;
		}
		for (const name of (props.without ?? "").split(/\s+/).filter(Boolean)) {
			if (name !== "ifaceList" && name !== "addrList") {
				throw new Error(
					`ManualSteps: without="${name}": only ifaceList and addrList are on by default`,
				);
			}
			p[name] = false;
		}
		return p;
	};
	if (variant === "script") {
		const id = props.case ?? "pull-dockerhub";
		const c = cases.find((x) => x.id === id);
		if (!c)
			throw new Error(
				`ManualSteps: no golden case "${id}" in src/data/rsc/cases.json`,
			);
		return render(c.options, spec).replace(/\n$/, "");
	}
	if (variant === "remove" || variant === "sweep") {
		const p = predicatesFor([]);
		const v = placeholderValues(spec, p);
		return removalCommands(v, p, spec)
			.filter((c) =>
				variant === "sweep" ? c.kind === "sweep" : c.kind !== "sweep",
			)
			.map((c) => c.command)
			.join("\n");
	}
	if (variant !== undefined)
		throw new Error(
			`ManualSteps: variant="${variant}" is remove, sweep or script`,
		);
	if (!step)
		throw new Error(
			'ManualSteps: give step="…" or variant="remove|sweep|script"',
		);
	if (CONTAINER_PARTS.includes(step)) {
		if (field !== "create") {
			throw new Error(
				`ManualSteps: step="${step}" is a part of the container step's create; its ${field} is step="container-step"`,
			);
		}
		const p = predicatesFor(
			step === "env-optional" ? ["floorHz", "triggers", "token"] : [],
		);
		const v = placeholderValues(spec, p);
		const container = spec.steps.find((x) => x.id === "container");
		if (!container)
			throw new Error("ManualSteps: the spec has no container step");
		const kind =
			step === "container"
				? "container-add"
				: step === "env-optional"
					? "env"
					: step;
		const parts = splitCreate(container, v, p).filter(
			(c) =>
				c.kind === kind &&
				(step === "env-optional"
					? c.when !== ""
					: step === "env"
						? c.when === ""
						: true),
		);
		if (parts.length === 0)
			throw new Error(
				`ManualSteps: step="${step}" renders nothing with source="${source}"`,
			);
		return parts.map((c) => c.command).join("\n");
	}
	const id = step === "container-step" ? "container" : step;
	const s = spec.steps.find((x) => x.id === id);
	if (!s) {
		throw new Error(
			`ManualSteps: no step "${step}"; the spec has ${spec.steps.map((x) => x.id).join(", ")}, and the container step's parts are ${CONTAINER_PARTS.join(", ")}`,
		);
	}
	if (!["create", "check", "owned", "remove", "present"].includes(field)) {
		throw new Error(
			`ManualSteps: field="${field}" is create, check, owned, remove or present`,
		);
	}
	const p = predicatesFor(id.startsWith("expose-") ? ["expose"] : []);
	if (!holds(s.when, p))
		throw new Error(
			`ManualSteps: step="${step}" does not apply with these parts`,
		);
	const v = placeholderValues(spec, p);
	const code = renderText(
		/** @type {Fragment[] | undefined} */ (s[/** @type {"create"} */ (field)]),
		v,
		p,
	);
	if (code === "")
		throw new Error(`ManualSteps: step="${step}" has no ${field} command`);
	return code;
}

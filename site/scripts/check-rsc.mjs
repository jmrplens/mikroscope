#!/usr/bin/env node
/**
 * The script generator prints what `mikroscope plan --rsc` prints.
 *
 * The generator (install/generator) and the manual install pages render
 * RouterOS commands in the browser and at build time with src/lib/rsc.mjs,
 * from the steps spec cmd/gen_rsc exports (src/data/rsc/spec.json). A
 * renderer written twice drifts, so this holds the JavaScript one to the Go
 * one on every case of the golden matrix (src/data/rsc/cases.json, from
 * internal/router/testdata/cases.json), offline and without a build:
 *
 *   1. the values: resolve() derives, for the case's option object, exactly
 *      the placeholder values Go computed (the /30's ends, the memory limit,
 *      the registry reference, the tag, the paths, the manifest…);
 *   2. the script: render() equals `plan --rsc`'s output byte for byte, both
 *      the copy gen_rsc wrote for the site (cases/<id>.rsc) and Go's own
 *      golden (internal/router/testdata/golden/<id>.rsc.txt);
 *   3. every step: its check, create, owned, present and remove commands, as
 *      the plan golden (<id>.plan.txt) prints them, so the manual pages and
 *      the uninstall script, which render those, are held too; and the
 *      uninstall script removes the steps in the order the golden's removal
 *      listing names them;
 *   4. the command line: cliArgs() and cliEnv() equal Go's CLIArgs and
 *      CLIEnv, so the command the page shows renders the script it shows;
 *   5. the container step splits into commands the manual pages know, and
 *      joins back into the command the script carries;
 *   6. the spec: its version is VERSION; every predicate it names has a
 *      renderer here; every value it names has a placeholder for the manual
 *      pages, and each step renders with them;
 *   7. the form: every `data-opt` field of ScriptGenerator.astro is a key of
 *      spec.defaults, and every key of spec.defaults has a field. A flag the
 *      CLI does not have cannot be offered, and one it gains cannot be left
 *      out. The registries it offers are the ones .goreleaser.yaml publishes
 *      the agent image to.
 *
 * Usage: node scripts/check-rsc.mjs
 */
import { existsSync, readFileSync } from "node:fs";
import path from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";
import { isDeepStrictEqual } from "node:util";

import {
	AGENT_IMAGES,
	PREDICATES,
	cliArgs,
	cliEnv,
	holds,
	placeholderValues,
	planSteps,
	removalCommands,
	render,
	renderText,
	renderUninstall,
	resolve,
	splitCreate,
} from "../src/lib/rsc.mjs";

const SITE = path.dirname(path.dirname(fileURLToPath(import.meta.url)));
const REPO = path.dirname(SITE);
const DATA = path.join(SITE, "src/data/rsc");
const GOLDEN = path.join(REPO, "internal/router/testdata/golden");
const COMPONENT = path.join(SITE, "src/components/ScriptGenerator.astro");

/** @type {import("../src/lib/rsc.mjs").Spec} */
const spec = JSON.parse(readFileSync(path.join(DATA, "spec.json"), "utf8"));
/** @type {{ id: string, options: Record<string, string | number | boolean>, cliArgs: string, cliEnv?: string[], values: Record<string, string> }[]} */
const cases = JSON.parse(readFileSync(path.join(DATA, "cases.json"), "utf8"));

/** @type {string[]} */
const problems = [];
const fail = (/** @type {string} */ msg) => problems.push(msg);

/** The first line two texts differ on, for a message a person can act on. */
function firstDiff(/** @type {string} */ got, /** @type {string} */ want) {
	const a = got.split("\n");
	const b = want.split("\n");
	for (let i = 0; i < Math.max(a.length, b.length); i++) {
		if (a[i] !== b[i]) {
			const x = a[i] ?? "";
			const y = b[i] ?? "";
			let col = 0;
			while (col < x.length && x[col] === y[col]) col++;
			const around = (/** @type {string} */ l) =>
				JSON.stringify(l.slice(Math.max(0, col - 60), col + 60));
			return `line ${i + 1}, column ${col + 1}:\n      got  …${around(x)}…\n      want …${around(y)}…`;
		}
	}
	return "(same lines, different bytes)";
}

/** writePlan's step part, as internal/router/golden_test.go prints it. */
function planText(/** @type {import("../src/lib/rsc.mjs").Step[]} */ steps) {
	let out = "";
	steps.forEach((s, i) => {
		out += `${i + 1}. ${s.name}\n`;
		for (const key of /** @type {const} */ ([
			"check",
			"create",
			"owned",
			"present",
			"remove",
		])) {
			if (s[key] !== "") out += `   ${key.padEnd(7)} ${s[key]}\n`;
		}
		out += "\n";
	});
	return out;
}

// 6. the spec
const version = readFileSync(path.join(REPO, "VERSION"), "utf8").trim();
if (spec.version !== version) {
	fail(
		`spec.json is for ${spec.version} and VERSION is ${version}: run \`make gen-rsc\` from the repository root`,
	);
}
for (const name of spec.predicates) {
	if (!PREDICATES[name])
		fail(
			`spec.json names the predicate "${name}", which src/lib/rsc.mjs does not render`,
		);
}
for (const name of Object.keys(PREDICATES)) {
	if (!spec.predicates.includes(name))
		fail(
			`src/lib/rsc.mjs renders the predicate "${name}", which spec.json no longer names`,
		);
}
for (const remote of [true, false]) {
	/** @type {Record<string, boolean>} */
	const p = Object.fromEntries(spec.predicates.map((n) => [n, true]));
	p.remote = remote;
	p.tar = !remote;
	try {
		const v = placeholderValues(spec, p);
		for (const s of spec.steps) {
			for (const field of /** @type {const} */ ([
				"name",
				"check",
				"create",
				"owned",
				"remove",
				"present",
			])) {
				renderText(s[field], v, p);
			}
		}
		splitCreate(
			/** @type {any} */ (spec.steps.find((s) => s.id === "container")),
			v,
			p,
		);
	} catch (err) {
		fail(
			`the manual pages' placeholders: ${/** @type {Error} */ (err).message}`,
		);
	}
}

// 1–5. every case
for (const c of cases) {
	const where = `case ${c.id}`;
	const r = resolve(c.options, spec);
	if (!r.values || !r.predicates) {
		fail(
			`${where}: resolve() refuses the case: ${r.errors.map((e) => `${e.field} ${e.code}`).join(", ")}`,
		);
		continue;
	}
	if (!isDeepStrictEqual(r.values, c.values)) {
		const keys = new Set([...Object.keys(r.values), ...Object.keys(c.values)]);
		for (const k of keys) {
			if (r.values[k] !== c.values[k]) {
				fail(
					`${where}: value ${k} is ${JSON.stringify(r.values[k])}, Go computed ${JSON.stringify(c.values[k])}`,
				);
			}
		}
	}
	const script = render(c.options, spec);
	for (const file of [
		path.join(DATA, "cases", `${c.id}.rsc`),
		path.join(GOLDEN, `${c.id}.rsc.txt`),
	]) {
		if (!existsSync(file)) {
			fail(`${where}: ${path.relative(REPO, file)} is missing`);
			continue;
		}
		const want = readFileSync(file, "utf8");
		if (script !== want)
			fail(
				`${where}: render() differs from ${path.relative(REPO, file)} at ${firstDiff(script, want)}`,
			);
	}
	const planFile = path.join(GOLDEN, `${c.id}.plan.txt`);
	if (existsSync(planFile)) {
		const golden = readFileSync(planFile, "utf8");
		const at = golden.indexOf("removal listing:\n");
		const steps = planSteps(r.values, r.predicates, spec);
		const got = planText(steps);
		if (at < 0)
			fail(`${where}: ${path.relative(REPO, planFile)} has no removal listing`);
		else if (got !== golden.slice(0, at)) {
			fail(
				`${where}: the steps differ from ${path.relative(REPO, planFile)} at ${firstDiff(got, golden.slice(0, at))}`,
			);
		}
		// The listing names the steps uninstall removes, in its order, after a
		// count line; the sweep and the manifest follow in prose.
		const listed = golden
			.slice(at)
			.split("\n")
			.filter((l) => l.startsWith("    "))
			.map((l) => l.trim());
		const removed = removalCommands(r.values, r.predicates, spec)
			.filter((x) => x.kind === "step")
			.map((x) => x.name);
		if (!isDeepStrictEqual(listed, removed)) {
			fail(
				`${where}: the uninstall script removes ${JSON.stringify(removed)}, the removal listing names ${JSON.stringify(listed)}`,
			);
		}
	} else {
		fail(`${where}: ${path.relative(REPO, planFile)} is missing`);
	}
	try {
		const uninstall = renderUninstall(c.options, spec);
		if (!uninstall.startsWith("# mikroscope ") || !uninstall.endsWith("}\n")) {
			fail(`${where}: the uninstall script is not one block`);
		}
	} catch (err) {
		fail(`${where}: renderUninstall(): ${/** @type {Error} */ (err).message}`);
	}
	const args = cliArgs(c.options, spec);
	if (args !== c.cliArgs)
		fail(
			`${where}: cliArgs() is ${JSON.stringify(args)}, Go's CLIArgs ${JSON.stringify(c.cliArgs)}`,
		);
	const env = cliEnv(c.options, spec);
	if (!isDeepStrictEqual(env, c.cliEnv ?? [])) {
		fail(
			`${where}: cliEnv() is ${JSON.stringify(env)}, Go's CLIEnv ${JSON.stringify(c.cliEnv ?? [])}`,
		);
	}
	const container = spec.steps.find((s) => s.id === "container");
	if (container && holds(container.when, r.predicates)) {
		try {
			const parts = splitCreate(container, r.values, r.predicates);
			const joined = parts.map((x) => x.command).join("; ");
			const step = planSteps(r.values, r.predicates, spec).find(
				(s) => s.id === "container",
			);
			if (joined !== step?.create)
				fail(
					`${where}: the container step's commands do not join back into its create command`,
				);
		} catch (err) {
			fail(`${where}: ${/** @type {Error} */ (err).message}`);
		}
	}
}

// 7. the form
if (existsSync(COMPONENT)) {
	const source = readFileSync(COMPONENT, "utf8");
	const fields = new Set(
		[...source.matchAll(/data-opt="([A-Za-z]+)"/g)].map((m) => m[1]),
	);
	for (const f of fields) {
		if (!(f in spec.defaults))
			fail(
				`ScriptGenerator.astro has a field data-opt="${f}", which is not an option of the spec: the CLI has no such flag`,
			);
	}
	for (const k of Object.keys(spec.defaults)) {
		if (!fields.has(k))
			fail(
				`the spec has the option ${k} and ScriptGenerator.astro no field for it (data-opt="${k}")`,
			);
	}
} else {
	fail(`${path.relative(REPO, COMPONENT)} is missing`);
}

const goreleaser = readFileSync(path.join(REPO, ".goreleaser.yaml"), "utf8");
for (const img of AGENT_IMAGES) {
	if (!goreleaser.includes(`"${img.published}"`)) {
		fail(
			`the generator offers ${img.ref}, and .goreleaser.yaml publishes no image "${img.published}"`,
		);
	}
}

if (problems.length > 0) {
	console.error(`rsc:check: ${problems.length} problem(s)\n`);
	for (const p of problems) console.error(`  ${p}`);
	process.exit(1);
}
console.log(
	`rsc:check: ${cases.length} cases render as \`mikroscope plan --rsc\` does, byte for byte (script, steps, values, command line); the form's fields are the spec's ${Object.keys(spec.defaults).length} options`,
);

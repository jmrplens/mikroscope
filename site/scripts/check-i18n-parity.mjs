#!/usr/bin/env node
/**
 * Keeps the Spanish tree structurally identical to the English one.
 *
 * What it compares is structure, never prose: the set of pages, the shape of
 * the frontmatter, the ladder of headings, and the component tags a page
 * invokes. A translated paragraph that has gone stale passes this gate and
 * always will — no checker can tell a deliberate rewording from a forgotten
 * one, and pretending otherwise would make the gate a thing people route
 * around.
 *
 * What it does catch is the failure that actually happens: a page added in one
 * language and not the other, a `hero` block that exists in English only, a
 * heading dropped in translation so the two pages no longer have the same
 * outline, a component left out so the Spanish page silently renders less.
 *
 * Adding a locale is one edit, to LOCALES below.
 *
 * Usage: node scripts/check-i18n-parity.mjs
 */

import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { parseDocument, isMap, isSeq, isPair } from "yaml";

const LOCALES = ["es"];
const siteRoot = path.dirname(
	fileURLToPath(new URL("../package.json", import.meta.url)),
);
const DOCS_DIR = path.join(siteRoot, "src", "content", "docs");

/* Anchored, so a `---` used as a horizontal rule inside the body is not
 * mistaken for the end of the frontmatter. */
const FRONTMATTER = /^---[ \t]*\r?\n([\s\S]*?)\r?\n---[ \t]*(?:\r?\n|$)/;

/* -------------------------------------------------------------- the corpus */

/** Every page under the docs collection, as paths relative to it. */
function pages(dir = DOCS_DIR, prefix = "") {
	const out = [];
	for (const entry of fs
		.readdirSync(dir, { withFileTypes: true })
		.sort((a, b) => a.name.localeCompare(b.name))) {
		const rel = prefix ? `${prefix}/${entry.name}` : entry.name;
		if (entry.isDirectory())
			out.push(...pages(path.join(dir, entry.name), rel));
		else if (/\.mdx?$/.test(entry.name)) out.push(rel);
	}
	return out;
}

const localeOf = (page) =>
	LOCALES.find((l) => page === l || page.startsWith(`${l}/`)) ?? null;
const withoutLocale = (page) => page.replace(/^[^/]+\//, "");

/* --------------------------------------------------------------- the shape */

/**
 * The key paths a frontmatter block declares, with sequence indices collapsed:
 * `hero.actions[].text` rather than `hero.actions[0].text`. Values are never
 * compared — the value IS the translation.
 *
 * Sequence lengths are compared separately, because two `actions` entries in
 * English and one in Spanish is a real difference that identical key paths
 * would hide.
 */
function frontmatterShape(source, problems, page) {
	const match = FRONTMATTER.exec(source);
	if (match === null) {
		problems.push(`${page}: no frontmatter`);
		return null;
	}
	const doc = parseDocument(match[1]);
	if (doc.errors.length > 0) {
		problems.push(`${page}: unreadable frontmatter — ${doc.errors[0].message}`);
		return null;
	}
	const keys = new Set();
	const counts = new Map();
	const walk = (node, trail) => {
		if (isMap(node)) {
			for (const item of node.items) {
				if (!isPair(item)) continue;
				const here = trail
					? `${trail}.${item.key.value}`
					: String(item.key.value);
				keys.add(here);
				walk(item.value, here);
			}
		} else if (isSeq(node)) {
			counts.set(trail, node.items.length);
			for (const item of node.items) walk(item, `${trail}[]`);
		}
	};
	walk(doc.contents, "");
	return { keys, counts };
}

/** The body, with fenced code blocks blanked out so nothing inside one counts. */
function body(source) {
	const after = source.replace(FRONTMATTER, "");
	let fenced = false;
	return after
		.split("\n")
		.map((line) => {
			if (/^\s*(```|~~~)/.test(line)) {
				fenced = !fenced;
				return "";
			}
			return fenced ? "" : line;
		})
		.join("\n");
}

/** The sequence of heading levels, e.g. [2, 2, 3, 2]. */
const headings = (text) =>
	[...text.matchAll(/^(#{1,6})\s+\S/gm)].map((m) => m[1].length);

/**
 * The component tags a page invokes, counted by name.
 *
 * This reads tags out of the text rather than out of an MDX syntax tree, so it
 * sees a component written plainly and would miss one produced inside a
 * JavaScript expression. That is a real limit and it is stated here rather than
 * hidden: if this project starts generating components from expressions, this
 * function has to grow an mdast walk.
 */
function components(text) {
	const counts = new Map();
	for (const m of text.matchAll(/<([A-Z][A-Za-z0-9]*)\b/g)) {
		counts.set(m[1], (counts.get(m[1]) ?? 0) + 1);
	}
	return counts;
}

/**
 * The attributes whose VALUES are structure rather than prose: which
 * measurement a page cites (`id`), which campaign (`of`), which runs (`only`),
 * which router command (`command`), which kind of claim (`variant`), whose
 * memory flags (`run`). Tag counts alone let a Spanish twin cite a different
 * number and stay green.
 */
// `type` is here because an <Aside type="note"> turned into type="danger" in
// one language passed this gate until 2026-09-15 (checked on a copy). `title`
// is not, because it is translated.
// `origin` and `date` (FaultSignature), `section` (DashboardPanels), `store`
// and `show` (DashboardCount, AlertRules) choose what a component renders, so
// they are structure too.
const STRUCTURAL_ATTRS = [
	"id",
	"of",
	"only",
	"command",
	"variant",
	"run",
	"type",
	"origin",
	"date",
	"section",
	"store",
	"show",
];

/**
 * An attribute value with its spelling taken out, so only its meaning is
 * compared: `id="x"`, `id='x'` and `id={"x"}` are all `x`, and
 * `only={['10hz']}` is `only={["10hz"]}`. Quote style is a formatting choice,
 * and a gate that fails on it is a gate people learn to route around.
 */
function normaliseValue(raw) {
	const unquote = (v) => (/^(["'])[^"']*\1$/.test(v) ? v.slice(1, -1) : null);
	const bare = unquote(raw);
	if (bare !== null) return bare;
	const inner = raw.slice(1, -1).replace(/\s+/g, "");
	return unquote(inner) ?? `{${inner.replace(/'/g, '"')}}`;
}

/**
 * Each capitalised tag's occurrences, in order, as the structural attributes
 * each one carries: `{ RunsTable: ['', 'only={["10hz"]}'] }`.
 *
 * Every occurrence is listed, including one with none of those attributes, so
 * the nth `<RunsTable>` in English is compared with the nth `<RunsTable>` in
 * the twin. Pairing the nth attributed tag of any kind instead named the wrong
 * component: an `only` added to a bare `<RunsTable />` was reported against
 * the `<RunFlags run="10hz">` that happened to share its index.
 *
 * Text-level, like `components` above, and with one more stated limit: an
 * expression value is read up to its first `}`, which is enough for the flat
 * arrays and strings these props take and would misread a nested object.
 */
function structuralAttributes(text) {
	const out = new Map();
	for (const tag of text.matchAll(/<([A-Z][A-Za-z0-9]*)\b([^>]*)>/g)) {
		const attrs = new RegExp(
			`(?:^|\\s)(${STRUCTURAL_ATTRS.join("|")})=("[^"]*"|'[^']*'|\\{[^}]*\\})`,
			"g",
		);
		const found = [...tag[2].matchAll(attrs)].map(
			(m) => `${m[1]}=${normaliseValue(m[2])}`,
		);
		if (!out.has(tag[1])) out.set(tag[1], []);
		out.get(tag[1]).push(found.join(" "));
	}
	return out;
}

/**
 * Components that render inline, inside a sentence.
 *
 * Prettier's MDX parser reads a line that starts with a capitalised tag as a
 * JSX block, even with prose after it on the same line, and `pnpm format`
 * then splits the paragraph there and escapes any `**` it spans. That shipped
 * once: the Spanish cost page rendered literal asterisks and broke its key
 * sentence in two, with every other gate green. So an inline component never
 * starts a line; wrap the sentence one word earlier.
 */
const INLINE_COMPONENTS = [
	"Measured",
	"RunFlags",
	"Verified",
	"PrivilegedOnly",
	"DashboardCount",
];

/** 1-based line numbers where an inline component opens the line. */
function inlineAtLineStart(text) {
	const opener = new RegExp(`^\\s*<(?:${INLINE_COMPONENTS.join("|")})\\b`);
	return text
		.split("\n")
		.map((line, i) => (opener.test(line) ? i + 1 : 0))
		.filter((n) => n > 0);
}

/* -------------------------------------------------------------- the report */

function main() {
	if (!fs.existsSync(DOCS_DIR)) {
		console.error(`No docs collection at ${DOCS_DIR}`);
		process.exit(1);
	}

	const all = pages();
	const english = all.filter((p) => localeOf(p) === null);
	const translated = new Map();
	for (const p of all) {
		const locale = localeOf(p);
		if (locale !== null) translated.set(p, locale);
	}

	const missing = [];
	const orphans = [];
	const frontmatterDiff = [];
	const headingDiff = [];
	const componentDiff = [];
	const attributeDiff = [];
	const unreadable = [];
	const markdownComponents = [];
	const inlineStarts = [];

	for (const page of all) {
		const text = body(fs.readFileSync(path.join(DOCS_DIR, page), "utf8"));
		// body() drops the frontmatter, so its line numbers are offset; report the
		// source line, which is what an editor jumps to.
		const offset =
			fs.readFileSync(path.join(DOCS_DIR, page), "utf8").split("\n").length -
			text.split("\n").length;
		for (const n of inlineAtLineStart(text))
			inlineStarts.push(`${page}:${n + offset}`);
	}

	for (const [page] of translated) {
		if (!english.includes(withoutLocale(page))) orphans.push(page);
	}

	for (const page of english) {
		const source = fs.readFileSync(path.join(DOCS_DIR, page), "utf8");
		const sourceShape = frontmatterShape(source, unreadable, page);
		const sourceBody = body(source);
		const sourceHeadings = headings(sourceBody);
		const sourceComponents = components(sourceBody);

		if (page.endsWith(".md") && sourceComponents.size > 0)
			markdownComponents.push(page);

		for (const locale of LOCALES) {
			const twinPath = `${locale}/${page}`;
			const onDisk = path.join(DOCS_DIR, twinPath);
			if (!fs.existsSync(onDisk)) {
				missing.push(twinPath);
				continue;
			}
			const twin = fs.readFileSync(onDisk, "utf8");
			const twinShape = frontmatterShape(twin, unreadable, twinPath);
			const twinBody = body(twin);

			if (sourceShape !== null && twinShape !== null) {
				const onlyEn = [...sourceShape.keys].filter(
					(k) => !twinShape.keys.has(k),
				);
				const onlyTr = [...twinShape.keys].filter(
					(k) => !sourceShape.keys.has(k),
				);
				for (const k of onlyEn)
					frontmatterDiff.push(`${twinPath}: ${k} is in en and not ${locale}`);
				for (const k of onlyTr)
					frontmatterDiff.push(`${twinPath}: ${k} is in ${locale} and not en`);
				for (const [k, n] of sourceShape.counts) {
					const m = twinShape.counts.get(k);
					if (m !== undefined && m !== n) {
						frontmatterDiff.push(
							`${twinPath}: ${k} has ${n} entries in en, ${m} in ${locale}`,
						);
					}
				}
			}

			const twinHeadings = headings(twinBody);
			if (sourceHeadings.join(" ") !== twinHeadings.join(" ")) {
				headingDiff.push(
					`${twinPath}: [${sourceHeadings.join(" ")}] in en, [${twinHeadings.join(" ")}] in ${locale}`,
				);
			}

			const twinComponents = components(twinBody);
			for (const name of new Set([
				...sourceComponents.keys(),
				...twinComponents.keys(),
			])) {
				const a = sourceComponents.get(name) ?? 0;
				const b = twinComponents.get(name) ?? 0;
				if (a !== b)
					componentDiff.push(
						`${twinPath}: <${name}> ${a} in en, ${b} in ${locale}`,
					);
			}

			const sourceAttrs = structuralAttributes(sourceBody);
			const twinAttrs = structuralAttributes(twinBody);
			// A tag count that differs is already reported above; here the
			// occurrences both sides have are compared, first difference per tag.
			for (const [name, en] of sourceAttrs) {
				const tr = twinAttrs.get(name) ?? [];
				const length = Math.min(en.length, tr.length);
				for (let i = 0; i < length; i += 1) {
					if (en[i] !== tr[i]) {
						attributeDiff.push(
							`${twinPath}: <${name}> #${i + 1} has ${en[i] || "no structural attributes"} in en, ${tr[i] || "no structural attributes"} in ${locale}`,
						);
						break;
					}
				}
			}
		}
	}

	const buckets = [
		[
			"Translated pages missing",
			missing,
			"write the twin, or delete the English page",
		],
		[
			"Translated pages whose English source is gone",
			orphans,
			"delete it, or restore the source",
		],
		[
			"Frontmatter shape mismatches",
			frontmatterDiff,
			"the keys are structure, only the values translate",
		],
		[
			"Heading structure mismatches",
			headingDiff,
			"the two outlines have to match; translate the text, not the ladder",
		],
		[
			"Component invocation mismatches",
			componentDiff,
			"a component left out renders less on one side",
		],
		[
			"Component attribute mismatches",
			attributeDiff,
			"a twin cites the same measurement, campaign, runs, command, variant and run flags as its source",
		],
		[
			"Inline components opening a line",
			inlineStarts,
			"Prettier will split the paragraph there; move one word from the line above onto this one",
		],
		[
			"Component tags in a .md page",
			markdownComponents,
			"a component in .md renders as raw HTML, i.e. nothing — rename it .mdx",
		],
		[
			"Pages that could not be read",
			unreadable,
			"fix the frontmatter before this gate can say anything about the page",
		],
	];

	let failures = 0;
	for (const [title, items, hint] of buckets) {
		if (items.length === 0) continue;
		failures += items.length;
		console.error(`\n  ${title} (${items.length})`);
		for (const item of items) console.error(`    ${item}`);
		console.error(`    → ${hint}`);
	}

	if (failures > 0) {
		console.error(`\n✗ ${failures} i18n parity problems`);
		process.exit(1);
	}
	console.log(
		`✓ ${english.length} English pages, each with a structurally identical twin in ${LOCALES.map((l) => l).join(", ")}.`,
	);
}

main();

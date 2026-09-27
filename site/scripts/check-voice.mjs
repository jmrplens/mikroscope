// The documentation reads as a tool's documentation, and its proof lives on
// the evidence pages.
//
// A guide, a reference page or an explanation states what the tool does:
// short, task first, headings that name what a section holds. The device the
// fact was checked on, the RouterOS release, the date, the conditions and
// what was not tried belong on the evidence pages (src/lib/voice.mjs lists
// them: Tested on, the cost pages, the case studies, the test suites), and a
// guide links there. Nothing else in the pipeline reads prose for this, and a
// dated sentence on a guide is a valid page, so this is what notices one.
//
// It reads the MDX sources, English and Spanish, and the build when there is
// one. The build shows what the source does not: the words a component or a
// data file prints (a <Verified> on a guide, a doctor check's text, the
// landing's copy). A build finding whose text is also in the page's source is
// left to the source finding.
//
// On guides (every page that is not evidence or about):
//
//   date                an ISO date, or a month and year
//   routeros-version    "RouterOS 7.x[.y]" that is not the minimum
//                       (ROUTEROS_MINIMUM) nor followed by "or later"
//   patch-version       a bare RouterOS 7.x.y
//   kernel-version      "Linux 5.6.3", "kernel 5.6" and the like
//   device-model        RB5009 and the other MikroTik model names, Cortex-A72
//   reference-device    "reference device/router/store/deployment/board"
//   owner               the project's owner as a party ("the owner's router")
//   release-history     "Since 1.2.2", "Fixed in 1.0.3", "desde la 1.1.0"
//   evidence-component  <Provenance>, <Verified>, <NotClaimed>, <RunsTable>,
//                       <RunFlags>, the registers, <FaultSignature date|origin>
//
// On every page:
//
//   label-words         a heading of more than six words
//   label-comma         a comma in a heading, except in a list ("A, B and C")
//   label-question      a question mark in a heading
//   label-question-word a heading that opens with What/Why/How/When/Where/Which
//                       (Qué/Por qué/Cómo/Cuándo/Dónde/Cuál)
//   opening             a page or description that opens "This page…"
//   doctype-missing     no `docType` in the frontmatter
//   doctype-mismatch    a docType that disagrees with src/lib/voice.mjs, or
//                       with the page's twin
//   evidence-link       (build) a <TestedOn> or <Measured> link to an entry
//                       Tested on does not have
//
// Not read: fenced code, link targets and URLs, heading ids pinned with
// `{#id}`, component props that are not text, the inside of <BoardTable>,
// anything marked `data-voice-exempt` in the build, and the "Last updated"
// footer. Inline code is read for dates and versions, not for the words
// "owner" and "reference …", which name things in code (`owner/name:1.0.0`).
// EXEMPT below lists the rest, each with its reason.
//
// Whether a finding fails is src/lib/voice.mjs's DEFAULT_MODE, "error";
// MS_VOICE=warn or MS_VOICE=off overrides it for a run.
//
// Usage:
//   node scripts/check-voice.mjs                     every page, source and build
//   node scripts/check-voice.mjs install/firewall    one page and its twin
//   node scripts/check-voice.mjs src/content/docs/es/cost/index.mdx
//   node scripts/check-voice.mjs --summary           counts per rule and page
//   node scripts/check-voice.mjs --src-only          without reading dist/
//   node scripts/check-voice.mjs --dist <dir>        another build
import { existsSync, readFileSync, readdirSync } from "node:fs";
import { join, relative, resolve, sep } from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";

import { parse as parseYaml } from "yaml";

import "./data-hooks.mjs";
import { isRedirectStub } from "./redirect-stub.mjs";
import { readRelease } from "../src/lib/release.mjs";
import {
	DOC_TYPES,
	ROUTEROS_MINIMUM,
	TESTED_ON_SLUG,
	slugOf,
	voiceKind,
	voiceMode,
} from "../src/lib/voice.mjs";

// Loaded after data-hooks.mjs, which teaches Node the `?raw` imports it uses.
const { sectionNames } = await import("../src/data/dashboards.ts");

const SITE = fileURLToPath(new URL("..", import.meta.url));
const REPO = fileURLToPath(new URL("../..", import.meta.url));
const DOCS = join(SITE, "src", "content", "docs");

/* ------------------------------------------------------------ arguments */

const args = process.argv.slice(2);
let summary = false;
let srcOnly = false;
let distDir = join(SITE, "dist");
const filters = [];
for (let i = 0; i < args.length; i += 1) {
	const arg = args[i];
	if (arg === "--summary") summary = true;
	else if (arg === "--src-only") srcOnly = true;
	else if (arg === "--dist") {
		i += 1;
		distDir = resolve(args[i]);
	} else if (arg.startsWith("--")) {
		console.error(`[voice] unknown option ${arg}`);
		process.exit(2);
	} else filters.push(arg);
}

const mode = voiceMode();
if (mode === "off") {
	console.log("[voice] MS_VOICE=off: not checked.");
	process.exit(0);
}

/* -------------------------------------------------------------- the rules */

const QUESTION_WORDS = {
	en: /^(?:what|why|how|when|where|which|who)\b/i,
	es: /^(?:qué|por qué|cómo|cuándo|dónde|cuál|cuáles|quién|quiénes)(?=\s|$)/i,
};

const MONTHS =
	"January|February|March|April|May|June|July|August|September|October|November|December";
const MESES =
	"enero|febrero|marzo|abril|mayo|junio|julio|agosto|septiembre|setiembre|octubre|noviembre|diciembre";

// A requirement, where a release is named as a floor.
const LATER =
	/^\s*(?:or later|and later|or newer|or above|\+|o posterior|o superior|o más reciente|y posteriores)/i;

/**
 * The text rules for guides. `code: false` rules read prose with inline code
 * blanked. `test` returns the match to report, or null; `keep` lets a match go.
 */
const TEXT_RULES = [
	{
		id: "date",
		code: true,
		re: new RegExp(
			`\\b\\d{4}-\\d{2}-\\d{2}(?:[T ]\\d{2}:\\d{2}(?::\\d{2})?(?:\\.\\d+)?Z?)?\\b|\\b(?:${MONTHS})\\s+(?:\\d{1,2},\\s+)?\\d{4}\\b|\\b(?:${MESES})\\s+(?:de\\s+)?\\d{4}\\b`,
			"gi",
		),
	},
	{
		id: "routeros-version",
		code: true,
		re: /\bRouterOS\s+v?(\d+\.\d+(?:\.\d+)?)(?!\d|\.\d)/g,
		keep: (m, line) =>
			m[1] === ROUTEROS_MINIMUM ||
			LATER.test(line.slice(m.index + m[0].length)),
	},
	{
		id: "patch-version",
		code: true,
		re: /(?<![\w.]|RouterOS\s+v?)7\.\d{1,2}\.\d{1,2}(?!\d|\.\d)/g,
	},
	{
		id: "kernel-version",
		code: true,
		re: /\b(?:Linux|kernel|núcleo)\s+v?(\d+\.\d+(?:\.\d+)?)(?!\d|\.\d)/gi,
		keep: (m, line) => LATER.test(line.slice(m.index + m[0].length)),
	},
	{
		id: "device-model",
		code: true,
		re: /\b(?:RB\d{3,4}|CCR\d{4}|CRS\d{3,4}|L009|hAP|hEX|cAP|wAP|Chateau)[\w+³²-]*|\bCortex-A\d+\b/g,
	},
	{
		id: "reference-device",
		code: false,
		re: /\breference\s+(?:device|router|store|deployment|board)s?\b|\b(?:router|equipo|almacén|despliegue|dispositivo|placa)\s+de\s+referencia\b/gi,
	},
	{
		id: "owner",
		code: false,
		re: /\b(?:the|this|our)\s+owner\b|\bowner['’]s\b|\(owner,|\b(?:el|del|al|este|ese)\s+propietario\b|\(propietario,/gi,
	},
	{
		id: "release-history",
		code: true,
		re: /\b(?:since|until|up to|through|before|fixed in|as of|introduced in|added in|removed in|changed in|desde|hasta|antes de|corregido en|a partir de|introducido en|añadido en|eliminado en)\s+(?:la\s+|mikroscope\s+|release\s+|version\s+|versión\s+)?v?\d+\.\d+\.\d+/gi,
	},
];

const EVIDENCE_COMPONENTS =
	/<(Provenance|Verified|NotClaimed|RunsTable|RunFlags|CampaignRegister|VerifiedRegister)\b|<FaultSignature\b(?:"[^"]*"|'[^']*'|\{[^}]*\}|[^>"'{])*?\s(date|origin)=/g;

/**
 * What is allowed, and why. `page` is a locale-independent slug or "*";
 * `rule` a rule id or "*"; `text` the exact match or heading, or a RegExp.
 * A heading exempted here is exempted on the Spanish twin too, by position.
 */
const EXEMPT = [
	// The landing's "Current release: x.y.z, <date>" line is release metadata,
	// like Starlight's "Last updated", and follows VERSION and the CHANGELOG.
	{
		page: "",
		rule: "date",
		text: readRelease(REPO).date,
		reason: "the release line on the landing",
	},
	// Labels the D3 map of tool-docs-spec chose: each opens with a question
	// word and still names what its section holds.
	...[
		["start/questions", "Why RouterOS 7.24"],
		["start/compared", "When to use another tool"],
		["dashboards/import-and-check", "What check verifies"],
		["security", "What runs where"],
		["limits/privileged", "What it enables"],
		["limits/privileged", "What it does not enable"],
		["security/api-user", "What read can see"],
		["how-it-works", "How it works"],
		["how-it-works", "Cómo funciona"],
		["how-it-works", "What the agent reads"],
		["how-it-works", "What the collector adds"],
		["reference/port-names", "Why names differ"],
		["reference/port-names", "How the agent uses it"],
		["sinks/derive", "Where values are derived"],
		["playbooks/idle", "What to look at"],
		["playbooks/cpu", "What to look at"],
		["playbooks/packet-flood", "What to look at"],
		["playbooks/flash-wear", "Why --ephemeral"],
		["about/lineage", "Why vendored"],
		["sinks/device-info", "When it is sent"],
	].map(([page, text]) => ({
		page,
		rule: "label-question-word",
		text,
		reason: "a label the D3 map chose",
	})),
];

/** The glossary's headings are its terms. */
const TERM_PAGES = new Set(["reference/glossary"]);

/**
 * The troubleshooting headings that are the error a reader searches for,
 * verbatim, and so cannot be relabelled.
 */
const ERROR_STRINGS = [
	"device-mode container=yes",
	"No container package",
	"unknown parameter privileged",
	"exec format error",
	"flightsql: Unauthenticated",
	"tls: first record",
	"sink does not know the address",
	"is a write URL",
	"standard_conforming_strings",
	"does not exist",
	"grafana: could not publish",
	"uninstall --targets data",
];

/** The dashboards' h3s are Grafana row titles, which must match the JSON. */
const ROW_TITLES = new Set(sectionNames);

/* --------------------------------------------------------------- findings */

/**
 * Whether an EXEMPT entry's text covers a finding's. A string matches with
 * inline-code backticks ignored, so "Why --ephemeral" covers the heading
 * "Why `--ephemeral`", whose code span keeps the `--` from becoming a dash.
 *
 * @param {string | RegExp} pattern
 * @param {string} text
 */
function exemptText(pattern, text) {
	if (pattern instanceof RegExp) return pattern.test(text);
	const bare = (value) => value.replaceAll("`", "");
	return bare(pattern) === bare(text);
}

const findings = [];

/**
 * @param {{ file: string, line?: number, rule: string, text: string, slug: string, lang: string, origin: "src" | "build" }} f
 */
function report(f) {
	const exempt = EXEMPT.some(
		(e) =>
			(e.page === "*" || e.page === f.slug) &&
			(e.rule === "*" || e.rule === f.rule) &&
			exemptText(e.text, f.text),
	);
	if (!exempt) findings.push(f);
}

/* ---------------------------------------------------------------- sources */

/** Every page under the docs collection, as paths relative to it. */
function sourceFiles(dir = DOCS, prefix = "") {
	const out = [];
	for (const entry of readdirSync(dir, { withFileTypes: true })) {
		const rel = prefix ? `${prefix}/${entry.name}` : entry.name;
		if (entry.isDirectory())
			out.push(...sourceFiles(join(dir, entry.name), rel));
		else if (/\.mdx?$/.test(entry.name)) out.push(rel);
	}
	return out.sort();
}

const NOT_A_DOCUMENT = new Set(["404"]);

/** Blank a span, keeping its newlines so line numbers survive. */
const blank = (text) => text.replaceAll(/[^\n]/g, " ");

/**
 * The body with its fenced code, import and export lines and comments
 * blanked, line for line.
 */
function unfenced(body) {
	const lines = body.split("\n");
	let fence = null;
	for (let i = 0; i < lines.length; i += 1) {
		const marker = /^[ \t]*(`{3,}|~{3,})/.exec(lines[i]);
		if (fence !== null) {
			if (
				marker &&
				marker[1][0] === fence[0] &&
				marker[1].length >= fence.length
			)
				fence = null;
			lines[i] = "";
			continue;
		}
		if (marker) {
			fence = marker[1];
			lines[i] = "";
			continue;
		}
		if (/^(?:import|export)\s/.test(lines[i])) lines[i] = "";
	}
	return lines
		.join("\n")
		.replaceAll(/<!--[\s\S]*?-->/g, blank)
		.replaceAll(/\{\/\*[\s\S]*?\*\/\}/g, blank);
}

/**
 * The body with everything that is not read by the text rules blanked,
 * line for line: fenced code, imports, comments, <BoardTable> blocks, tags
 * but for the props a reader sees, link targets and URLs. Inline code comes
 * back in two versions, kept and blanked.
 */
function prose(body) {
	let text = unfenced(body);
	text = text.replaceAll(/<BoardTable\b[\s\S]*?<\/BoardTable>/g, blank);
	// Inline code out of the way of the tag and link patterns, restored after.
	const codes = [];
	text = text.replaceAll(/(`+)([^`\n]*?)\1/g, (_, _ticks, inner) => {
		codes.push(inner);
		return `\u0001${codes.length - 1}\u0002`;
	});
	// Tags: a component or an HTML element becomes the props a reader sees.
	text = text.replaceAll(
		/<\/?[A-Za-z][\w.]*(?:"[^"]*"|'[^']*'|\{[^}]*\}|[^>"'{])*?\/?>/g,
		(tag) => {
			const visible = [
				...tag.matchAll(
					/\b(?:title|label|caption|alt|description)=(?:"([^"]*)"|'([^']*)')/g,
				),
			].map((m) => m[1] ?? m[2]);
			const newlines = (tag.match(/\n/g) ?? []).length;
			return `${visible.join(" ")}${"\n".repeat(newlines)}`;
		},
	);
	text = text
		.replaceAll(/\{#[^}\n]*\}/g, "")
		.replaceAll(/\]\([^)\n]*\)/g, "]")
		.replaceAll(/<https?:\/\/[^>\s]*>/g, "")
		.replaceAll(/https?:\/\/[^\s)\]]+/g, "")
		.replaceAll(/\{"\s*"\}/g, " ");
	const restore = (keep) =>
		text.replaceAll(/\u0001(\d+)\u0002/g, (_, n) =>
			keep ? codes[Number(n)] : " ",
		);
	return {
		withCode: restore(true).split("\n"),
		noCode: restore(false).split("\n"),
	};
}

/** The ATX headings of a body, outside fences, with their line numbers. */
function headings(body, offset) {
	const out = [];
	let fence = null;
	body.split("\n").forEach((line, i) => {
		const marker = /^[ \t]*(`{3,}|~{3,})/.exec(line);
		if (fence !== null) {
			if (
				marker &&
				marker[1][0] === fence[0] &&
				marker[1].length >= fence.length
			)
				fence = null;
			return;
		}
		if (marker) {
			fence = marker[1];
			return;
		}
		const m = /^(#{2,6})\s+(.*?)\s*$/.exec(line);
		if (m) {
			out.push({
				level: m[1].length,
				text: m[2].replace(/\s*\{#[^}]*\}\s*$/, "").trim(),
				line: offset + i + 1,
			});
		}
	});
	return out;
}

/** A heading's words as a reader counts them: a code span is one word. */
function words(text) {
	return text
		.replaceAll(/`[^`]*`/g, "code")
		.replaceAll(/[*_]/g, "")
		.split(/\s+/)
		.filter((w) => /[\p{L}\p{N}]/u.test(w));
}

/**
 * A heading that lists things, "Sensors, slab caches and flash": items split
 * by commas, the last one joined by "and" (y, e, o, u) with no comma before
 * it. Its commas are the list's, not a clause's, so the comma rule lets it be.
 */
const ENUMERATION = /^[^,]+(?:,\s+[^,]+)*(?<!,)\s+(?:and|or|y|e|o|u)\s+[^,]+$/;

/**
 * Checks one heading (or the title) against the label rules.
 *
 * @returns {string[]} the rules it breaks
 */
function labelRules(text, lang) {
	const broken = [];
	const plain = text.replaceAll(/`[^`]*`/g, "code");
	if (words(text).length > 6) broken.push("label-words");
	if (plain.includes(",") && !ENUMERATION.test(plain))
		broken.push("label-comma");
	if (/[?¿]/.test(plain)) broken.push("label-question");
	const opening = plain
		.replace(/^[¿¡]/, "")
		.replace(/^\d+\.\s+/, "")
		.trim();
	if (QUESTION_WORDS[lang].test(opening)) broken.push("label-question-word");
	return broken;
}

/** Whether a heading of a page is exempt from the label rules as a whole. */
function labelExempt(slug, heading) {
	if (TERM_PAGES.has(slug)) return true;
	if (heading.text === "See also" || heading.text === "Véase también")
		return true;
	if (
		slug === "reference/troubleshooting" &&
		heading.level === 3 &&
		ERROR_STRINGS.some((s) => heading.text.includes(s))
	)
		return true;
	if (
		slug === "dashboards" &&
		heading.level === 3 &&
		ROW_TITLES.has(heading.text.replaceAll("`", ""))
	)
		return true;
	return false;
}

const FRONTMATTER = /^---[ \t]*\r?\n([\s\S]*?)\r?\n---[ \t]*(?:\r?\n|$)/;

/** The pages, parsed once: the English twin's headings pair the Spanish ones. */
const pages = sourceFiles()
	.map((rel) => {
		const raw = readFileSync(join(DOCS, rel), "utf8");
		const front = FRONTMATTER.exec(raw);
		const data = front ? (parseYaml(front[1]) ?? {}) : {};
		const bodyStart = front ? front[0].length : 0;
		const offset = raw.slice(0, bodyStart).split("\n").length - 1;
		const lang = rel.startsWith("es/") ? "es" : "en";
		const slug = slugOf(rel);
		return {
			rel,
			file: relative(SITE, join(DOCS, rel)).split(sep).join("/"),
			raw,
			front: front ? front[1] : "",
			data,
			body: raw.slice(bodyStart),
			offset,
			lang,
			slug,
			kind: voiceKind(slug),
		};
	})
	.filter((p) => !NOT_A_DOCUMENT.has(p.slug));

const bySlug = new Map();
for (const page of pages) {
	if (!bySlug.has(page.slug)) bySlug.set(page.slug, {});
	bySlug.get(page.slug)[page.lang] = page;
}

/** Whether a page is one the arguments ask for. */
function selected(page) {
	if (filters.length === 0) return true;
	return filters.some((f) => {
		if (/\.mdx?$/.test(f)) return resolve(f) === join(DOCS, page.rel);
		const want = f.replace(/^\/+|\/+$/g, "");
		if (want.startsWith("es/") || want === "es")
			return page.lang === "es" && slugOf(want) === page.slug;
		return slugOf(want) === page.slug;
	});
}

/** The line of the frontmatter a key is on, 1-based. */
function frontLine(page, key) {
	const i = page.front.split("\n").findIndex((l) => l.startsWith(`${key}:`));
	return i === -1 ? 1 : i + 2;
}

function checkSource(page) {
	const base = {
		file: page.file,
		slug: page.slug,
		lang: page.lang,
		origin: "src",
	};

	// docType: present, one of the values, and the one voice.mjs implies.
	const docType = page.data.docType;
	if (docType === undefined) {
		report({ ...base, line: 1, rule: "doctype-missing", text: "no docType" });
	} else if (!DOC_TYPES.includes(docType)) {
		report({
			...base,
			line: frontLine(page, "docType"),
			rule: "doctype-mismatch",
			text: `docType ${docType} is not one of ${DOC_TYPES.join(", ")}`,
		});
	} else if ((docType === "evidence") !== (page.kind === "evidence")) {
		report({
			...base,
			line: frontLine(page, "docType"),
			rule: "doctype-mismatch",
			text:
				page.kind === "evidence"
					? `docType ${docType} on an evidence page (src/lib/voice.mjs EVIDENCE_SLUGS)`
					: `docType evidence on a page src/lib/voice.mjs does not list as evidence`,
		});
	} else if ((docType === "about") !== (page.kind === "about")) {
		report({
			...base,
			line: frontLine(page, "docType"),
			rule: "doctype-mismatch",
			text: `docType ${docType} disagrees with src/lib/voice.mjs (${page.kind})`,
		});
	}
	const twin = bySlug.get(page.slug)?.[page.lang === "en" ? "es" : "en"];
	if (page.lang === "es" && twin && twin.data.docType !== docType) {
		report({
			...base,
			line: frontLine(page, "docType"),
			rule: "doctype-mismatch",
			text: `docType ${docType} here, ${twin.data.docType} in the English twin`,
		});
	}

	// The title is a label too.
	if (typeof page.data.title === "string" && page.slug !== "") {
		for (const rule of labelRules(page.data.title, page.lang)) {
			report({
				...base,
				line: frontLine(page, "title"),
				rule,
				text: page.data.title,
			});
		}
	}

	// Headings, every page. A Spanish heading is exempt where the English
	// heading at the same position is, when the twins have the same outline.
	const hs = headings(page.body, page.offset);
	const enHeadings =
		page.lang === "es" && twin ? headings(twin.body, twin.offset) : null;
	hs.forEach((h, i) => {
		if (labelExempt(page.slug, h)) return;
		const pair =
			enHeadings && enHeadings.length === hs.length ? enHeadings[i] : null;
		for (const rule of labelRules(h.text, page.lang)) {
			const pairedExempt =
				pair !== null &&
				EXEMPT.some(
					(e) =>
						(e.page === "*" || e.page === page.slug) &&
						(e.rule === "*" || e.rule === rule) &&
						exemptText(e.text, pair.text),
				);
			if (pairedExempt || (pair && labelExempt(page.slug, pair))) continue;
			report({ ...base, line: h.line, rule, text: h.text });
		}
	});

	// The opening: the task or the result, never "This page…".
	const opener = /^\s*(?:this page|esta página)\b/i;
	if (
		typeof page.data.description === "string" &&
		opener.test(page.data.description)
	) {
		report({
			...base,
			line: frontLine(page, "description"),
			rule: "opening",
			text: page.data.description.slice(0, 80),
		});
	}
	const { withCode, noCode } = prose(page.body);
	const first = withCode.findIndex(
		(l) => l.trim() !== "" && !/^#/.test(l.trim()),
	);
	if (first !== -1 && opener.test(withCode[first])) {
		report({
			...base,
			line: page.offset + first + 1,
			rule: "opening",
			text: withCode[first].trim().slice(0, 80),
		});
	}

	if (page.kind !== "guide") return;

	// Components that print provenance, outside code.
	const code = unfenced(page.body).replaceAll(/(`+)[^`\n]*?\1/g, blank);
	for (const m of code.matchAll(EVIDENCE_COMPONENTS)) {
		const line = page.offset + code.slice(0, m.index).split("\n").length;
		report({
			...base,
			line,
			rule: "evidence-component",
			text: m[1] ? `<${m[1]}>` : `<FaultSignature ${m[2]}>`,
		});
	}

	// The text: frontmatter title, description and hero tagline, then the body.
	const front = [
		["title", page.data.title],
		["description", page.data.description],
		["hero", page.data.hero?.tagline],
	].filter(([, v]) => typeof v === "string");
	for (const [key, value] of front) {
		scanLine(value, value, base, frontLine(page, key));
	}
	withCode.forEach((line, i) => {
		if (line.trim() === "") return;
		scanLine(line, noCode[i], base, page.offset + i + 1);
	});
}

/** The words around a match, for the report. */
function around(text, index, length) {
	const from = Math.max(0, index - 45);
	const to = Math.min(text.length, index + length + 45);
	return `${from > 0 ? "…" : ""}${text.slice(from, to).trim()}${to < text.length ? "…" : ""}`;
}

/** Runs the text rules over one line, in its two versions. */
function scanLine(withCode, noCode, base, line) {
	for (const rule of TEXT_RULES) {
		const text = rule.code ? withCode : noCode;
		for (const m of text.matchAll(rule.re)) {
			if (rule.keep?.(m, text)) continue;
			report({
				...base,
				line,
				rule: rule.id,
				text: m[0],
				context: text.trim(),
			});
		}
	}
}

/* ------------------------------------------------------------------ build */

/** Every built page, as `route` ("" for the landing) and the file. */
function* builtPages(dir, prefix = "") {
	for (const entry of readdirSync(dir, { withFileTypes: true })) {
		const rel = prefix ? `${prefix}/${entry.name}` : entry.name;
		if (entry.isDirectory()) yield* builtPages(join(dir, entry.name), rel);
		else if (entry.name === "index.html")
			yield rel.replace(/\/?index\.html$/, "");
	}
}

/**
 * The HTML of an element that starts at `start` in `html`, through its
 * matching close tag, counting nested elements of the same name.
 */
function elementEnd(html, start) {
	const name = /^<([a-z][a-z0-9]*)/i.exec(html.slice(start))?.[1];
	if (!name) return start + 1;
	const re = new RegExp(`<${name}\\b[^>]*>|</${name}>`, "gi");
	re.lastIndex = start;
	let depth = 0;
	for (let m = re.exec(html); m; m = re.exec(html)) {
		if (m[0].startsWith("</")) depth -= 1;
		else if (!m[0].endsWith("/>")) depth += 1;
		if (depth === 0) return re.lastIndex;
	}
	return html.length;
}

/** Removes every element whose opening tag matches `open`. */
function removeElements(html, open) {
	let out = html;
	for (;;) {
		const m = open.exec(out);
		if (!m) return out;
		out = out.slice(0, m.index) + out.slice(elementEnd(out, m.index));
	}
}

const decode = (text) =>
	text
		.replaceAll(/&#x([0-9a-f]+);/gi, (_, hex) =>
			String.fromCodePoint(Number.parseInt(hex, 16)),
		)
		.replaceAll(/&#(\d+);/g, (_, dec) => String.fromCodePoint(Number(dec)))
		.replaceAll("&quot;", '"')
		.replaceAll("&lt;", "<")
		.replaceAll("&gt;", ">")
		.replaceAll("&nbsp;", " ")
		.replaceAll("&amp;", "&");

/** The text of an HTML fragment, one line per block. */
const textOf = (html) =>
	decode(
		html
			.replaceAll(
				/<\/(?:p|li|h[1-6]|td|th|tr|div|dt|dd|figcaption|caption)>/gi,
				"\n",
			)
			.replaceAll(/<br\s*\/?>/gi, "\n")
			.replaceAll(/<[^>]+>/g, ""),
	)
		.replaceAll(/[\t    ]+/g, " ")
		.split("\n")
		.map((l) => l.trim())
		.filter(Boolean);

/** What the source of a page says, flattened, to tell a component's words from the page's. */
const flat = (text) => text.replaceAll(/[\s   `*_]+/g, " ");

function checkBuild() {
	if (!existsSync(distDir)) {
		console.log(
			`[voice] no build at ${relative(process.cwd(), distDir) || distDir}: the source only. Run pnpm build to read what components print too.`,
		);
		return;
	}
	const testedOn = {};
	for (const lang of ["en", "es"]) {
		const file = join(
			distDir,
			lang === "en" ? "" : "es",
			TESTED_ON_SLUG,
			"index.html",
		);
		testedOn[lang] = existsSync(file) ? readFileSync(file, "utf8") : "";
	}
	for (const route of builtPages(distDir)) {
		const lang = route === "es" || route.startsWith("es/") ? "es" : "en";
		const slug = slugOf(route);
		if (NOT_A_DOCUMENT.has(slug)) continue;
		const source = bySlug.get(slug)?.[lang];
		if (!source || !selected(source)) continue;
		const file = `dist/${route ? `${route}/` : ""}index.html`;
		const html = readFileSync(join(distDir, route, "index.html"), "utf8");
		if (isRedirectStub(html)) continue;
		const base = { file, slug, lang, origin: "build" };

		// Links to the proof land on an entry Tested on has, once per target.
		const targets = new Set();
		for (const m of html.matchAll(
			/<a\b[^>]*\sdata-evidence="([^"]+)"[^>]*>/g,
		)) {
			const href = /\shref="([^"]+)"/.exec(m[0])?.[1] ?? "";
			const fragment = decodeURIComponent(href.split("#")[1] ?? "");
			if (targets.has(fragment)) continue;
			targets.add(fragment);
			if (fragment && !testedOn[lang].includes(`id="${fragment}"`)) {
				report({
					...base,
					rule: "evidence-link",
					text: `#${fragment}`,
					context: `Tested on (${lang}) has no id "${fragment}" yet`,
				});
			}
		}

		if (voiceKind(slug) !== "guide") continue;
		const main = /<main\b[\s\S]*?<\/main>/.exec(html)?.[0] ?? "";
		const start = main.search(/<h1\b/);
		const footer = main.search(/<footer\b/);
		let body = main.slice(
			start === -1 ? 0 : start,
			footer === -1 ? main.length : footer,
		);
		body = body.replaceAll(/<(script|style)\b[\s\S]*?<\/\1>/gi, "");
		body = removeElements(body, /<[a-z]+\b[^>]*\sdata-voice-exempt\b[^>]*>/i);
		body = removeElements(body, /<pre\b[^>]*>/i);
		body = removeElements(body, /<span class="sr-only"[^>]*>/i);
		const withCode = textOf(body);
		const noCode = textOf(removeElements(body, /<code\b[^>]*>/i));
		const said = flat(source.raw);
		const seen = new Set();
		for (const [lines, codeRule] of [
			[withCode, true],
			[noCode, false],
		]) {
			for (const line of lines) {
				for (const rule of TEXT_RULES) {
					if (rule.code !== codeRule) continue;
					for (const m of line.matchAll(rule.re)) {
						if (rule.keep?.(m, line)) continue;
						// The page's own words are the source finding's, or its exemption's.
						if (said.includes(flat(m[0]))) continue;
						const key = `${rule.id}\u0000${line}`;
						if (seen.has(key)) continue;
						seen.add(key);
						report({
							...base,
							rule: rule.id,
							text: m[0],
							context: around(line, m.index, m[0].length),
						});
					}
				}
			}
		}
	}
}

/* ------------------------------------------------------------------- run */

for (const page of pages) if (selected(page)) checkSource(page);
if (!srcOnly) checkBuild();

const byFile = new Map();
for (const f of findings) {
	if (!byFile.has(f.file)) byFile.set(f.file, []);
	byFile.get(f.file).push(f);
}
const byRule = new Map();
for (const f of findings) byRule.set(f.rule, (byRule.get(f.rule) ?? 0) + 1);

if (summary) {
	for (const [rule, n] of [...byRule].sort((a, b) => b[1] - a[1])) {
		console.log(`  ${String(n).padStart(5)}  ${rule}`);
	}
} else {
	let firstFile = true;
	for (const [, list] of byFile) {
		if (!firstFile) console.log("");
		firstFile = false;
		for (const f of list.sort((a, b) => (a.line ?? 0) - (b.line ?? 0))) {
			const at = f.line === undefined ? "" : `:${f.line}`;
			const context =
				f.context && f.context !== f.text ? ` in "${f.context}"` : "";
			console.log(`${f.file}${at} ${f.rule}: "${f.text}"${context}`);
		}
	}
}

const pagesHit = byFile.size;
const verdict = `${findings.length} finding(s) on ${pagesHit} file(s)`;
if (findings.length === 0) {
	console.log(
		"[voice] every page reads as tool documentation, and its proof is on the evidence pages.",
	);
} else if (mode === "error") {
	console.error(`[voice] ${verdict}. MS_VOICE=error: failing.`);
	process.exit(1);
} else {
	console.log(`[voice] ${verdict}. MS_VOICE=warn: reported, not failing.`);
}

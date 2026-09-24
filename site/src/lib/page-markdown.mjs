// The Markdown reduction of one MDX page: the same words, without the chrome.
//
// The pages under src/content/docs are the only source of documentation this
// repository has. docs/ is generated from them (site/scripts/gen-docs.mjs),
// and this file is the function that turns a page into the text that lands
// there. Everything a reader sees on the rendered page has to survive the
// trip, including the parts the page does not spell out: <Measured id="…" />
// is four characters in the source and "2.85 %" on the page, so the reduction
// reads src/data/measurements.ts and formats the figure with the same
// src/lib/format.ts the component uses. A number reformatted by hand here
// would be a second copy of the thing this pipeline exists to abolish.
//
// A component this file does not know THROWS, naming the page and the tag.
// That is the whole safety property: adding a component to the content fails
// the build instead of silently deleting a section from docs/, which is the
// failure nobody notices, because a document missing a section is still a
// valid document.
//
// Two callers load this file, and they resolve modules differently. The Astro
// build serves every page's markdown twin and the llms bundles from
// src/pages, through Vite, which reads `.ts`, extensionless relative imports
// and `?raw` on its own. scripts/gen-docs.mjs runs under plain Node, which
// strips types but resolves no extensionless specifier, and src/data/*.ts
// import each other as `./measurements` because that is what the Astro build
// wants; dashboards.ts also reads the committed dashboards with Vite's `?raw`.
// So the imports below are static, which is what lets Vite bundle this file
// into a route, and the Node loader hook that teaches Node both spellings lives
// in scripts/data-hooks.mjs, registered by the script before it imports this
// file. The hook used to be registered here, at module top level. Imported from
// a route, that would install it in the build's own process, and Node applies
// `registerHooks` hooks to `require()` as well, so a dependency's CommonJS
// `require("./x")` would be resolved as `./x.ts`. That was not tried; the split
// is what keeps the question from arising. One copy of every data module and
// of the parsing, either way.
import {
	campaigns,
	describeCpu,
	isCampaignId,
	isMeasurementId,
	measurements,
	runs,
} from "../data/measurements.ts";
import { verifications, isVerificationId } from "../data/verifications.ts";
import { routerObjects, isRouterCommand } from "../data/router-objects.ts";
import { envlist } from "../data/envlist.ts";
import { doctorChecks, isDoctorCheckId } from "../data/doctor-checks.ts";
import { cadenceReasons } from "../data/cadence-reasons.ts";
import * as dashboards from "../data/dashboards.ts";
import * as home from "../data/home.ts";
import { release } from "../data/release.ts";
import figureMeta from "../data/figures/figures.json" with { type: "json" };
// The landing's own source, for the one thing its twin needs from the
// frontmatter rather than the body: the hero tagline. See TAGLINES.
import landingEn from "../content/docs/index.mdx?raw";
import landingEs from "../content/docs/es/index.mdx?raw";
import { parse as parseYaml } from "yaml";

import stats from "../data/stats.json" with { type: "json" };
import en from "../content/i18n/en.json" with { type: "json" };
import es from "../content/i18n/es.json" with { type: "json" };
import { formatNumber, formatQuantity, numberWord } from "./format.ts";
import { withVersion } from "./release.mjs";
import { ORIGIN, localeOf, pageUrl } from "./site.mjs";
import { srcUrl } from "./src-link.mjs";

/** The site's own UI strings, by locale: what `Astro.locals.t` hands a component. */
const STRINGS = { en, es };

/**
 * Starlight's default titles for an aside that gives none. The rendered page
 * shows one of these, so the reduction says it too rather than losing the
 * difference between a note and a warning.
 */
const ASIDE_TITLES = {
	en: { note: "Note", tip: "Tip", caution: "Caution", danger: "Danger" },
	es: {
		note: "Nota",
		tip: "Consejo",
		caution: "Precaución",
		danger: "Peligro",
	},
};

/* ------------------------------------------------------------ code masking */

// Inside code, `<` and `{` are text: `--out <UTC time>` on reference/cli and
// `${DS_MIKROSCOPE}` on dashboards/alerts are inline code, and eleven fenced
// blocks hold shell here-documents and Python f-strings. The component scanner
// reads them as tags and as expressions. So the four characters that start a
// tag or an expression are swapped for control characters before the scan and
// swapped back after it. The swap is one character for one character, so every
// line keeps its length and its indentation, which is what <Steps> and
// <TabItem> need when their children are shifted back to the margin.
const MASKED = { "<": "\u0011", ">": "\u0012", "{": "\u0013", "}": "\u0014" };
const UNMASKED = Object.fromEntries(
	Object.entries(MASKED).map(([k, v]) => [v, k]),
);

const mask = (text) => text.replaceAll(/[<>{}]/g, (c) => MASKED[c]);
const unmask = (text) =>
	text.replaceAll(/[\u0011-\u0014]/g, (c) => UNMASKED[c]);

/**
 * Masks the code spans of a run of prose: a run of N backticks closed by a run
 * of N, which may be on a later line — record/index wraps
 * `mark --out <prefix> --log-markers` across two — but never past a blank
 * line, which is where CommonMark ends a code span and where an unpaired
 * backtick in prose would otherwise swallow the rest of the page.
 */
function maskInlineCode(text) {
	let out = "";
	let i = 0;
	while (i < text.length) {
		if (text[i] !== "`") {
			out += text[i];
			i += 1;
			continue;
		}
		let n = 0;
		while (text[i + n] === "`") n += 1;
		const rest = text.slice(i + n);
		const blank = rest.search(/\n[ \t]*\n/);
		const window = blank === -1 ? rest : rest.slice(0, blank);
		const close = new RegExp("(?<!`)`{" + n + "}(?!`)").exec(window);
		if (!close) {
			out += "`".repeat(n);
			i += n;
			continue;
		}
		out += "`".repeat(n) + mask(window.slice(0, close.index)) + close[0];
		i += n + close.index + close[0].length;
	}
	return out;
}

/** The body with every fenced block and every inline code span masked. */
function maskCode(source) {
	const out = [];
	let prose = [];
	let fence = null;
	const flush = () => {
		if (prose.length > 0) out.push(maskInlineCode(prose.join("\n")));
		prose = [];
	};
	for (const line of source.split("\n")) {
		const marker = /^[ \t]*(`{3,}|~{3,})/.exec(line);
		if (fence !== null) {
			if (
				marker !== null &&
				marker[1][0] === fence[0] &&
				marker[1].length >= fence.length
			) {
				fence = null;
			}
			out.push(mask(line));
			continue;
		}
		if (marker !== null) {
			flush();
			out.push(mask(line));
			fence = marker[1];
			continue;
		}
		prose.push(line);
	}
	flush();
	return out.join("\n");
}

/* ------------------------------------------------------------- the scanner */

// A tag, opening, closing or self-closing, across newlines: <Mark> and <img>
// spread their attributes over five lines, so `.` would stop at the first.
// Lowercase names are matched too, because four pages write plain HTML and a
// reduction that passed `<a href={burst.src}>` through would publish the
// expression as text.
const TAG =
	/<(\/?)([A-Za-z][A-Za-z0-9]*)((?:"[^"]*"|'[^']*'|\{[^}]*\}|[^>"'{])*?)(\/?)>/;

const ATTRIBUTE =
	/([A-Za-z][\w:-]*)\s*=\s*(?:"([^"]*)"|'([^']*)'|\{((?:[^{}]|\{[^}]*\})*)\})|([A-Za-z][\w:-]*)/g;

/**
 * The props of a tag, split by how they were written: a quoted string is text
 * the reduction can print, an expression is source it has to interpret.
 *
 * @param {string} raw the text between the tag name and the closing bracket
 * @returns {{ attributes: Record<string, string>, expressions: Record<string, string> }}
 */
function parseAttributes(raw) {
	const attributes = {};
	const expressions = {};
	for (const m of raw.matchAll(ATTRIBUTE)) {
		if (m[5] !== undefined) attributes[m[5]] = "";
		else if (m[4] !== undefined) expressions[m[1]] = m[4].trim();
		else attributes[m[1]] = m[2] ?? m[3];
	}
	return { attributes, expressions };
}

/** @param {string} text @returns {string} the text as a Markdown blockquote. */
const blockquote = (text) =>
	text
		.trim()
		.split("\n")
		.map((line) => (line.trim() ? `> ${line}` : ">"))
		.join("\n");

/**
 * Removes the indentation a component's children carry because of the tag
 * around them, keeping their indentation relative to each other. Left alone,
 * the children of a <TabItem> inside a <Steps> list arrive seven columns in,
 * which Markdown reads as a code block rather than as the prose it is.
 */
function dedent(text) {
	const widths = text
		.split("\n")
		.filter((line) => line.trim())
		.map((line) => (/^[ \t]*/.exec(line) ?? [""])[0].length);
	const shift = widths.length > 0 ? Math.min(...widths) : 0;
	return text
		.split("\n")
		.map((line) => line.slice(shift))
		.join("\n");
}

/** Puts a block back where its opening tag sat, so a component in a list item stays in it. */
const indentBy = (text, indent) =>
	indent
		? text
				.split("\n")
				.map((line) => (line.trim() ? indent + line : line))
				.join("\n")
		: text;

/* ---------------------------------------------------------------- tables */

/** A cell's text, with the one character that would end it early escaped. */
const cell = (text) =>
	String(text).replaceAll("|", "\\|").replaceAll("\n", " ").trim();

/**
 * A Markdown table. The site's tables are read inside a scroll region with a
 * visually hidden caption naming them; here the caption becomes the line above
 * the table, because Markdown has no caption and a table of thirty metric
 * families with no name is a table a reader arrives at from nowhere.
 *
 * @param {string | null} caption the table's own name, or null
 * @param {string[]} headers
 * @param {(string | number)[][]} rows
 * @returns {string}
 */
function table(caption, headers, rows) {
	const lines = [
		`| ${headers.map(cell).join(" | ")} |`,
		`| ${headers.map(() => "---").join(" | ")} |`,
		...rows.map((row) => `| ${row.map(cell).join(" | ")} |`),
	];
	return `\n\n${caption ? `${caption}:\n\n` : ""}${lines.join("\n")}\n\n`;
}

/** `code`, or nothing at all for an empty string. */
const code = (text) =>
	text === "" || text === null || text === undefined ? "" : `\`${text}\``;

/* ------------------------------------------------------ the component list */

/**
 * The page being reduced: its path for failures, its locale, its UI strings
 * and the assets its import block names.
 *
 * @typedef {{ file: string, lang: "en" | "es", t: (key: string) => string, assets: Record<string, string> }} Context
 */

/**
 * One self-closing component, rendered to the text its page shows.
 *
 * @param {string} name
 * @param {Record<string, string>} attributes double-quoted props
 * @param {Record<string, string>} expressions props written as expressions
 * @param {Context} context
 * @returns {string} Markdown
 */
function renderSelfClosing(name, attributes, expressions, context) {
	const { lang, t } = context;
	switch (name) {
		// One measured figure, formatted by the same src/lib/format.ts the
		// component calls: "2.85 %" in English, "2,85 %" in Spanish, thin space
		// between thousands, and a range kept as a range.
		case "Measured": {
			const id = attributes.id;
			if (!isMeasurementId(id)) {
				throw new Error(
					`${context.file}: <Measured id="${id}" /> is not a figure; ids live in src/data/measurements.ts`,
				);
			}
			return formatQuantity(measurements[id], lang);
		}
		// A generated diagram. docs/ is text, so the reduction is the figure's
		// own <desc> — the sentence the SVG already carries for a screen
		// reader, which is the same thing a reader of the plain file needs.
		case "Figure": {
			const figure = figureMeta[attributes.name]?.[lang === "es" ? "es" : "en"];
			if (!figure) {
				throw new Error(
					`${context.file}: <Figure name="${attributes.name}" /> is not in src/data/figures/figures.json. Run \`pnpm run figures\` in site/.`,
				);
			}
			return `_${figure.title}_ — ${figure.desc}`;
		}
		// The dashboard section captures. docs/ is a text file in a checkout,
		// with no images and no site to serve them from, so the reduction is
		// the list of what the captures show and where they live — which is
		// what a reader of docs/ can act on.
		case "DashboardCaptures": {
			// A site-relative link: gen-docs.mjs, and absoluteTargets for the
			// twins, make every link of the page absolute afterwards, the same
			// way they do for the prose.
			const dashboards_ =
				lang === "es"
					? "/mikroscope/es/dashboards/"
					: "/mikroscope/dashboards/";
			const lines = dashboards.sectionNames
				.map((title) => {
					const count = dashboards.countOf("influxdb", title);
					return count === null ? null : `- ${title} (${count})`;
				})
				.filter(Boolean);
			const intro =
				lang === "es"
					? `Una captura por sección del dashboard de InfluxDB, sobre una base de demostración llenada por el agente simulado, en [la página](${dashboards_}):`
					: `One capture per section of the InfluxDB dashboard, over a demonstration database filled by the fake agent, on [the page](${dashboards_}):`;
			return [intro, "", ...lines].join("\n");
		}
		// One screenshot, inline. docs/ is a text file in a checkout with no
		// images, so the reduction is the caption: the sentence the picture
		// was put there to make.
		case "Capture": {
			if (!attributes.caption) {
				throw new Error(
					`${context.file}: <Capture name="${attributes.name}" /> has no caption`,
				);
			}
			return `*${attributes.caption}*`;
		}
		// A count the Go source decides, written the way the page writes it:
		// `as="word"` is prose ("ten"), anything else is digits, with the same
		// table of words Stat.astro uses (numberWord in format.ts).
		case "Stat": {
			const value = stats[attributes.name];
			if (typeof value !== "number") {
				throw new Error(
					`${context.file}: <Stat name="${attributes.name}" /> is not a number in src/data/stats.json`,
				);
			}
			const word = numberWord(value, lang === "es" ? "es" : "en");
			return attributes.as === "word" && word ? word : String(value);
		}
		// The sentence every figure needs beside it: device · CPU · RouterOS ·
		// [Linux ·] date · conditions, exactly as Provenance.astro composes it.
		case "Provenance": {
			const of = attributes.of;
			if (!isCampaignId(of)) {
				throw new Error(
					`${context.file}: <Provenance of="${of}" /> is not a campaign; campaigns live in src/data/measurements.ts`,
				);
			}
			if (of === "image" || of === "image-v100") {
				throw new Error(
					`${context.file}: <Provenance of="${of}" /> — the image size was not measured on a device`,
				);
			}
			const c = campaigns[of];
			const facts = [
				c.device,
				describeCpu(c, lang),
				`RouterOS ${c.routeros}`,
				...("kernel" in c ? [`Linux ${c.kernel}`] : []),
				c.date === null ? t("ms.provenance.undated") : c.date,
				c.conditions[lang],
			];
			return `\n\n${t("ms.provenance.measured")} ${facts.join(" · ")}\n\n`;
		}
		// "RB5009UG+S+, RouterOS 7.24.2, 2026-09-11", inline in the page's own
		// sentence, which is where the verb ("verified on …") stays.
		case "Verified": {
			const of = attributes.of;
			if (!isVerificationId(of)) {
				throw new Error(
					`${context.file}: <Verified of="${of}" /> is not a fact; facts live in src/data/verifications.ts`,
				);
			}
			const v = verifications[of];
			return `${v.device}, RouterOS ${v.routeros}, ${v.date}`;
		}
		// The release this build documents, as Version.astro writes it: the
		// number, the tag or the date, linked to the release page with `link`.
		case "Version": {
			const show = attributes.show ?? "version";
			const values = {
				version: release.version,
				tag: release.tag,
				date: release.date,
			};
			if (!Object.hasOwn(values, show)) {
				throw new Error(
					`${context.file}: <Version show="${show}" /> — show is "version", "tag" or "date"`,
				);
			}
			return "link" in attributes || expressions.link === "true"
				? `[${values[show]}](${release.notesUrl})`
				: values[show];
		}
		// A file of this repository, as code linked to it on main, the way
		// Src.astro renders it. scripts/check-src.mjs holds every path to a file
		// that exists.
		case "Src":
			return `[${code(attributes.path)}](${srcUrl(attributes.path, context.file)})`;
		case "RunsTable": {
			const only =
				expressions.only === undefined
					? null
					: parseStringArray(expressions.only, context);
			for (const key of only ?? []) {
				if (!runs.some((r) => r.key === key)) {
					throw new Error(
						`${context.file}: <RunsTable only> names no run "${key}"`,
					);
				}
			}
			// Data order, whatever order `only` lists them in, as the component does.
			const rows = (only ? runs.filter((r) => only.includes(r.key)) : runs).map(
				(r) => [
					`${formatQuantity({ value: r.rateHz, unit: "Hz", digits: 0 }, lang)}${
						r.installDefault ? ` (${t("ms.runs.default")})` : ""
					}`,
					r.floorHz === 0
						? t("ms.runs.default")
						: code(`FLOOR_HZ=${r.floorHz}`),
					`**${formatQuantity({ value: r.cpuPct, unit: "%", digits: 2 }, lang)}**`,
					formatNumber(r.usPerSample, lang, 0),
					formatQuantity({ value: r.rssMiB, unit: "MiB", digits: 1 }, lang),
					r.slipped === 0
						? "**0**"
						: `${formatNumber(r.slipped, lang, 0)} (${formatQuantity(
								{ value: r.slippedPct, unit: "%", digits: 2 },
								lang,
							)})`,
					`${formatNumber(r.gaps, lang, 0)} / ${formatNumber(r.drops, lang, 0)}`,
				],
			);
			return table(
				t("ms.runs.label"),
				[
					t("ms.runs.rate"),
					t("ms.runs.floors"),
					t("ms.runs.cpu"),
					t("ms.runs.usPerSample"),
					t("ms.runs.rss"),
					t("ms.runs.slipped"),
					t("ms.runs.gapsDrops"),
				],
				rows,
			);
		}
		// What a command writes to the reader's router, from
		// src/data/router-objects.ts, with the ownership tag and the two
		// sentences the component prints under the list.
		case "RouterWrites": {
			const command = attributes.command;
			if (!isRouterCommand(command)) {
				throw new Error(
					`${context.file}: <RouterWrites command="${command}" /> is not a command; they live in src/data/router-objects.ts`,
				);
			}
			const titles = {
				install: "ms.writes.title.install",
				expose: "ms.writes.title.expose",
				upgrade: "ms.writes.title.upgrade",
				uninstall: "ms.writes.title.uninstall",
			};
			const parts = [
				`**${t(titles[command])}**`,
				"",
				...routerObjects[command].map((o) => `- ${o[lang]}`),
				"",
				`${t("ms.writes.tag")} \`mikroscope:<name> (managed by mikroscope)\``,
				"",
				t("ms.writes.plan"),
			];
			// Only install and uninstall touch the removal path verify describes.
			if (command === "install" || command === "uninstall") {
				parts.push("", t("ms.writes.verify"));
			}
			return `\n\n${parts.join("\n")}\n\n`;
		}
		case "RunFlags": {
			const key = attributes.run;
			const r = runs.find((x) => x.key === key);
			if (r === undefined) {
				throw new Error(
					`${context.file}: <RunFlags run="${key}" /> names no run`,
				);
			}
			return code(r.flags);
		}
		// An inline mark that links to the page explaining what privileged buys.
		// The href is the locale's own and still site-rooted: gen-docs.mjs and
		// absoluteTargets turn it into an absolute URL, as they do every other
		// link on the page.
		case "PrivilegedOnly": {
			const href =
				lang === "en"
					? "/mikroscope/limits/privileged/"
					: `/mikroscope/${lang}/limits/privileged/`;
			return `[${t("ms.privileged.badge")}](${href})`;
		}
		case "EnvlistKeys":
			return table(
				t("ms.envlist.label"),
				[
					t("ms.envlist.key"),
					t("ms.envlist.written"),
					t("ms.envlist.from"),
					t("ms.envlist.holds"),
				],
				envlist.map((e) => {
					const written = {
						always: "ms.envlist.always",
						aboveZero: "ms.envlist.aboveZero",
						whenSet: "ms.envlist.whenSet",
					};
					const from = [
						e.flag !== null ? code(e.flag) : "",
						e.default !== null
							? `${t("ms.envlist.default")} ${code(e.default)}`
							: "",
						e.range !== null ? e.range : "",
					].filter(Boolean);
					return [
						code(e.key),
						t(written[e.written]),
						from.join(", "),
						e.holds[lang],
					];
				}),
			);
		case "DoctorChecks": {
			const only =
				expressions.only === undefined
					? null
					: parseStringArray(expressions.only, context);
			for (const id of only ?? []) {
				if (!isDoctorCheckId(id)) {
					throw new Error(
						`${context.file}: <DoctorChecks only> names no check "${id}"`,
					);
				}
			}
			const rows = only
				? doctorChecks.filter((c) => only.includes(c.id))
				: doctorChecks;
			return table(
				t("ms.doctor.label"),
				[t("ms.doctor.check"), t("ms.doctor.passes"), t("ms.doctor.fix")],
				rows.map((c) => [c.printed, c.passes[lang], c.fix[lang]]),
			);
		}
		case "CadenceReasons":
			return table(
				t("ms.cadence.label"),
				["`reason`", t("ms.cadence.meaning")],
				cadenceReasons.map((r) => [code(r.reason), r.meaning[lang]]),
			);
		case "DashboardCount": {
			const { store, of } = attributes;
			if (!dashboards.STORES.includes(store)) {
				throw new Error(
					`${context.file}: <DashboardCount store="${store}" /> is not a store`,
				);
			}
			if (of !== "panels" && of !== "rules") {
				throw new Error(
					`${context.file}: <DashboardCount of="${of}" /> must be "panels" or "rules"`,
				);
			}
			const n =
				of === "panels"
					? dashboards.panelTotal(store)
					: dashboards.alertRules[store].length;
			return formatNumber(n, lang, 0);
		}
		case "DashboardCounts":
			return table(
				t("ms.dash.countsLabel"),
				[
					t("ms.dash.section"),
					...dashboards.STORES.map((store) => dashboards.STORE_NAMES[store]),
				],
				[
					...dashboards.sectionNames.map((s) => [
						s,
						...dashboards.STORES.map((store) => {
							const n = dashboards.countOf(store, s);
							return n === null ? t("ms.dash.noRow") : formatNumber(n, lang, 0);
						}),
					]),
					[
						`**${t("ms.dash.total")}**`,
						...dashboards.STORES.map(
							(store) =>
								`**${formatNumber(dashboards.panelTotal(store), lang, 0)}**`,
						),
					],
				],
			);
		case "DashboardPanels": {
			const section = attributes.section;
			if (!dashboards.isSection(section)) {
				throw new Error(
					`${context.file}: <DashboardPanels section="${section}" /> names no section of dashboards/`,
				);
			}
			const only = {
				influxdb: "ms.dash.influxOnly",
				prometheus: "ms.dash.promOnly",
			};
			const { entries, stores } = dashboards.panelsOf(section);
			const items = entries.map(
				(e) =>
					`- ${e.title}${e.only !== null && stores.length === 2 ? ` — ${t(only[e.only])}` : ""}`,
			);
			return `\n\n${items.join("\n")}\n\n`;
		}
		case "AlertRules": {
			const { show, store } = attributes;
			if (show === "table") return alertTable(context);
			if (show !== "queries" || !dashboards.STORES.includes(store)) {
				throw new Error(
					`${context.file}: <AlertRules show="${show}" store="${store}" /> — show is "table", or "queries" with a store`,
				);
			}
			// The A query of every rule, under a comment with its uid and its
			// threshold, unlocalised because that is what the file carries.
			const queries = dashboards.alertRules[store]
				.map((r) => {
					const marker = store === "prometheus" ? "#" : "--";
					return `${marker} ${r.uid.padEnd(34)} (${r.op} ${r.value})\n${r.query}`;
				})
				.join("\n");
			return `\n\n\`\`\`${store === "prometheus" ? "text" : "sql"}\n${queries}\n\`\`\`\n\n`;
		}
		// An image imported by the page and rendered as a component: the mark on
		// about/brand, the walkthrough's chart. The target is the source file in
		// this checkout; gen-docs.mjs makes it repository-relative.
		case "Mark":
		case "img": {
			const src =
				attributes.src ??
				resolveAsset(expressions.src, context) ??
				context.assets.Mark;
			const alt = attributes.alt ?? attributes["aria-label"] ?? "";
			if (!src)
				throw new Error(
					`${context.file}: <${name} /> has no resolvable source`,
				);
			return `\n\n![${alt}](${src})\n\n`;
		}
		case "br":
			return "\n";
		// The landing. Its copy is a typed object, not prose, so there is nothing
		// in the page to reduce; renderHome walks that object instead.
		case "Home":
			return `\n\n${renderHome(expressions.content === "es" ? home.es : home.en, context)}\n\n`;
		default:
			throw new Error(
				`${context.file}: no Markdown reduction for <${name} />. Add one in ` +
					"site/src/lib/page-markdown.mjs so docs/ keeps saying what the page says.",
			);
	}
}

/** The alert table, which both the table form and the reader of dashboards/alerts want. */
function alertTable(context) {
	const { lang, t } = context;
	const find = (store, uid) =>
		dashboards.alertRules[store].find((r) => r.uid === uid);
	const rows = dashboards.alertUids.map((uid) => {
		const inStores = dashboards.STORES.filter(
			(s) => find(s, uid) !== undefined,
		);
		const rule = find(inStores[0], uid);
		const digits = String(rule.value).split(".")[1]?.length ?? 0;
		return [
			code(rule.uid),
			dashboards.alertFiresWhen[rule.uid][lang],
			`${rule.op} ${formatNumber(rule.value, lang, digits)}`,
			rule.severity,
			rule.for,
			rule.noDataState,
			inStores.length === 2
				? t("ms.alerts.both")
				: t(
						inStores[0] === "influxdb"
							? "ms.dash.influxOnly"
							: "ms.dash.promOnly",
					),
		];
	});
	return table(
		t("ms.alerts.label"),
		[
			t("ms.alerts.rule"),
			t("ms.alerts.firesWhen"),
			t("ms.alerts.threshold"),
			t("ms.alerts.severity"),
			`\`${t("ms.alerts.for")}\``,
			t("ms.alerts.noData"),
			t("ms.alerts.stores"),
		],
		rows,
	);
}

/**
 * One component that wraps content.
 *
 * @param {string} name
 * @param {Record<string, string>} attributes
 * @param {Record<string, string>} expressions
 * @param {string} children the already reduced children
 * @param {Context} context
 * @returns {string} Markdown
 */
function renderWrapper(name, attributes, expressions, children, context) {
	const { lang, t } = context;
	switch (name) {
		// "Not measured" is not a hazard, so the page does not draw it as a
		// warning; but Markdown has one block for a set-apart note, and the
		// legend is the meaning this block exists to carry, so it leads it.
		case "NotClaimed": {
			const variants = {
				"not-measured": "ms.claim.notMeasured",
				untested: "ms.claim.untested",
				"not-provoked": "ms.claim.notProvoked",
				"device-specific": "ms.claim.deviceSpecific",
			};
			const variant = attributes.variant ?? "not-measured";
			if (!Object.hasOwn(variants, variant)) {
				throw new Error(
					`${context.file}: <NotClaimed variant="${variant}"> is not a variant`,
				);
			}
			const legend = attributes.title ?? t(variants[variant]);
			return `\n\n${blockquote(`**${legend}**\n\n${children.trim()}`)}\n\n`;
		}
		// How the fault came about, which the page repeats here because a reader
		// who jumped to the signature has lost the first paragraph that said it.
		case "FaultSignature": {
			const origins = {
				real: "ms.fault.real",
				provoked: "ms.fault.provoked",
				unprovoked: "ms.fault.unprovoked",
			};
			if (!Object.hasOwn(origins, attributes.origin)) {
				throw new Error(
					`${context.file}: <FaultSignature origin="${attributes.origin}"> is not an origin`,
				);
			}
			if (!/^\d{4}-\d{2}-\d{2}$/.test(attributes.date ?? "")) {
				throw new Error(
					`${context.file}: <FaultSignature date="${attributes.date}"> is not an ISO date`,
				);
			}
			return `\n\n**${t(origins[attributes.origin])}** · ${attributes.date}\n\n${children.trim()}\n\n`;
		}
		case "Aside": {
			const type = attributes.type ?? "note";
			const title = attributes.title ?? ASIDE_TITLES[lang][type] ?? type;
			return `\n\n${blockquote(`**${title}**\n\n${children.trim()}`)}\n\n`;
		}
		// A tab strip is a set of alternatives, and that is what it stays: one
		// list item per tab, its label leading it, so each tab's body stays
		// attached to its own label rather than to whatever follows.
		case "TabItem":
			return `\n\n- **${attributes.label ?? ""}**\n\n${indentBy(children.trim(), "  ")}\n\n`;
		// Pure layout around content that is already Markdown. <ScrollTable> is a
		// scroll box around a table, <SeeAlso> a landmark around an authored
		// heading and its list, <Steps> an ordered list, <FileTree> a list of
		// files: unwrapping them leaves exactly what the page shows. A raw <div>
		// holds the chart's "open at full size" line on start/walkthrough; it was
		// a <p> until html-validate found MDX's own paragraph nested inside it
		// (2026-09-15). Chromium parses that as an empty classed <p>, the line in
		// an unclassed one after it, and a third, empty one, so the line never
		// took the muted small style its class names.
		case "ScrollTable":
		case "SeeAlso":
		case "Steps":
		case "Tabs":
		case "FileTree":
		case "div":
		case "p":
			return `\n\n${children.trim()}\n\n`;
		// Inline wrappers: the Spanish quotation on dashboards/index is marked
		// `lang="es"` for a screen reader and is otherwise just its words.
		case "span":
			return children;
		case "a": {
			const href = attributes.href ?? resolveAsset(expressions.href, context);
			if (!href) throw new Error(`${context.file}: <a> has no resolvable href`);
			return `[${children.trim()}](${href})`;
		}
		default:
			throw new Error(
				`${context.file}: no Markdown reduction for <${name}>. Add one in ` +
					"site/src/lib/page-markdown.mjs so docs/ keeps saying what the page says.",
			);
	}
}

/* ----------------------------------------------------------- the landing */

/**
 * The hero tagline of each landing, read from the page's own frontmatter.
 *
 * Starlight renders `hero.tagline` above the body, so the reduction of the
 * body never saw it, and the landing's twin — the first page of
 * llms-full.txt — lost the pitch the HTML leads with, "Two MIT-licensed
 * binaries" included (GEO audit, 2026-09-24). The frontmatter stays the one
 * copy: the source file is imported as text, the way src/data/release.ts reads
 * VERSION, which both the Astro build and scripts/data-hooks.mjs understand,
 * and scripts/check-twins.mjs holds the twin to the tagline the built page
 * shows.
 */
const TAGLINES = Object.fromEntries(
	Object.entries({ en: landingEn, es: landingEs }).map(([lang, source]) => {
		const file = `src/content/docs/${lang === "es" ? "es/" : ""}index.mdx`;
		const front = /^---[ \t]*\r?\n([\s\S]*?)\r?\n---/.exec(source)?.[1];
		const tagline =
			front === undefined ? undefined : parseYaml(front)?.hero?.tagline;
		if (typeof tagline !== "string" || tagline.trim() === "") {
			throw new Error(
				`${file}: no hero.tagline in the frontmatter, which the landing's markdown twin opens with`,
			);
		}
		return [lang, tagline.trim()];
	}),
);

/**
 * The landing page's body from src/data/home.ts.
 *
 * The page itself is one component tag over a typed object: every heading,
 * paragraph, link and command below is in that object, and the figures come
 * from src/data/measurements.ts through it. Reducing the tag alone would
 * publish an empty page, so the object is walked here. The `<code>` spans the
 * copy carries become Markdown code, which is the same thing said the other
 * way round.
 *
 * It says what the HTML says in the same order, with the hero's tagline first
 * because the hero is the first thing the page shows. The campaign under the
 * cost table is `home.LANDING_CAMPAIGN`, the constant `<Home>` renders too:
 * this function named its own until 2026-09-24 and kept the 2026-09-15 one
 * after the page had moved on.
 */
function renderHome(content, context) {
	const { t } = context;
	const inline = (html) => html.replaceAll(/<\/?code>/g, "`");
	const readout = content.readout.items.map(
		(item) =>
			`- [**${item.id === "run.gapsDrops" ? home.gapsDrops(content.lang) : formatQuantity(measurements[item.id], content.lang)}** — ${item.label}](${item.href})`,
	);
	const parts = [
		TAGLINES[content.lang],
		"",
		`## ${content.readout.title}`,
		"",
		readout.join("\n"),
		"",
		inline(content.readout.claim),
		"",
		`## ${content.hides.title}`,
		"",
		content.hides.paragraphs.map(inline).join("\n\n"),
		"",
		`[${inline(content.hides.linkText)}](${content.hides.href})`,
		"",
		`## ${content.cost.title}`,
		"",
		content.cost.lead,
		"",
		renderSelfClosing(
			"Provenance",
			{ of: home.LANDING_CAMPAIGN },
			{},
			context,
		).trim(),
		"",
		renderSelfClosing(
			"RunsTable",
			{},
			{ only: JSON.stringify(content.cost.runs) },
			context,
		).trim(),
		"",
		content.cost.after,
		"",
		`[${content.cost.linkText}](${content.cost.href})`,
		"",
		`## ${content.tiers.title}`,
		"",
		[content.tiers.kernel, content.tiers.api]
			.map((tier) => `### ${tier.title}\n\n${inline(tier.body)}`)
			.join("\n\n"),
		"",
		`## ${content.install.title}`,
		"",
		`${content.release.label} [${release.version}](${release.notesUrl}), ${release.date} · [${content.release.changelog}](${release.changelogUrl})`,
		"",
		inline(content.install.prereq),
		"",
		content.install.steps
			.map((step, i) => `${i + 1}. \`${step.cmd}\`\n\n   ${step.note}`)
			.join("\n\n"),
		"",
		renderSelfClosing(
			"RouterWrites",
			{ command: "install" },
			{},
			context,
		).trim(),
		"",
		`## ${content.notClaimed.title}`,
		"",
		blockquote(
			[
				`**${t("ms.claim.notMeasured")}**`,
				...content.notClaimed.paragraphs.map(inline),
			].join("\n\n"),
		),
		"",
		`## ${content.next.title}`,
		"",
		content.next.links
			.map((link) => `- [${link.text}](${link.href}): ${link.note}`)
			.join("\n"),
	];
	return parts.join("\n");
}

/* ------------------------------------------------------------- the engine */

/** `{["10hz"]}` and `{["a", "b"]}` — the one expression shape the content writes. */
function parseStringArray(source, context) {
	const values = [...source.matchAll(/"([^"]*)"|'([^']*)'/g)].map(
		(m) => m[1] ?? m[2],
	);
	if (!source.trim().startsWith("[") || values.length === 0) {
		throw new Error(
			`${context.file}: {${source}} is not a list of strings, which is the only expression prop this reduction reads`,
		);
	}
	return values;
}

/**
 * The file an imported asset expression names: `{burst.src}` is the SVG the
 * page imported as `burst`, as the page's own directory reaches it.
 */
function resolveAsset(expression, context) {
	if (!expression) return undefined;
	const binding = /^([A-Za-z_$][\w$]*)\.(src|href)$/.exec(expression.trim());
	if (!binding || !context.assets[binding[1]]) {
		throw new Error(
			`${context.file}: {${expression}} names no imported asset; this reduction reads \`name.src\` only`,
		);
	}
	return context.assets[binding[1]];
}

/**
 * Reduces MDX to Markdown, resolving components as it goes.
 *
 * @param {string} source the body with code already masked
 * @param {Context} context
 * @returns {string}
 */
function reduce(source, context) {
	let out = "";
	let rest = source;
	for (;;) {
		const match = TAG.exec(rest);
		if (!match) return out + rest;
		const [whole, closing, name, raw, selfClosing] = match;
		out += rest.slice(0, match.index);
		rest = rest.slice(match.index + whole.length);
		if (closing)
			throw new Error(`${context.file}: </${name}> without an opening tag`);
		const { attributes, expressions } = parseAttributes(raw);
		// The indentation of the line the tag opened on. A <Provenance> under
		// point 2 of a <Steps> list sits three columns in, and a block put back
		// at the margin ends the list and restarts the numbering under it.
		const indent = (/(?:^|\n)([ \t]*)$/.exec(out) ?? ["", ""])[1];
		if (selfClosing || name === "img" || name === "br") {
			out += indentBy(
				renderSelfClosing(name, attributes, expressions, context),
				indent,
			);
			continue;
		}
		// Scan to the matching close, counting nested tags of the same name so a
		// component inside another of its kind closes the inner one first.
		const scanner = new RegExp(`<${name}(?=[\\s/>])|</${name}>`, "g");
		let depth = 1;
		let end = -1;
		let after = -1;
		for (let inner = scanner.exec(rest); inner; inner = scanner.exec(rest)) {
			depth += inner[0].startsWith("</") ? -1 : 1;
			if (depth === 0) {
				end = inner.index;
				after = scanner.lastIndex;
				break;
			}
		}
		if (end === -1)
			throw new Error(`${context.file}: <${name}> is never closed`);
		const children = rest.slice(0, end);
		rest = rest.slice(after);
		out += indentBy(
			renderWrapper(
				name,
				attributes,
				expressions,
				dedent(reduce(children, context)),
				context,
			),
			indent,
		);
	}
}

/**
 * The import block: what each asset binding points at, before the block is
 * dropped. The specifier is kept as the page wrote it — relative to the page's
 * own directory — so the target reduces to exactly what a hand-written
 * `![](../../assets/…)` would be, and gen-docs.mjs retargets it like any other.
 */
function readImports(source) {
	const assets = {};
	for (const m of source.matchAll(
		/^import\s+([A-Za-z_$][\w$]*)\s+from\s+["']([^"']+)["'];?\s*$/gm,
	)) {
		if (/\.(svg|png|jpe?g|webp|avif|gif)$/.test(m[2])) assets[m[1]] = m[2];
	}
	return assets;
}

/** Drops the import block: it names the machinery this reduction has just resolved. */
const stripImports = (source) =>
	source
		.replace(/^import\s+\{[^}]*\}\s+from\s+["'][^"']+["'];?\s*$/gms, "")
		.replace(/^import\s+.+?\s+from\s+["'][^"']+["'];?\s*$/gm, "");

/** The text with its trailing spaces and its runs of blank lines collapsed. */
const tidy = (text) => text.replace(/[ \t]+$/gm, "").replace(/\n{3,}/g, "\n\n");

// A JSX expression left in prose after the reduction. Outside code no page
// writes a brace today — every `{…}` in the corpus is a metric label or a path
// inside backticks, which maskCode hides — so any brace that survives is an
// expression MDX would have evaluated and docs/ would publish as source.
const EXPRESSION = /\{[^}\n]*\}/;

// `{" "}`: the literal space Prettier writes when it wraps JSX text next to a
// component. MDX renders the string, so the reduction does too.
const JSX_STRING = /\{(?:"([^"\n]*)"|'([^'\n]*)')\}/g;

/**
 * The body of one page as Markdown: the version placeholder replaced, code
 * protected, components resolved, imports dropped, blank lines tidied.
 *
 * The placeholder is replaced first and everywhere, because this one function
 * feeds the markdown twins, llms-full.txt and docs/, and a replacement done
 * per caller is how ghchronicle's twins came to publish its token literally
 * while its HTML was right (checked on its live site, 2026-09-24). Prose has
 * no placeholder to replace: MDX would have failed the HTML build on it.
 *
 * @param {object} page
 * @param {string} page.body the MDX body, without frontmatter
 * @param {string} page.file the source path, named in every failure
 * @param {"en" | "es"} page.locale the locale the page is served in
 * @returns {string} Markdown
 */
export function reduceBody({ body: page, file, locale }) {
	const body = withVersion(page, release.version);
	const context = {
		file,
		lang: locale,
		t: (key) => {
			const value = STRINGS[locale][key];
			if (value === undefined) {
				throw new Error(
					`${file}: src/content/i18n/${locale}.json has no "${key}"`,
				);
			}
			return value;
		},
		assets: readImports(body),
	};
	const source = maskCode(stripImports(body)).replaceAll(
		JSX_STRING,
		(_, a, b) => a ?? b,
	);
	const markdown = unmask(tidy(reduce(source, context)).trim());
	const left = EXPRESSION.exec(maskCode(markdown));
	if (left) {
		throw new Error(
			`${file}: ${left[0]} survived the reduction, so docs/ would publish the expression ` +
				"rather than its value. Teach site/src/lib/page-markdown.mjs to resolve it.",
		);
	}
	if (!markdown) throw new Error(`${file}: nothing left after reduction`);
	return markdown;
}

/**
 * The markdown twin of one documentation page: the page's title, its
 * description, the absolute URL it doubles, then the same reduction docs/ is
 * generated from, with every link target made absolute (see absoluteTargets).
 * gen-docs.mjs retargets the same reduction for a file read in a checkout, to
 * the advertised jmrp.io address, and otherwise the two say the same thing.
 *
 * @param {object} page
 * @param {string} page.route the page route, "" for the English home
 * @param {string} page.title frontmatter title
 * @param {string} page.description frontmatter description
 * @param {string} page.body MDX body, without frontmatter
 * @param {string} page.file source path, named in any failure
 * @returns {string} the twin document
 */
export function renderTwin({ route, title, description, body, file }) {
	const markdown = absoluteTargets(
		reduceBody({ body, file, locale: localeOf(route) }),
		{ file, route },
	);
	return `${[
		`# ${title}`,
		"",
		description,
		"",
		`Source: ${pageUrl(route)}`,
		"",
		markdown,
	].join("\n")}\n`;
}

// Where the twin's images point. The reduction keeps an asset's target as the
// page wrote it, relative to the page's own directory (`../../assets/mark.svg`),
// which is right for docs/, read in a checkout. Served beside the page at
// /mikroscope/about/brand/index.md, the same target resolves to /assets/…,
// which the site does not serve: the build hashes each asset under /_astro/.
// So the twin names the source file in the repository instead, the same file
// docs/ points at.
const SOURCE_ROOT =
	"https://raw.githubusercontent.com/jmrplens/mikroscope/main/site/";

// A code span, which is text, or a link target with its optional title.
const TARGET_OR_CODE = /(`+)[^`]*?\1|\]\(([^)\s]+)((?:\s+"[^"]*")?)\)/g;

// A scheme, which is what separates a target somewhere else from one on this site.
const SCHEME = /^[a-z][a-z0-9+.-]*:/i;

/**
 * Every link and image target of a reduced page, made absolute.
 *
 * The twins are what llms-full.txt and the other llms bundles concatenate,
 * and what llms.txt sends a model to, and a model reads text: it does not
 * resolve `/mikroscope/cost/` against a host it may not have kept, and a
 * `#heading` in a file of fifty pages names whichever page that file puts
 * first (llms-full.txt had 424 site-rooted links and 7 absolute ones, GEO
 * audit, 2026-09-24). So a site-rooted target gets the site's origin, the one
 * the canonical links and the sitemap use; a fragment gets the page it was
 * written on; an asset gets its source file in the repository, see
 * SOURCE_ROOT. Fenced code and code spans are text and are left alone. A
 * target still relative afterwards fails here, because it resolves nowhere a
 * reader of the text can follow.
 *
 * @param {string} markdown a reduced page body
 * @param {{ file: string, route: string }} page the page's path from the site
 *   directory, as the content collection reports it
 *   (`src/content/docs/about/brand.mdx`), and its route
 * @returns {string} the body with every target absolute
 */
function absoluteTargets(markdown, { file, route }) {
	const dir = file.split("/").slice(0, -1);
	const source = (target) => {
		const parts = [...dir];
		for (const segment of target.split("/")) {
			if (segment === "..") parts.pop();
			else if (segment !== ".") parts.push(segment);
		}
		return `${SOURCE_ROOT}${parts.join("/")}`;
	};
	const absolute = (target) => {
		if (SCHEME.test(target)) return target;
		if (target.startsWith("./") || target.startsWith("../")) {
			return source(target);
		}
		if (target.startsWith("/")) return `${ORIGIN}${target}`;
		if (target.startsWith("#")) return `${pageUrl(route)}${target}`;
		throw new Error(
			`${file}: the link target "${target}" is relative to nothing a reader of the markdown twin has. ` +
				"Write it site-rooted (/mikroscope/…), as a fragment, or relative with ./ or ../.",
		);
	};
	let fence = null;
	return markdown
		.split("\n")
		.map((line) => {
			const marker = /^[ \t]*(`{3,}|~{3,})/.exec(line);
			if (fence !== null) {
				if (
					marker !== null &&
					marker[1][0] === fence[0] &&
					marker[1].length >= fence.length
				) {
					fence = null;
				}
				return line;
			}
			if (marker !== null) {
				fence = marker[1];
				return line;
			}
			return line.replaceAll(TARGET_OR_CODE, (whole, ticks, target, title) =>
				ticks === undefined ? `](${absolute(target)}${title})` : whole,
			);
		})
		.join("\n");
}

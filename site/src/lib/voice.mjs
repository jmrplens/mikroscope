// Which pages may carry provenance, and what a page that may not is held to.
//
// The site has two voices. Guides, reference and explanation pages are tool
// documentation: they state what the tool does, with no device, RouterOS
// release or date beside it. The evidence pages say where each of those facts
// was measured or checked: the device, the version, the date, the conditions,
// the spread and what was not tried. A guide that needs the proof links to it.
//
// This module is the one list of evidence pages and the one switch between
// warning and failing, read by the components that print provenance
// (Provenance, Verified, NotClaimed, RunsTable, RunFlags, FaultSignature's
// date and origin, the two registers), by src/lib/page-markdown.mjs and by
// scripts/check-voice.mjs, so the three cannot disagree about a page.
//
// A page's `docType` frontmatter says what it is, but it is not what makes a
// page evidence: EVIDENCE_SLUGS is. Marking a guide `docType: evidence` would
// otherwise be a way to print provenance on it, and check-voice reports a
// docType that disagrees with the list instead of obeying it. Adding an
// evidence page is an edit to this file, where a reviewer sees it.

/**
 * The values `docType` takes in a page's frontmatter (src/content.config.ts).
 *
 * @type {["tutorial", "how-to", "reference", "explanation", "evidence", "about"]}
 */
export const DOC_TYPES = [
	"tutorial",
	"how-to",
	"reference",
	"explanation",
	"evidence",
	"about",
];

/**
 * The evidence pages, as locale-independent slugs. Their Spanish twins are
 * evidence too. Tested on is the register every guide links to; the cost
 * pages, the case studies and the test suites keep their own measurements.
 */
export const EVIDENCE_SLUGS = new Set([
	"about/status",
	"cost",
	"cost/rate-ceiling",
	"cost/limits",
	"playbooks/idle",
	"playbooks/loop",
	"playbooks/cpu",
	"playbooks/packet-flood",
	"playbooks/flash-wear",
	"playbooks/port-errors",
	"playbooks/conntrack",
	"reference/testing",
]);

/**
 * The about pages keep release and attribution dates, which are their
 * content, so the provenance rules skip them. Headings are still labels.
 */
export const ABOUT_SLUGS = new Set([
	"about/changelog",
	"about/lineage",
	"about/brand",
]);

/**
 * The oldest RouterOS the agent runs on, which a guide states as a
 * requirement ("RouterOS 7.24 or later"): the container step writes
 * `privileged=`, which RouterOS added in 7.24 (install/prerequisites). A
 * RouterOS release named on a guide is this one or nothing.
 */
export const ROUTEROS_MINIMUM = "7.24";

/** The page every guide links to for the proof: Tested on. */
export const TESTED_ON_SLUG = "about/status";

/**
 * What a failed rule does: "error" fails the build and
 * scripts/check-voice.mjs, "warn" prints and goes on, "off" says nothing.
 * Every page passes, so a new finding fails; MS_VOICE=warn in the
 * environment lists them all in one run instead of stopping at the first.
 */
export const DEFAULT_MODE = "error";

/** @returns {"warn" | "error" | "off"} */
export function voiceMode() {
	const value = globalThis.process?.env?.MS_VOICE ?? DEFAULT_MODE;
	if (value !== "warn" && value !== "error" && value !== "off") {
		throw new Error(
			`MS_VOICE="${value}": use "warn", "error" or "off" (src/lib/voice.mjs)`,
		);
	}
	return value;
}

/**
 * The locale-independent slug of a page, from anything that names it: a
 * collection entry id ("es/cost/index.mdx", "cost"), a route ("es/cost") or a
 * source path ("site/src/content/docs/es/cost/index.mdx"). "" is the landing.
 *
 * @param {string} name
 * @returns {string}
 */
export function slugOf(name) {
	const inDocs = name.includes("content/docs/")
		? name.slice(name.lastIndexOf("content/docs/") + "content/docs/".length)
		: name;
	const bare = inDocs
		.replace(/^\/+|\/+$/g, "")
		.replace(/\.mdx?$/, "")
		.replace(/(^|\/)index$/, "");
	return bare === "es" ? "" : bare.replace(/^es\//, "");
}

/**
 * What the voice rules treat a page as.
 *
 * - "evidence": provenance belongs here (EVIDENCE_SLUGS).
 * - "about": release and attribution dates belong here (ABOUT_SLUGS).
 * - "guide": everything else, the landing included. No device, no RouterOS
 *   release other than the minimum, no date; the proof is linked.
 *
 * @param {string} slug locale-independent, see slugOf
 * @returns {"evidence" | "about" | "guide"}
 */
export function voiceKind(slug) {
	if (EVIDENCE_SLUGS.has(slug)) return "evidence";
	if (ABOUT_SLUGS.has(slug)) return "about";
	return "guide";
}

/**
 * The page a component is rendered on, from Starlight's route data.
 *
 * @param {{ entry?: { id?: string, filePath?: string, data?: { docType?: string } } } | undefined} route
 *   `Astro.locals.starlightRoute`
 * @returns {{ slug: string, docType: string | undefined, kind: "evidence" | "about" | "guide", lang: "en" | "es" }}
 */
export function pageOf(route) {
	const id = route?.entry?.id ?? "";
	const slug = slugOf(id);
	const lang = id === "es" || id.startsWith("es/") ? "es" : "en";
	return {
		slug,
		docType: route?.entry?.data?.docType,
		kind: voiceKind(slug),
		lang,
	};
}

// One warning per page and component, however many times it is used there:
// the build renders each page once, and a page with nine <NotClaimed> is one
// thing to fix.
const warned = new Set();

/**
 * Called by a component that prints provenance. On an evidence page it does
 * nothing. Anywhere else it warns or throws, by voiceMode().
 *
 * @param {string} what the component, or the component and prop ("<FaultSignature date>")
 * @param {{ entry?: { id?: string } } | undefined} route `Astro.locals.starlightRoute`
 * @param {{ allow?: (slug: string) => boolean }} [options] pages this use is allowed on besides the evidence pages
 */
export function evidenceOnly(what, route, options = {}) {
	const page = pageOf(route);
	if (page.kind === "evidence" || options.allow?.(page.slug)) return;
	const mode = voiceMode();
	if (mode === "off") return;
	const where = route?.entry?.id ?? "(unknown page)";
	const hint =
		"it prints provenance, which belongs on the evidence pages (src/lib/voice.mjs EVIDENCE_SLUGS). " +
		'State the fact and link the proof with <TestedOn of="…">, or move the passage to Tested on';
	if (mode === "error") throw new Error(`[voice] ${where}: ${what}: ${hint}.`);
	const key = `${where}\u0000${what}`;
	if (warned.has(key)) return;
	// The reason once per build, the page and the component every time.
	console.warn(
		warned.size === 0
			? `[voice] ${where}: ${what}: ${hint}. (MS_VOICE=warn; the same for each line below.)`
			: `[voice] ${where}: ${what}`,
	);
	warned.add(key);
}

/** The id of a campaign's entry in <CampaignRegister>. */
export const campaignAnchor = (id) => `campaign-${id}`;

/** The id of a verified fact's entry in <VerifiedRegister>. */
export const verifiedAnchor = (id) => `verified-${id}`;

/**
 * The sections of Tested on a guide may link to with <TestedOn of="…">, by
 * key, with the heading id each has in each language.
 *
 * Three kinds of heading. Those about/status.mdx was published with keep the
 * ids they were published under, pinned when relabelled. The sections Tested
 * on gained in PR3 (reference-hardware to not-tested) have the same id in
 * both languages, like the register ids, pinned with `{#id}` on both twins.
 * The h3s a guide links for one feature or for the lab's findings (api-tier,
 * dashboards, alert-rules, lab-findings) are pinned on each twin with that
 * language's id. scripts/check-voice.mjs reports a link whose target the
 * built page does not have.
 */
export const TESTED_ON_SECTIONS = {
	"feature-status": {
		en: "what-works-end-to-end",
		es: "qué-funciona-de-extremo-a-extremo",
	},
	"agent-cost": {
		en: "what-the-agent-costs-today",
		es: "lo-que-cuesta-hoy-el-agente",
	},
	"cost-by-configuration": {
		en: "cost-at-10-hz-by-configuration",
		es: "coste-a-10-hz-por-configuración",
	},
	"devices-and-versions": {
		en: "one-device-two-routeros-versions",
		es: "un-equipo-dos-versiones-de-routeros",
	},
	"api-tier": { en: "routeros-api-tier", es: "capa-de-la-api" },
	dashboards: { en: "the-dashboards", es: "los-dashboards" },
	"alert-rules": { en: "the-alert-rules", es: "las-reglas-de-alerta" },
	"test-suites": { en: "the-test-suites", es: "las-baterías-de-pruebas" },
	"known-issues": {
		en: "found-and-not-fixed",
		es: "encontrado-y-sin-corregir",
	},
	"uncollected-sources": {
		en: "found-on-the-device-and-not-collected",
		es: "encontrado-en-el-equipo-y-sin-recoger",
	},
	"reference-hardware": {
		en: "reference-hardware",
		es: "reference-hardware",
	},
	"virtual-lab": { en: "virtual-lab", es: "virtual-lab" },
	"install-routes": {
		en: "install-routes-tested",
		es: "install-routes-tested",
	},
	campaigns: { en: "campaigns", es: "campaigns" },
	verified: {
		en: "verified-routeros-behaviour",
		es: "verified-routeros-behaviour",
	},
	"not-tested": { en: "not-tested", es: "not-tested" },
	"lab-findings": {
		en: "found-in-the-virtual-lab",
		es: "encontrado-en-el-laboratorio-virtual",
	},
};

/**
 * The path of Tested on in a locale, base included, with an optional fragment.
 *
 * The base is written out, as PrivilegedOnly and page-markdown.mjs write it,
 * because this runs under plain Node too, where the build's BASE_URL is not
 * set; gen-docs.mjs and the twins make it absolute afterwards.
 *
 * @param {"en" | "es"} lang
 * @param {string} [fragment]
 * @returns {string}
 */
export function testedOnHref(lang, fragment) {
	const path =
		lang === "en"
			? `/mikroscope/${TESTED_ON_SLUG}/`
			: `/mikroscope/${lang}/${TESTED_ON_SLUG}/`;
	return fragment ? `${path}#${fragment}` : path;
}

/**
 * The fragment <TestedOn of="…"> points at, or undefined for a name that is
 * none of a campaign, a verified fact or a section. `of` may be empty, which
 * is the top of the page.
 *
 * @param {string} of
 * @param {"en" | "es"} lang
 * @param {{ isCampaign: (id: string) => boolean, isVerified: (id: string) => boolean }} data
 * @returns {string | null | undefined} the fragment, null for the page itself
 */
export function testedOnFragment(of, lang, data) {
	if (of === undefined || of === "") return null;
	const hits = [
		data.isCampaign(of) ? campaignAnchor(of) : undefined,
		data.isVerified(of) ? verifiedAnchor(of) : undefined,
		Object.hasOwn(TESTED_ON_SECTIONS, of)
			? TESTED_ON_SECTIONS[of][lang]
			: undefined,
	].filter((hit) => hit !== undefined);
	if (hits.length > 1) {
		throw new Error(
			`TestedOn: "${of}" is both a campaign, a verified fact or a section; rename one (src/lib/voice.mjs)`,
		);
	}
	return hits[0];
}

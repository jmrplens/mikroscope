// @ts-check
/**
 * Keeps a value on one line in running text: an ISO date, a number and its
 * unit, a number's digit groups.
 *
 * The components already do this for the values they render —
 * FaultSignature's `<time>`, Provenance's facts, `formatQuantity`'s no-break
 * space — because "2026-09-" / "12" reads as a different date and "10" / "Hz"
 * as a number with no unit. Prose typed into the pages had no such rule: at
 * WebKit 390 px 100 ISO dates broke at a hyphen, 103 numbers left their unit
 * behind and 15 digit groups split ("6" / "690 304 B"), on 77 pages; at
 * Chromium 360 px 88, 109 and 8 on 76 (every text node outside code, all 116
 * pages, 2026-09-24). With this pass and its uses in the components, 0 and 0.
 *
 * A date is wrapped in a `.ms-nowrap` span, which leaves its hyphens as they
 * were typed: a date copied from the page is still the date. The space inside
 * "10 Hz" or "12 000" becomes U+00A0, which is safe because it is only ever
 * prose: nothing a reader copies into a shell passes through here.
 *
 * Only text directly inside the prose elements below is touched: never code,
 * whose characters are the reader's to copy exactly, and never a heading,
 * whose text is also its anchor.
 */

const DATE = /\b\d{4}-\d{2}-\d{2}\b/g;

// A digit, an ordinary space, then a unit that ends there. Letters after the
// unit mean a word ("10 samples", "5 hours"), which is left alone.
const UNIT =
	/(\d) (%|‰|°C|µs|us|ns|ms|s|min|h|Hz|kHz|MHz|GHz|B|kB|KB|MB|GB|TB|KiB|MiB|GiB|TiB|bit\/s|kbit\/s|Mbit\/s|Gbit\/s|Mbps|Gbps|pps|\/s|×)(?![\p{L}\p{N}_])/gu;

// Digit groups written with an ordinary space: "12 000", "6 690 304".
const GROUP = /(?<![\d.,])(\d{1,3})((?: \d{3})+)(?![\d\p{L}])/gu;

const PROSE = [
	"p",
	"li",
	"td",
	"th",
	"dd",
	"dt",
	"figcaption",
	"blockquote",
	"a",
	"strong",
	"em",
	"span",
];

/** @param {string} text */
export function keepValues(text) {
	return text
		.replace(GROUP, (_, head, rest) => head + rest.replaceAll(" ", " "))
		.replace(UNIT, "$1 $2");
}

/**
 * @param {string} text
 * @returns {({ kind: "text", value: string } | { kind: "date", value: string })[]}
 */
export function valueParts(text) {
	const parts = [];
	let at = 0;
	for (const match of text.matchAll(DATE)) {
		const start = match.index ?? 0;
		if (start > at)
			parts.push({
				kind: /** @type {const} */ ("text"),
				value: keepValues(text.slice(at, start)),
			});
		parts.push({ kind: /** @type {const} */ ("date"), value: match[0] });
		at = start + match[0].length;
	}
	if (at < text.length)
		parts.push({
			kind: /** @type {const} */ ("text"),
			value: keepValues(text.slice(at)),
		});
	return parts;
}

/**
 * The same rule for an HTML string a component renders with `set:html` (the
 * landing's copy in src/data/home.ts): text between tags only, and never
 * inside `<code>`.
 * @param {string} html
 */
export function valuesInHtml(html) {
	let inCode = 0;
	return html
		.split(/(<[^>]+>)/)
		.map((part) => {
			if (part.startsWith("<")) {
				if (/^<code\b/i.test(part)) inCode += 1;
				else if (/^<\/code>/i.test(part)) inCode = Math.max(0, inCode - 1);
				return part;
			}
			if (inCode > 0) return part;
			return valueParts(part)
				.map((v) =>
					v.kind === "date"
						? `<span class="ms-nowrap">${v.value}</span>`
						: v.value,
				)
				.join("");
		})
		.join("");
}

const SKIP = new Set([
	"pre",
	"code",
	"svg",
	"h1",
	"h2",
	"h3",
	"h4",
	"h5",
	"h6",
]);

/** @param {any} node @param {any} ctx */
function skipped(node, ctx) {
	for (let at = node; at; at = ctx.parent(at)) {
		if (at.type === "element" && SKIP.has(at.tagName)) return true;
	}
	return false;
}

/** The Sätteri hast plugin that applies valueParts to prose text. */
export function nowrapValues() {
	return {
		name: "mikroscope-nowrap-values",
		element: {
			filter: PROSE,
			/** @param {any} node @param {any} ctx */
			visit(node, ctx) {
				const cls = node.properties?.class ?? node.properties?.className ?? "";
				if (String(cls).includes("ms-nowrap") || skipped(node, ctx)) return;
				const children = node.children ?? [];
				let changed = false;
				const next = children.flatMap((/** @type {any} */ child) => {
					if (child.type !== "text") return [child];
					const parts = valueParts(child.value);
					if (
						parts.length === 1 &&
						parts[0].kind === "text" &&
						parts[0].value === child.value
					)
						return [child];
					changed = true;
					return parts.map((p) =>
						p.kind === "date"
							? {
									type: "element",
									tagName: "span",
									properties: { class: "ms-nowrap" },
									children: [{ type: "text", value: p.value }],
								}
							: { type: "text", value: p.value },
					);
				});
				if (!changed) return;
				return { ...node, children: next };
			},
		},
	};
}

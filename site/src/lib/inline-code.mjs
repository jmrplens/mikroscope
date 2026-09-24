// @ts-check
/**
 * Where inline code may break, decided from its text.
 *
 * Browsers break a line after a hyphen, so `--for` rendered as "-" at the end
 * of one line and "-for" at the start of the next, and a reader copying by eye
 * saw a single-dash flag: 126 broken flags on 42 pages at 390 px and 55 on 31
 * pages at 1280 px (Range rects on every inline code, Playwright, 2026-09-15).
 * The opposite failure is a long metric name with no break opportunity at all,
 * which widens its table column to 300-350 px and pushes the description
 * column out of a 616 px content box.
 *
 * So: code of up to SHORT characters never breaks. In longer code, each
 * whitespace-separated token that starts with dashes keeps `--name` together,
 * and a long token gets a `<wbr>` after each `_`, `/`, `.` or `,`, so
 * `mikroscope_cpu_busy_ticks` may wrap at an underscore and nowhere else.
 *
 * Three places are not breaks even so, because the halves read as two values
 * (all 116 pages at WebKit 390 px and Chromium 360 px, 2026-09-24):
 *
 * - after a LEADING separator: `/` then `system/package/enable container`,
 *   `.` then `proplist=…`, a lone character at the end of a line on 17-20
 *   pages;
 * - before a digit: `mikroscope-agent:1.` / `2.2`, `172.30.10.` / `0/30`;
 * - after a `.` that opens a short last part: `prometheus.` / `go`,
 *   `ghcr.` / `io/…`.
 *
 * Nor, fourth, between two separators: `https:/` / `/raw.…`. The built site
 * offered such a break inside 20 chips (2026-09-25): after the first slash
 * of `http://` or `https://` in 10, between `_` and `.` of a character class
 * `[A-Za-z0-9_.-]` in 8, inside `$__interval` in 2; whether any of them fell
 * at a line end was not measured. It was ruled out when the installer's URL
 * in a `wrap` code block began to break at these same places
 * (src/lib/code-wrap-words.mjs).
 *
 * The comma is a break for the opposite reason: in
 * `mikroscope_api_interface_info{interface,label,type,role,bridge,default_name}`
 * the 46 characters from `info{` to `default_` had no break opportunity, so
 * Starlight's `overflow-wrap: anywhere` in list items split them mid-word,
 * "def|ault_name" (sinks/api-tier and three more pages, both locales). No
 * character is inserted: `<wbr>` is not text, so a copied flag or metric name
 * is exactly what the page shows. A word joiner would travel with the copy and
 * break it in a shell.
 *
 * A piece that must not break is also drawn as one box, `.ms-code-unit`
 * (`display: inline-block`), with the characters that touch it. As a nowrap
 * inline, a chip that did not fit the rest of its line made WebKit paint the
 * word before it twice, once at the end of the line and again at the start of
 * the next: "the list your / your `in-interface-list=!…`" (install/
 * prerequisites, reference/cli), "one of / of `page_writes`" (playbooks/
 * flash-wear), "PPPoE_DIGI / DIGI `--counters-every`" inside a long command
 * (sinks/api-tier); WebKit 390 px, 2026-09-24. An atomic box does not
 * trigger it, but it is a break opportunity on both sides, where a nowrap
 * inline is not: on its own it left "(" at a line end and opened the next
 * line with ")", "," or ":" 123 times at WebKit 390 px, 161 at Chromium 360
 * and 186 at Chromium 1280 (all 116 pages, 2026-09-24).
 * So the box takes what touches the chip with no space between: an opening
 * "(", "[" or "“" before it, and ")", "]", ",", ".", ":", ";", "”" or the
 * rest of a word ("`GET`s", "`eBPF`-") after it, up to the first place the
 * line could have broken anyway. With both: 0 words painted twice and 0
 * punctuation marks on the wrong line at WebKit 390 px, Chromium 360 px and
 * Chromium 1280 px, all 116 pages (2026-09-25).
 *
 * Used four ways: by the Sätteri hast plugins below for markdown, by
 * <InlineCode> and <CodeText> for code the components render from data
 * strings (codeRuns), by codeInHtml for the landing's HTML strings, and by
 * src/lib/code-wrap-words.mjs for a word too wide for a `wrap` code block
 * (breakOffsets).
 */

const SHORT = 24;
const LONG_TOKEN = 16;
const FLAG = /^-+[^\s=/,]*/;

/**
 * @typedef {{ kind: "text", value: string }
 *   | { kind: "keep", value: string, unit: boolean }
 *   | { kind: "wbr" }} Part
 */

/**
 * The break opportunities of one long token, as the pieces between them.
 * @param {string} rest
 * @returns {string[]}
 */
function pieces(rest) {
	const out = rest.split(/(?<=[_/.,])/);
	if (out.length > 1 && out[0].length === 1) out.splice(0, 2, out[0] + out[1]);
	/** @type {string[]} */
	const joined = [];
	out.forEach((piece, i) => {
		const prev = out[i - 1];
		const glued =
			prev !== undefined &&
			(/^\d/.test(piece) ||
				/^[_/.,]+$/.test(piece) ||
				(prev.endsWith(".") && piece.replace(/[_/.,]$/, "").length <= 3));
		if (glued) joined[joined.length - 1] += piece;
		else joined.push(piece);
	});
	return joined;
}

/**
 * @param {string} text
 * @returns {{ nowrap: boolean, parts: Part[] }}
 */
export function inlineCodeParts(text) {
	if (text.length <= SHORT)
		return { nowrap: true, parts: [{ kind: "text", value: text }] };
	/** @type {Part[]} */
	const parts = [];
	const push = (/** @type {string} */ value) => {
		if (value === "") return;
		const last = parts.at(-1);
		if (last?.kind === "text") last.value += value;
		else parts.push({ kind: "text", value });
	};
	for (const token of text.split(/(\s+)/)) {
		if (token === "") continue;
		if (/^\s+$/.test(token)) {
			push(token);
			continue;
		}
		const flag = FLAG.exec(token)?.[0] ?? "";
		const rest = token.slice(flag.length);
		const segments = token.length <= LONG_TOKEN ? [rest] : pieces(rest);
		segments.forEach((segment, i) => {
			if (i > 0) parts.push({ kind: "wbr" });
			// A flag keeps everything up to the token's first break with it,
			// `--out=/`, so the box it is drawn as never parts it from the
			// characters it touches.
			if (i === 0 && flag !== "")
				parts.push({ kind: "keep", value: flag + segment, unit: false });
			else push(segment);
		});
	}
	// A kept flag between two other parts of the code touches only a space or a
	// break: it is safe to draw as a box. At either end what touches it is
	// outside the code, and inlineCodeUnits decides.
	parts.forEach((p, i) => {
		if (p.kind === "keep") p.unit = i > 0 && i < parts.length - 1;
	});
	return { nowrap: false, parts };
}

/**
 * Where one long word of code may break, as offsets into it: the places
 * inlineCodeParts puts a `<wbr>`. None for a word of up to SHORT characters.
 * @param {string} word
 * @returns {number[]}
 */
export function breakOffsets(word) {
	/** @type {number[]} */
	const out = [];
	let at = 0;
	for (const part of inlineCodeParts(word).parts) {
		if (part.kind === "wbr") out.push(at);
		else at += part.value.length;
	}
	return out;
}

// What a unit takes from the text around it: the characters that touch it
// with no space between, up to the first place the line could break anyway.
// Before: back to a space, a hyphen, a dash or a slash, which a line may break
// after. After: up to a space, or up to and including a hyphen, a dash or a
// slash. `<` and `>` stop both, so a tag in an HTML string is never taken.
const BEFORE = /[^\s\-‐–—/<>]+$/u;
const AFTER = /^[^\s\-‐–—/<>]*[-‐–—/]?/u;

/**
 * The text between two inline pieces, split into what the unit on its left
 * takes, what stays, and what the unit on its right takes. Both neighbours
 * compute it the same way, so a text between two chips is split once.
 * @param {string} text
 * @param {boolean} leftUnit
 * @param {boolean} rightUnit
 * @returns {[string, string, string]}
 */
export function partition(text, leftUnit, rightUnit) {
	const after = leftUnit ? (AFTER.exec(text)?.[0] ?? "") : "";
	const rest = text.slice(after.length);
	const before = rightUnit ? (BEFORE.exec(rest)?.[0] ?? "") : "";
	return [after, rest.slice(0, rest.length - before.length), before];
}

/**
 * A data string with `backtick` code spans, as text and code runs, each short
 * code carrying the characters around it that its unit takes.
 * @param {string} s
 * @returns {({ kind: "text", value: string } | { kind: "code", value: string, before: string, after: string })[]}
 */
export function codeRuns(s) {
	const raw = s.split("`").map((value, i) => ({ code: i % 2 === 1, value }));
	const unit = (/** @type {number} */ i) =>
		raw[i]?.code === true && inlineCodeParts(raw[i].value).nowrap;
	const texts = raw.map((r, i) =>
		r.code ? null : partition(r.value, unit(i - 1), unit(i + 1)),
	);
	/** @type {({ kind: "text", value: string } | { kind: "code", value: string, before: string, after: string })[]} */
	const out = [];
	raw.forEach((r, i) => {
		if (r.code) {
			if (r.value === "") return;
			const own = unit(i);
			out.push({
				kind: "code",
				value: r.value,
				before: own ? (texts[i - 1]?.[2] ?? "") : "",
				after: own ? (texts[i + 1]?.[0] ?? "") : "",
			});
			return;
		}
		// Whatever a unit on either side takes is drawn inside that unit.
		const value = /** @type {[string, string, string]} */ (texts[i])[1];
		if (value !== "") out.push({ kind: "text", value });
	});
	return out;
}

/** @param {string} s */
const escapeHtml = (s) =>
	s.replaceAll("&", "&amp;").replaceAll("<", "&lt;").replaceAll(">", "&gt;");

/** @param {string} s */
const unescapeHtml = (s) =>
	s
		.replaceAll("&lt;", "<")
		.replaceAll("&gt;", ">")
		.replaceAll("&quot;", '"')
		.replaceAll("&#39;", "'")
		.replaceAll("&amp;", "&");

/**
 * The same rules for an HTML string a component renders with `set:html` (the
 * landing's copy in src/data/home.ts): each plain `<code>…</code>` gets its
 * breaks, and a short one becomes a unit with what touches it.
 * @param {string} html
 */
export function codeInHtml(html) {
	const tokens = html.split(/(<code>[^<]*<\/code>)/);
	const isCode = (/** @type {number} */ i) =>
		tokens[i]?.startsWith("<code>") === true;
	const inner = (/** @type {number} */ i) =>
		unescapeHtml(tokens[i].slice("<code>".length, -"</code>".length));
	const unit = (/** @type {number} */ i) =>
		isCode(i) && inlineCodeParts(inner(i)).nowrap;
	const split = tokens.map((t, i) =>
		isCode(i) ? null : partition(t, unit(i - 1), unit(i + 1)),
	);
	return tokens
		.map((_, i) => {
			if (!isCode(i))
				return /** @type {[string, string, string]} */ (split[i])[1];
			const { nowrap, parts } = inlineCodeParts(inner(i));
			if (nowrap) {
				const before = split[i - 1]?.[2] ?? "";
				const after = split[i + 1]?.[0] ?? "";
				return `<span class="ms-code-unit">${before}<code class="ms-code-nowrap">${escapeHtml(inner(i))}</code>${after}</span>`;
			}
			const body = parts
				.map((p) =>
					p.kind === "wbr"
						? "<wbr>"
						: p.kind === "keep"
							? `<span class="${keepClass(p)}">${escapeHtml(p.value)}</span>`
							: escapeHtml(p.value),
				)
				.join("");
			return `<code>${body}</code>`;
		})
		.join("");
}

/** @param {{ unit: boolean }} keep */
export const keepClass = (keep) =>
	keep.unit ? "ms-code-nowrap ms-code-unit" : "ms-code-nowrap";

/** @param {any} node @param {any} ctx */
function insidePre(node, ctx) {
	let child = node;
	while (child) {
		const parent = ctx.parent(child);
		if (parent?.type === "element" && parent.tagName === "pre") return true;
		child = parent;
	}
	return false;
}

/** The Sätteri hast plugin that applies inlineCodeParts to markdown code. */
export function inlineCodeNowrap() {
	return {
		name: "mikroscope-inline-code",
		element: {
			filter: ["code"],
			/** @param {any} node @param {any} ctx */
			visit(node, ctx) {
				if (insidePre(node, ctx)) return;
				const onlyText =
					node.children?.length === 1 && node.children[0].type === "text";
				if (!onlyText) return;
				const { nowrap, parts } = inlineCodeParts(node.children[0].value);
				if (nowrap) {
					ctx.setProperty(node, "class", "ms-code-nowrap");
					return;
				}
				if (parts.length === 1 && parts[0].kind === "text") return;
				return {
					...node,
					children: parts.map((p) =>
						p.kind === "wbr"
							? {
									type: "element",
									tagName: "wbr",
									properties: {},
									children: [],
								}
							: p.kind === "keep"
								? {
										type: "element",
										tagName: "span",
										properties: { class: keepClass(p) },
										children: [{ type: "text", value: p.value }],
									}
								: { type: "text", value: p.value },
					),
				};
			},
		},
	};
}

// Inline elements a chip can sit in alone, `[`x`](…)` or `**`x`**`: the unit
// is then the outermost of them, so the link or the emphasis moves with it.
const WRAPPERS = new Set(["a", "strong", "em", "b", "i", "del"]);

// Components a page writes inline that render a chip: <Src> as a linked path,
// <PrivilegedOnly> as "needs `privileged=yes`". Their markup is not in the
// tree this plugin sees, so the unit is drawn around the component.
const CHIP_COMPONENTS = new Set(["Src", "PrivilegedOnly"]);

/** @param {any} node */
const classOf = (node) =>
	String(node?.properties?.class ?? node?.properties?.className ?? "");

/** @param {any} node */
const isKeep = (node) =>
	node?.type === "element" &&
	node.tagName === "span" &&
	/\bms-code-nowrap\b/.test(classOf(node));

/**
 * Code drawn as one box: short code, or long code that is a single kept flag.
 * @param {any} node
 */
const isChip = (node) =>
	node?.type === "element" &&
	node.tagName === "code" &&
	(/\bms-code-nowrap\b/.test(classOf(node)) ||
		(node.children?.length === 1 && isKeep(node.children[0])));

/** @param {any} node */
function isComponentChip(node) {
	if (node?.type !== "mdxJsxTextElement" || !CHIP_COMPONENTS.has(node.name))
		return false;
	if (node.name !== "Src") return true;
	const path = node.attributes?.find(
		(/** @type {any} */ a) => a.type === "mdxJsxAttribute" && a.name === "path",
	)?.value;
	return typeof path === "string" && inlineCodeParts(path).nowrap;
}

/**
 * Whether a sibling is, or will be made, a unit. Both neighbours of a text
 * ask it of each other, so it reads only what the previous pass left.
 * @param {any} node
 */
function isUnit(node) {
	let at = node;
	while (
		at?.type === "element" &&
		WRAPPERS.has(at.tagName) &&
		at.children?.length === 1
	)
		at = at.children[0];
	return isChip(at) || isComponentChip(at);
}

/**
 * The text beside `unit` split the way both of its neighbours split it.
 * @param {any[]} siblings @param {number} j
 */
function splitAt(siblings, j) {
	const t = siblings[j];
	if (t?.type !== "text") return null;
	return partition(t.value, isUnit(siblings[j - 1]), isUnit(siblings[j + 1]));
}

/**
 * Wraps one unit and the characters that touch it in `.ms-code-unit`.
 * @param {any} unit @param {any} ctx
 */
function wrapUnit(unit, ctx) {
	const parent = ctx.parent(unit);
	const i = ctx.indexOf(unit);
	if (!parent || i === undefined) return;
	const siblings = parent.children;
	const children = [];
	// A text between two units is set to the same remainder by both of them.
	const left = splitAt(siblings, i - 1);
	if (left) {
		if (left[2] !== "") children.push({ type: "text", value: left[2] });
		if (left[1] !== siblings[i - 1].value)
			ctx.setProperty(siblings[i - 1], "value", left[1]);
	}
	children.push(unit);
	const right = splitAt(siblings, i + 1);
	if (right) {
		if (right[0] !== "") children.push({ type: "text", value: right[0] });
		if (right[1] !== siblings[i + 1].value)
			ctx.setProperty(siblings[i + 1], "value", right[1]);
	}
	ctx.replaceNode(unit, {
		type: "element",
		tagName: "span",
		properties: { class: "ms-code-unit" },
		children,
	});
}

/**
 * The Sätteri hast plugin that draws each chip as a unit with the characters
 * that touch it. Runs after inlineCodeNowrap, whose classes it reads.
 */
export function inlineCodeUnits() {
	return {
		name: "mikroscope-inline-code-units",
		element: {
			filter: ["code"],
			/** @param {any} node @param {any} ctx */
			visit(node, ctx) {
				if (insidePre(node, ctx)) return;
				let unit = node;
				for (
					let up = ctx.parent(unit);
					up?.type === "element" &&
					WRAPPERS.has(up.tagName) &&
					up.children?.length === 1;
					up = ctx.parent(unit)
				)
					unit = up;
				if (isChip(node)) {
					wrapUnit(unit, ctx);
					return;
				}
				// Long code: a kept flag at either end is a box only where what
				// touches it outside the code is a space or nothing.
				const kids = node.children ?? [];
				const parent = ctx.parent(unit);
				const i = ctx.indexOf(unit);
				if (!parent || i === undefined) return;
				const prev = parent.children[i - 1];
				const next = parent.children[i + 1];
				const first = kids[0];
				const last = kids.at(-1);
				if (
					isKeep(first) &&
					(prev === undefined ||
						(prev.type === "text" && /\s$/.test(prev.value)))
				)
					ctx.setProperty(first, "class", "ms-code-nowrap ms-code-unit");
				if (
					last !== first &&
					isKeep(last) &&
					(next === undefined ||
						(next.type === "text" && /^\s/.test(next.value)))
				)
					ctx.setProperty(last, "class", "ms-code-nowrap ms-code-unit");
			},
		},
		mdxJsxTextElement: {
			filter: [...CHIP_COMPONENTS],
			/** @param {any} node @param {any} ctx */
			visit(node, ctx) {
				if (isComponentChip(node)) wrapUnit(node, ctx);
			},
		},
	};
}

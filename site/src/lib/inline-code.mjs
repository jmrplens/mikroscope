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
 * and a long token gets a `<wbr>` after each `_`, `/` or `.`, so
 * `mikroscope_cpu_busy_ticks` may wrap at an underscore and nowhere else. No
 * character is inserted: `<wbr>` is not text, so a copied flag or metric name
 * is exactly what the page shows. A word joiner would travel with the copy and
 * break it in a shell.
 *
 * Used twice: by the Sätteri hast plugin below for markdown, and by
 * <InlineCode> for code the components render from data strings.
 */

const SHORT = 24;
const LONG_TOKEN = 16;
const FLAG = /^-+[^\s=/,]*/;

/**
 * @typedef {{ kind: "text", value: string } | { kind: "keep", value: string } | { kind: "wbr" }} Part
 */

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
		let rest = token;
		const flag = FLAG.exec(token)?.[0];
		if (flag) {
			parts.push({ kind: "keep", value: flag });
			rest = token.slice(flag.length);
		}
		if (token.length <= LONG_TOKEN) {
			push(rest);
			continue;
		}
		for (const piece of rest.split(/(?<=[_/.])/)) {
			push(piece);
			if (/[_/.]$/.test(piece)) parts.push({ kind: "wbr" });
		}
		if (parts.at(-1)?.kind === "wbr") parts.pop();
	}
	return { nowrap: false, parts };
}

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
										properties: { class: "ms-code-nowrap" },
										children: [{ type: "text", value: p.value }],
									}
								: { type: "text", value: p.value },
					),
				};
			},
		},
	};
}

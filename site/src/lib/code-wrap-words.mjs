// @ts-check
/**
 * An Expressive Code plugin: in a code block marked `wrap`, each run of
 * characters between spaces becomes one `span.ms-code-word`, so a wrapped
 * line breaks at its spaces and nowhere else.
 *
 * CSS alone cannot say that. Expressive Code sets `overflow-wrap: break-word`
 * on a wrapped line, and with it the start/walkthrough blocks broke
 * "--e|phemeral" and "192.16|8.88.1", 17-18 times a page in WebKit at 390 px
 * and Chromium at 360 px. `overflow-wrap: normal`, tried next, still leaves
 * the break after every hyphen that the line-breaking rules allow: 6 a page
 * at 390 and 360 px and 2 at 1280, "install --|ephemeral", "-|-yes",
 * "2026-09-|16T08:38:27Z", and a URL wider than the box was cut off at its
 * edge with nothing to show it went on (2026-09-25). A reader copying a
 * command by eye sees a different flag, the failure src/lib/inline-code.mjs
 * exists to stop in inline code.
 *
 * components.css draws each `.ms-code-word` as an inline block no wider than
 * the box: a word that fits moves to the next line whole. A long word is also
 * cut into `.ms-code-piece` blocks where inline code of the same text may
 * break (breakOffsets in src/lib/inline-code.mjs: after a `_`, `/`, `.` or
 * `,`, never before a digit or between two separators). Those cuts matter
 * only once the word is wider than the whole box, the one case in which it
 * breaks inside itself: the installer's URL then wraps as
 * "https://raw.githubusercontent.com/jmrplens/mikroscope/main/" /
 * "install.sh" at 1280 px, and at 390 px
 * `MIKROSCOPE_ROUTER=admin@192.168.88.1` as "MIKROSCOPE_" /
 * "ROUTER=admin@192.168.88.1", where the box's edge would have cut it at
 * ".88.1". Only a piece wider than the box breaks where the box ends.
 *
 * The highlighter's token spans do not follow the spaces: one token can hold
 * a space, and one word can span several tokens, such as a quoted variable.
 * So the line is flattened to its text runs, each with the chain of elements
 * it sat in, and rebuilt: every word's runs go inside its own span, each run
 * inside copies of its original elements, which keep their inline colours.
 * The text, and so what a reader selects and copies, is unchanged; the copy
 * button copies the block's source, which this never touches.
 */

import { getClassNames } from "@astrojs/starlight/expressive-code/hast";
import { breakOffsets } from "./inline-code.mjs";

/**
 * @typedef {import("@astrojs/starlight/expressive-code/hast").Element} Element
 * @typedef {import("@astrojs/starlight/expressive-code/hast").ElementContent} ElementContent
 * @typedef {{ children: ElementContent[] }} Parent
 * @typedef {{ node: ElementContent, chain: Element[], space: boolean }} Run
 */

/**
 * Appends `node` to `parent` inside copies of the elements in `chain`,
 * reusing the copies the previous run of the same element left open.
 * @param {Parent} parent
 * @param {Element[]} chain
 * @param {ElementContent} node
 * @param {WeakMap<object, Element>} copyOf
 */
function append(parent, chain, node, copyOf) {
	let at = parent;
	for (const element of chain) {
		const last = at.children.at(-1);
		if (last && copyOf.get(last) === element) {
			at = /** @type {Element} */ (last);
			continue;
		}
		/** @type {Element} */
		const copy = {
			...element,
			properties: { ...element.properties },
			children: [],
		};
		copyOf.set(copy, element);
		at.children.push(copy);
		at = copy;
	}
	const last = at.children.at(-1);
	if (node.type === "text" && last?.type === "text") last.value += node.value;
	else at.children.push(node);
}

/**
 * The line's content as runs of text, each a space or not, with the elements
 * it sits in.
 * @param {ElementContent[]} children
 * @returns {Run[]}
 */
function runsOf(children) {
	/** @type {Run[]} */
	const runs = [];
	/** @param {ElementContent[]} nodes @param {Element[]} chain */
	const walk = (nodes, chain) => {
		for (const node of nodes) {
			if (node.type === "element") {
				walk(node.children, [...chain, node]);
			} else if (node.type !== "text") {
				runs.push({ node, chain, space: false });
			} else {
				for (const value of node.value.split(/([ \t]+)/)) {
					if (value === "") continue;
					const space = /^[ \t]/.test(value);
					runs.push({ node: { type: "text", value }, chain, space });
				}
			}
		}
	};
	walk(children, []);
	return runs;
}

/**
 * The children of a line's `.code` element, regrouped so that every word is
 * one `span.ms-code-word`, a long one cut into `span.ms-code-piece` inside
 * it, and every space sits between words.
 * @param {ElementContent[]} children
 * @returns {ElementContent[]}
 */
export function groupWords(children) {
	const runs = runsOf(children);
	/** @type {Parent} */
	const line = { children: [] };
	/** @type {WeakMap<object, Element>} */
	const copyOf = new WeakMap();
	/** @param {Parent} parent @param {string} name */
	const open = (parent, name) => {
		/** @type {Element} */
		const span = {
			type: "element",
			tagName: "span",
			properties: { className: [name] },
			children: [],
		};
		parent.children.push(span);
		return span;
	};
	let i = 0;
	while (i < runs.length) {
		if (runs[i].space) {
			append(line, runs[i].chain, runs[i].node, copyOf);
			i += 1;
			continue;
		}
		let end = i;
		while (end < runs.length && !runs[end].space) end += 1;
		const word = runs.slice(i, end);
		const text = word
			.map(({ node }) => (node.type === "text" ? node.value : ""))
			.join("");
		const cuts = breakOffsets(text);
		const whole = open(line, "ms-code-word");
		let box = cuts.length > 0 ? open(whole, "ms-code-piece") : whole;
		let at = 0;
		for (const { node, chain } of word) {
			if (node.type !== "text") {
				append(box, chain, node, copyOf);
				continue;
			}
			let value = node.value;
			while (cuts.length > 0 && cuts[0] < at + value.length) {
				const cut = /** @type {number} */ (cuts.shift()) - at;
				if (cut > 0)
					append(
						box,
						chain,
						{ type: "text", value: value.slice(0, cut) },
						copyOf,
					);
				box = open(whole, "ms-code-piece");
				value = value.slice(cut);
				at += cut;
			}
			if (value !== "") append(box, chain, { type: "text", value }, copyOf);
			at += value.length;
		}
		i = end;
	}
	return line.children;
}

/** @returns {import("@astrojs/starlight/expressive-code").ExpressiveCodePlugin} */
export function wrapWords() {
	return {
		name: "mikroscope-wrap-words",
		hooks: {
			postprocessRenderedLine({ codeBlock, renderData }) {
				if (!codeBlock.props.wrap) return;
				const code = renderData.lineAst.children.find(
					(child) =>
						child.type === "element" && getClassNames(child).includes("code"),
				);
				if (!code || code.type !== "element") return;
				code.children = groupWords(code.children);
			},
		},
	};
}

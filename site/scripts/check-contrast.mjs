#!/usr/bin/env node
/**
 * Measures the palette against WCAG 2.2 AA, in both themes, from the sheets the
 * site actually loads.
 *
 * The point is that nothing here restates a colour. The stylesheet list comes
 * out of `astro.config.mjs`, every value comes out of those sheets by token
 * name, and the ratios are computed. A hex typed into this file would be a
 * second palette, and a second palette drifts the first time the real one
 * moves — so a token deleted or renamed makes this gate measure whatever the
 * page really resolves to, and fail, rather than keep reporting the old number.
 *
 * Two gates run:
 *
 *   1. Every pair in PAIRS clears its threshold in BOTH themes.
 *   2. Every colour token declared for one theme is declared for the other.
 *      This is checked against the DECLARED blocks, not the resolved palettes:
 *      the light palette inherits every dark token it does not override, so
 *      comparing resolved maps would only ever catch light-only tokens and miss
 *      the dangerous direction — a token declared for dark alone keeps its dark
 *      value on a white page.
 *
 * Usage: node scripts/check-contrast.mjs
 */

import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const siteRoot = path.dirname(
	fileURLToPath(new URL("../package.json", import.meta.url)),
);

/* WCAG 2.2: 1.4.3 for text, 1.4.11 for anything that is not. */
const NORMAL_TEXT = 4.5;
const LARGE_TEXT = 3;
const NON_TEXT = 3;

/* ------------------------------------------------------------------ parsing */

/** The sheets the site loads, in the order it loads them. */
function registeredSheets() {
	const source = fs.readFileSync(
		path.join(siteRoot, "astro.config.mjs"),
		"utf8",
	);
	const array = /customCss:\s*\[([\s\S]*?)\]/.exec(source);
	if (array === null)
		throw new Error("no `customCss` array found in astro.config.mjs");
	const paths = [...array[1].matchAll(/"([^"]+\.css)"/g)].map((m) => m[1]);
	if (paths.length === 0)
		throw new Error("`customCss` in astro.config.mjs registers no sheet");
	return paths.map((p) => path.join(siteRoot, p));
}

/**
 * Every `selector { … }` block in a sheet, remembering the at-rule it sits in.
 * Enough of a parser for the sheets this project writes, and it says so loudly
 * rather than pretending to be a CSS engine.
 */
function blocksOf(css) {
	const blocks = [];
	const stripped = css.replace(/\/\*[\s\S]*?\*\//g, "");
	let atRule = null;
	let depth = 0;
	const re = /([^{}]+)\{|\}/g;
	let match;
	while ((match = re.exec(stripped)) !== null) {
		if (match[0] === "}") {
			depth -= 1;
			if (depth === 0) atRule = null;
			continue;
		}
		const head = match[1].trim();
		if (head.startsWith("@")) {
			atRule = head;
			depth += 1;
			continue;
		}
		const start = re.lastIndex;
		let end = start;
		let nested = 1;
		while (end < stripped.length && nested > 0) {
			if (stripped[end] === "{") nested += 1;
			if (stripped[end] === "}") nested -= 1;
			end += 1;
		}
		blocks.push({
			selector: head,
			atRule,
			body: stripped.slice(start, end - 1),
		});
		re.lastIndex = end;
	}
	return blocks;
}

/** The custom properties one block declares, in source order. */
function tokensIn(body) {
	const out = new Map();
	for (const m of body.matchAll(/(--[a-z0-9-]+)\s*:\s*([^;]+);/gi)) {
		out.set(m[1].trim(), m[2].trim());
	}
	return out;
}

/* --------------------------------------------------------------- resolution */

/** Resolves a value to a literal colour, following var() and color-mix(). */
function resolve(value, palette, seen = new Set()) {
	let v = String(value).trim();

	const varMatch = /^var\(\s*(--[a-z0-9-]+)\s*(?:,\s*([\s\S]+))?\)$/i.exec(v);
	if (varMatch) {
		const name = varMatch[1];
		if (seen.has(name)) throw new Error(`${name} resolves through itself`);
		if (palette.has(name))
			return resolve(palette.get(name), palette, new Set([...seen, name]));
		if (varMatch[2] !== undefined) return resolve(varMatch[2], palette, seen);
		throw new Error(`${name} is not declared in this theme`);
	}

	const mix = /^color-mix\(\s*in\s+srgb\s*,\s*([\s\S]+)\)$/i.exec(v);
	if (mix) {
		const [a, b] = splitTop(mix[1]);
		const first = withShare(a, palette, seen);
		const second = withShare(b, palette, seen);
		/* CSS normalises the two shares to 100%; an omitted one takes the rest. */
		let p1 = first.share;
		let p2 = second.share;
		if (p1 === null && p2 === null) p1 = p2 = 50;
		else if (p1 === null) p1 = 100 - p2;
		else if (p2 === null) p2 = 100 - p1;
		const total = p1 + p2;
		return mixRgb(first.rgb, second.rgb, p1 / total);
	}

	return v;
}

/** Splits `a, b` on the top-level comma only. */
function splitTop(s) {
	const parts = [];
	let depth = 0;
	let start = 0;
	for (let i = 0; i < s.length; i += 1) {
		if (s[i] === "(") depth += 1;
		else if (s[i] === ")") depth -= 1;
		else if (s[i] === "," && depth === 0) {
			parts.push(s.slice(start, i));
			start = i + 1;
		}
	}
	parts.push(s.slice(start));
	return parts.map((p) => p.trim());
}

/** One side of a color-mix: a colour and, maybe, its percentage. */
function withShare(part, palette, seen) {
	const pct = /\s(\d+(?:\.\d+)?)%$/.exec(part);
	const colour = pct ? part.slice(0, pct.index).trim() : part;
	return {
		rgb: toRgb(resolve(colour, palette, seen)),
		share: pct ? Number(pct[1]) : null,
	};
}

function mixRgb(a, b, weightOfA) {
	const c = a.map((x, i) => Math.round(x * weightOfA + b[i] * (1 - weightOfA)));
	return "#" + c.map((x) => x.toString(16).padStart(2, "0")).join("");
}

function toRgb(colour) {
	const hex = /^#([0-9a-f]{3}|[0-9a-f]{6})$/i.exec(colour.trim());
	if (hex) {
		const h =
			hex[1].length === 3 ? [...hex[1]].map((c) => c + c).join("") : hex[1];
		return [0, 2, 4].map((i) => parseInt(h.slice(i, i + 2), 16));
	}
	const rgb = /^rgba?\(([^)]+)\)$/i.exec(colour.trim());
	if (rgb) {
		const parts = rgb[1]
			.split(/[\s,/]+/)
			.filter(Boolean)
			.map(Number);
		return parts.slice(0, 3);
	}
	throw new Error(`cannot read the colour ${colour}`);
}

/* ---------------------------------------------------------------- the maths */

/** WCAG 2.2 relative luminance, §1.4.3 verbatim. */
function luminance(colour) {
	const [r, g, b] = toRgb(colour).map((v) => {
		const c = v / 255;
		return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
	});
	return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

function contrast(a, b) {
	const [la, lb] = [luminance(a), luminance(b)];
	return (Math.max(la, lb) + 0.05) / (Math.min(la, lb) + 0.05);
}

/* ----------------------------------------------------------------- the list */

/**
 * What is measured. `fg` and `bg` are token names, never colours: this file
 * knows what has to be legible against what, and the sheet knows what colour
 * that is.
 *
 * `minimum: null` marks a pair that is reported but not gated, which is for
 * decoration that carries no information on its own.
 */
const PAIRS = [
	{
		label: "body text",
		fg: "--ms-body",
		bg: "--ms-page",
		minimum: NORMAL_TEXT,
	},
	{
		label: "body text on a surface",
		fg: "--ms-body",
		bg: "--ms-surface",
		minimum: NORMAL_TEXT,
	},
	{
		label: "headings",
		fg: "--ms-heading",
		bg: "--ms-page",
		minimum: NORMAL_TEXT,
	},
	{
		label: "muted text",
		fg: "--ms-muted",
		bg: "--ms-page",
		minimum: NORMAL_TEXT,
	},
	{
		label: "muted text on a surface",
		fg: "--ms-muted",
		bg: "--ms-surface",
		minimum: NORMAL_TEXT,
	},
	{
		label: "links and controls",
		fg: "--ms-accent",
		bg: "--ms-page",
		minimum: NORMAL_TEXT,
	},
	{
		label: "links on a surface",
		fg: "--ms-accent",
		bg: "--ms-surface",
		minimum: NORMAL_TEXT,
	},
	{
		label: "a link being hovered",
		fg: "--ms-accent-strong",
		bg: "--ms-page",
		minimum: NORMAL_TEXT,
	},
	{
		label: "the accent on its own tint",
		fg: "--ms-accent",
		bg: "--ms-accent-soft",
		minimum: LARGE_TEXT,
	},

	/* The mark is a logotype, which WCAG 1.4.3 exempts outright. It is gated
	 * anyway, at the graphic threshold, because the whole argument of
	 * brand/README.md is that the drawing survives the background it lands on. */
	{
		label: "the mark's quiet tone",
		fg: "--ms-mark-quiet",
		bg: "--ms-page",
		minimum: NON_TEXT,
	},
	{
		label: "the mark's two tones apart",
		fg: "--ms-accent",
		bg: "--ms-mark-quiet",
		minimum: 2,
	},

	{
		label: "a border against the page",
		fg: "--ms-border",
		bg: "--ms-page",
		minimum: null,
	},
	{
		label: "a strong border",
		fg: "--ms-border-strong",
		bg: "--ms-page",
		minimum: null,
	},

	{
		label: "bad, as text",
		fg: "--ms-status-bad",
		bg: "--ms-page",
		minimum: NORMAL_TEXT,
	},
	{
		label: "bad on its tint",
		fg: "--ms-status-bad",
		bg: "--ms-status-bad-soft",
		minimum: NON_TEXT,
	},
	{
		label: "good, as text",
		fg: "--ms-status-good",
		bg: "--ms-page",
		minimum: NORMAL_TEXT,
	},
	{
		label: "good on its tint",
		fg: "--ms-status-good",
		bg: "--ms-status-good-soft",
		minimum: NON_TEXT,
	},
	{
		label: "warn, as text",
		fg: "--ms-status-warn",
		bg: "--ms-page",
		minimum: NORMAL_TEXT,
	},
	{
		label: "warn on its tint",
		fg: "--ms-status-warn",
		bg: "--ms-status-warn-soft",
		minimum: NON_TEXT,
	},
	{
		label: "info, as text",
		fg: "--ms-info",
		bg: "--ms-page",
		minimum: NORMAL_TEXT,
	},
	{
		label: "info on its tint",
		fg: "--ms-info",
		bg: "--ms-info-soft",
		minimum: NON_TEXT,
	},
	{
		label: "a note, as text",
		fg: "--ms-note",
		bg: "--ms-page",
		minimum: NORMAL_TEXT,
	},
	{
		label: "a note on its tint",
		fg: "--ms-note",
		bg: "--ms-note-soft",
		minimum: NON_TEXT,
	},

	{
		label: "text over a highlight",
		fg: "--ms-body",
		bg: "--ms-mark-highlight",
		minimum: NORMAL_TEXT,
	},

	/* The content components in src/components/ (components.css, home.css). */
	{
		label: "prose on a note tint",
		fg: "--ms-body",
		bg: "--ms-note-soft",
		minimum: NORMAL_TEXT,
	},
	{
		label: "a note legend on its tint, as text",
		fg: "--ms-note",
		bg: "--ms-note-soft",
		minimum: NORMAL_TEXT,
	},
	/* NotClaimed's body carries links (cost/limits.mdx). Inline code inside it
	 * is not on the tint: Starlight gives `code` its own background, gated below. */
	{
		label: "links on a note tint",
		fg: "--ms-accent",
		bg: "--ms-note-soft",
		minimum: NORMAL_TEXT,
	},
	/* A rule that carries meaning (NotClaimed's dashed edge, RouterWrites'
	 * leading border) is a graphic, so 1.4.11. `--ms-border-strong` is not in
	 * this list because it fails it: 1.81:1 dark, 1.52:1 light. */
	{
		label: "a note rule against a surface",
		fg: "--ms-note",
		bg: "--ms-surface",
		minimum: NON_TEXT,
	},
	{
		label: "provenance text on a raised surface",
		fg: "--ms-muted",
		bg: "--ms-surface-raised",
		minimum: NORMAL_TEXT,
	},
	{
		label: "body on a raised surface",
		fg: "--ms-body",
		bg: "--ms-surface-raised",
		minimum: NORMAL_TEXT,
	},
	{
		label: "a readout number on a surface",
		fg: "--ms-heading",
		bg: "--ms-surface",
		minimum: NORMAL_TEXT,
	},
	{
		label: "a panel title on a raised surface",
		fg: "--ms-heading",
		bg: "--ms-surface-raised",
		minimum: NORMAL_TEXT,
	},
	{
		label: "links on a raised surface",
		fg: "--ms-accent",
		bg: "--ms-surface-raised",
		minimum: NORMAL_TEXT,
	},
	{
		label: "a meaningful rule (muted) against a surface",
		fg: "--ms-muted",
		bg: "--ms-surface",
		minimum: NON_TEXT,
	},

	/* Starlight's own tokens, because they are what the components read. */
	{
		label: "sl gray-2 (prose)",
		fg: "--sl-color-gray-2",
		bg: "--sl-color-black",
		minimum: NORMAL_TEXT,
	},
	{
		label: "sl gray-3 (secondary)",
		fg: "--sl-color-gray-3",
		bg: "--sl-color-black",
		minimum: NORMAL_TEXT,
	},
	/* `--sl-color-bg-inline-code` is gray-5 in the dark theme and gray-6 in the
	 * light one (Starlight props.css); it is not redeclared in theme.css, so
	 * both of its sources are gated, in both themes. */
	{
		label: "inline code (dark source: sl gray-5)",
		fg: "--ms-body",
		bg: "--sl-color-gray-5",
		minimum: NORMAL_TEXT,
	},
	{
		label: "inline code (light source: sl gray-6)",
		fg: "--ms-body",
		bg: "--sl-color-gray-6",
		minimum: NORMAL_TEXT,
	},
	{
		label: "sl gray-4 (dividers)",
		fg: "--sl-color-gray-4",
		bg: "--sl-color-black",
		minimum: null,
	},
	{
		label: "sl text-accent",
		fg: "--sl-color-text-accent",
		bg: "--sl-color-black",
		minimum: NORMAL_TEXT,
	},
];

/* ------------------------------------------------------------------- report */

function main() {
	const blocks = registeredSheets().flatMap((file) =>
		blocksOf(fs.readFileSync(file, "utf8")),
	);

	const declared = (selector) => {
		const merged = new Map();
		for (const b of blocks) {
			if (b.selector !== selector || b.atRule !== null) continue;
			for (const [k, v] of tokensIn(b.body)) merged.set(k, v);
		}
		return merged;
	};

	const darkDeclared = declared(":root");
	const lightDeclared = declared(':root[data-theme="light"]');
	if (darkDeclared.size === 0)
		throw new Error("no `:root` block in the registered sheets");
	if (lightDeclared.size === 0)
		throw new Error('no `:root[data-theme="light"]` block');

	/* A light page inherits every dark token it does not override. That is the
	 * cascade, and it is also the bug this gate exists to find. */
	const palettes = {
		dark: darkDeclared,
		light: new Map([...darkDeclared, ...lightDeclared]),
	};

	let failures = 0;
	for (const theme of ["dark", "light"]) {
		console.log(`\n  ${theme}`);
		for (const pair of PAIRS) {
			let ratio;
			try {
				ratio = contrast(
					resolve(`var(${pair.fg})`, palettes[theme]),
					resolve(`var(${pair.bg})`, palettes[theme]),
				);
			} catch (error) {
				console.log(`    FAIL  ${pair.label}: ${error.message}`);
				failures += 1;
				continue;
			}
			const shown = ratio.toFixed(2).padStart(6);
			if (pair.minimum === null) {
				console.log(`    ----  ${shown}:1  ${pair.label} (not gated)`);
			} else if (ratio + 1e-9 < pair.minimum) {
				console.log(
					`    FAIL  ${shown}:1  ${pair.label}, wants ${pair.minimum.toFixed(2)}:1`,
				);
				failures += 1;
			} else {
				console.log(`    ok    ${shown}:1  ${pair.label}`);
			}
		}
	}

	/* The second gate. A colour token is one whose value mentions a colour or
	 * points at something that does; a radius or a z-index is theme-agnostic by
	 * nature and is reported rather than demanded. */
	const isColour = (name, block) => {
		const v = block.get(name);
		if (v === undefined) return false;
		return /^#|^rgb|^color-mix|^var\(/.test(v);
	};
	const lopsided = [
		...new Set([...darkDeclared.keys(), ...lightDeclared.keys()]),
	]
		.filter((name) => {
			const inDark = isColour(name, darkDeclared);
			const inLight = isColour(name, lightDeclared);
			return inDark !== inLight;
		})
		.sort();

	console.log("\n  token symmetry");
	if (lopsided.length === 0) {
		console.log("    ok    every colour token is declared for both themes");
	} else {
		for (const name of lopsided) {
			const only = isColour(name, darkDeclared) ? "dark" : "light";
			const consequence =
				only === "dark"
					? `, so it keeps its dark value (${darkDeclared.get(name)}) on a light page`
					: ", so a dark page has no value for it at all";
			console.log(
				`    FAIL  ${name} is declared for ${only} only${consequence}`,
			);
			failures += 1;
		}
	}

	console.log("");
	if (failures > 0) {
		console.error(
			`✗ ${failures} contrast failures in the registered stylesheets`,
		);
		process.exit(1);
	}
	console.log(
		"✓ Every gated pair clears its WCAG 2.2 AA threshold, in both themes.",
	);
}

main();

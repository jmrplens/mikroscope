// The documentation's diagrams, generated rather than drawn.
//
// A diagram in a bilingual site is four files — two languages by two layouts,
// because a row of boxes that reads well on a laptop is unreadable at 390 px —
// and four files drawn by hand drift apart the first time a label changes. So
// the labels live in one object here, the geometry is computed, and
// `pnpm run figures:check` fails when the committed SVGs disagree with this
// script.
//
// The output is inline SVG, not an <img>: inline, it inherits the site's own
// colour tokens, so a figure follows the theme toggle instead of guessing from
// prefers-color-scheme. Every fill and stroke below is a var(--ms-…) with a
// literal fallback, which is what keeps the file legible on its own.
//
// Usage: node scripts/gen-figures.mjs           # write src/data/figures/
//        node scripts/gen-figures.mjs --check   # fail if they are stale
import { mkdirSync, readFileSync, readdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";

const SITE = path.dirname(path.dirname(fileURLToPath(import.meta.url)));
const OUT = path.join(SITE, "src/data/figures");

/** The one diagram, in both languages. Geometry is below; this is the words. */
const dataPath = {
	en: {
		title: "Where the data comes from and where it goes",
		desc: "The router runs the agent in a container that reads the shared kernel and serves it over a veth. The collector on your machine pulls that, merges the RouterOS API tier into it, derives, and writes to every sink you named.",
		router: "The router",
		kernel: "Shared kernel",
		kernelDetail: "/proc · /sys · /dev/kmsg · PMU",
		agent: "mikroscope-agent",
		agentDetail: "1–100 Hz · 300 s ring · HTTP",
		api: "RouterOS API",
		apiDetail: "1 Hz, what the kernel cannot see",
		host: "Your machine",
		collector: "mikroscope forward",
		collectorDetail: "pull · merge · derive · fan out",
		sinks: "Sinks",
		sinkList: [
			"InfluxDB 3",
			"Prometheus",
			"file · SQL",
			"Loki · OTLP",
			"Graphite · Elastic",
			"Telegraf · stdout",
		],
		veth: "veth /30",
		pull: "pull every 500 ms",
	},
	es: {
		title: "De dónde vienen los datos y a dónde van",
		desc: "El router ejecuta el agente en un contenedor que lee el kernel compartido y lo sirve por un veth. El colector de tu máquina tira de ahí, le une la capa de la API de RouterOS, deriva y escribe en cada destino que hayas nombrado.",
		router: "El router",
		kernel: "Kernel compartido",
		kernelDetail: "/proc · /sys · /dev/kmsg · PMU",
		agent: "mikroscope-agent",
		agentDetail: "1–100 Hz · anillo 300 s · HTTP",
		api: "API de RouterOS",
		apiDetail: "1 Hz, lo que el kernel no ve",
		host: "Tu máquina",
		collector: "mikroscope forward",
		collectorDetail: "tirar · unir · derivar · repartir",
		sinks: "Destinos",
		sinkList: [
			"InfluxDB 3",
			"Prometheus",
			"fichero · SQL",
			"Loki · OTLP",
			"Graphite · Elastic",
			"Telegraf · stdout",
		],
		veth: "veth /30",
		pull: "cada 500 ms",
	},
};

const C = {
	surface: "var(--ms-surface, #f5f7f8)",
	raised: "var(--ms-surface-raised, #ffffff)",
	border: "var(--ms-border, #d4dade)",
	strong: "var(--ms-border-strong, #aab4ba)",
	heading: "var(--ms-heading, #10171b)",
	body: "var(--ms-body, #2d383f)",
	muted: "var(--ms-muted, #5a666d)",
	accent: "var(--ms-mark-quiet, #c2740a)",
};
const FONT = "ui-sans-serif,system-ui,sans-serif";
const MONO = "ui-monospace,SFMono-Regular,Menlo,monospace";

const esc = (s) =>
	String(s)
		.replaceAll("&", "&amp;")
		.replaceAll("<", "&lt;")
		.replaceAll(">", "&gt;");

/** A titled box with an optional second line. */
function box({ x, y, w, h, title, detail, mono = false, dashed = false }) {
	const parts = [
		`<rect x="${x}" y="${y}" width="${w}" height="${h}" rx="8" fill="${C.raised}" stroke="${dashed ? C.strong : C.border}"${dashed ? ' stroke-dasharray="4 3"' : ""}/>`,
		`<text x="${x + w / 2}" y="${y + (detail ? h / 2 - 4 : h / 2 + 5)}" text-anchor="middle" font-family="${mono ? MONO : FONT}" font-size="14" font-weight="600" fill="${C.heading}">${esc(title)}</text>`,
	];
	if (detail) {
		parts.push(
			`<text x="${x + w / 2}" y="${y + h / 2 + 15}" text-anchor="middle" font-family="${FONT}" font-size="11.5" fill="${C.muted}">${esc(detail)}</text>`,
		);
	}
	return parts.join("\n\t");
}

/** A straight arrow with a label above it. */
function arrow({ x1, y1, x2, y2, label, marker }) {
	const mid = { x: (x1 + x2) / 2, y: (y1 + y2) / 2 };
	const horizontal = y1 === y2;
	return [
		`<line x1="${x1}" y1="${y1}" x2="${x2}" y2="${y2}" stroke="${C.strong}" stroke-width="1.5" marker-end="url(#${marker})"/>`,
		label
			? `<text x="${horizontal ? mid.x : mid.x + 8}" y="${horizontal ? mid.y - 7 : mid.y}" text-anchor="${horizontal ? "middle" : "start"}" font-family="${FONT}" font-size="11" fill="${C.muted}">${esc(label)}</text>`
			: "",
	]
		.filter(Boolean)
		.join("\n\t");
}

/** A group label over a dashed container. */
function group({ x, y, w, h, label }) {
	return [
		`<rect x="${x}" y="${y}" width="${w}" height="${h}" rx="12" fill="${C.surface}" stroke="${C.border}" stroke-dasharray="6 4"/>`,
		`<text x="${x + 14}" y="${y + 20}" font-family="${FONT}" font-size="12" font-weight="600" fill="${C.body}" letter-spacing="0.03em">${esc(label).toUpperCase()}</text>`,
	].join("\n\t");
}

// Both layouts of a figure are in the page at once — CSS picks one — so every
// id has to be unique across them, or the document has two `#t` and the arrow
// marker of one resolves to the other's.
function svg({ id, width, height, title, desc, body }) {
	return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${width} ${height}" width="100%" role="img" aria-labelledby="${id}-t ${id}-d">
	<title id="${id}-t">${esc(title)}</title>
	<desc id="${id}-d">${esc(desc)}</desc>
	<defs>
		<marker id="${id}-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse">
			<path d="M 0 0 L 10 5 L 0 10 z" fill="${C.strong}"/>
		</marker>
	</defs>
	${body}
</svg>
`;
}

/** Laptop layout: the router on the left, the collector and sinks to the right. */
function wide(t, id) {
	const parts = [];
	const marker = `${id}-arrow`;
	parts.push(group({ x: 8, y: 8, w: 300, h: 250, label: t.router }));
	parts.push(
		box({
			x: 28,
			y: 40,
			w: 260,
			h: 56,
			title: t.kernel,
			detail: t.kernelDetail,
		}),
	);
	parts.push(arrow({ x1: 158, y1: 96, x2: 158, y2: 124, marker }));
	parts.push(
		box({
			x: 28,
			y: 126,
			w: 260,
			h: 56,
			title: t.agent,
			detail: t.agentDetail,
			mono: true,
		}),
	);
	parts.push(
		box({
			x: 28,
			y: 192,
			w: 260,
			h: 50,
			title: t.api,
			detail: t.apiDetail,
			dashed: true,
		}),
	);

	parts.push(group({ x: 340, y: 8, w: 280, h: 250, label: t.host }));
	parts.push(
		box({
			x: 360,
			y: 96,
			w: 240,
			h: 66,
			title: t.collector,
			detail: t.collectorDetail,
			mono: true,
		}),
	);
	parts.push(
		arrow({ x1: 288, y1: 150, x2: 356, y2: 124, label: t.veth, marker }),
	);
	parts.push(arrow({ x1: 288, y1: 217, x2: 356, y2: 148, marker }));

	parts.push(group({ x: 652, y: 8, w: 230, h: 250, label: t.sinks }));
	t.sinkList.forEach((name, i) => {
		const y = 40 + i * 34;
		parts.push(
			`<rect x="672" y="${y}" width="190" height="26" rx="6" fill="${C.raised}" stroke="${C.border}"/>`,
			`<text x="767" y="${y + 17}" text-anchor="middle" font-family="${FONT}" font-size="12" fill="${C.body}">${esc(name)}</text>`,
		);
	});
	parts.push(
		arrow({ x1: 600, y1: 129, x2: 670, y2: 129, label: t.pull, marker }),
	);
	return svg({
		id,
		width: 890,
		height: 266,
		title: t.title,
		desc: t.desc,
		body: parts.join("\n\t"),
	});
}

/** Phone layout: the same path, stacked. */
function narrow(t, id) {
	const parts = [];
	const marker = `${id}-arrow`;
	const w = 300;
	parts.push(group({ x: 8, y: 8, w, h: 204, label: t.router }));
	parts.push(
		box({
			x: 24,
			y: 36,
			w: w - 32,
			h: 50,
			title: t.kernel,
			detail: t.kernelDetail,
		}),
	);
	parts.push(arrow({ x1: 158, y1: 86, x2: 158, y2: 106, marker }));
	parts.push(
		box({
			x: 24,
			y: 108,
			w: w - 32,
			h: 50,
			title: t.agent,
			detail: t.agentDetail,
			mono: true,
		}),
	);
	parts.push(
		box({ x: 24, y: 164, w: w - 32, h: 32, title: t.api, dashed: true }),
	);
	parts.push(
		arrow({ x1: 158, y1: 204, x2: 158, y2: 232, label: t.veth, marker }),
	);

	parts.push(
		box({
			x: 24,
			y: 240,
			w: w - 32,
			h: 52,
			title: t.collector,
			detail: t.collectorDetail,
			mono: true,
		}),
	);
	parts.push(arrow({ x1: 158, y1: 292, x2: 158, y2: 316, marker }));

	parts.push(group({ x: 8, y: 318, w, h: 128, label: t.sinks }));
	t.sinkList.forEach((name, i) => {
		const col = i % 2;
		const row = Math.floor(i / 2);
		const x = 22 + col * 140;
		const y = 346 + row * 30;
		parts.push(
			`<rect x="${x}" y="${y}" width="132" height="24" rx="6" fill="${C.raised}" stroke="${C.border}"/>`,
			`<text x="${x + 66}" y="${y + 16}" text-anchor="middle" font-family="${FONT}" font-size="11" fill="${C.body}">${esc(name)}</text>`,
		);
	});
	return svg({
		id,
		width: 316,
		height: 454,
		title: t.title,
		desc: t.desc,
		body: parts.join("\n\t"),
	});
}

const figures = {};
// The title and description of each figure, as a module rather than only
// inside the SVGs: page-markdown.mjs reduces <Figure> to that sentence for
// docs/, and it is loaded both by node from site/ and by Vite from a build
// directory, so it has to be an import rather than a path on disk.
const meta = {};
for (const [lang, words] of Object.entries(dataPath)) {
	figures[`data-path.${lang}.wide.svg`] = wide(words, `data-path-${lang}-wide`);
	figures[`data-path.${lang}.narrow.svg`] = narrow(
		words,
		`data-path-${lang}-narrow`,
	);
	meta["data-path"] ??= {};
	meta["data-path"][lang] = { title: words.title, desc: words.desc };
}
figures["figures.json"] = `${JSON.stringify(meta, null, 2)}\n`;

mkdirSync(OUT, { recursive: true });
if (process.argv.includes("--check")) {
	const onDisk = new Set(
		readdirSync(OUT).filter((f) => f.endsWith(".svg") || f.endsWith(".json")),
	);
	const problems = [];
	for (const [name, body] of Object.entries(figures)) {
		onDisk.delete(name);
		let current = null;
		try {
			current = readFileSync(path.join(OUT, name), "utf8");
		} catch {
			problems.push(`${name} is missing`);
			continue;
		}
		if (current !== body) problems.push(`${name} is stale`);
	}
	for (const extra of onDisk)
		problems.push(`${extra} is not generated by this script`);
	if (problems.length > 0) {
		console.error(
			`[figures] ${problems.length} problem(s). Run \`pnpm run figures\`:`,
		);
		for (const p of problems) console.error(`  ${p}`);
		process.exit(1);
	}
	console.log(
		`[figures] ${Object.keys(figures).length} files, all up to date.`,
	);
} else {
	for (const [name, body] of Object.entries(figures)) {
		writeFileSync(path.join(OUT, name), body);
	}
	console.log(
		`[figures] wrote ${Object.keys(figures).length} files into src/data/figures.`,
	);
}

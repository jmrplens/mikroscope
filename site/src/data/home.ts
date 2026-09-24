/**
 * The landing page's copy, one typed object per locale.
 *
 * Both objects satisfy one interface, so the two landings cannot drift in
 * shape: a section added to one and not the other is a type error, not a
 * Spanish page that silently renders less. `<Home>` also refuses content
 * whose `lang` is not the page's, which the parity gate cannot see.
 *
 * No measured figure is typed here. Numbers, rates, windows, devices,
 * versions and dates come from src/data/measurements.ts through `q()`, the
 * run and campaign records, so a repeated run updates the landing in both
 * languages. Counts the Go source decides (the sinks) come from
 * src/data/stats.json, and the release from src/data/release.ts, through
 * `<Home>`. The exceptions are design facts, not readings, each named once
 * below (`API_HZ`), and the requirements a reader needs to install
 * (`REQUIRES`), which no run can change. Strings in `hides`, `tiers`,
 * `install.prereq`, `notClaimed.paragraphs`, `readout.claim` and
 * `hides.linkText` may carry `<code>` and nothing else; `<Home>` renders them
 * with `set:html`, which is safe because they are in the repository, not user
 * input.
 */
import {
	burst,
	campaigns,
	RB5009,
	RB5009_NOW,
	describeCpu,
	measurements,
	readBound,
	runs,
	type Campaign,
	type CampaignId,
	type Lang,
	type MeasurementId,
	type RunKey,
} from "./measurements";
import stats from "./stats.json" with { type: "json" };
import {
	formatNumber,
	formatQuantity,
	numberWord,
	spellCount,
} from "../lib/format";

export interface HomeContent {
	lang: Lang;
	readout: {
		title: string;
		items: {
			id: MeasurementId | "run.gapsDrops";
			label: string;
			href: string;
		}[];
		claim: string;
	};
	hides: {
		title: string;
		paragraphs: string[];
		/** The measured answer to what `cpu-load` averages, one link below the arithmetic. */
		linkText: string;
		href: string;
	};
	/** The line that names the release this build documents; the values come from src/data/release.ts. */
	release: { label: string; changelog: string };
	cost: {
		title: string;
		lead: string;
		runs: RunKey[];
		after: string;
		linkText: string;
		href: string;
	};
	tiers: {
		title: string;
		kernel: { title: string; body: string };
		api: { title: string; body: string };
	};
	install: {
		title: string;
		prereq: string;
		steps: { cmd: string; note: string }[];
	};
	/**
	 * Short statements, one subject each: the device, the load, what cost
	 * scales with, the sinks, the builds. They were one 95-word sentence,
	 * which a reader, or a model quoting one of them, could not lift out whole
	 * (GEO audit, 2026-09-24).
	 */
	notClaimed: { title: string; paragraphs: string[] };
	next: {
		title: string;
		links: { text: string; href: string; note: string }[];
	};
}

const q = (id: MeasurementId, lang: Lang): string =>
	formatQuantity(measurements[id], lang);

/** Gaps and drops summed over every run, so a lossy run cannot hide in one row. */
const totalGaps = runs.reduce((n, r) => n + r.gaps, 0);
const totalDrops = runs.reduce((n, r) => n + r.drops, 0);

/** "0 / 0", in the locale's digits. */
export function gapsDrops(lang: Lang): string {
	return `${formatNumber(totalGaps, lang, 0)} / ${formatNumber(totalDrops, lang, 0)}`;
}

/**
 * The campaign the landing's cost figures come from, named once for every
 * renderer of the landing: `<Home>` puts its `<Provenance>` under the cost
 * table, and src/lib/page-markdown.mjs writes the same sentence into the
 * markdown twin, and so into the head of llms-full.txt. Each used to name it
 * separately, and the twin kept saying rates-2026-09-15 (60 s windows, three
 * sinks) under the 2026-09-18 table (300 s windows, InfluxDB 3 alone) that the
 * HTML described correctly (GEO audit, 2026-09-24). scripts/check-twins.mjs
 * now holds every twin to the provenance its page shows.
 */
export const LANDING_CAMPAIGN = "rates-2026-09-18" satisfies CampaignId;

const rates = campaigns[LANDING_CAMPAIGN];
const kernel = campaigns["kernel-2026-09-11"];
const netns = campaigns["netns-2026-09-12"];

function run(key: RunKey) {
	const r = runs.find((x) => x.key === key);
	if (r === undefined)
		throw new Error(`home.ts: no run "${key}" in src/data/measurements.ts`);
	return r;
}

/*
 * The claims "above the budget" and "nothing was lost" are guarded where the
 * data lives, in src/data/measurements.ts, because doc pages make them too.
 * This one is the landing's own.
 */
if (measurements["burst.100msOn4Cores"].kind !== "derived") {
	throw new Error(
		"home.ts: hides calls burst.100msOn4Cores arithmetic; it is marked otherwise",
	);
}
const run100 = run("100hz");
/**
 * The sample window `tick.stepOneCore` is true for: a step of S % means the
 * window holds 100 / S ticks, and at T ms a tick it is T × 100 / S ms long. Printed beside the step
 * instead of the install default's period, which would make "a 20 ms sample
 * resolves one core to 10 % steps" the day the default moved to 50 Hz.
 */
const stepWindow = (lang: Lang) =>
	formatQuantity(
		{
			value:
				(measurements["tick.ms"].value * 100) /
				measurements["tick.stepOneCore"].value,
			unit: "ms",
			digits: 0,
		},
		lang,
	);

const period = (hz: number, lang: Lang) =>
	formatQuantity({ value: 1000 / hz, unit: "ms", digits: 0 }, lang);
const hz = (value: number, lang: Lang) =>
	formatQuantity({ value, unit: "Hz", digits: 0 }, lang);
const minRate = Math.min(...runs.map((r) => r.rateHz));
const maxRate = Math.max(...runs.map((r) => r.rateHz));
const run10 = run("10hz");
const installDefault = runs.find((r) => r.installDefault);
if (installDefault === undefined)
	throw new Error("home.ts: no run is the install default");
if (rates.windowS === undefined)
	throw new Error(`home.ts: ${LANDING_CAMPAIGN} records no window`);
const windowS = rates.windowS;
const win = (lang: Lang) =>
	formatQuantity({ value: windowS, unit: "s", digits: 0 }, lang);
const cores = rates.cpu.cores;
/** How often the RouterOS API reports `cpu-load`, and so the collector polls it: a design fact. */
const API_HZ = 1;
if (burst.windowMs !== 1000 / API_HZ) {
	throw new Error(
		"home.ts: hides describes the burst window as one API report; burst.windowMs no longer is",
	);
}
const burstOn = (lang: Lang) =>
	formatQuantity({ value: burst.onMs, unit: "ms", digits: 0 }, lang);
const burstOff = (lang: Lang) =>
	formatNumber(burst.windowMs - burst.onMs, lang, 0);
const kernelRange = (lang: Lang) =>
	`${formatNumber(minRate, lang, 0)}${lang === "es" ? " a " : " to "}${hz(maxRate, lang)}`;

/** The commands are the same in both languages; only their notes translate. */
const CMD = {
	doctor: "mikroscope doctor",
	plan: "mikroscope plan",
	install: "mikroscope install",
	status: "mikroscope status",
	uninstall: "mikroscope uninstall",
} as const;

const RUNS_ON_LANDING: RunKey[] = ["10hz", "50hz", "100hz"];

/**
 * What the router needs before `install` can run: design facts, not readings,
 * so no campaign can change them. RouterOS 7.24, because the container step
 * writes `privileged=`, an attribute earlier 7.x releases reject. Named once
 * here for the install step below and for the facts that head both llms.txt
 * indexes (src/lib/llms.mjs).
 */
export const REQUIRES = {
	routeros: "7.24",
	arches: ["arm64", "arm", "x86_64"],
	notArches: ["MIPS", "TILE"],
} as const;

/** The RB5009's architecture: the one build of the three that has run on hardware. */
const DEVICE_ARCH = "arm64" satisfies (typeof REQUIRES.arches)[number];
const crossBuilt = REQUIRES.arches.filter((a) => a !== DEVICE_ARCH);

/**
 * Where the measured answer to "how many seconds does `cpu-load` average
 * over?" lives: a heading on sinks/api-tier in each language, linked from the
 * arithmetic in `hides`. The fragments are those headings' ids as the build
 * writes them (github-slugger), so a renamed heading breaks the link, and
 * scripts/check-twins.mjs fails on any landing link whose page or fragment the
 * build does not contain.
 */
const CPU_LOAD_ANSWER: Record<Lang, string> = {
	en: "/mikroscope/sinks/api-tier/#how-many-seconds-does-routeros-cpu-load-average-over",
	es: "/mikroscope/es/sinks/api-tier/#cuántos-segundos-promedia-el-cpu-load-de-routeros",
};

/**
 * "a, b and c" / "a, b y c", with Spanish "e" for "y" before a word that
 * starts with the sound of i ("Prometheus e InfluxDB 3").
 */
function joinList(items: readonly string[], lang: Lang): string {
	if (items.length < 2) return items.join("");
	const last = items.at(-1) ?? "";
	const and =
		lang === "en" ? "and" : /^h?i(?![aeiouáéíóú])/i.test(last) ? "e" : "y";
	return `${items.slice(0, -1).join(", ")} ${and} ${last}`;
}

/**
 * The campaign's sinks, named rather than counted. spellCount against a fixed
 * plural rendered "the one sinks the collector forwarded to" on the landing
 * page the day the 2026-09-18 campaign came down to a single sink.
 */
const sinksPhrase = (lang: "en" | "es"): string => {
	const list = rates.sinks;
	if (list.length === 1) {
		return lang === "en"
			? `${list[0]}, the one sink`
			: `${list[0]}, el único destino`;
	}
	const joined = joinList(list, lang);
	return lang === "en"
		? `the ${spellCount(list.length, "en")} sinks (${joined})`
		: `los ${spellCount(list.length, "es")} destinos (${joined})`;
};

/*
 * Which sinks have had samples from the router: every sink a campaign on the
 * device forwarded to, oldest campaign first. It is three (file, Prometheus
 * and InfluxDB 3, about/status) out of the eleven the collector has
 * (src/data/stats.json, from the Go source), and the callout below says so
 * by count and by name; a campaign that adds a sink changes both.
 */
const SINK_NAME: Record<Lang, Record<string, string>> = {
	en: {},
	es: { file: "fichero" },
};
const sinksFromRouter = [
	...new Set(
		(Object.values(campaigns) as Campaign[])
			.filter((c) => c.sinks !== undefined && c.date !== null)
			.sort((a, b) => String(a.date).localeCompare(String(b.date)))
			.flatMap((c) => c.sinks ?? []),
	),
];
const sinksTotal = stats.sinks;
/*
 * The sink count in words, as the rest of the site writes it (<Stat
 * name="sinks" as="word" />), from the count the Go source gives and the same
 * table of words; src/lib/llms.mjs uses it too, in the facts both llms.txt
 * indexes open with.
 */
const sinksWord = (lang: Lang): string => {
	const word = numberWord(sinksTotal, lang);
	if (word === undefined) {
		throw new Error(
			`home.ts: src/data/stats.json counts ${sinksTotal} sinks, past the words numberWord knows; the landing and llms.txt write the count as a word`,
		);
	}
	return word;
};
export const SINKS_WORD: Record<Lang, string> = {
	en: sinksWord("en"),
	es: sinksWord("es"),
};
if (sinksFromRouter.length === 0 || sinksFromRouter.length >= sinksTotal) {
	throw new Error(
		`home.ts: notClaimed says ${sinksFromRouter.length} of ${sinksTotal} sinks have run from the router and the rest have not; rewrite it`,
	);
}
const routerSinks = (lang: Lang) =>
	joinList(
		sinksFromRouter.map((s) => SINK_NAME[lang][s] ?? s),
		lang,
	);

/*
 * "One window, not repeated, so no spread": the readout and the cost lead
 * say it of every figure they show, because each is one run of the campaign
 * and a single window has no spread to give (GEO audit, 2026-09-24). A run
 * holds one window when its samples are one window's worth at its rate; a
 * measurement that gained a range (`max`) would carry its spread. Either
 * change makes the sentence false, so either fails the build here.
 */
const windowsIn = (key: RunKey) => {
	const r = run(key);
	return Math.round(r.samples / (r.rateHz * windowS));
};
for (const key of new Set<RunKey>([
	...RUNS_ON_LANDING,
	run10.key,
	run100.key,
])) {
	if (windowsIn(key) !== 1) {
		throw new Error(
			`home.ts: run ${key} now spans ${windowsIn(key)} windows; rewrite readout.claim and cost.lead, which call each figure one window`,
		);
	}
	for (const metric of ["cpu", "rss"] as const) {
		const m = measurements[`run.${key}.${metric}` as const];
		if (m.max !== undefined) {
			throw new Error(
				`home.ts: run.${key}.${metric} now carries a range; rewrite readout.claim and cost.lead, which say no figure has a spread`,
			);
		}
		if (m.campaign !== LANDING_CAMPAIGN) {
			throw new Error(
				`home.ts: run.${key}.${metric} is from ${m.campaign}, not ${LANDING_CAMPAIGN}, the campaign the landing's provenance names`,
			);
		}
	}
}

export const en: HomeContent = {
	lang: "en",
	readout: {
		title: "Measured, not budgeted",
		items: [
			{
				id: "run.10hz.cpu",
				label: `of one core at ${hz(run10.rateHz, "en")}, the install default`,
				href: "/mikroscope/cost/",
			},
			{
				id: "run.10hz.rss",
				label: `resident memory at ${hz(run10.rateHz, "en")}`,
				href: "/mikroscope/cost/",
			},
			{
				id: "run.100hz.cpu",
				label: `of one core at ${hz(run100.rateHz, "en")}, the CLI's cap`,
				href: "/mikroscope/cost/rate-ceiling/",
			},
			{
				id: "run.gapsDrops",
				label: `gaps and drops, across ${spellCount(runs.length, "en")} runs`,
				href: "/mikroscope/cost/rate-ceiling/",
			},
		],
		claim: `The first three are from the agent's own cgroup, carried on every sample; the fourth is from ${sinksPhrase("en")} the collector forwarded to. All four were measured on an RB5009 (${describeCpu(rates, "en")}, RouterOS ${rates.routeros}) on ${rates.date}. Each of the first three is one ${win("en")} window at steady state, not repeated, so none has a spread. At ${hz(run10.rateHz, "en")} the memory is inside the ≤ ${q("budget.rss", "en")} the budget asks for and the CPU is above the ≤ ${q("budget.cpu", "en")}.`,
	},
	hides: {
		title: "A one-second average is a report about a second",
		paragraphs: [
			`The RouterOS API reports <code>cpu-load</code> once a second. A core saturated for ${burstOn("en")} and idle for the other ${burstOff("en")} moves a ${spellCount(cores, "en")}-core, one-second average by ${q("burst.100msOn4Cores", "en")}. That is arithmetic, not a measurement, and the figure is true: it just cannot say when.`,
			`The agent reads <code>/proc/stat</code>, <code>/proc/interrupts</code>, <code>/proc/softirqs</code> and <code>/proc/net/softnet_stat</code> from inside the router, at ${hz(installDefault.rateHz, "en")} by default, and ships raw tick deltas with the interval each one covers. It never computes a percentage; the window is yours.`,
			`The floor is the kernel's, not the tool's. <code>/proc/stat</code> counts in ticks of ${q("tick.ms", "en")}, so a ${stepWindow("en")} sample resolves one core to ${q("tick.stepOneCore", "en")} steps. On the RB5009 (RouterOS ${kernel.routeros}, Linux ${kernel.kernel}) there is no PSI and no schedstat to go finer: both files are absent, checked ${kernel.date}.`,
		],
		linkText:
			"How many seconds <code>cpu-load</code> averages over, fitted against <code>/proc/stat</code> →",
		href: CPU_LOAD_ANSWER.en,
	},
	release: { label: "Current release:", changelog: "Changelog" },
	cost: {
		title: `What it costs, at ${spellCount(RUNS_ON_LANDING.length, "en")} rates`,
		lead: `Each row is one ${win("en")} window with the ring already full, not repeated, so no row has a spread. Memory differs by row because the ring and the memory limit do.`,
		runs: RUNS_ON_LANDING,
		after: `Nothing was lost at any of these rates: every sink reported ${formatNumber(totalGaps, "en", 0)} gaps and ${formatNumber(totalDrops, "en", 0)} drops, and the delivered rate matched the configured one to three figures. At the default floors and ${hz(run100.rateHz, "en")}, a whole tick's sources were read in under ${formatQuantity(readBound, "en")} for ${q("read.under2ms", "en")} of samples, inside a ${period(run100.rateHz, "en")} period.`,
		linkText: `All ${spellCount(runs.length, "en")} runs, including every source on every tick →`,
		href: "/mikroscope/cost/rate-ceiling/",
	},
	tiers: {
		title: "The router's CPU from the kernel, its interfaces from the API",
		kernel: {
			title: `Kernel tier · the agent · ${kernelRange("en")}`,
			body: "Global inside the container, so these are the router's own: per-core CPU ticks, interrupts, softirqs, softnet drops and time squeezes, <code>/proc/meminfo</code>, <code>/proc/vmstat</code>, load and disk I/O. A privileged container adds the kernel log as timestamped events and the global slab caches.",
		},
		api: {
			title: `API tier · the collector · ${hz(API_HZ, "en")}`,
			body: `The container has its own network namespace, so <code>/proc/net/dev</code> describes the container, not the router. Interface bytes and packets come from the RouterOS API and are merged by the collector, not interpolated. <code>privileged=yes</code> does not change that (checked ${netns.date}).`,
		},
	},
	install: {
		title: "Every write listed before it is made",
		prereq: `Download the archive for your platform from the release, or build the CLI from a checkout with <code>make build</code>. The router needs RouterOS ${REQUIRES.routeros} or later — the container step writes <code>privileged=</code>, an attribute earlier 7.x releases reject — with the <code>container</code> package and <code>device-mode container=yes</code>, which MikroTik gates behind a reset-button press or a power cycle. ${joinList(REQUIRES.arches, "en")}; not ${REQUIRES.notArches.join(", not ")}.`,
		steps: [
			{
				cmd: CMD.doctor,
				note: "Read-only preflight that names the fix for anything missing, then what the running agent's ring shows: a layer-2 loop, STP churn, a link flap or softnet drops.",
			},
			{ cmd: CMD.plan, note: "Every RouterOS command, nothing written." },
			{
				cmd: CMD.install,
				note: "Doctor, confirmation, the writes, then a probe of the agent. The image comes from your own Go toolchain, from the published agent tar, or from the registry the router pulls it from.",
			},
			{ cmd: CMD.status, note: "Ownership counts and the agent's health." },
			{ cmd: CMD.uninstall, note: "Removes and verifies." },
		],
	},
	notClaimed: {
		title: "What is not claimed",
		paragraphs: [
			`The project has run on one device: an ${RB5009.device}, ${DEVICE_ARCH}, on RouterOS ${RB5009.routeros} and later ${RB5009_NOW.routeros}. No rate is claimed for any other board.`,
			`No traffic load heavier than this router's ordinary traffic has been measured. That traffic is about ${q("load.ordinary", "en")} on the WAN.`,
			"Cost scales with core speed, source set and ring size. Measure it on your own device before you budget for it.",
			`${spellCount(sinksFromRouter.length, "en", true)} of the ${SINKS_WORD.en} sinks have run from the router: ${routerSinks("en")}. The other ${spellCount(sinksTotal - sinksFromRouter.length, "en")} have not yet had router samples through them. The test suite writes into each one's real product in containers and reads it back, off the device. The SQL script is loaded into PostgreSQL, and stdout is read locally.`,
			`The ${joinList(crossBuilt, "en")} builds are cross-built and checked in CI. They have never run on hardware.`,
		],
	},
	next: {
		title: "Where to go next",
		links: [
			{
				text: "What it is",
				href: "/mikroscope/start/",
				note: "Two programs, one container, four limits stated first",
			},
			{
				text: "How it compares",
				href: "/mikroscope/start/compared/",
				note: "The other ways to watch a RouterOS device, what each one reads, and when not to use this one",
			},
			{
				text: "Five minutes with a router",
				href: "/mikroscope/start/walkthrough/",
				note: "Install, record while you change something, draw the chart, from a real RB5009 recording",
			},
			{
				text: "Installing the agent",
				href: "/mikroscope/install/",
				note: "Four ways to get the agent onto the router, what install writes and in which order, and how uninstall removes only what it created",
			},
			{
				text: "The collector",
				href: "/mikroscope/sinks/",
				note: `Pull, merge, derive, fan out to ${SINKS_WORD.en} sinks, and why a slow one never stops the loop`,
			},
			{
				text: "How to read what it shows",
				href: "/mikroscope/playbooks/",
				note: "A production fault the API could not see, provoked faults, and the idle shape they are read against",
			},
			{
				text: "What the numbers do not say",
				href: "/mikroscope/cost/limits/",
				note: "Every limit on the figures above",
			},
		],
	},
};

export const es: HomeContent = {
	lang: "es",
	readout: {
		title: "Medido, no presupuestado",
		items: [
			{
				id: "run.10hz.cpu",
				label: `de un núcleo a ${hz(run10.rateHz, "es")}, la cadencia por defecto`,
				href: "/mikroscope/es/cost/",
			},
			{
				id: "run.10hz.rss",
				label: `de memoria residente a ${hz(run10.rateHz, "es")}`,
				href: "/mikroscope/es/cost/",
			},
			{
				id: "run.100hz.cpu",
				label: `de un núcleo a ${hz(run100.rateHz, "es")}, el máximo de la CLI`,
				href: "/mikroscope/es/cost/rate-ceiling/",
			},
			{
				id: "run.gapsDrops",
				label: `huecos y descartes, en ${spellCount(runs.length, "es")} ejecuciones`,
				href: "/mikroscope/es/cost/rate-ceiling/",
			},
		],
		claim: `Las tres primeras salen del propio cgroup del agente, en cada muestra; la cuarta, de ${sinksPhrase("es")} al que reenviaba el colector. Las cuatro se midieron en un RB5009 (${describeCpu(rates, "es")}, RouterOS ${rates.routeros}) el ${rates.date}. Cada una de las tres primeras es una sola ventana de ${win("es")} en régimen estacionario, sin repetir, así que ninguna tiene dispersión. A ${hz(run10.rateHz, "es")} la memoria está dentro de los ≤ ${q("budget.rss", "es")} que pide el presupuesto y la CPU por encima del ≤ ${q("budget.cpu", "es")}.`,
	},
	hides: {
		title: "Una media de un segundo es un informe sobre un segundo",
		paragraphs: [
			`La API de RouterOS publica <code>cpu-load</code> una vez por segundo. Un núcleo saturado ${burstOn("es")} y ocioso los otros ${burstOff("es")} mueve la media de un segundo de ${spellCount(cores, "es")} núcleos en ${q("burst.100msOn4Cores", "es")}. Es aritmética, no una medida, y la cifra es cierta: solo que no puede decir cuándo.`,
			`El agente lee <code>/proc/stat</code>, <code>/proc/interrupts</code>, <code>/proc/softirqs</code> y <code>/proc/net/softnet_stat</code> desde dentro del router, a ${hz(installDefault.rateHz, "es")} por defecto, y envía los deltas crudos de ticks con el intervalo que cubre cada uno. Nunca calcula un porcentaje; la ventana la eliges tú.`,
			`El suelo es del kernel, no de la herramienta. <code>/proc/stat</code> cuenta en ticks de ${q("tick.ms", "es")}, así que una muestra de ${stepWindow("es")} resuelve un núcleo en escalones de ${q("tick.stepOneCore", "es")}. En el RB5009 (RouterOS ${kernel.routeros}, Linux ${kernel.kernel}) no hay PSI ni schedstat con los que afinar: ambos ficheros faltan, comprobado el ${kernel.date}.`,
		],
		linkText:
			"Cuántos segundos promedia <code>cpu-load</code>, ajustado contra <code>/proc/stat</code> →",
		href: CPU_LOAD_ANSWER.es,
	},
	release: { label: "Versión actual:", changelog: "Registro de cambios" },
	cost: {
		title: `Lo que cuesta, a ${spellCount(RUNS_ON_LANDING.length, "es")} cadencias`,
		lead: `Cada fila es una ventana de ${win("es")} con el anillo ya lleno, sin repetir, así que ninguna fila tiene dispersión. La memoria cambia por fila porque cambian el anillo y el límite de memoria.`,
		runs: RUNS_ON_LANDING,
		after: `No se perdió nada a ninguna de estas cadencias: todos los destinos informaron de ${formatNumber(totalGaps, "es", 0)} huecos y ${formatNumber(totalDrops, "es", 0)} descartes, y la cadencia entregada coincidió con la configurada a tres cifras. Con los suelos por defecto y a ${hz(run100.rateHz, "es")}, todas las fuentes de un tick se leyeron en menos de ${formatQuantity(readBound, "es")} en el ${q("read.under2ms", "es")} de las muestras, dentro de un periodo de ${period(run100.rateHz, "es")}.`,
		linkText: `Las ${spellCount(runs.length, "es")} ejecuciones, incluida la de todas las fuentes en cada tick →`,
		href: "/mikroscope/es/cost/rate-ceiling/",
	},
	tiers: {
		title: "La CPU del router desde el kernel, sus interfaces desde la API",
		kernel: {
			title: `Capa del kernel · el agente · de ${kernelRange("es")}`,
			body: "Globales dentro del contenedor, así que son los del propio router: ticks de CPU por núcleo, interrupciones, softirqs, descartes y time squeezes de softnet, <code>/proc/meminfo</code>, <code>/proc/vmstat</code>, carga y E/S de disco. Un contenedor privilegiado añade el log del kernel como eventos con marca de tiempo y las cachés slab globales.",
		},
		api: {
			title: `Capa de la API · el colector · ${hz(API_HZ, "es")}`,
			body: `El contenedor tiene su propio espacio de nombres de red, así que <code>/proc/net/dev</code> describe al contenedor, no al router. Los bytes y paquetes por interfaz vienen de la API de RouterOS y los fusiona el colector, sin interpolar. <code>privileged=yes</code> no cambia eso (comprobado el ${netns.date}).`,
		},
	},
	install: {
		title: "Cada escritura, listada antes de hacerla",
		prereq: `Descarga el archivo para tu plataforma de la versión publicada, o compila la CLI desde una copia del repositorio con <code>make build</code>. El router necesita RouterOS ${REQUIRES.routeros} o posterior —el paso del contenedor escribe <code>privileged=</code>, un atributo que las versiones 7.x anteriores rechazan— con el paquete <code>container</code> y <code>device-mode container=yes</code>, que MikroTik condiciona a pulsar el botón de reset o a un corte de alimentación. ${joinList(REQUIRES.arches, "es")}; ni ${REQUIRES.notArches.join(" ni ")}.`,
		steps: [
			{
				cmd: CMD.doctor,
				note: "Comprobación de solo lectura que nombra el arreglo de lo que falte, y luego lo que muestra el anillo del agente en marcha: un bucle de capa 2, churn de STP, un link flap o descartes de softnet.",
			},
			{ cmd: CMD.plan, note: "Cada orden de RouterOS, sin escribir nada." },
			{
				cmd: CMD.install,
				note: "Doctor, confirmación, las escrituras y luego una sonda al agente. La imagen sale de tu propia toolchain de Go, del tar del agente publicado o del registro del que el router se la descarga.",
			},
			{ cmd: CMD.status, note: "Recuento de propiedad y salud del agente." },
			{ cmd: CMD.uninstall, note: "Elimina y verifica." },
		],
	},
	notClaimed: {
		title: "Lo que no se afirma",
		paragraphs: [
			`El proyecto ha corrido en un solo equipo: un ${RB5009.device}, ${DEVICE_ARCH}, con RouterOS ${RB5009.routeros} y después ${RB5009_NOW.routeros}. No se afirma ninguna cadencia en otra placa.`,
			`No se ha medido ninguna carga de tráfico mayor que el tráfico corriente de este router. Ese tráfico ronda los ${q("load.ordinary", "es")} en la WAN.`,
			"El coste depende de la velocidad del núcleo, del conjunto de fuentes y del tamaño del anillo. Mídelo en tu propio equipo antes de presupuestarlo.",
			`${spellCount(sinksFromRouter.length, "es", true)} de los ${SINKS_WORD.es} destinos han corrido desde el router: ${routerSinks("es")}. Los otros ${spellCount(sinksTotal - sinksFromRouter.length, "es")} aún no han recibido muestras del router. La batería de pruebas escribe en el producto real de cada uno, en contenedores, y lo relee, fuera del equipo. El script SQL se carga en PostgreSQL y stdout se lee en local.`,
			`Las compilaciones para ${joinList(crossBuilt, "es")} son cruzadas y pasan por CI. No han corrido nunca en hardware.`,
		],
	},
	next: {
		title: "Por dónde seguir",
		links: [
			{
				text: "Qué es",
				href: "/mikroscope/es/start/",
				note: "Dos programas, un contenedor, cuatro límites dichos primero",
			},
			{
				text: "Cómo se compara",
				href: "/mikroscope/es/start/compared/",
				note: "Las otras formas de vigilar un equipo RouterOS, qué lee cada una y cuándo no usar esta",
			},
			{
				text: "Cinco minutos con un router",
				href: "/mikroscope/es/start/walkthrough/",
				note: "Instalar, grabar mientras cambias algo y dibujar el gráfico, con una grabación real de un RB5009",
			},
			{
				text: "Instalar el agente",
				href: "/mikroscope/es/install/",
				note: "Cuatro maneras de llevar el agente al router, qué escribe install y en qué orden, y cómo uninstall quita solo lo que creó",
			},
			{
				text: "El colector",
				href: "/mikroscope/es/sinks/",
				note: `Extraer, fusionar, derivar, repartir a ${SINKS_WORD.es} destinos, y por qué uno lento nunca para el bucle`,
			},
			{
				text: "Cómo leer lo que muestra",
				href: "/mikroscope/es/playbooks/",
				note: "Un fallo de producción que la API no veía, fallos provocados y la forma en reposo contra la que se leen",
			},
			{
				text: "Lo que los números no dicen",
				href: "/mikroscope/es/cost/limits/",
				note: "Cada límite de las cifras de arriba",
			},
		],
	},
};

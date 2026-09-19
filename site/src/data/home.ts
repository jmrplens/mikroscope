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
 * languages. The exceptions are design facts, not readings, each named once
 * below (`API_HZ`), and the requirements a reader needs to install
 * (RouterOS 7.24 or later, the architectures), which no run can change. Strings in `hides`,
 * `tiers`, `install.prereq`, `notClaimed.body` and `readout.claim` may carry
 * `<code>` and nothing else; `<Home>` renders them with `set:html`, which is
 * safe because they are in the repository, not user input.
 */
import {
	burst,
	campaigns,
	RB5009,
	describeCpu,
	measurements,
	readBound,
	runs,
	type Lang,
	type MeasurementId,
	type RunKey,
} from "./measurements";
import { formatNumber, formatQuantity, spellCount } from "../lib/format";

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
	hides: { title: string; paragraphs: string[] };
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
	notClaimed: { title: string; body: string };
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

const rates = campaigns["rates-2026-09-18"];
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
	throw new Error("home.ts: rates-2026-09-18 records no window");
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
				label: `gaps and drops, in every sink, across ${spellCount(runs.length, "en")} runs`,
				href: "/mikroscope/cost/rate-ceiling/",
			},
		],
		claim: `The first three from the agent's own cgroup and <code>/metrics</code>, the fourth from the ${spellCount(rates.sinks.length, "en")} sinks the collector forwarded to, on an RB5009 (${describeCpu(rates, "en")}, RouterOS ${rates.routeros}), ${win("en")} windows at steady state, ${rates.date}. At ${hz(run10.rateHz, "en")} the memory is inside the ≤ ${q("budget.rss", "en")} the budget asks for and the CPU is above the ≤ ${q("budget.cpu", "en")}.`,
	},
	hides: {
		title: "A one-second average is a report about a second",
		paragraphs: [
			`The RouterOS API reports <code>cpu-load</code> once a second. A core saturated for ${burstOn("en")} and idle for the other ${burstOff("en")} moves a ${spellCount(cores, "en")}-core, one-second average by ${q("burst.100msOn4Cores", "en")}. That is arithmetic, not a measurement, and the figure is true: it just cannot say when.`,
			`The agent reads <code>/proc/stat</code>, <code>/proc/interrupts</code>, <code>/proc/softirqs</code> and <code>/proc/net/softnet_stat</code> from inside the router, at ${hz(installDefault.rateHz, "en")} by default, and ships raw tick deltas with the interval each one covers. It never computes a percentage; the window is yours.`,
			`The floor is the kernel's, not the tool's. <code>/proc/stat</code> counts in ticks of ${q("tick.ms", "en")}, so a ${stepWindow("en")} sample resolves one core to ${q("tick.stepOneCore", "en")} steps. On the RB5009 (RouterOS ${kernel.routeros}, Linux ${kernel.kernel}) there is no PSI and no schedstat to go finer: both files are absent, checked ${kernel.date}.`,
		],
	},
	cost: {
		title: `What it costs, at ${spellCount(RUNS_ON_LANDING.length, "en")} rates`,
		lead: `Each row is one ${win("en")} window with the ring already full. Memory differs by row because the ring and the memory limit do.`,
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
		prereq:
			"Download the archive for your platform from the release, or build the CLI from a checkout with <code>make build</code>. The router needs RouterOS 7.24 or later — the container step writes <code>privileged=</code>, an attribute earlier 7.x releases reject — with the <code>container</code> package and <code>device-mode container=yes</code>, which MikroTik gates behind a reset-button press or a power cycle. arm64, arm and x86_64; not MIPS, not TILE.",
		steps: [
			{
				cmd: CMD.doctor,
				note: "Read-only preflight; names the fix for anything missing.",
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
		body: `Any rate on a board that is not this RB5009, and any traffic load heavier than this router's ordinary traffic, about ${q("load.ordinary", "en")} on the WAN. Cost scales with core speed, source set and ring size: measure it on your own device before you budget for it. The project has run on one device, an RB5009UG+S+ on RouterOS ${RB5009.routeros}, arm64; the arm and x86_64 builds are cross-built and checked in CI and have never run on hardware, and seven of the ten sinks are exercised only against fakes.`,
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
				note: "Pull, merge, derive, fan out to ten sinks, and why a slow one never stops the loop",
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
				label: `huecos y descartes, en todos los destinos, en ${spellCount(runs.length, "es")} ejecuciones`,
				href: "/mikroscope/es/cost/rate-ceiling/",
			},
		],
		claim: `Las tres primeras, desde el propio cgroup del agente y <code>/metrics</code>; la cuarta, desde los ${spellCount(rates.sinks.length, "es")} destinos a los que reenviaba el colector; en un RB5009 (${describeCpu(rates, "es")}, RouterOS ${rates.routeros}), ventanas de ${win("es")} en régimen estacionario, ${rates.date}. A ${hz(run10.rateHz, "es")} la memoria está dentro de los ≤ ${q("budget.rss", "es")} que pide el presupuesto y la CPU por encima del ≤ ${q("budget.cpu", "es")}.`,
	},
	hides: {
		title: "Una media de un segundo es un informe sobre un segundo",
		paragraphs: [
			`La API de RouterOS publica <code>cpu-load</code> una vez por segundo. Un núcleo saturado ${burstOn("es")} y ocioso los otros ${burstOff("es")} mueve la media de un segundo de ${spellCount(cores, "es")} núcleos en ${q("burst.100msOn4Cores", "es")}. Es aritmética, no una medida, y la cifra es cierta: solo que no puede decir cuándo.`,
			`El agente lee <code>/proc/stat</code>, <code>/proc/interrupts</code>, <code>/proc/softirqs</code> y <code>/proc/net/softnet_stat</code> desde dentro del router, a ${hz(installDefault.rateHz, "es")} por defecto, y envía los deltas crudos de ticks con el intervalo que cubre cada uno. Nunca calcula un porcentaje; la ventana la eliges tú.`,
			`El suelo es del kernel, no de la herramienta. <code>/proc/stat</code> cuenta en ticks de ${q("tick.ms", "es")}, así que una muestra de ${stepWindow("es")} resuelve un núcleo en escalones de ${q("tick.stepOneCore", "es")}. En el RB5009 (RouterOS ${kernel.routeros}, Linux ${kernel.kernel}) no hay PSI ni schedstat con los que afinar: ambos ficheros faltan, comprobado el ${kernel.date}.`,
		],
	},
	cost: {
		title: `Lo que cuesta, a ${spellCount(RUNS_ON_LANDING.length, "es")} cadencias`,
		lead: `Cada fila es una ventana de ${win("es")} con el anillo ya lleno. La memoria cambia por fila porque cambian el anillo y el límite de memoria.`,
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
		prereq:
			"Descarga el archivo para tu plataforma de la versión publicada, o compila la CLI desde una copia del repositorio con <code>make build</code>. El router necesita RouterOS 7.24 o posterior —el paso del contenedor escribe <code>privileged=</code>, un atributo que las versiones 7.x anteriores rechazan— con el paquete <code>container</code> y <code>device-mode container=yes</code>, que MikroTik condiciona a pulsar el botón de reset o a un corte de alimentación. arm64, arm y x86_64; ni MIPS ni TILE.",
		steps: [
			{
				cmd: CMD.doctor,
				note: "Comprobación de solo lectura; nombra el arreglo de lo que falte.",
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
		body: `Cualquier cadencia en una placa que no sea este RB5009, y cualquier carga de tráfico mayor que el tráfico corriente de este router, unos ${q("load.ordinary", "es")} en la WAN. El coste depende de la velocidad del núcleo, del conjunto de fuentes y del tamaño del anillo: mídelo en tu propio equipo antes de presupuestarlo. El proyecto ha corrido en un equipo, un RB5009UG+S+ con RouterOS ${RB5009.routeros}, arm64; las compilaciones para arm y x86_64 son cruzadas y pasan por CI, pero no han corrido nunca en hardware, y siete de los diez destinos solo se ejercitan contra dobles.`,
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
				note: "Extraer, fusionar, derivar, repartir a diez destinos, y por qué uno lento nunca para el bucle",
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

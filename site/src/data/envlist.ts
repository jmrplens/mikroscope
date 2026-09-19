/**
 * The entries `install` and `upgrade` write into the agent's envlist, in the
 * order `containerStep` writes them (internal/router/steps.go:133-149).
 *
 * Three pages need this list — where things go, the environment reference and
 * the security page, which argues from it that nothing but configuration and
 * one token sits on the router. One list, rendered by `<EnvlistKeys>`, keeps
 * the three pages and both locales on the code's side.
 *
 * Defaults and accepted ranges are `Defaults()` and `Finish()` in
 * internal/router/options.go:100-180; `--rate` accepts 1–100 Hz
 * (cmd/mikroscope/main.go:87).
 */
import type { Lang } from "./measurements";

/** When `containerStep` writes the entry. */
export type Written = "always" | "aboveZero" | "whenSet";

export interface EnvlistEntry {
	key: string;
	written: Written;
	/** The flag the value comes from, or null when install derives it. */
	flag: string | null;
	/** Install's default, verbatim; null where there is none. */
	default: string | null;
	/** What `Finish()` accepts, where it bounds the value. */
	range: string | null;
	/** What the value is. May carry `code` spans. */
	holds: Record<Lang, string>;
}

export const envlist: readonly EnvlistEntry[] = [
	{
		key: "MIKROSCOPE_TAG",
		written: "always",
		flag: "--name",
		default: null,
		range: null,
		holds: {
			en: "the ownership marker `mikroscope:<name> (managed by mikroscope)`, written first and removed last; the agent ignores it",
			es: "la marca de propiedad `mikroscope:<name> (managed by mikroscope)`, que se escribe la primera y se borra la última; el agente la ignora",
		},
	},
	{
		key: "RATE_HZ",
		written: "always",
		flag: "--rate",
		default: "10",
		range: "1–100",
		holds: {
			en: "the sampler rate, in Hz",
			es: "la cadencia del muestreador, en Hz",
		},
	},
	{
		key: "BUFFER_S",
		written: "always",
		flag: "--buffer",
		default: "60",
		range: "10–3600",
		holds: {
			en: "the ring's length, in seconds",
			es: "la longitud del anillo, en segundos",
		},
	},
	{
		key: "PORT",
		written: "always",
		flag: "--port",
		default: "9123",
		range: "1–65535",
		holds: { en: "the agent's HTTP port", es: "el puerto HTTP del agente" },
	},
	{
		key: "ADDR",
		written: "always",
		flag: "--subnet",
		default: null,
		range: null,
		holds: {
			en: "the agent's address, the `.2` of the /30; the agent binds only there",
			es: "la dirección del agente, la `.2` de la /30; el agente solo escucha ahí",
		},
	},
	{
		key: "MEM_LIMIT_MB",
		written: "always",
		flag: "--mem-limit-mb",
		default: null,
		range: "8–1024",
		holds: {
			en: "the agent's Go soft memory limit, in MiB; derived from the ring since 1.0.6 (rate × buffer × line, × 2.5, floored at 16 MiB) rather than a flat number",
			es: "el límite blando de memoria de Go del agente, en MiB; desde 1.0.6 se deriva del anillo (cadencia × buffer × línea, × 2,5, con suelo de 16 MiB) en vez de ser un número fijo",
		},
	},
	{
		key: "FLOOR_HZ",
		written: "aboveZero",
		flag: "--floor-hz",
		default: "0",
		range: "0–1000",
		holds: {
			en: "one cadence for every level source, in Hz",
			es: "una sola cadencia para todas las fuentes de nivel, en Hz",
		},
	},
	{
		key: "CAPTURE_MB",
		written: "always",
		flag: "--capture-mb",
		default: "4",
		range: "0–256",
		holds: {
			en: "the triggered-capture budget, in MiB; `0` turns captures off",
			es: "el presupuesto de capturas por disparo, en MiB; `0` las desactiva",
		},
	},
	{
		key: "TRIGGERS",
		written: "whenSet",
		flag: "--triggers",
		default: null,
		range: null,
		holds: {
			en: "the trigger conditions; unset, the agent uses its default set",
			es: "las condiciones de disparo; sin ella, el agente usa su conjunto por defecto",
		},
	},
	{
		key: "TOKEN",
		written: "whenSet",
		flag: "--token",
		default: null,
		range: null,
		holds: {
			en: "the bearer token the agent requires, from `--token` or `MIKROSCOPE_TOKEN`, with or without `--expose`",
			es: "el token bearer que exige el agente, de `--token` o `MIKROSCOPE_TOKEN`, con o sin `--expose`",
		},
	},
];

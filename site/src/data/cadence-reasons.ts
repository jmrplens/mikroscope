/**
 * The reasons a level source gives for its cadence, as `cadences()` assigns
 * them (internal/agent/source.go:61-74 and :394-431), for `<CadenceReasons>`.
 *
 * The meanings are the code's comment; where the code applies a reason more
 * loosely than its comment says, the page says so beside the table.
 */
import type { Lang } from "./measurements";

export const cadenceReasons = [
	{
		reason: "rate",
		meaning: {
			en: "read at the full sampler rate; nothing the device declares justifies less",
			es: "se lee a la cadencia completa del muestreador; nada de lo que declara el equipo justifica menos",
		},
	},
	{
		reason: "declared",
		meaning: {
			en: "the device publishes its own refresh cadence, and reading faster returns the same value with new dither",
			es: "el equipo publica su propia cadencia de refresco, y leer más rápido devuelve el mismo valor con un temblor nuevo",
		},
	},
	{
		reason: "policy",
		meaning: {
			en: "a setting says the value cannot move on its own: a `userspace` cpufreq governor",
			es: "un ajuste dice que el valor no puede moverse por sí solo: un gobernador cpufreq `userspace`",
		},
	},
	{
		reason: "budget",
		meaning: { en: "a measured parse cost", es: "un coste de análisis medido" },
	},
	{
		reason: "change",
		meaning: {
			en: "read every tick, stored only when it moves",
			es: "se lee en cada tick y se guarda solo cuando se mueve",
		},
	},
	{
		reason: "override",
		meaning: {
			en: "`FLOOR_HZ` is set, and every level source is on its one cadence",
			es: "`FLOOR_HZ` está fijado, y todas las fuentes de nivel van a su única cadencia",
		},
	},
] as const satisfies readonly {
	reason: string;
	meaning: Record<Lang, string>;
}[];

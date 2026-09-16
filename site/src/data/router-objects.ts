/**
 * What each installer command writes to, adds to or removes from a router,
 * as README.md:45-51 and :81 list it, corrected against internal/router where
 * the README says less than the code does. An entry may carry `code` spans. `<RouterWrites>` renders these; the
 * pages never retype the list, so a new object is one edit here.
 */
import type { Lang } from "./measurements";

export type RouterObject = Record<Lang, string>;

const install: RouterObject[] = [
	{ en: "a veth", es: "una veth" },
	{ en: "one address", es: "una dirección" },
	{
		en: "one interface-list membership",
		es: "una pertenencia a lista de interfaces",
	},
	{ en: "one address-list entry", es: "una entrada de address-list" },
	{ en: "an envlist", es: "una envlist" },
	// `--remote-image` has RouterOS pull the image: nothing is uploaded, so
	// there is no tar on the device for install to write or uninstall to
	// remove (internal/router/steps.go, waitAndDropTar and plusImageFile).
	{
		en: "the image tar, unless `--remote-image` has the router pull the image",
		es: "el tar de la imagen, salvo que `--remote-image` haga que el router se la baje",
	},
	{ en: "the container", es: "el contenedor" },
];

export const routerObjects = {
	install,
	expose: [
		{
			en: "two firewall rules, tagged",
			es: "dos reglas de cortafuegos, etiquetadas",
		},
		{ en: "a token becomes mandatory", es: "el token pasa a ser obligatorio" },
		// Plan(o) adds the two rules only when o.Expose is set, and uninstall and
		// status select from that plan (internal/router/steps.go:85).
		{
			en: "`uninstall` and `status` see the two rules only when given `--expose` again",
			es: "`uninstall` y `status` solo ven las dos reglas si se les vuelve a dar `--expose`",
		},
	],
	// Upgrade removes and re-creates the whole container step, and that step
	// owns the envlist (internal/router/probe.go:80-96, steps.go:125-149): the
	// envlist is written again from upgrade's own flags, not kept.
	upgrade: [
		{
			en: "a new image and the container",
			es: "una imagen nueva y el contenedor",
		},
		{
			en: "the envlist, rewritten from the flags `upgrade` is given",
			es: "la envlist, reescrita con las opciones que recibe `upgrade`",
		},
		{ en: "network objects stay", es: "los objetos de red se quedan" },
	],
	// uninstall removes exactly what install wrote, so it is the same list.
	uninstall: install,
} satisfies Record<string, RouterObject[]>;

export type RouterCommand = keyof typeof routerObjects;

export const isRouterCommand = (c: string): c is RouterCommand =>
	Object.hasOwn(routerObjects, c);

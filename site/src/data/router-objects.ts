/**
 * What each installer command writes to, adds to or removes from a router,
 * from the steps spec in internal/router: the install manifest first, then
 * every object in creation order. An entry may carry `code` spans.
 * `<RouterWrites>` renders these; the pages never retype the list, so a new
 * object is one edit here.
 */
import type { Lang } from "./measurements";

export type RouterObject = Record<Lang, string>;

const install: RouterObject[] = [
	// Written first, so that an install that stops half-way still leaves the
	// record of what it meant to create. Files carry no comment; the
	// manifest's own tag line is what makes it this install's.
	{
		en: "the install manifest, a file `mikroscope/<name>.manifest.txt` on the install's disk that lists the options and every object below",
		es: "el manifiesto de la instalación, un fichero `mikroscope/<name>.manifest.txt` en el disco de la instalación que lista las opciones y cada objeto de abajo",
	},
	{ en: "a veth", es: "una veth" },
	{ en: "one address", es: "una dirección" },
	{
		en: "one interface-list membership, unless `--iface-list none`",
		es: "una pertenencia a lista de interfaces, salvo con `--iface-list none`",
	},
	{
		en: "one address-list entry, unless `--addr-list none`",
		es: "una entrada de address-list, salvo con `--addr-list none`",
	},
	{ en: "an envlist", es: "una envlist" },
	// `--remote-image` has RouterOS pull the image: nothing is uploaded, so
	// there is no tar on the device for install to write or uninstall to
	// remove.
	{
		en: "the image tar, deleted once the container is extracted, unless `--remote-image` has the router pull the image",
		es: "el tar de la imagen, que se borra en cuanto el contenedor está extraído, salvo que `--remote-image` haga que el router se la baje",
	},
	{
		en: "the container, and its root `mikroscope/<name>` on the same disk",
		es: "el contenedor, y su raíz `mikroscope/<name>` en el mismo disco",
	},
];

export const routerObjects = {
	install,
	expose: [
		{
			en: "two firewall rules, tagged",
			es: "dos reglas de cortafuegos, etiquetadas",
		},
		{ en: "a token becomes mandatory", es: "el token pasa a ser obligatorio" },
		// The manifest records --expose, and the tag sweep finds a rule the
		// flags do not name.
		{
			en: "`uninstall` and `status` find the two rules through the manifest and the tag, with or without `--expose`",
			es: "`uninstall` y `status` encuentran las dos reglas por el manifiesto y la etiqueta, con `--expose` o sin él",
		},
	],
	// Upgrade writes the manifest first every time, then removes and
	// re-creates the container step, which owns the envlist: the envlist is
	// written again from upgrade's own flags. Another step the router does
	// not hold is created before the container.
	upgrade: [
		{
			en: "the install manifest, written first every time, so an install made before there was one gets it",
			es: "el manifiesto de la instalación, que se escribe siempre el primero, así que una instalación hecha antes de que existiera lo recibe",
		},
		{
			en: "a new image and the container",
			es: "una imagen nueva y el contenedor",
		},
		{
			en: "the envlist, rewritten from the flags `upgrade` is given; it refuses an install with a token when no `--token` is given",
			es: "la envlist, reescrita con las opciones que recibe `upgrade`; se niega con una instalación con token si no recibe `--token`",
		},
		{
			en: "any other object of the install the router no longer holds, created again before the container",
			es: "cualquier otro objeto de la instalación que el router ya no tenga, que se crea de nuevo antes del contenedor",
		},
		{ en: "network objects stay", es: "los objetos de red se quedan" },
	],
	// Everything install created, read from the manifest when there is one
	// and from the tag and the known paths when there is not (an install made
	// by 1.3.x). Nothing it did not create.
	uninstall: [
		{
			en: "every object in the install's manifest, and any other object that carries its tag",
			es: "cada objeto del manifiesto de la instalación, y cualquier otro objeto que lleve su etiqueta",
		},
		{
			en: "the container root `mikroscope/<name>`, with the container, or on the manifest's word when a root is left",
			es: "la raíz del contenedor `mikroscope/<name>`, con el contenedor, o con la palabra del manifiesto si queda una raíz",
		},
		{
			en: "the manifest, last, and then the `mikroscope` directory when nothing else is in it",
			es: "el manifiesto, lo último, y después el directorio `mikroscope` si no queda nada más en él",
		},
		{
			en: "never device-mode, the `container` package, `/container/config`, or a list, disk or rule the router had before",
			es: "nunca device-mode, el paquete `container`, `/container/config`, ni una lista, disco o regla que el router ya tuviera",
		},
	],
} satisfies Record<string, RouterObject[]>;

export type RouterCommand = keyof typeof routerObjects;

export const isRouterCommand = (c: string): c is RouterCommand =>
	Object.hasOwn(routerObjects, c);

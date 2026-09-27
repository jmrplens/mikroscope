/**
 * The checks `mikroscope doctor` runs, in the order it prints them
 * (internal/router/doctor.go), for `<DoctorChecks>`.
 *
 * The check names stay in English in both locales, because they are what the
 * terminal prints; `<name>` stands for the value doctor fills in. The fix is
 * a summary of the code's fix string, not the string itself, which is longer
 * than a table cell.
 *
 * Some rows are alternatives, and doctor prints one of each group: the
 * architecture row depends on the image route (`--remote-image`,
 * `--agent-tar`, an explicit `--arch`, or none of them); the free-flash row
 * runs without `--disk` or `--ephemeral` and the disk rows with one; the list
 * rows print `none` in place of a list when the flag is `none`; the
 * container-name row only with `--container-name`; the two `--lan-address`
 * rows only with `--expose`; the registry-credential row only with
 * `--remote-image`; and the exposed-token row only when an install of that
 * `--name` is published on the LAN.
 *
 * `passes` starts with "a warning" for the rows doctor prints as `WARN`: they
 * change neither its exit status nor whether `install` goes ahead. A check
 * whose answer doctor could not read is `MISSING` when it is a prerequisite
 * and `WARN` when it is advice, and its fix names what the router printed
 * instead.
 */
import type { Lang } from "./measurements";

export interface DoctorCheck {
	/** A stable key, for `only`. */
	id: string;
	/** As doctor prints it, with `<…>` for what it substitutes. */
	printed: string;
	passes: Record<Lang, string>;
	fix: Record<Lang, string>;
}

export const doctorChecks: readonly DoctorCheck[] = [
	{
		id: "routeros-version",
		printed: "RouterOS 7.24 or later",
		passes: {
			en: "`/system/resource` reports a `version` of 7.24 or later, read with or without a patch number and whatever the channel, as in `7.24 (stable)` or `7.25rc1 (testing)`",
			es: "`/system/resource` informa una `version` 7.24 o posterior, que se lee con número de parche o sin él y sea cual sea el canal, como en `7.24 (stable)` o `7.25rc1 (testing)`",
		},
		fix: {
			en: "upgrade RouterOS to 7.24 or later (`/system/package/update`), and the `container` package with it",
			es: "actualiza RouterOS a 7.24 o posterior (`/system/package/update`), y el paquete `container` con él",
		},
	},
	{
		id: "arch-package",
		printed: "architecture has a container package",
		passes: {
			en: "the router's `architecture-name` is `arm`, `arm64` or `x86_64`, the architectures MikroTik publishes a `container` package for",
			es: "el `architecture-name` del router es `arm`, `arm64` o `x86_64`, las arquitecturas para las que MikroTik publica un paquete `container`",
		},
		fix: {
			en: "none: no agent can run on this router",
			es: "ninguno: en este router no puede ejecutarse ningún agente",
		},
	},
	{
		id: "arch-remote",
		printed: "the router picks the image's architecture",
		passes: {
			en: "with `--remote-image`: always, naming the router's architecture, because RouterOS picks it from the image's multi-architecture index. A warning on `arm`, where the index holds both `linux/arm/v5` and `linux/arm/v7` and which one RouterOS pulls is not known",
			es: "con `--remote-image`: siempre, con la arquitectura del router, porque RouterOS la elige del índice multiarquitectura de la imagen. Un aviso en `arm`, donde el índice tiene `linux/arm/v5` y `linux/arm/v7` y no se sabe cuál descarga RouterOS",
		},
		fix: {
			en: "on `arm`, if the container stops with `Exec format error`, install from `mikroscope-agent-armv5.tar` with `--agent-tar`",
			es: "en `arm`, si el contenedor se detiene con `Exec format error`, instala desde `mikroscope-agent-armv5.tar` con `--agent-tar`",
		},
	},
	{
		id: "arch-tar",
		printed: "architecture matches the --agent-tar image",
		passes: {
			en: "with `--agent-tar`: the tar's own architecture is the router's (`amd64` for `x86_64`); `--arch` is not needed",
			es: "con `--agent-tar`: la arquitectura del propio tar es la del router (`amd64` para `x86_64`); `--arch` no hace falta",
		},
		fix: {
			en: "download the release asset it names, `mikroscope-agent-<arch>.tar`",
			es: "descarga el recurso de la versión que nombra, `mikroscope-agent-<arch>.tar`",
		},
	},
	{
		id: "arch-detected",
		printed: "architecture read from the router",
		passes: {
			en: "with neither image flag and `--arch` unset (or `auto`): always; `install` and `upgrade` build or load the image for the architecture doctor read",
			es: "sin ninguna de las dos opciones de imagen y con `--arch` sin fijar (o `auto`): siempre; `install` y `upgrade` construyen o cargan la imagen de la arquitectura que leyó doctor",
		},
		fix: {
			en: "none",
			es: "ninguno",
		},
	},
	{
		id: "arch",
		printed: "architecture matches --arch <arch>",
		passes: {
			en: "with an explicit `--arch` and neither image flag: the router's `architecture-name` is the one `--arch` maps to (`arm64`, `arm`, `x86_64`)",
			es: "con un `--arch` explícito y sin opciones de imagen: el `architecture-name` del router es el que corresponde a `--arch` (`arm64`, `arm`, `x86_64`)",
		},
		fix: {
			en: "re-run with the `--arch` it names, or leave `--arch` out so that `install` reads it from the router",
			es: "vuelve a ejecutar con el `--arch` que nombra, o quita `--arch` para que `install` la lea del router",
		},
	},
	{
		id: "container-package",
		printed: "container package installed and enabled",
		passes: {
			en: "a `container` package exists with `disabled=no`",
			es: "existe un paquete `container` con `disabled=no`",
		},
		fix: {
			en: "download the `container` package for this architecture and RouterOS version, upload it and reboot; when it is there and disabled, `/system/package/enable container` and reboot",
			es: "descarga el paquete `container` de esta arquitectura y versión de RouterOS, súbelo y reinicia; si ya está y deshabilitado, `/system/package/enable container` y reinicia",
		},
	},
	{
		id: "device-mode",
		printed: "device-mode container=yes",
		passes: {
			en: "`/system/device-mode` reports `container=yes`",
			es: "`/system/device-mode` informa `container=yes`",
		},
		fix: {
			en: "`/system/device-mode/update container=yes`, then confirm it as the console asks: on a router that says `update: please activate by turning power off or pressing reset or mode button`, press the reset or mode button or cut the power; on CHR, which says `update: turn off power in 5m to activate changes`, power the VM off and on again within 5 minutes",
			es: "`/system/device-mode/update container=yes` y confírmalo como pide la consola: en un router que dice `update: please activate by turning power off or pressing reset or mode button`, pulsa el botón reset o mode o corta la alimentación; en CHR, que dice `update: turn off power in 5m to activate changes`, apaga y enciende la VM en menos de 5 minutos",
		},
	},
	{
		id: "memory",
		printed: "free memory ≥ <--memory-max>",
		passes: {
			en: "`free-memory` is at least what `--memory-max` asks for, 64 MiB by default",
			es: "`free-memory` es al menos lo que pide `--memory-max`, 64 MiB por defecto",
		},
		fix: {
			en: "free memory on the router, or ask for less with `--memory-max`",
			es: "libera memoria en el router, o pide menos con `--memory-max`",
		},
	},
	{
		id: "memory-pull",
		printed: "free memory leaves room for the pull",
		passes: {
			en: "a warning, with `--remote-image` only: `free-memory` is at least `--memory-max` plus 16 MiB, room for RouterOS to pull and extract the image before the agent starts. How much a pull takes is not measured, so the margin is an estimate",
			es: "un aviso, solo con `--remote-image`: `free-memory` es al menos `--memory-max` más 16 MiB, margen para que RouterOS descargue y extraiga la imagen antes de que arranque el agente. No se ha medido cuánto ocupa una descarga, así que el margen es una estimación",
		},
		fix: {
			en: "install from a tar with `--agent-tar` if the pull fails",
			es: "instala desde un tar con `--agent-tar` si la descarga falla",
		},
	},
	{
		id: "flash",
		printed: "free flash ≥ <size> (image tar + extracted root)",
		passes: {
			en: "without `--disk` or `--ephemeral`: `free-hdd-space` is at least twice the image plus 4 MiB. With `--remote-image` nothing is uploaded and the name ends in `(extracted root)`: the root the pulled image is extracted into, 7 MiB, plus 4 MiB",
			es: "sin `--disk` ni `--ephemeral`: `free-hdd-space` es al menos el doble de la imagen más 4 MiB. Con `--remote-image` no se sube nada y el nombre acaba en `(extracted root)`: la raíz en la que se extrae la imagen descargada, 7 MiB, más 4 MiB",
		},
		fix: {
			en: "free flash, or install with `--disk tmpfs` or `--ephemeral` where a tmpfs disk exists",
			es: "libera flash, o instala con `--disk tmpfs` o `--ephemeral` donde exista un disco tmpfs",
		},
	},
	{
		id: "disk",
		printed: "disk <disk> exists",
		passes: {
			en: "with `--disk` or `--ephemeral`: a disk with that slot exists",
			es: "con `--disk` o `--ephemeral`: existe un disco con ese slot",
		},
		fix: {
			en: "`/disk/add type=tmpfs tmpfs-max-size=64M slot=tmpfs` for a RAM disk, or name an existing disk with `--disk`",
			es: "`/disk/add type=tmpfs tmpfs-max-size=64M slot=tmpfs` para un disco en RAM, o nombra un disco existente con `--disk`",
		},
	},
	{
		id: "disk-free",
		printed: "disk <disk> has ≥ <size> free (image tar + extracted root)",
		passes: {
			en: "with `--disk` or `--ephemeral`, once the disk exists: its free space is at least twice the image plus 4 MiB; with `--remote-image`, the 7 MiB root plus 4 MiB, as the flash check",
			es: "con `--disk` o `--ephemeral`, cuando el disco existe: su espacio libre es al menos el doble de la imagen más 4 MiB; con `--remote-image`, la raíz de 7 MiB más 4 MiB, como en la comprobación de la flash",
		},
		fix: {
			en: "free space on that disk, or give a tmpfs disk a larger `tmpfs-max-size`",
			es: "libera espacio en ese disco, o da a un disco tmpfs un `tmpfs-max-size` mayor",
		},
	},
	{
		id: "disk-ram",
		printed: "disk tmpfs is RAM",
		passes: {
			en: "with `--ephemeral`: the disk in slot `tmpfs` is of type `tmpfs`",
			es: "con `--ephemeral`: el disco del slot `tmpfs` es de tipo `tmpfs`",
		},
		fix: {
			en: "free the slot for a tmpfs disk, or install with `--disk <slot>` without `--ephemeral`",
			es: "libera el slot para un disco tmpfs, o instala con `--disk <slot>` sin `--ephemeral`",
		},
	},
	{
		id: "start-on-boot",
		printed: "start-on-boot suits a root in RAM",
		passes: {
			en: "a warning, when the disk is a tmpfs disk: start-on-boot resolves to `no`, since a reboot empties the disk and a container started at boot has no root",
			es: "un aviso, cuando el disco es tmpfs: start-on-boot queda en `no`, porque un reinicio vacía el disco y un contenedor que arranca con el equipo no tiene raíz",
		},
		fix: {
			en: "pass `--start-on-boot no`, or `--ephemeral`",
			es: "pasa `--start-on-boot no`, o `--ephemeral`",
		},
	},
	{
		id: "veth",
		printed: "veth name <veth> is free or ours",
		passes: {
			en: "no veth has that name, or the one that has it carries this install's tag",
			es: "ninguna veth tiene ese nombre, o la que lo tiene lleva la etiqueta de esta instalación",
		},
		fix: {
			en: "pick another `--veth` (and `--subnet`), or remove the veth by hand if it is a leftover of yours",
			es: "elige otra `--veth` (y otra `--subnet`), o borra la veth a mano si es un resto tuyo",
		},
	},
	{
		id: "envlist",
		printed: "envlist <name>-env is free or ours",
		passes: {
			en: "no envlist has that name, or the one that has it holds this install's `MIKROSCOPE_TAG` entry",
			es: "ninguna envlist tiene ese nombre, o la que lo tiene contiene la entrada `MIKROSCOPE_TAG` de esta instalación",
		},
		fix: {
			en: "pick another `--name`",
			es: "elige otro `--name`",
		},
	},
	{
		id: "manifest-path",
		printed:
			"install manifest <disk/>mikroscope/<name>.manifest.txt is free or ours",
		passes: {
			en: "no file is at the install manifest's path, or the one there holds this install's `tag=` line",
			es: "no hay ningún fichero en la ruta del manifiesto de instalación, o el que hay contiene la línea `tag=` de esta instalación",
		},
		fix: {
			en: "move the file away, or pick another `--name`",
			es: "mueve el fichero a otro sitio, o elige otro `--name`",
		},
	},
	{
		id: "container-name",
		printed: "container name <name> is free or ours",
		passes: {
			en: "with `--container-name`: no container has that name, or the one that has it carries this install's tag",
			es: "con `--container-name`: ningún contenedor tiene ese nombre, o el que lo tiene lleva la etiqueta de esta instalación",
		},
		fix: {
			en: "pick another `--container-name`",
			es: "elige otro `--container-name`",
		},
	},
	{
		id: "overlap",
		printed: "subnet <subnet> does not overlap a route",
		passes: {
			en: "no route of the main table, active or not, lies inside the /30, and no connected network on another interface holds its router end. Routes that only contain the /30 (a default route, a wider prefix to a VPN), blackhole routes and the install's own veth are left out",
			es: "ninguna ruta de la tabla main, activa o no, cae dentro de la /30, y ninguna red conectada de otra interfaz contiene su extremo del router. Las rutas que solo contienen la /30 (una ruta por defecto, un prefijo más amplio hacia una VPN), las rutas blackhole y la propia veth de la instalación no cuentan",
		},
		fix: {
			en: "pick another /30 with `--subnet`",
			es: "elige otra /30 con `--subnet`",
		},
	},
	{
		id: "iface-list",
		printed: "interface list <list> exists",
		passes: {
			en: "the `--iface-list` list (default `LAN`) exists. With `--iface-list none` doctor prints `interface list the veth joins` and passes: no membership is written",
			es: "existe la lista `--iface-list` (por defecto `LAN`). Con `--iface-list none` doctor imprime `interface list the veth joins` y lo da por bueno: no se escribe ninguna pertenencia",
		},
		fix: {
			en: "`--iface-list none` when no firewall rule needs the veth in a list (doctor offers it first then); otherwise `/interface/list/add name=…`, or pass the list your `in-interface-list=!…` drop rule uses",
			es: "`--iface-list none` si ninguna regla del cortafuegos necesita la veth en una lista (doctor lo ofrece primero en ese caso); si no, `/interface/list/add name=…`, o pasa la lista que usa tu regla de descarte `in-interface-list=!…`",
		},
	},
	{
		id: "addr-list",
		printed: "address list <list>",
		passes: {
			en: "always: install adds the /30 to the `--addr-list` list (default `LANs`), which creates it when it is missing, and uninstall removes the entry. With `--addr-list none` doctor prints `address list the /30 joins`. Whether a rule needs the membership is the next row's question",
			es: "siempre: install añade la /30 a la lista `--addr-list` (por defecto `LANs`), lo que la crea si falta, y uninstall retira la entrada. Con `--addr-list none` doctor imprime `address list the /30 joins`. Si alguna regla necesita esa pertenencia lo responde la fila siguiente",
		},
		fix: {
			en: "none",
			es: "ninguno",
		},
	},
	{
		id: "firewall-traps",
		printed: "no firewall rule drops the agent's replies",
		passes: {
			en: "doctor reads every enabled rule of the chains the agent's replies meet, `/ip/firewall/raw` prerouting and `/ip/firewall/filter` forward and input, and walks each one as RouterOS does, first match wins, with the replies in the lists the plan joins: no rule drops them. A warning when a rule might, because it matches on something doctor does not judge (a destination, a mark, a rate), and when the replies to a LAN host pass but a rule may drop the ones to the router itself (filter input), which the relay transport needs",
			es: "doctor lee todas las reglas activas de las cadenas por las que pasan las respuestas del agente, `/ip/firewall/raw` prerouting y `/ip/firewall/filter` forward e input, y recorre cada una como RouterOS, gana la primera que coincide, con las respuestas en las listas a las que se une el plan: ninguna regla las descarta. Un aviso cuando una regla podría hacerlo, porque filtra por algo que doctor no evalúa (un destino, una marca, un límite de tasa), y cuando las respuestas a un equipo de la LAN pasan pero una regla podría descartar las que van al propio router (filter input), que necesita el transporte por relay",
		},
		fix: {
			en: "the `--iface-list` and `--addr-list` that let the replies through; when no list does (a `src-address=!<range>` rule, say), add an accept rule for `in-interface=<veth>` before that rule, or pick a `--subnet` inside the range",
			es: "las `--iface-list` y `--addr-list` que dejan pasar las respuestas; cuando ninguna lista lo consigue (una regla `src-address=!<rango>`, por ejemplo), añade antes de esa regla una de aceptación para `in-interface=<veth>`, o elige una `--subnet` dentro del rango",
		},
	},
	{
		id: "lan-address",
		printed: "--lan-address <address> is the router's",
		passes: {
			en: "with `--expose`: an interface of the router holds that address",
			es: "con `--expose`: una interfaz del router tiene esa dirección",
		},
		fix: {
			en: "pass the address the router has on its LAN, as `/ip/address/print` lists it",
			es: "pasa la dirección que el router tiene en su LAN, tal como la lista `/ip/address/print`",
		},
	},
	{
		id: "lan-uplink",
		printed: "--lan-address is not on the uplink",
		passes: {
			en: "a warning, with `--expose`: the interface that holds the address carries no default route, is in no `WAN` list, and shares no interface list with the interface that carries the default route",
			es: "un aviso, con `--expose`: la interfaz que tiene la dirección no lleva la ruta por defecto, no está en ninguna lista `WAN` y no comparte ninguna lista de interfaces con la interfaz que lleva la ruta por defecto",
		},
		fix: {
			en: "pass the router's LAN address: on the uplink the dst-nat would publish the agent on the Internet side",
			es: "pasa la dirección LAN del router: en el enlace de subida el dst-nat publicaría el agente hacia Internet",
		},
	},
	{
		id: "registry-credential",
		printed: "no registry credential meant for another registry",
		passes: {
			en: "a warning, with `--remote-image` only: no `/container/config` username is set, or the host of `registry-url` is the host the image is pulled from, every spelling of Docker Hub counted as one. An empty `registry-url` with a username set warns. Doctor reads whether a username is set, never the name, and cannot read the password",
			es: "un aviso, solo con `--remote-image`: no hay usuario en `/container/config`, o el host de `registry-url` es el host del que se descarga la imagen, contando todas las grafías de Docker Hub como una. Un `registry-url` vacío con usuario puesto avisa. Doctor lee si hay usuario, nunca el nombre, y no puede leer la contraseña",
		},
		fix: {
			en: "`/container/config` holds one username for the whole device, and a credential from another registry can make the pull of a public image end in `auth error`. Install from a tar with `--agent-tar`, pass a `--remote-image` on the registry the username belongs to, or clear the username if nothing else needs it",
			es: "`/container/config` guarda un solo usuario para todo el equipo, y una credencial de otro registro puede hacer que la descarga de una imagen pública acabe en `auth error`. Instala desde un tar con `--agent-tar`, pasa un `--remote-image` del registro al que pertenece el usuario, o borra el usuario si nada más lo necesita",
		},
	},
	{
		id: "exposed-token",
		printed: "the installed agent published on the LAN asks for a token",
		passes: {
			en: "a warning, shown only when an install of this `--name` has a dst-nat on the LAN: its environment holds a `TOKEN`. Doctor counts the entries, never reads the value",
			es: "un aviso, que solo aparece cuando una instalación con este `--name` tiene un dst-nat en la LAN: su entorno contiene un `TOKEN`. Doctor cuenta las entradas, nunca lee el valor",
		},
		fix: {
			en: "`upgrade` with the same `--name` and `--token <secret>`; or remove the agent, the LAN rules and the container together with `uninstall --name <name> --yes`",
			es: "`upgrade` con el mismo `--name` y `--token <secreto>`; o retira el agente, las reglas de LAN y el contenedor juntos con `uninstall --name <nombre> --yes`",
		},
	},
	{
		id: "leftovers",
		printed: "nothing tagged for <name> that these flags do not select",
		passes: {
			en: "a warning: in every menu an install writes to, the objects that carry the install's tag are no more than the plan for these flags selects. An install made with other flags (`--expose`, other lists, another `--subnet`) leaves more",
			es: "un aviso: en cada menú donde escribe una instalación, los objetos con su etiqueta no son más que los que selecciona el plan de estas opciones. Una instalación hecha con otras opciones (`--expose`, otras listas, otra `--subnet`) deja más",
		},
		fix: {
			en: "run `status` and `uninstall` with no shape flag, so that they read the install manifest, or with the flags that install was given",
			es: "ejecuta `status` y `uninstall` sin opciones de forma, para que lean el manifiesto de la instalación, o con las opciones con que se hizo",
		},
	},
];

export const isDoctorCheckId = (id: string): boolean =>
	doctorChecks.some((c) => c.id === id);

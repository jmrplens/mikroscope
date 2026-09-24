/**
 * The checks `mikroscope doctor` runs, in the order it prints them
 * (internal/router/doctor.go), for `<DoctorChecks>`.
 *
 * The check names stay in English in both locales, because they are what the
 * terminal prints; `<name>` stands for the value doctor fills in. The fix is
 * a summary of the code's fix string, not the string itself, which is longer
 * than a table cell. The free-flash and disk checks are exclusive: doctor runs
 * the first without `--disk` or `--ephemeral`, the second with one. The
 * registry-url check runs only with a `--remote-image` that names a host.
 * The registry-credential check runs with any `--remote-image`, and the
 * exposed-token check only when an install of that `--name` is published on
 * the LAN. Those two are warnings: doctor prints them as `WARN`, and they
 * change neither its exit status nor whether `install` goes ahead.
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
		id: "registry-url",
		printed: "registry-url is https://<host>",
		passes: {
			en: "with `--remote-image`, `/container/config registry-url` names the reference's registry host. Without `--remote-image` doctor does not ask: the setting is global to the device and mikroscope never writes it",
			es: "con `--remote-image`, `/container/config registry-url` nombra el host de registro de la referencia. Sin `--remote-image` doctor no lo pregunta: el ajuste es global del equipo y mikroscope nunca lo escribe",
		},
		fix: {
			en: "`/container/config/set registry-url=https://<host>` on the router, which applies to every container on it, or install from a tar with `--agent-tar`",
			es: "`/container/config/set registry-url=https://<host>` en el router, que afecta a todos sus contenedores, o instalar desde un tar con `--agent-tar`",
		},
	},
	{
		id: "registry-credential",
		printed: "no registry credential meant for another registry",
		passes: {
			en: "a warning, with `--remote-image` only: no `/container/config` username is set, or the pull goes to Docker Hub. Doctor reads whether a username is set, never the name, and cannot read the password",
			es: "un aviso, solo con `--remote-image`: no hay usuario en `/container/config`, o la descarga va a Docker Hub. Doctor lee si hay usuario, nunca el nombre, y no puede leer la contraseña",
		},
		fix: {
			en: "RouterOS presents the one device-wide credential to whichever registry it pulls from, and a Docker Hub account sent to GHCR ends the pull in `auth error`. Install from a tar with `--agent-tar`, or clear the username if nothing else needs it",
			es: "RouterOS presenta la única credencial del equipo a cualquier registro del que descargue, y una cuenta de Docker Hub enviada a GHCR termina la descarga en `auth error`. Instalar desde un tar con `--agent-tar`, o borrar el usuario si nada más lo necesita",
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
			en: "download, upload, reboot; then `/system/package/enable container`",
			es: "descargar, subir, reiniciar; después `/system/package/enable container`",
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
			en: "`/system/device-mode/update container=yes`, then the reset or mode button, or a power cycle, within 5 minutes",
			es: "`/system/device-mode/update container=yes` y, en menos de 5 minutos, el botón reset o mode, o un ciclo de alimentación",
		},
	},
	{
		id: "arch",
		printed: "architecture matches --arch <arch>",
		passes: {
			en: "the router's `architecture-name` is the one `--arch` maps to (`arm64`, `arm`, `x86_64`)",
			es: "el `architecture-name` del router es el que corresponde a `--arch` (`arm64`, `arm`, `x86_64`)",
		},
		fix: {
			en: "re-run with the `--arch` it names",
			es: "volver a ejecutar con el `--arch` que nombra",
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
			es: "liberar memoria en el router, o pedir menos con `--memory-max`",
		},
	},
	{
		id: "flash",
		printed: "free flash ≥ <size> (image tar + extracted root)",
		passes: {
			en: "without `--disk`: `free-hdd-space` is at least twice the image plus 4 MiB",
			es: "sin `--disk`: `free-hdd-space` es al menos el doble de la imagen más 4 MiB",
		},
		fix: {
			en: "free flash, or install with `--disk tmpfs` or `--ephemeral` where a tmpfs disk exists",
			es: "liberar flash, o instalar con `--disk tmpfs` o `--ephemeral` donde exista un disco tmpfs",
		},
	},
	{
		id: "disk",
		printed: "disk <disk> exists",
		passes: {
			en: "with `--disk` or `--ephemeral`: a disk with that slot exists; its free space is not checked",
			es: "con `--disk` o `--ephemeral`: existe un disco con ese slot; su espacio libre no se comprueba",
		},
		fix: {
			en: "`/disk/add type=tmpfs tmpfs-max-size=64M slot=tmpfs` for a RAM disk, or name an existing disk with `--disk`",
			es: "`/disk/add type=tmpfs tmpfs-max-size=64M slot=tmpfs` para un disco en RAM, o nombrar un disco existente con `--disk`",
		},
	},
	{
		id: "iface-list",
		printed: "interface list <list> exists (raw rule trap)",
		passes: {
			en: "the `--iface-list` list (default `LAN`) exists",
			es: "existe la lista `--iface-list` (por defecto `LAN`)",
		},
		fix: {
			en: "`/interface/list/add name=…`, or pass the list your `in-interface-list=!…` drop rule uses",
			es: "`/interface/list/add name=…`, o pasar la lista que usa tu regla de descarte `in-interface-list=!…`",
		},
	},
	{
		id: "addr-list",
		printed: "address list <list> has entries (raw rule trap)",
		passes: {
			en: "the `--addr-list` list (default `LANs`) has at least one entry",
			es: "la lista `--addr-list` (por defecto `LANs`) tiene al menos una entrada",
		},
		fix: {
			en: "pass the list your `drop local if not from default IP range` rule uses; an empty list is fine only if there is no such rule",
			es: "pasar la lista que usa tu regla `drop local if not from default IP range`; una lista vacía vale solo si no hay tal regla",
		},
	},
	{
		id: "veth",
		printed: "veth name <veth> is free or ours",
		passes: {
			en: "always reported `ok`, with the count found",
			es: "siempre se informa `ok`, con el recuento encontrado",
		},
		fix: {
			en: "none: a collision is caught by `install` itself",
			es: "ninguna: una colisión la detecta el propio `install`",
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
			en: "`upgrade` with the same `--name`, the flags it was installed with (`--expose --lan-address` among them) and `--token <secret>`; or remove the agent, LAN rules and container together, with `uninstall --name <name> --expose --lan-address <router LAN IPv4> --token <any> --yes` plus any other shape flag the install was given (`--port`, `--veth`, `--subnet`)",
			es: "`upgrade` con el mismo `--name`, las opciones con que se instaló (entre ellas `--expose --lan-address`) y `--token <secreto>`; o retira el agente, reglas de LAN y contenedor juntos, con `uninstall --name <nombre> --expose --lan-address <IPv4 LAN del router> --token <cualquiera> --yes` más cualquier otra opción de forma que tuviera la instalación (`--port`, `--veth`, `--subnet`)",
		},
	},
];

export const isDoctorCheckId = (id: string): boolean =>
	doctorChecks.some((c) => c.id === id);

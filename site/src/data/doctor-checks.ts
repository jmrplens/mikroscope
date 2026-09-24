/**
 * The checks `mikroscope doctor` runs, in the order it prints them
 * (internal/router/doctor.go), for `<DoctorChecks>`.
 *
 * The check names stay in English in both locales, because they are what the
 * terminal prints; `<name>` stands for the value doctor fills in. The fix is
 * a summary of the code's fix string, not the string itself, which is longer
 * than a table cell. The free-flash and disk checks are exclusive: doctor runs
 * the first without `--disk` or `--ephemeral`, the second with one. There is
 * no registry-url check: `--remote-image` sends RouterOS the whole reference,
 * registry host included, so that device-wide setting decides nothing about
 * the pull. The registry-credential check runs with any `--remote-image`, and the
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
		id: "registry-credential",
		printed: "no registry credential meant for another registry",
		passes: {
			en: "a warning, with `--remote-image` only: no `/container/config` username is set, or the host of `registry-url` is the host the image is pulled from, every spelling of Docker Hub counted as one. An empty `registry-url` with a username set warns. Doctor reads whether a username is set, never the name, and cannot read the password",
			es: "un aviso, solo con `--remote-image`: no hay usuario en `/container/config`, o el host de `registry-url` es el host del que se descarga la imagen, contando todas las grafías de Docker Hub como una. Un `registry-url` vacío con usuario puesto avisa. Doctor lee si hay usuario, nunca el nombre, y no puede leer la contraseña",
		},
		fix: {
			en: "`/container/config` holds one username for the whole device. A credential from another registry makes a pull end in `auth error` even for a public image (measured on the reference RB5009 with a Docker Hub login sent to GHCR, 2026-09-21); whether RouterOS presents it to a host named only in `remote-image=` was not measured. Install from a tar with `--agent-tar`, pass a `--remote-image` on the registry the username belongs to, or clear the username if nothing else needs it",
			es: "`/container/config` guarda un solo usuario para todo el equipo. Una credencial de otro registro hace que la descarga acabe en `auth error` aunque la imagen sea pública (medido en el RB5009 de referencia con un login de Docker Hub enviado a GHCR, 2026-09-21); no se ha medido si RouterOS la presenta a un host que solo nombra `remote-image=`. Instalar desde un tar con `--agent-tar`, pasar un `--remote-image` del registro al que pertenece el usuario, o borrar el usuario si nada más lo necesita",
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

/**
 * Yes-or-no facts about RouterOS that the guides rest on, each checked once
 * on the reference device, for `<Verified>` and for the register of them on
 * Tested on (about/status, <VerifiedRegister>), where a guide links each one
 * with `<TestedOn of="<id>">`.
 *
 * They are not figures, so they are not in measurements.ts and `<Provenance>`
 * refuses them: "Measured on" beside "a read user can list every envlist" would
 * be the wrong verb. Each one is cited from several pages in both locales, in
 * the middle of a sentence ("verified on …"), with the device, the version
 * and the date written once here.
 *
 * `fact` and `source` are for the reviewer. `statement` is what the register
 * prints, in each language, followed by where and when; it never names an
 * image by its versioned reference, which scripts/check-version.mjs reads as
 * a stale install command.
 */
import { RB5009, RB5009_NOW, type Lang } from "./measurements";

export interface Verification {
	/**
	 * The fact's heading in <VerifiedRegister> on Tested on, a label of a few
	 * words in each language. Without one the entry is headed by its id.
	 */
	title?: Record<Lang, string>;
	/**
	 * The fact as the register states it, one sentence in each language.
	 * Without one the register shows `fact`, which is English, marked as such.
	 */
	statement?: Record<Lang, string>;
	device: string;
	routeros: string;
	/** ISO date the source gives. */
	date: string;
	fact: string;
	source: string;
}

const onRB5009 = { device: RB5009.device, routeros: RB5009.routeros } as const;
/** The same device on the RouterOS it runs now, for a fact checked after the upgrade to it. */
const onRB5009Now = {
	device: RB5009.device,
	routeros: RB5009_NOW.routeros,
} as const;

export const verifications = {
	"read-user-envlist": {
		title: {
			en: "Read user sees envlists",
			es: "Un usuario read ve las envlists",
		},
		statement: {
			en: "Over the binary API, `/container/print` returned every property of every container, `cmd` and `envlist` included, to a user with only `read,api`, the user `mikroscope`; only the property names were printed, and reading the values in `/container/envs` as that user was not checked",
			es: "Por la API binaria, `/container/print` devolvió todas las propiedades de todos los contenedores, `cmd` y `envlist` incluidas, a un usuario con solo `read,api`, el usuario `mikroscope`; solo se imprimieron los nombres de las propiedades, y no se comprobó si ese usuario lee los valores de `/container/envs`",
		},
		...onRB5009,
		date: "2026-09-11",
		fact: "/container/print over the binary API returns every container's cmd and envlist to a read,api user",
		source: "site/src/content/docs/security/api-user.mdx",
	},
	"test-policy": {
		title: {
			en: "Test policy for fetch and profile",
			es: "Política test para fetch y profile",
		},
		statement: {
			en: "`/tool fetch` and `/tool profile` both require the `test` policy",
			es: "`/tool fetch` y `/tool profile` exigen ambos la política `test`",
		},
		...onRB5009,
		date: "2026-09-11",
		fact: "/tool fetch and /tool profile both require the test policy",
		source:
			"site/src/content/docs/security/api-user.mdx (the fact); site/src/content/docs/install/reaching-the-agent.mdx (the version, for /tool fetch); date checked on the reference device, no published document records it",
	},
	"find-quoting": {
		title: {
			en: "Quoted values in find",
			es: "Valores entre comillas en find",
		},
		statement: {
			en: "In a RouterOS `find`, address and port attributes match only when their values are quoted",
			es: "En un `find` de RouterOS, los atributos de dirección y de puerto solo coinciden cuando su valor va entre comillas",
		},
		...onRB5009,
		date: "2026-09-11",
		fact: "in a RouterOS find, address and port attributes match only when quoted",
		source:
			"site/src/content/docs/security/installer.mdx (the fact); internal/router/router_test.go, TestFindsQuoteAddressesAndPorts (the fact, the version and the date)",
	},
	"expose-rules": {
		title: {
			en: "Expose rules reach the agent",
			es: "Las reglas de exposición llegan al agente",
		},
		statement: {
			en: "A `dst-nat` rule plus a forward `accept` rule reach the agent from the LAN, and both are removed by their tag",
			es: "Una regla `dst-nat` más una regla `accept` en forward llegan al agente desde la LAN, y las dos se retiran por su etiqueta",
		},
		...onRB5009,
		date: "2026-09-11",
		fact: "dst-nat plus forward accept reaches the agent from the LAN; both rules are removable by tag",
		source:
			"site/src/content/docs/security/expose.mdx (both rules, removed by uninstall); internal/router/router_test.go, the comment above fakeRunner (the date)",
	},
	"direct-lists": {
		title: {
			en: "LAN reaches the veth",
			es: "La LAN llega a la veth",
		},
		statement: {
			en: "The LAN reaches the veth once the veth joins the interface list `LAN` and the /30 joins the address list `LANs`",
			es: "La LAN llega a la veth en cuanto la veth entra en la lista de interfaces `LAN` y el /30 en la lista de direcciones `LANs`",
		},
		...onRB5009,
		date: "2026-09-11",
		fact: "the LAN reaches the veth once the veth joins LAN and the /30 joins LANs",
		source:
			"site/src/content/docs/install/firewall.mdx (the fact and the date); internal/router/steps.go, Plan",
	},
	"remove-race": {
		title: {
			en: "Container removal returns early",
			es: "El borrado del contenedor vuelve antes",
		},
		statement: {
			en: "`/container/remove` returns before the container is gone, and a `/file/remove` issued meanwhile does nothing, silently",
			es: "`/container/remove` vuelve antes de que el contenedor haya desaparecido, y un `/file/remove` lanzado mientras tanto no hace nada, sin avisar",
		},
		...onRB5009,
		date: "2026-09-11",
		fact: "/container/remove returns before the container is gone; a /file/remove issued meanwhile does nothing, silently",
		source:
			"site/src/content/docs/security/installer.mdx; internal/router/steps.go, containerStep",
	},
	"remote-image-change": {
		title: {
			en: "Removing the tar re-extracts",
			es: "Borrar el tar vuelve a extraer",
		},
		statement: {
			en: "With `ignore-remote-image-change=no`, removing the image tar makes RouterOS stop and remove the container and extract it again minutes later",
			es: "Con `ignore-remote-image-change=no`, borrar el tar de la imagen hace que RouterOS detenga y elimine el contenedor y lo vuelva a extraer minutos después",
		},
		...onRB5009,
		date: "2026-09-11",
		fact: "with ignore-remote-image-change=no, removing the tar makes RouterOS stop, remove and re-extract the container",
		source:
			"site/src/content/docs/security/index.mdx; internal/router/steps.go, containerStep",
	},
	"export-identical": {
		title: {
			en: "Round trip leaves /export unchanged",
			es: "La ida y vuelta deja /export intacto",
		},
		statement: {
			en: "`doctor`, `install`, `status`, `upgrade` and `uninstall`, run in that order, left the router's `/export` byte-identical, its `#` header lines aside, compared by hash in memory and never written to disk",
			es: "`doctor`, `install`, `status`, `upgrade` y `uninstall`, en ese orden, dejaron el `/export` del router idéntico byte a byte, salvo sus líneas de cabecera `#`, comparado por hash en memoria y sin escribirlo nunca a disco",
		},
		...onRB5009,
		date: "2026-09-12",
		fact: "doctor, install, status, upgrade, uninstall leaves /export byte-identical",
		source:
			"site/src/content/docs/install/index.mdx (the full round trip, the device and the version)",
	},
	"tmpfs-zero-writes": {
		title: {
			en: "tmpfs install writes no NAND",
			es: "Instalar en tmpfs no escribe en la NAND",
		},
		statement: {
			en: "A tmpfs disk holds the image tar and the container root with no NAND writes: `write-sect-since-reboot` stayed at 58 279 across install, run and removal",
			es: "Un disco tmpfs aloja el tar de la imagen y la raíz del contenedor sin escribir en la NAND: `write-sect-since-reboot` se quedó en 58 279 durante la instalación, el funcionamiento y la retirada",
		},
		...onRB5009,
		date: "2026-09-11",
		fact: "a tmpfs disk hosts the image tar and the container root with zero NAND writes: write-sect-since-reboot stayed at 58 279 across install, run and removal",
		source:
			"site/src/content/docs/install/layout.mdx (tmpfs adds nothing to the flash counters, and write-sect-since-reboot read 58 279 before the install and after the removal); site/src/content/docs/start/walkthrough.mdx (58 279 across install, run and removal)",
	},
	"privileged-namespaces": {
		title: {
			en: "Privileged keeps network and PID",
			es: "Privileged conserva red y PID",
		},
		statement: {
			en: "`privileged=yes` drops the container's user namespace but not its network or PID namespace",
			es: "`privileged=yes` quita el espacio de nombres de usuario del contenedor, pero no el de red ni el de PID",
		},
		...onRB5009,
		date: "2026-09-12",
		fact: "privileged=yes drops the container's user namespace but not its network or PID namespace",
		source:
			"site/src/content/docs/limits/privileged.mdx (user, network and PID); internal/router/steps.go, containerStep",
	},
	"remote-image-host": {
		title: {
			en: "Registry host in the reference",
			es: "Host del registro en la referencia",
		},
		statement: {
			en: "A registry host inside `remote-image=` overrides `/container/config registry-url`, and `docker.io` is pulled as `registry-1.docker.io`: with `registry-url=https://registry-1.docker.io`, a reference on `registry.invalid` was logged as `registry=registry.invalid` and failed with `resolving error`, while the agent's 1.2.2 image named as `docker.io/…` and as `registry-1.docker.io/…` was logged as `registry=registry-1.docker.io` and ended in `download/extract done`, its arm64 layer 2 799 648 bytes; `/container/config` read the same afterwards. The containers were created in a temporary veth, never started and removed again",
			es: "Un host de registro dentro de `remote-image=` manda sobre `/container/config registry-url`, y `docker.io` se descarga como `registry-1.docker.io`: con `registry-url=https://registry-1.docker.io`, una referencia en `registry.invalid` quedó registrada como `registry=registry.invalid` y falló con `resolving error`, mientras que la imagen 1.2.2 del agente escrita como `docker.io/…` y como `registry-1.docker.io/…` quedó registrada como `registry=registry-1.docker.io` y terminó en `download/extract done`, con su capa arm64 de 2 799 648 bytes; `/container/config` quedó igual que antes. Los contenedores se crearon en una veth temporal, nunca se arrancaron y se retiraron después",
		},
		...onRB5009Now,
		date: "2026-09-24",
		fact: "a registry host inside remote-image= overrides /container/config registry-url, and docker.io is pulled as registry-1.docker.io: with registry-url=https://registry-1.docker.io, remote-image=registry.invalid/jmrplens/mikroscope-agent:1.2.2 was logged as registry=registry.invalid and failed with resolving error, docker.io/… and registry-1.docker.io/… were both logged as registry=registry-1.docker.io and ended in download/extract done; /container/config was unchanged afterwards",
		source:
			"internal/router/options.go, RemoteRef (the three references and what the log said); site/src/content/docs/install/routes.mdx; containers created in a temporary veth, never started, then removed",
	},
	"docker-hub-anonymous": {
		title: {
			en: "Docker Hub pull without login",
			es: "Descarga de Docker Hub sin login",
		},
		statement: {
			en: "With the `/container/config` username and password cleared, RouterOS pulled the agent's 1.2.2 image from Docker Hub anonymously, both as `remote-image=registry-1.docker.io/…` and without a host through `registry-url=https://registry-1.docker.io`, each ending in `download/extract done` 5 s after the add; the configuration was then restored and verified identical",
			es: "Con el usuario y la contraseña de `/container/config` borrados, RouterOS descargó de Docker Hub la imagen 1.2.2 del agente de forma anónima, tanto como `remote-image=registry-1.docker.io/…` como sin host a través de `registry-url=https://registry-1.docker.io`, y las dos terminaron en `download/extract done` 5 s después del alta; después se restauró la configuración y se comprobó que era idéntica",
		},
		...onRB5009Now,
		date: "2026-09-24",
		fact: "with the /container/config username and password cleared, RouterOS pulled jmrplens/mikroscope-agent:1.2.2 from Docker Hub anonymously, both as remote-image=registry-1.docker.io/… and host-less through registry-url=https://registry-1.docker.io, each download/extract done 5 s after the add; the configuration was restored and verified identical",
		source:
			"internal/router/options.go, RemoteRef (the anonymous run); site/src/content/docs/install/routes.mdx",
	},
	// The facts below left the guides in PR3 (tool-docs-spec D4); each one's
	// `source` names the page and commit that stated it.
	"log-time-full-date": {
		title: {
			en: "Log times over the API",
			es: "Horas del log por la API",
		},
		statement: {
			en: "Over the API, `/log/print` returns an entry's `time` as a full date and time with no zone, `2026-09-12 02:21:24`, and accepts that format in a `?>time=` query",
			es: "Por la API, `/log/print` devuelve el `time` de una entrada como fecha y hora completas y sin zona, `2026-09-12 02:21:24`, y acepta ese formato en una consulta `?>time=`",
		},
		...onRB5009,
		date: "2026-09-12",
		fact: "over the API, /log/print returns time as a full YYYY-MM-DD hh:mm:ss date and takes that format in a ?>time= query",
		source:
			'internal/record/logmarkers.go, LogEntry.Time ("2026-09-12 02:21:24" (7.24.2 over the API)) and APITimeLayout; site/src/content/docs/record/index.mdx at dc0e354 ("RouterOS 7.24.2 prints a full date over the API")',
	},
	"monitor-traffic-keys": {
		title: {
			en: "Loss keys from monitor-traffic",
			es: "Claves de pérdidas de monitor-traffic",
		},
		statement: {
			en: "`/interface/monitor-traffic` returns `rx-drops`, `tx-drops` and `tx-queue-drops` per second and no error keys at all",
			es: "`/interface/monitor-traffic` devuelve `rx-drops`, `tx-drops` y `tx-queue-drops` por segundo y ninguna clave de errores",
		},
		...onRB5009,
		date: "2026-09-15",
		fact: "monitor-traffic returns rx-drops, tx-drops and tx-queue-drops and no rx-errors or tx-errors key",
		source:
			"site/src/content/docs/sinks/{api-tier,prometheus,other}.mdx, reference/metrics.mdx and dashboards/index.mdx at dc0e354",
	},
	"rx-overflow-port-counters": {
		title: {
			en: "rx-overflow only in port counters",
			es: "rx-overflow solo en los contadores de puerto",
		},
		statement: {
			en: "`ether1` had counted 652 364 `rx-overflow` events, and growing, in its port counters while `monitor-traffic` returned no error key for that port: that count reaches a consumer only through the port counters",
			es: "`ether1` llevaba 652 364 sucesos `rx-overflow`, en aumento, en sus contadores de puerto mientras `monitor-traffic` no devolvía ninguna clave de errores para ese puerto: esa cuenta solo llega a un consumidor por los contadores de puerto",
		},
		...onRB5009,
		date: "2026-09-15",
		fact: "rx-overflow on ether1 (652 364, growing) appears in the port counters and in no monitor-traffic key",
		source:
			"site/src/content/docs/sinks/api-tier.mdx at dc0e354, “A value that is absent”",
	},
	"port-planes": {
		title: {
			en: "Switch port and bridge planes",
			es: "Planos del puerto y del bridge",
		},
		statement: {
			en: "An `ether` port in a bridge counts its wire, frames the switch chip forwarded in hardware included, and the `bridge` counts its CPU side, so neither is a subset of the other: since the port's last counter reset `ether1` had received 255.8 GB on the wire (`rx-bytes`) and handed 29.7 GB of it to the CPU (`driver-rx-byte`)",
			es: "Un puerto `ether` de un bridge cuenta su cable, incluidas las tramas que el chip de conmutación reenvió por hardware, y el `bridge` cuenta su lado de CPU, así que ninguno es un subconjunto del otro: desde el último reinicio del contador del puerto, `ether1` había recibido 255,8 GB por el cable (`rx-bytes`) y había entregado 29,7 GB de ellos a la CPU (`driver-rx-byte`)",
		},
		...onRB5009,
		date: "2026-09-16",
		fact: "a switch port counts its wire including hardware-forwarded frames, the bridge its CPU side; ether1 255.8 GB rx-bytes against 29.7 GB driver-rx-byte",
		source:
			"site/src/content/docs/dashboards/index.mdx, sinks/{prometheus,api-tier}.mdx and reference/{metrics,measurements}.mdx at dc0e354; the same read is campaign counters-2026-09-16 in measurements.ts",
	},
	"fp-tx-zero": {
		title: {
			en: "fp-tx-byte stays at zero",
			es: "fp-tx-byte se queda a cero",
		},
		statement: {
			en: "`fp-tx-byte` read 0 on every interface after hundreds of GB transmitted, while `fp-rx-byte` counted; why RouterOS leaves it at 0 is not established",
			es: "`fp-tx-byte` marcaba 0 en todas las interfaces tras cientos de GB transmitidos, mientras `fp-rx-byte` sí contaba; no está establecido por qué RouterOS lo deja a 0",
		},
		...onRB5009,
		date: "2026-09-16",
		fact: "fp-tx-byte is 0 on every interface after hundreds of GB transmitted",
		source:
			"site/src/content/docs/sinks/{derive,influxdb}.mdx, reference/{metrics,measurements}.mdx and dashboards/index.mdx at dc0e354; internal/derive/derive.go",
	},
	"host-mounts": {
		title: {
			en: "Host paths in a privileged container",
			es: "Rutas del host en un contenedor privilegiado",
		},
		statement: {
			en: "A privileged container given the host's `/proc`, `/sys` and `/` as bind mounts read zero PIDs in the host's `/proc` and found no `class/net` in its `/sys`, so the namespaces held; the host's `/` did mount, and it exposed the RouterOS flash filesystem, configuration and files, secrets included. Run with the maintainer's consent",
			es: "Un contenedor privilegiado con `/proc`, `/sys` y `/` del host montados leyó cero PID en el `/proc` del host y no encontró `class/net` en su `/sys`, así que los espacios de nombres aguantaron; el `/` del host sí se montó, y expuso el sistema de ficheros de la flash de RouterOS, configuración y ficheros, secretos incluidos. Se hizo con el consentimiento del mantenedor",
		},
		...onRB5009,
		date: "2026-09-15",
		fact: "bind-mounting host /proc, /sys and / into a privileged container: /proc reads zero PIDs, /sys has no class/net, / exposes the flash filesystem with configuration and secrets",
		source:
			"site/src/content/docs/limits/namespaces.mdx, limits/privileged.mdx and security/index.mdx at dc0e354",
	},
} as const satisfies Record<string, Verification>;

export type VerificationId = keyof typeof verifications;

export const isVerificationId = (id: string): id is VerificationId =>
	Object.hasOwn(verifications, id);

/**
 * The site's own preview server, started on a port nobody holds and stopped
 * when the caller is done.
 *
 * Astro's preview has no strict-port flag: when the port it is given is taken
 * it prints "Port … is in use, trying another one" and serves somewhere else,
 * and a checker pointed at the first port then audits whatever happens to be
 * answering. That happened in ghchronicle on 2026-09-11, where nine pages
 * passed against another process and thirteen failed with connection refused.
 * So the port is never assumed: the URL the preview announces on its "Local"
 * line is the one the caller is given.
 *
 * Extracted from run-pa11y.mjs when check-layout.mjs needed the same thing.
 */
import { spawn } from "node:child_process";
import { createServer } from "node:net";
import process from "node:process";

const HOST = "127.0.0.1";
// Long enough for a cold start on a CI runner; the preview is ready in
// milliseconds once node is up.
const READY_TIMEOUT_MS = 60_000;
// Color codes, which Astro writes even into a pipe.
const ANSI = /\u001b\[[0-9;]*m/g;
const LOCAL = /Local\s+(http:\/\/\S+)/;

// freePort asks the kernel for a port nobody holds. Another process can still
// take it before the preview binds it, which is why the announced URL, not
// this number, is what pa11y is given.
function freePort() {
	return new Promise((resolvePort, reject) => {
		const server = createServer();
		server.once("error", reject);
		server.listen(0, HOST, () => {
			const { port } = server.address();
			server.close(() => resolvePort(port));
		});
	});
}

// startPreview runs the preview in a process group of its own, so stopping it
// stops the node process behind the pnpm wrapper too. Killing only the wrapper
// leaves the server listening.
function startPreview(port) {
	const child = spawn(
		"pnpm",
		[
			"exec",
			"astro",
			"preview",
			"--host",
			HOST,
			"--port",
			String(port),
			"--ignore-lock",
		],
		{
			detached: true,
			// Astro 7.2 moves the preview to the background when it thinks an
			// agent started it, and a background preview outlives this script.
			env: { ...process.env, ASTRO_PREVIEW_BACKGROUND: "0" },
			stdio: ["ignore", "pipe", "pipe"],
		},
	);
	const stop = () => {
		try {
			process.kill(-child.pid, "SIGTERM");
		} catch {
			// Already gone, which is the state stop() is for.
		}
	};
	const announced = new Promise((resolveURL, reject) => {
		let output = "";
		const timer = setTimeout(
			() =>
				reject(
					new Error(
						`the preview announced no URL within ${READY_TIMEOUT_MS / 1000}s:\n${output}`,
					),
				),
			READY_TIMEOUT_MS,
		);
		const read = (chunk) => {
			output += chunk.toString().replace(ANSI, "");
			const match = LOCAL.exec(output);
			if (match) {
				clearTimeout(timer);
				resolveURL(new URL(match[1]));
			}
		};
		child.stdout.on("data", read);
		child.stderr.on("data", read);
		child.once("exit", (code) => {
			clearTimeout(timer);
			reject(
				new Error(
					`the preview exited with ${code} before announcing a URL:\n${output}`,
				),
			);
		});
	});
	return { announced, stop };
}

/** Starts the preview, hands its origin to `fn`, and always stops it. */
export async function withPreview(fn) {
	const port = await freePort();
	const { announced, stop } = startPreview(port);
	try {
		const url = await announced;
		return await fn(url);
	} finally {
		stop();
	}
}

export { freePort, startPreview };

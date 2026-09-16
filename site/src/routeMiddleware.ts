/**
 * Starlight route middleware: the mark in the hero's image slot.
 *
 * `hero.image.file` renders an `<img>`, and the mark's two tones are painted
 * by the palette (`currentColor` and `--ms-mark-quiet`), which a stylesheet
 * cannot reach inside an image to do. Filling the slot here rather than as
 * `hero.image.html` in each index file keeps one copy of the markup instead of
 * one per locale. The layout rules live in chrome.css.
 *
 * Route middleware is Starlight's supported place to change route data. The
 * entry is replaced with a copy, never written into: `entry.data` is the
 * content collection's cached object, shared with every other reader of it,
 * and a mutation there would show them an image the frontmatter never
 * declared. A page that declares its own image keeps it.
 */
import { defineRouteMiddleware } from "@astrojs/starlight/route-data";
import mark from "./assets/mark-inline.svg?raw";

export const onRequest = defineRouteMiddleware((context) => {
	const route = context.locals.starlightRoute;
	const { hero } = route.entry.data;
	if (!hero || hero.image) return;
	route.entry = {
		...route.entry,
		data: {
			...route.entry.data,
			// aria-hidden, because the mark is decorative: it repeats the header's
			// mark and says nothing the page does not. This reaches every page with
			// a hero and no image, the 404 pages included. `alt: ""` is not a text
			// alternative: Starlight's hero schema transforms the `html` variant to
			// `{ html, alt: "" }`, so route data requires the key, and Hero.astro
			// renders `html` without reading it (Starlight 0.42.0).
			hero: {
				...hero,
				image: {
					html: `<span class="ms-hero-mark" aria-hidden="true">${mark}</span>`,
					alt: "",
				},
			},
		},
	};
});

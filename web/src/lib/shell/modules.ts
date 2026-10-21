// Loading a building block's UI: its entry.js registers the custom elements
// the manifest names. Each entry is loaded once.
import type { UIModule } from './core';

const loading = new Map<string, Promise<void>>();

export function loadUI(m: UIModule): Promise<void> {
	if (!m.hasUi || !m.entry || m.service) return Promise.reject(new Error(`${m.name} has no UI`));
	let p = loading.get(m.id);
	if (!p) {
		p = import(/* @vite-ignore */ m.entry).then(() => undefined);
		p.catch(() => loading.delete(m.id));
		loading.set(m.id, p);
	}
	return p;
}

/** whenDefined waits until a custom element is registered, at most ms. */
export async function whenDefined(tag: string, ms = 5000): Promise<boolean> {
	if (customElements.get(tag)) return true;
	return Promise.race([
		customElements.whenDefined(tag).then(() => true),
		new Promise<boolean>((resolve) => setTimeout(() => resolve(false), ms))
	]);
}

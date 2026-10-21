// The admin session: an HttpOnly cookie the core sets on login. The shell
// only asks whether there is one; no key is kept in the browser.
import { writable } from 'svelte/store';
import { core, type Manifest } from './core';

export const authenticated = writable<boolean | null>(null);
export const manifest = writable<Manifest | null>(null);

export async function checkSession(): Promise<boolean> {
	try {
		const s = await core.get<{ authenticated: boolean }>('session');
		authenticated.set(s.authenticated);
		return s.authenticated;
	} catch {
		authenticated.set(false);
		return false;
	}
}

export async function login(key: string): Promise<boolean> {
	try {
		await core.post('login', { key });
		authenticated.set(true);
		return true;
	} catch {
		return false;
	}
}

export async function logout(): Promise<void> {
	try {
		await core.post('logout');
	} finally {
		authenticated.set(false);
		manifest.set(null);
	}
}

export async function loadManifest(): Promise<Manifest> {
	const m = await core.get<Manifest>('ui');
	manifest.set(m);
	return m;
}

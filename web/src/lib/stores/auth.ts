import { writable } from 'svelte/store';
import { api } from '$lib/api';
import { browser } from '$app/environment';

export const isAuthenticated = writable<boolean>(false);
export const isLoading = writable<boolean>(true);

export async function checkAuth(): Promise<void> {
	if (!browser) return;

	isLoading.set(true);

	const key = api.loadApiKey();
	if (key) {
		const valid = await api.verifyAuth();
		isAuthenticated.set(valid);
		if (!valid) {
			api.clearApiKey();
		}
	} else {
		isAuthenticated.set(false);
	}

	isLoading.set(false);
}

export async function login(apiKey: string): Promise<boolean> {
	api.setApiKey(apiKey);
	const valid = await api.verifyAuth();
	isAuthenticated.set(valid);

	if (!valid) {
		api.clearApiKey();
	}

	return valid;
}

export function logout(): void {
	api.clearApiKey();
	isAuthenticated.set(false);
}

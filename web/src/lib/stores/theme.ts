import { writable } from 'svelte/store';
import { browser } from '$app/environment';

export type Theme = 'light' | 'dark';

const STORAGE_KEY = 'theme';

function initial(): Theme {
	if (!browser) return 'light';
	const saved = localStorage.getItem(STORAGE_KEY);
	if (saved === 'light' || saved === 'dark') return saved;
	if (window.matchMedia('(prefers-color-scheme: dark)').matches) return 'dark';
	return 'light';
}

function apply(value: Theme) {
	if (!browser) return;
	const root = document.documentElement;
	if (value === 'dark') root.classList.add('dark');
	else root.classList.remove('dark');
}

export const theme = writable<Theme>(initial());

if (browser) {
	theme.subscribe((value) => {
		apply(value);
		localStorage.setItem(STORAGE_KEY, value);
	});
}

export function toggleTheme(): void {
	theme.update((t) => (t === 'dark' ? 'light' : 'dark'));
}

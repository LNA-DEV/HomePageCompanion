import { writable } from 'svelte/store';
import type { ToastKind } from '@companion/ui';

export interface Toast {
	id: number;
	message: string;
	kind: ToastKind;
}

export const toasts = writable<Toast[]>([]);
let next = 1;

export function toast(message: string, kind: ToastKind = 'info'): void {
	const id = next++;
	toasts.update((list) => [...list, { id, message, kind }]);
	setTimeout(() => toasts.update((list) => list.filter((t) => t.id !== id)), kind === 'error' ? 8000 : 4000);
}

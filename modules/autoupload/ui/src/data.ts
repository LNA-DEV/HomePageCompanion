import { connect } from '@companion/ui';
import type { Dashboard } from './types';

export const au = connect('autoupload');

// The widgets share one dashboard request.
let cached: { at: number; p: Promise<Dashboard> } | null = null;

export function dashboard(): Promise<Dashboard> {
	if (!cached || Date.now() - cached.at > 5000) {
		cached = { at: Date.now(), p: au.api.get<Dashboard>('dashboard') };
	}
	return cached.p;
}

export function short(time?: string): string {
	if (!time) return '';
	const d = new Date(time);
	const now = new Date();
	if (d.toDateString() === now.toDateString()) return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
	return d.toLocaleDateString([], { day: 'numeric', month: 'short' });
}

export function targetName(id: string): string {
	const [platform, name] = id.split(':');
	return name && name !== 'default' ? `${platform} · ${name}` : platform;
}

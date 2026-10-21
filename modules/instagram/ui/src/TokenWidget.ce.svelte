<svelte:options customElement={{ tag: 'instagram-token-widget', shadow: 'none' }} />

<script lang="ts">
	// The Instagram token of each account: refreshed every week, it expires
	// after 60 days without one (lib/metatoken).
	import { onMount } from 'svelte';
	import { connect, formatDate, timeAgo } from '@companion/ui';

	interface Status {
		instance: string;
		status: 'ok' | 'expiring' | 'expired' | 'unknown';
		source?: string;
		refreshedAt?: string;
		expiresAt?: string;
		daysLeft?: number;
		lastAttemptAt?: string;
		lastError?: string;
	}

	const mod = connect('instagram');
	let tokens = $state<Status[] | null>(null);
	let error = $state('');
	let busy = $state('');

	const color = {
		ok: 'text-emerald-700 dark:text-emerald-300',
		expiring: 'text-amber-700 dark:text-amber-300',
		expired: 'text-red-700 dark:text-red-300',
		unknown: 'text-gray-600 dark:text-gray-300'
	};

	async function load() {
		try {
			tokens = await mod.api.get<Status[]>('tokens');
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	}

	async function refresh(t: Status) {
		busy = t.instance;
		try {
			const s = await mod.api.post<Status>('tokens/refresh', { instance: t.instance });
			if (s.lastError) mod.toast(`Instagram ${t.instance}: ${s.lastError}`, 'error');
			else mod.toast(`Instagram ${t.instance}: token refreshed`, 'success');
			await load();
		} catch (e) {
			mod.toast(e instanceof Error ? e.message : String(e), 'error');
		}
		busy = '';
	}

	onMount(load);
</script>

{#if error}
	<p class="text-sm text-red-700 dark:text-red-300">{error}</p>
{:else if !tokens}
	<p class="text-sm text-gray-500">Loading…</p>
{:else}
	<ul class="space-y-3 text-sm" data-testid="instagram-tokens">
		{#each tokens as t (t.instance)}
			<li>
				<div class="flex items-center justify-between gap-2">
					<span class="font-medium">{t.instance}</span>
					<span class={color[t.status] ?? ''}>
						{#if t.status === 'unknown'}not refreshed yet{:else if t.daysLeft !== undefined}{t.status === 'expired' ? 'expired' : `${t.daysLeft} days left`}{:else}{t.status}{/if}
					</span>
				</div>
				<p class="text-xs text-gray-500 dark:text-gray-400 mt-0.5">
					{#if t.refreshedAt}refreshed {timeAgo(t.refreshedAt)}{:else}from the config{/if}
					{#if t.expiresAt} · expires {formatDate(t.expiresAt, false)}{/if}
				</p>
				{#if t.lastError}<p class="text-xs text-red-700 dark:text-red-300 mt-0.5 break-words">{t.lastError}</p>{/if}
				<button class="mt-1 text-xs text-primary-700 dark:text-primary-300 hover:underline disabled:opacity-50" disabled={busy !== ''} onclick={() => refresh(t)}>
					{busy === t.instance ? 'Refreshing…' : 'Refresh now'}
				</button>
			</li>
		{:else}
			<li class="text-gray-500">No account configured.</li>
		{/each}
	</ul>
{/if}

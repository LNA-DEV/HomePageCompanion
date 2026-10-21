<svelte:options customElement={{ tag: 'webpush-broadcast', shadow: 'none' }} />

<script lang="ts">
	// Broadcast: one notification to every subscriber of an audience; the
	// answer says how many devices it reached.
	import { onMount } from 'svelte';
	import { PageHeader } from '@companion/ui/components';
	import { wp, audiences, type Audience } from './audiences';

	let groups = $state<Audience[]>([]);
	let audience = $state('default');
	let title = $state('');
	let body = $state('');
	let url = $state('');
	let icon = $state('');
	let sending = $state(false);
	let result = $state<{ sent: number; failed: number } | null>(null);
	let error = $state('');

	const count = $derived(groups.find((g) => g.name === audience)?.subscribers ?? 0);

	onMount(async () => {
		try {
			groups = await audiences();
			if (groups.length && !groups.some((g) => g.name === audience)) audience = groups[0].name;
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	});

	async function send(e: Event) {
		e.preventDefault();
		sending = true;
		error = '';
		result = null;
		try {
			result = await wp.api.post(`audiences/${encodeURIComponent(audience)}/broadcast`, { title, body, url: url || undefined, icon: icon || undefined });
			title = body = url = icon = '';
			groups = await audiences();
		} catch (err) {
			error = err instanceof Error ? err.message : String(err);
		}
		sending = false;
	}
</script>

<PageHeader title="Broadcast" description="Send a push notification to every subscriber" />

<form class="card max-w-2xl space-y-5" onsubmit={send}>
	{#if groups.length > 1}
		<div>
			<label for="wp-aud" class="label">Audience</label>
			<select id="wp-aud" class="input" bind:value={audience}>
				{#each groups as g (g.name)}<option value={g.name}>{g.name === 'default' ? 'Website' : g.name} ({g.subscribers})</option>{/each}
			</select>
		</div>
	{/if}
	<div>
		<label for="wp-title" class="label">Title</label>
		<input id="wp-title" class="input" bind:value={title} required disabled={sending} />
	</div>
	<div>
		<label for="wp-body" class="label">Text</label>
		<textarea id="wp-body" class="input min-h-[100px]" bind:value={body} required disabled={sending}></textarea>
	</div>
	<div>
		<label for="wp-url" class="label">Link (optional)</label>
		<input id="wp-url" type="url" class="input" bind:value={url} placeholder="https://" disabled={sending} />
	</div>
	<div>
		<label for="wp-icon" class="label">Icon URL (optional)</label>
		<input id="wp-icon" type="url" class="input" bind:value={icon} placeholder="https://" disabled={sending} />
	</div>
	{#if result}
		<div class="p-3 rounded-lg {result.failed ? 'bg-amber-50 text-amber-800 dark:bg-amber-900/30 dark:text-amber-200' : 'bg-emerald-50 text-emerald-800 dark:bg-emerald-900/30 dark:text-emerald-200'}" data-testid="broadcast-result">
			Reached {result.sent} {result.sent === 1 ? 'device' : 'devices'}{#if result.failed}, {result.failed} failed (expired subscriptions are removed){/if}.
		</div>
	{/if}
	{#if error}<div class="p-3 rounded-lg bg-red-50 text-red-700 dark:bg-red-900/30 dark:text-red-300">{error}</div>{/if}
	<div class="flex items-center justify-between gap-3">
		<span class="text-sm text-gray-500 dark:text-gray-400">{count} {count === 1 ? 'subscriber' : 'subscribers'}</span>
		<button class="btn-primary" type="submit" disabled={sending || !title || !body}>{sending ? 'Sending…' : 'Send'}</button>
	</div>
</form>

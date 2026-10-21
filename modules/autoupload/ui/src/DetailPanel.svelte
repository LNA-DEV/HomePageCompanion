<script lang="ts">
	// A post on every target: status, likes with their history, a preview
	// as the network will show it, and the actions (autoposting-ui.md §4.2).
	import { formatDate, stripHtml } from '@companion/ui';
	import { X, ExternalLink } from 'lucide-svelte';
	import CellView from './CellView.svelte';
	import { au, targetName } from './data';
	import type { ItemDetail, Rendered, Run } from './types';

	let { id, onClose, onChanged }: { id: string; onClose: () => void; onChanged: () => void } = $props();

	let detail = $state<ItemDetail | null>(null);
	let error = $state('');
	let previews = $state<Record<string, Rendered | { error: string } | 'loading'>>({});
	let busy = $state('');

	async function load() {
		error = '';
		try {
			detail = await au.api.get<ItemDetail>(`items/${encodeURIComponent(id)}`);
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	}

	$effect(() => {
		id;
		detail = null;
		previews = {};
		load();
	});

	function days(target: string) {
		return (detail?.likeDays ?? []).filter((d) => d.target === target).slice(-30);
	}

	function sparkline(target: string): string {
		const pts = days(target);
		if (pts.length < 2) return '';
		const max = Math.max(1, ...pts.map((p) => p.likes));
		return pts.map((p, i) => `${(i / (pts.length - 1)) * 100},${20 - (p.likes / max) * 18}`).join(' ');
	}

	async function preview(target: string) {
		previews[target] = 'loading';
		try {
			previews[target] = await au.api.get<Rendered>(`items/${encodeURIComponent(id)}/preview`, { target });
		} catch (e) {
			previews[target] = { error: e instanceof Error ? e.message : String(e) };
		}
	}

	async function act(target: string, action: string, body?: unknown) {
		busy = target + action;
		try {
			const r = await au.api.post<Run | { status?: string }>(`items/${encodeURIComponent(id)}/targets/${encodeURIComponent(target)}/${action}`, body);
			if (action === 'publish-now') {
				const run = r as Run;
				if (run.result === 'published') au.toast(`Published on ${targetName(target)}`, 'success');
				else au.toast(`${targetName(target)}: ${run.result}${run.error ? ' – ' + run.error : ''}`, run.result === 'failed' ? 'error' : 'info');
			} else {
				au.toast(`${targetName(target)}: done`, 'success');
			}
			await load();
			onChanged();
		} catch (e) {
			au.toast(`${targetName(target)}: ${e instanceof Error ? e.message : e}`, 'error');
		}
		busy = '';
	}

	function markPublished(target: string) {
		const url = prompt('URL of the post on ' + targetName(target) + ' (optional):', '');
		if (url === null) return;
		act(target, 'mark-published', { url });
	}

	function allowRepost(target: string) {
		if (
			!confirm(
				`Allow reposting on ${targetName(target)}?\n\nThe record of this publication is removed, so the post will be published there AGAIN by the next run that chooses it. The post on the network itself is not deleted.`
			)
		)
			return;
		act(target, 'allow-repost', { confirm: true });
	}
</script>

<div class="fixed inset-0 z-40 flex justify-end" role="dialog" aria-modal="true" aria-label="Post detail">
	<button class="absolute inset-0 bg-black/30" onclick={onClose} aria-label="Close"></button>
	<div class="relative w-full max-w-xl h-full overflow-y-auto bg-white dark:bg-gray-900 shadow-xl p-6" data-testid="detail-panel">
		<button class="absolute top-4 right-4 p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-800" onclick={onClose} aria-label="Close panel"><X size={20} /></button>
		{#if error}
			<p class="text-red-700 dark:text-red-300">{error}</p>
		{:else if !detail}
			<p class="text-gray-500">Loading…</p>
		{:else}
			{@const p = detail.post}
			{#if p.imageUrl}
				<img src={p.imageUrl} alt="" class="w-full max-h-72 object-cover rounded-lg mb-4" />
			{/if}
			<h2 class="text-xl font-semibold pr-8">{p.title || p.id}</h2>
			<p class="text-sm text-gray-500 dark:text-gray-400 mt-1">
				{p.kind === 'photo' ? 'Photo' : p.kind === 'article' ? 'Blog' : 'Microblog'} · {formatDate(p.published, false)} · {p.source}
				{#if !p.inFeed}<span class="badge bg-amber-100 text-amber-800 dark:bg-amber-900/40 dark:text-amber-200 ml-1">no longer in feed</span>{/if}
			</p>
			{#if p.summary}<p class="mt-2 text-sm text-gray-700 dark:text-gray-300">{stripHtml(p.summary).slice(0, 280)}</p>{/if}
			{#if p.link}<a class="mt-2 inline-flex items-center gap-1 text-sm text-primary-700 dark:text-primary-300 hover:underline" href={p.link} target="_blank" rel="noopener">{p.link} <ExternalLink size={14} /></a>{/if}
			<p class="mt-3 text-sm">{p.likes} platform likes · {p.websiteLikes} website likes</p>

			<div class="mt-6 space-y-4">
				{#each detail.columns as col}
					{@const cell = p.cells[col.id]}
					{#if cell && cell.state !== 'none'}
						{@const line = sparkline(col.id)}
						{@const pv = previews[col.id]}
						<section class="border border-gray-200 dark:border-gray-800 rounded-lg p-4" data-testid="target-card">
							<div class="flex items-center justify-between gap-3">
								<h3 class="font-medium">{targetName(col.id)}</h3>
								<CellView {cell} />
							</div>
							{#if cell.time}<p class="text-xs text-gray-500 mt-1">{formatDate(cell.time)}</p>{/if}
							{#if cell.state === 'failed'}<p class="text-xs text-red-700 dark:text-red-300 mt-1">{cell.cause}: {cell.reason}</p>{/if}
							{#if line}
								<svg viewBox="0 0 100 20" class="w-full h-8 mt-2 text-emerald-500" preserveAspectRatio="none" aria-label="likes over time">
									<polyline points={line} fill="none" stroke="currentColor" stroke-width="1.5" vector-effect="non-scaling-stroke" />
								</svg>
							{/if}
							{#if p.source === 'microblog'}
								<p class="text-xs text-gray-500 mt-3">
									Written in the microblog; retry or delete it
									<a class="underline" href="/b/microblog?q={encodeURIComponent(p.id)}">there</a>.
								</p>
							{:else}
								<div class="flex flex-wrap gap-2 mt-3">
									{#if cell.state !== 'published'}
										<button class="btn-primary btn-sm" disabled={busy !== ''} onclick={() => act(col.id, 'publish-now')}>{busy === col.id + 'publish-now' ? 'Posting…' : 'Post now'}</button>
										{#if cell.state !== 'skipped'}
											<button class="btn-secondary btn-sm" disabled={busy !== ''} onclick={() => act(col.id, 'skip')}>Skip for this target</button>
										{/if}
										<button class="btn-secondary btn-sm" disabled={busy !== ''} onclick={() => markPublished(col.id)}>Mark as posted</button>
									{/if}
									{#if cell.state === 'published' || cell.state === 'skipped'}
										<button class="btn-secondary btn-sm" disabled={busy !== ''} onclick={() => allowRepost(col.id)}>Allow reposting</button>
									{/if}
									<button class="btn-secondary btn-sm" onclick={() => preview(col.id)}>Preview</button>
								</div>
								{#if pv === 'loading'}
									<p class="text-sm text-gray-500 mt-3">Rendering…</p>
								{:else if pv && 'error' in pv}
									<p class="text-sm text-red-700 dark:text-red-300 mt-3">{pv.error}</p>
								{:else if pv}
									<div class="mt-3 rounded-lg bg-gray-50 dark:bg-gray-800 p-3 text-sm" data-testid="preview">
										<p class="whitespace-pre-wrap">{pv.text}</p>
										{#if pv.images?.length}
											<div class="flex gap-2 mt-2">
												{#each pv.images as im}<img src={im.url} alt={im.alt ?? ''} title={im.alt} class="h-16 w-16 object-cover rounded" />{/each}
											</div>
										{/if}
										{#if pv.link}
											<div class="mt-2 border border-gray-200 dark:border-gray-700 rounded p-2">
												<p class="font-medium">{pv.link.title ?? pv.link.url}</p>
												{#if pv.link.description}<p class="text-xs text-gray-500">{pv.link.description}</p>{/if}
											</div>
										{/if}
										{#each pv.warnings ?? [] as w}<p class="mt-1 text-xs text-amber-700 dark:text-amber-300">⚠ {w}</p>{/each}
									</div>
								{/if}
							{/if}
						</section>
					{/if}
				{/each}
			</div>

			{#if detail.attempts?.length}
				<h3 class="mt-8 mb-2 font-semibold">History</h3>
				<ul class="text-sm divide-y divide-gray-100 dark:divide-gray-800">
					{#each detail.attempts as a}
						<li class="py-1.5 flex gap-3">
							<span class={a.success ? 'text-emerald-600' : a.resolvedAt ? 'text-gray-400' : 'text-red-600'}>{a.success ? '✓' : '✕'}</span>
							<span class="text-gray-500 w-36 shrink-0">{formatDate(a.createdAt)}</span>
							<span class="w-32 shrink-0">{targetName(a.target)}</span>
							<span class="truncate" title={a.error}>{a.success ? (a.source === 'microblog' ? 'microblog' : 'published') : (a.error ?? '')}{a.resolvedAt ? ' (resolved)' : ''}</span>
						</li>
					{/each}
				</ul>
			{/if}
		{/if}
	</div>
</div>

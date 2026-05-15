<script lang="ts">
	import { onMount } from 'svelte';
	import { api, type DashboardStats, type TargetHealth } from '$lib/api';
	import { PageHeader, StatsCard, Loading, PlatformBadge } from '$lib/components';
	import { CheckCircle2, AlertTriangle, XCircle, HelpCircle } from 'lucide-svelte';

	let stats = $state<DashboardStats | null>(null);
	let healths = $state<TargetHealth[]>([]);
	let error = $state('');
	let loading = $state(true);

	let actionLoading = $state<'' | 'interactions' | 'backfill'>('');
	let actionMessage = $state<{ kind: 'success' | 'error'; text: string } | null>(null);

	async function runAction(kind: 'interactions' | 'backfill') {
		const label = kind === 'interactions' ? 'fetch interactions now' : 'run backfill';
		if (!confirm(`Are you sure you want to ${label}?`)) return;
		actionLoading = kind;
		actionMessage = null;
		try {
			if (kind === 'interactions') {
				await api.triggerInteractionsFetch();
				actionMessage = { kind: 'success', text: 'Interactions fetch triggered.' };
			} else {
				await api.triggerBackfill();
				actionMessage = { kind: 'success', text: 'Backfill started.' };
			}
		} catch (e) {
			actionMessage = { kind: 'error', text: `Failed to ${label}.` };
		}
		actionLoading = '';
		setTimeout(() => (actionMessage = null), 5000);
	}

	function relTime(iso: string | undefined): string {
		if (!iso) return '';
		const t = new Date(iso).getTime();
		if (!t) return '';
		const diff = (Date.now() - t) / 1000;
		if (diff < 60) return 'just now';
		if (diff < 3600) return `${Math.floor(diff / 60)} min ago`;
		if (diff < 86400) return `${Math.floor(diff / 3600)} h ago`;
		return `${Math.floor(diff / 86400)} d ago`;
	}

	const statusMeta = {
		healthy: {
			label: 'Healthy',
			dot: 'bg-emerald-500',
			text: 'text-emerald-700 dark:text-emerald-400',
			Icon: CheckCircle2
		},
		degraded: {
			label: 'Degraded',
			dot: 'bg-amber-500',
			text: 'text-amber-700 dark:text-amber-400',
			Icon: AlertTriangle
		},
		down: {
			label: 'Down',
			dot: 'bg-red-500',
			text: 'text-red-700 dark:text-red-400',
			Icon: XCircle
		},
		unknown: {
			label: 'No attempts yet',
			dot: 'bg-gray-400',
			text: 'text-gray-600 dark:text-gray-400',
			Icon: HelpCircle
		}
	} as const;

	onMount(async () => {
		try {
			const [s, h] = await Promise.all([api.getStats(), api.getTargetHealth()]);
			stats = s;
			healths = h ?? [];
		} catch (e) {
			error = 'Failed to load dashboard statistics';
		}
		loading = false;
	});
</script>

<PageHeader title="Dashboard" description="Overview of your HomePageCompanion instance" />

{#if loading}
	<Loading message="Loading statistics..." />
{:else if error}
	<div class="card">
		<p class="text-red-600 dark:text-red-400">{error}</p>
	</div>
{:else if stats}
	<div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-6 mb-8">
		<StatsCard title="Feeds" value={stats.feedCount} icon="R" color="blue" />
		<StatsCard title="Feed Items" value={stats.feedItemCount} icon="I" color="purple" />
		<StatsCard title="Publications" value={stats.publicationCount} icon="P" color="green" />
		<StatsCard title="Total Likes" value={stats.totalLikes} icon="L" color="red" />
	</div>

	<div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-6 mb-8">
		<StatsCard title="Subscribers" value={stats.subscriberCount} icon="S" color="orange" />
		<StatsCard title="Webmentions" value={stats.webmentionCount} icon="W" color="purple" />
		<StatsCard title="Native Likes" value={stats.nativeLikeCount} icon="N" color="green" />
		<StatsCard title="Connections" value={stats.connectionCount} icon="C" color="blue" />
	</div>

	{#if healths.length > 0}
		<div class="card mb-6">
			<div class="flex items-center justify-between mb-4">
				<h2 class="text-xl font-semibold">Target Health</h2>
				<a href="/uploads" class="text-sm text-primary-600 dark:text-primary-400 hover:underline">
					View all uploads →
				</a>
			</div>
			<div class="space-y-3">
				{#each healths as h}
					{@const meta = statusMeta[h.status] ?? statusMeta.unknown}
					{@const Icon = meta.Icon}
					<div class="flex items-start gap-3 p-3 rounded-lg border border-gray-200 dark:border-gray-700">
						<span class="w-2.5 h-2.5 rounded-full {meta.dot} mt-2 flex-shrink-0" aria-hidden="true"></span>
						<div class="flex-1 min-w-0">
							<div class="flex items-center gap-2 flex-wrap">
								<span class="font-medium">{h.name}</span>
								<PlatformBadge platform={h.platform} />
								<span class="inline-flex items-center gap-1 text-sm font-medium {meta.text}">
									<Icon size={14} />
									{meta.label}
								</span>
							</div>
							{#if h.lastError}
								<p class="text-sm text-red-700 dark:text-red-400 mt-1 break-words">
									{h.lastErrorCode ? h.lastErrorCode + ' · ' : ''}{h.lastError}
								</p>
							{/if}
							<div class="flex flex-wrap items-center gap-3 mt-2 text-xs text-gray-500 dark:text-gray-400">
								{#if h.lastAttemptAt}
									<span>Last attempt {relTime(h.lastAttemptAt)}</span>
								{/if}
								{#if h.lastSuccessAt}
									<span>Last success {relTime(h.lastSuccessAt)}</span>
								{/if}
								{#if h.recentFailures > 0}
									<span class="text-red-600 dark:text-red-400">{h.recentFailures} fail{h.recentFailures === 1 ? '' : 's'} (24h)</span>
								{/if}
							</div>
						</div>
					</div>
				{/each}
			</div>
		</div>
	{/if}

	<div class="grid grid-cols-1 lg:grid-cols-2 gap-6">
		<div class="card">
			<h2 class="text-xl font-semibold mb-4">Platform Breakdown</h2>
			{#if Object.keys(stats.platformBreakdown).length > 0}
				<div class="space-y-3">
					{#each Object.entries(stats.platformBreakdown) as [platform, count]}
						<div class="flex items-center justify-between">
							<span class="capitalize font-medium text-gray-700 dark:text-gray-300">{platform}</span>
							<span class="bg-gray-100 dark:bg-gray-800 dark:text-gray-200 px-3 py-1 rounded-full text-sm font-mono">
								{count.toLocaleString()} publications
							</span>
						</div>
					{/each}
				</div>
			{:else}
				<p class="text-gray-500 dark:text-gray-400">No publications yet</p>
			{/if}
		</div>

		<div class="card">
			<h2 class="text-xl font-semibold mb-4">Quick Actions</h2>
			<div class="space-y-3">
				<a
					href="/feeds"
					class="block p-3 bg-gray-50 dark:bg-gray-800 rounded-lg hover:bg-gray-100 dark:hover:bg-gray-700 transition-colors"
				>
					<span class="font-medium">Manage Feeds</span>
					<p class="text-sm text-gray-500 dark:text-gray-400 mt-1">View and manage RSS feed sources</p>
				</a>
				<a
					href="/broadcast"
					class="block p-3 bg-gray-50 dark:bg-gray-800 rounded-lg hover:bg-gray-100 dark:hover:bg-gray-700 transition-colors"
				>
					<span class="font-medium">Send Notification</span>
					<p class="text-sm text-gray-500 dark:text-gray-400 mt-1">Broadcast a push notification to subscribers</p>
				</a>
				<a
					href="/interactions"
					class="block p-3 bg-gray-50 dark:bg-gray-800 rounded-lg hover:bg-gray-100 dark:hover:bg-gray-700 transition-colors"
				>
					<span class="font-medium">View Interactions</span>
					<p class="text-sm text-gray-500 dark:text-gray-400 mt-1">See engagement metrics across platforms</p>
				</a>
				<button
					type="button"
					onclick={() => runAction('interactions')}
					disabled={actionLoading !== ''}
					class="w-full text-left block p-3 bg-gray-50 dark:bg-gray-800 rounded-lg hover:bg-gray-100 dark:hover:bg-gray-700 transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
				>
					<span class="font-medium">
						{actionLoading === 'interactions' ? 'Fetching…' : 'Fetch Interactions Now'}
					</span>
					<p class="text-sm text-gray-500 dark:text-gray-400 mt-1">Pulls the latest like counts in the background, paced to avoid rate limits</p>
				</button>
				<button
					type="button"
					onclick={() => runAction('backfill')}
					disabled={actionLoading !== ''}
					class="w-full text-left block p-3 bg-gray-50 dark:bg-gray-800 rounded-lg hover:bg-gray-100 dark:hover:bg-gray-700 transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
				>
					<span class="font-medium">
						{actionLoading === 'backfill' ? 'Running…' : 'Run Backfill'}
					</span>
					<p class="text-sm text-gray-500 dark:text-gray-400 mt-1">Re-process historical feed items and publications</p>
				</button>

				{#if actionMessage}
					<div
						class="p-3 rounded-lg {actionMessage.kind === 'success'
							? 'bg-green-50 text-green-700 dark:bg-green-900/30 dark:text-green-300'
							: 'bg-red-50 text-red-700 dark:bg-red-900/30 dark:text-red-300'}"
					>
						{actionMessage.text}
					</div>
				{/if}
			</div>
		</div>
	</div>
{/if}

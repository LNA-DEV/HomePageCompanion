<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/stores';
	import {
		api,
		type PublicMicroblogPost,
		type MicroblogComment
	} from '$lib/api';
	import { PageHeader, Loading, PlatformBadge } from '$lib/components';
	import { stripHtml } from '$lib/utils';
	import { ExternalLink, Heart, MessageSquare, RefreshCw } from 'lucide-svelte';

	let post = $state<PublicMicroblogPost | null>(null);
	let comments = $state<MicroblogComment[]>([]);
	let loading = $state(true);
	let error = $state('');
	let refreshing = $state(false);
	let refreshMessage = $state('');

	const slug = $derived($page.params.slug ?? '');

	onMount(async () => {
		if (!slug) {
			error = 'Missing slug';
			loading = false;
			return;
		}
		try {
			const [p, c] = await Promise.all([
				api.getPublicMicroblogPost(slug),
				api.getMicroblogComments(slug)
			]);
			post = p;
			comments = c;
		} catch {
			error = 'Failed to load post';
		}
		loading = false;
	});

	function formatTime(s: string): string {
		try {
			return new Date(s).toLocaleString();
		} catch {
			return s;
		}
	}

	async function refreshReplies() {
		if (!post || refreshing) return;
		refreshing = true;
		refreshMessage = '';
		try {
			const updated = await api.refreshMicroblogPost(post.id);
			post = updated;
			comments = await api.getMicroblogComments(slug);
		} catch (e) {
			const msg = e instanceof Error ? e.message : String(e);
			if (msg.includes('409')) {
				refreshMessage = 'Another refresh is running — try again in a moment.';
			} else {
				refreshMessage = 'Refresh failed: ' + msg;
			}
		}
		refreshing = false;
	}
</script>

{#if loading}
	<Loading message="Loading post..." />
{:else if error}
	<div class="card">
		<p class="text-red-600 dark:text-red-400">{error}</p>
		<a href="/microblog" class="text-primary-600 dark:text-primary-400 hover:underline mt-2 inline-block">Back to microblog</a>
	</div>
{:else if post}
	<PageHeader title="Microblog post">
		{#snippet actions()}
			<a href="/microblog" class="btn-secondary">Back</a>
		{/snippet}
	</PageHeader>

	<div class="card mb-6">
		<div class="text-xs text-gray-500 dark:text-gray-400 mb-2">{post.slug} · {formatTime(post.createdAt)}</div>
		{#if post.contentWarning}
			<details>
				<summary class="cursor-pointer text-sm text-amber-700 dark:text-amber-400">
					CW: {post.contentWarning}
				</summary>
				<p class="mt-2 whitespace-pre-wrap">{post.body}</p>
			</details>
		{:else}
			<p class="whitespace-pre-wrap">{post.body}</p>
		{/if}
		{#if post.imageUrl}
			<img
				src={post.imageUrl}
				alt={post.imageAltText || ''}
				class="mt-3 max-h-96 rounded-lg object-cover"
			/>
		{/if}

		<div class="flex flex-wrap items-center gap-3 mt-4 text-sm">
			<span class="inline-flex items-center gap-1 text-rose-600 dark:text-rose-400">
				<Heart size={14} />
				{post.likeCount}
			</span>
			<span class="inline-flex items-center gap-1 text-gray-600 dark:text-gray-400">
				<MessageSquare size={14} />
				{post.commentCount}
			</span>
			{#each post.publications as pub}
				<a
					href={pub.postUrl}
					target="_blank"
					rel="noopener noreferrer"
					class="inline-flex items-center gap-1 text-primary-700 dark:text-primary-300 hover:underline"
				>
					<PlatformBadge platform={pub.platform} showLabel={false} />
					{pub.targetName}
					<ExternalLink size={10} />
				</a>
			{/each}
		</div>
	</div>

	<div class="card">
		<div class="flex items-center justify-between mb-4 gap-3 flex-wrap">
			<h2 class="text-lg font-semibold">Replies</h2>
			<button
				type="button"
				onclick={refreshReplies}
				disabled={refreshing}
				class="btn-secondary btn-sm inline-flex items-center gap-1.5 disabled:opacity-50"
			>
				<RefreshCw size={14} class={refreshing ? 'animate-spin' : ''} />
				{refreshing ? 'Refreshing…' : 'Refresh replies'}
			</button>
		</div>
		{#if refreshMessage}
			<div class="mb-3 p-3 bg-amber-50 dark:bg-amber-900/30 text-amber-800 dark:text-amber-200 rounded-lg text-sm">
				{refreshMessage}
			</div>
		{/if}
		{#if comments.length === 0}
			<p class="text-gray-500 dark:text-gray-400">No replies yet.</p>
		{:else}
			<div class="space-y-4">
				{#each comments as c (c.id)}
					<div class="flex gap-3">
						{#if c.avatarUrl}
							<img src={c.avatarUrl} alt="" class="w-10 h-10 rounded-full flex-shrink-0" />
						{:else}
							<div class="w-10 h-10 rounded-full bg-gray-200 dark:bg-gray-700 flex-shrink-0"></div>
						{/if}
						<div class="flex-1 min-w-0">
							<div class="flex flex-wrap items-center gap-2 text-sm">
								{#if c.authorUrl}
									<a href={c.authorUrl} target="_blank" rel="noopener noreferrer" class="font-medium text-primary-700 dark:text-primary-300 hover:underline">
										{c.author}
									</a>
								{:else}
									<span class="font-medium">{c.author}</span>
								{/if}
								<PlatformBadge platform={c.platform} showLabel={false} />
								<span class="text-xs text-gray-500 dark:text-gray-400">{formatTime(c.postedAt)}</span>
							</div>
							<p class="text-sm text-gray-800 dark:text-gray-200 mt-1 whitespace-pre-wrap">
								{stripHtml(c.body)}
							</p>
						</div>
					</div>
				{/each}
			</div>
		{/if}
	</div>
{/if}

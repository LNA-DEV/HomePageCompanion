<script lang="ts">
	import { onMount } from 'svelte';
	import {
		api,
		type MicroblogPostWithPublications,
		type MicroblogPublication
	} from '$lib/api';
	import { PageHeader, Loading, Modal, PlatformBadge } from '$lib/components';
	import { stripHtml } from '$lib/utils';
	import {
		CheckCircle2,
		XCircle,
		RotateCw,
		Trash2,
		MessageSquare,
		Heart,
		ExternalLink,
		ImagePlus,
		X as XIcon
	} from 'lucide-svelte';

	const MAX_CHARS = 500;

	let posts = $state<MicroblogPostWithPublications[]>([]);
	let total = $state(0);
	let page = $state(1);
	let limit = 50;
	let loading = $state(true);
	let error = $state('');

	let composeOpen = $state(false);
	let composing = $state(false);
	let composeError = $state('');
	let composeBody = $state('');
	let composeWarning = $state('');
	let composeImageURL = $state('');
	let composeImageAlt = $state('');
	let uploadBusy = $state(false);

	let deletePostID = $state<number | null>(null);
	let deleting = $state(false);

	let retryBusy = $state<number | null>(null);

	async function load() {
		loading = true;
		try {
			const resp = await api.getMicroblogPosts({ page, limit });
			posts = resp.items ?? [];
			total = resp.total;
			error = '';
		} catch (e) {
			error = 'Failed to load microblog posts';
		}
		loading = false;
	}

	onMount(load);

	async function handleUpload(e: Event) {
		const input = e.target as HTMLInputElement;
		if (!input.files || input.files.length === 0) return;
		uploadBusy = true;
		composeError = '';
		try {
			const res = await api.uploadMicroblogImage(input.files[0]);
			composeImageURL = res.url;
		} catch {
			composeError = 'Image upload failed';
		}
		uploadBusy = false;
		input.value = '';
	}

	async function submit() {
		composeError = '';
		const body = composeBody.trim();
		if (!body && !composeImageURL) {
			composeError = 'Post needs body or image.';
			return;
		}
		if (body.length > MAX_CHARS) {
			composeError = `Body exceeds ${MAX_CHARS} characters.`;
			return;
		}
		composing = true;
		try {
			await api.createMicroblogPost({
				body,
				contentWarning: composeWarning.trim() || undefined,
				imageUrl: composeImageURL || undefined,
				imageAltText: composeImageAlt.trim() || undefined
			});
			composeOpen = false;
			composeBody = '';
			composeWarning = '';
			composeImageURL = '';
			composeImageAlt = '';
			await load();
		} catch (e) {
			composeError = e instanceof Error ? e.message : 'Failed to publish.';
		}
		composing = false;
	}

	async function confirmDelete() {
		if (deletePostID === null) return;
		deleting = true;
		try {
			await api.deleteMicroblogPost(deletePostID);
			deletePostID = null;
			await load();
		} catch {
			error = 'Delete failed';
		}
		deleting = false;
	}

	async function retry(post: MicroblogPostWithPublications, pub: MicroblogPublication) {
		retryBusy = pub.id;
		try {
			await api.retryMicroblogPublication(post.id, pub.id);
			await load();
		} catch {
			error = `Retry failed for ${pub.platform}`;
		}
		retryBusy = null;
	}

	function formatTime(s: string): string {
		try {
			return new Date(s).toLocaleString();
		} catch {
			return s;
		}
	}
</script>

<PageHeader title="Microblog" description="Author short posts that federate to Mastodon (and more, eventually)">
	{#snippet actions()}
		<button onclick={() => (composeOpen = true)} class="btn-primary">Compose</button>
	{/snippet}
</PageHeader>

{#if error}
	<div class="mb-4 p-3 bg-red-50 dark:bg-red-900/30 text-red-700 dark:text-red-300 rounded-lg">{error}</div>
{/if}

{#if loading}
	<Loading message="Loading microblog posts..." />
{:else if posts.length === 0}
	<div class="card text-center py-12">
		<p class="text-gray-500 dark:text-gray-400">No microblog posts yet — click Compose to publish your first.</p>
	</div>
{:else}
	<div class="space-y-4">
		{#each posts as post (post.id)}
			<div class="card">
				<div class="flex items-start justify-between gap-3 mb-2">
					<div class="text-xs text-gray-500 dark:text-gray-400 truncate">
						<a href="/microblog/{post.slug}" class="hover:underline">{post.slug}</a>
						<span class="mx-1">·</span>
						{formatTime(post.createdAt)}
					</div>
					<button
						onclick={() => (deletePostID = post.id)}
						class="text-red-600 dark:text-red-400 hover:text-red-800 dark:hover:text-red-300 text-sm flex items-center gap-1"
					>
						<Trash2 size={14} />
						Delete
					</button>
				</div>

				{#if post.contentWarning}
					<details class="mb-2">
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
						class="mt-3 max-h-80 rounded-lg object-cover"
					/>
				{/if}

				<div class="mt-3 flex flex-wrap items-center gap-2">
					{#each post.publications as pub}
						{#if pub.success}
							<a
								href={pub.postUrl}
								target="_blank"
								rel="noopener noreferrer"
								class="badge bg-emerald-100 text-emerald-700 dark:bg-emerald-900/40 dark:text-emerald-200 hover:opacity-80"
								title={`Published to ${pub.targetName}`}
							>
								<CheckCircle2 size={12} />
								<PlatformBadge platform={pub.platform} showLabel={false} />
								{pub.targetName}
								<ExternalLink size={10} />
							</a>
						{:else}
							<span class="inline-flex items-center gap-2">
								<span
									class="badge bg-red-100 text-red-700 dark:bg-red-900/40 dark:text-red-200"
									title={pub.errorMessage || pub.errorOnAttempt || 'failed'}
								>
									<XCircle size={12} />
									<PlatformBadge platform={pub.platform} showLabel={false} />
									{pub.targetName} — failed
								</span>
								<button
									onclick={() => retry(post, pub)}
									disabled={retryBusy === pub.id}
									class="text-xs inline-flex items-center gap-1 text-primary-700 dark:text-primary-300 hover:underline disabled:opacity-50"
								>
									<RotateCw size={12} class={retryBusy === pub.id ? 'animate-spin' : ''} />
									Retry
								</button>
							</span>
						{/if}
					{/each}
				</div>
			</div>
		{/each}
	</div>

	{#if total > limit}
		<div class="flex items-center justify-between mt-6">
			<button
				onclick={() => {
					if (page > 1) {
						page--;
						load();
					}
				}}
				disabled={page === 1}
				class="btn-secondary btn-sm disabled:opacity-50"
			>
				Previous
			</button>
			<span class="text-sm text-gray-500 dark:text-gray-400">
				Page {page} of {Math.ceil(total / limit) || 1}
			</span>
			<button
				onclick={() => {
					if (page * limit < total) {
						page++;
						load();
					}
				}}
				disabled={page * limit >= total}
				class="btn-secondary btn-sm disabled:opacity-50"
			>
				Next
			</button>
		</div>
	{/if}
{/if}

<Modal open={composeOpen} title="Compose Microblog Post" onClose={() => (composeOpen = false)}>
	<div class="space-y-4">
		<div>
			<label for="cw" class="label">Content warning (optional)</label>
			<input id="cw" type="text" bind:value={composeWarning} class="input" placeholder="e.g. politics, food, nsfw" />
		</div>
		<div>
			<label for="body" class="label">Body</label>
			<textarea
				id="body"
				bind:value={composeBody}
				class="input min-h-[140px]"
				placeholder="What's on your mind?"
			></textarea>
			<div class="text-xs text-gray-500 dark:text-gray-400 mt-1 text-right">
				<span class:text-red-600={composeBody.length > MAX_CHARS}>{composeBody.length}</span> / {MAX_CHARS}
			</div>
		</div>
		<div>
			<span class="label">Image (optional)</span>
			{#if composeImageURL}
				<div class="flex items-center gap-3">
					<img src={composeImageURL} alt="preview" class="w-20 h-20 object-cover rounded" />
					<input bind:value={composeImageAlt} placeholder="Alt text" class="input flex-1" />
					<button
						type="button"
						onclick={() => {
							composeImageURL = '';
							composeImageAlt = '';
						}}
						class="text-red-600 dark:text-red-400 hover:text-red-800 dark:hover:text-red-300"
						aria-label="Remove image"
					>
						<XIcon size={18} />
					</button>
				</div>
			{:else}
				<label
					class="flex items-center gap-2 p-3 border-2 border-dashed border-gray-300 dark:border-gray-700 rounded-lg cursor-pointer hover:bg-gray-50 dark:hover:bg-gray-800"
				>
					<ImagePlus size={16} />
					{uploadBusy ? 'Uploading…' : 'Click to upload an image'}
					<input
						type="file"
						accept="image/*"
						onchange={handleUpload}
						class="hidden"
						disabled={uploadBusy}
					/>
				</label>
			{/if}
		</div>
		{#if composeError}
			<div class="p-3 bg-red-50 dark:bg-red-900/30 text-red-700 dark:text-red-300 rounded-lg">{composeError}</div>
		{/if}
	</div>
	{#snippet actions()}
		<button onclick={() => (composeOpen = false)} class="btn-secondary" disabled={composing}>Cancel</button>
		<button onclick={submit} class="btn-primary" disabled={composing}>
			{composing ? 'Publishing…' : 'Publish'}
		</button>
	{/snippet}
</Modal>

<Modal open={deletePostID !== null} title="Delete Microblog Post" onClose={() => (deletePostID = null)}>
	<p>This will delete the post locally and best-effort delete each federated copy.</p>
	<p class="text-sm text-gray-500 dark:text-gray-400 mt-2">Imported replies for this post are also discarded.</p>
	{#snippet actions()}
		<button onclick={() => (deletePostID = null)} class="btn-secondary">Cancel</button>
		<button onclick={confirmDelete} class="btn-danger" disabled={deleting}>
			{deleting ? 'Deleting…' : 'Delete'}
		</button>
	{/snippet}
</Modal>

<svelte:options customElement={{ tag: 'microblog-page', shadow: 'none' }} />

<script lang="ts">
	// Microblog: write short posts that go to every publishTo target, see
	// where each went out, retry a failed one; /b/microblog/<id> shows a
	// post with its replies.
	import { formatDate, stripHtml, connect } from '@companion/ui';
	import { PageHeader, Modal, PlatformBadge } from '@companion/ui/components';

	interface Publication {
		id: number;
		target: string;
		targetName: string;
		platform: string;
		postUrl?: string;
		success: boolean;
		errorMessage?: string;
		errorOnAttempt?: string;
		likes: number;
	}
	interface Post {
		id: number;
		slug: string;
		body: string;
		contentWarning: string;
		imageUrl: string;
		imageAltText: string;
		nativeLikes: number;
		createdAt: string;
		publications: Publication[];
	}
	interface Comment {
		id: number;
		platform: string;
		author: string;
		authorUrl: string;
		avatarUrl: string;
		body: string;
		postedAt: string;
	}
	interface List {
		items: Post[];
		total: number;
		page: number;
		limit: number;
		targets: string[] | null;
		maxBodyChars: number;
	}

	let { path = '' }: { path?: string } = $props();

	const mb = connect('microblog');
	let list = $state<List | null>(null);
	let detail = $state<{ post: Post; comments: Comment[] | null } | null>(null);
	let error = $state('');
	let page = $state(1);
	// A link can preset the search: /b/microblog?q=<slug>.
	let q = $state(new URLSearchParams(location.search).get('q') ?? '');
	let timer: ReturnType<typeof setTimeout> | null = null;

	let composeOpen = $state(false);
	let composing = $state(false);
	let composeError = $state('');
	let body = $state('');
	let warning = $state('');
	let imageUrl = $state('');
	let imageAlt = $state('');
	let uploading = $state(false);
	let deleteId = $state<number | null>(null);
	let busy = $state('');

	const max = $derived(list?.maxBodyChars ?? 500);
	const pages = $derived(list ? Math.max(1, Math.ceil(list.total / list.limit)) : 1);

	async function load() {
		try {
			if (path) {
				detail = await mb.api.get(`posts/${encodeURIComponent(path)}`);
			} else {
				list = await mb.api.get<List>('posts', { page, q, limit: 30 });
			}
			error = '';
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	}

	$effect(() => {
		path;
		detail = null;
		load();
	});

	function typed() {
		if (timer) clearTimeout(timer);
		timer = setTimeout(() => {
			page = 1;
			load();
		}, 300);
	}

	async function upload(e: Event) {
		const input = e.target as HTMLInputElement;
		const file = input.files?.[0];
		if (!file) return;
		uploading = true;
		composeError = '';
		try {
			const form = new FormData();
			form.append('file', file);
			imageUrl = (await mb.api.upload<{ url: string }>('upload', form)).url;
		} catch (err) {
			composeError = err instanceof Error ? err.message : String(err);
		}
		uploading = false;
		input.value = '';
	}

	async function publish() {
		composeError = '';
		const text = body.trim();
		if (!text && !imageUrl) {
			composeError = 'A post needs text or an image.';
			return;
		}
		if ([...text].length > max) {
			composeError = `The text is longer than ${max} characters.`;
			return;
		}
		composing = true;
		try {
			const post = await mb.api.post<Post>('posts', { body: text, contentWarning: warning.trim(), imageUrl, imageAltText: imageAlt.trim() });
			const failed = post.publications.filter((p) => !p.success).map((p) => p.target);
			const ok = post.publications.length - failed.length;
			if (!failed.length) mb.toast(`Published on ${ok} target(s)`, 'success');
			else if (ok) mb.toast(`Published on ${ok} target(s); failed on ${failed.join(', ')}`, 'error');
			else mb.toast(`Saved, but every target failed (${failed.join(', ')}); retry below`, 'error');
			composeOpen = false;
			body = warning = imageUrl = imageAlt = '';
			page = 1;
			await load();
		} catch (err) {
			composeError = err instanceof Error ? err.message : String(err);
		}
		composing = false;
	}

	async function retry(post: Post, pub: Publication) {
		busy = 'retry' + pub.id;
		try {
			const r = await mb.api.post<Publication>(`posts/${post.id}/retry/${pub.id}`);
			if (r.success) mb.toast(`Published on ${pub.target}`, 'success');
			else mb.toast(`${pub.target}: ${r.errorOnAttempt || r.errorMessage || 'failed again'}`, 'error');
			await load();
		} catch (err) {
			mb.toast(err instanceof Error ? err.message : String(err), 'error');
		}
		busy = '';
	}

	async function remove() {
		if (deleteId === null) return;
		busy = 'delete';
		try {
			await mb.api.del(`posts/${deleteId}`);
			mb.toast('Post deleted', 'success');
			deleteId = null;
			if (path) mb.navigate('');
			else await load();
		} catch (err) {
			mb.toast(err instanceof Error ? err.message : String(err), 'error');
		}
		busy = '';
	}

	async function refreshReplies(post: Post) {
		busy = 'refresh';
		try {
			await mb.api.post(`posts/${post.id}/refresh`);
			await load();
			mb.toast('Replies and likes refreshed', 'success');
		} catch (err) {
			const msg = err instanceof Error ? err.message : String(err);
			mb.toast(msg.includes('busy') || msg.includes('running') ? 'Another refresh is running; try again in a moment.' : msg, 'error');
		}
		busy = '';
	}

	function likes(p: Post): number {
		return p.publications.reduce((n, x) => n + (x.success ? x.likes : 0), 0) + p.nativeLikes;
	}
</script>

{#snippet postBody(post: Post)}
	{#if post.contentWarning}
		<details>
			<summary class="cursor-pointer text-sm text-amber-700 dark:text-amber-400">CW: {post.contentWarning}</summary>
			<p class="mt-2 whitespace-pre-wrap">{post.body}</p>
		</details>
	{:else}
		<p class="whitespace-pre-wrap">{post.body}</p>
	{/if}
	{#if post.imageUrl}<img src={post.imageUrl} alt={post.imageAltText} class="mt-3 max-h-80 rounded-lg object-cover" />{/if}
{/snippet}

{#snippet publications(post: Post)}
	<div class="mt-3 flex flex-wrap items-center gap-2">
		{#each post.publications as pub (pub.id)}
			{#if pub.success}
				<a href={pub.postUrl} target="_blank" rel="noopener" class="badge bg-emerald-100 text-emerald-700 dark:bg-emerald-900/40 dark:text-emerald-200 hover:opacity-80" title="Published on {pub.target}">
					<PlatformBadge platform={pub.platform} showLabel={false} />
					{pub.target} · {pub.likes} likes
				</a>
			{:else}
				<span class="inline-flex items-center gap-2">
					<span class="badge bg-red-100 text-red-700 dark:bg-red-900/40 dark:text-red-200" title={pub.errorMessage}>
						<PlatformBadge platform={pub.platform} showLabel={false} />
						{pub.target} failed
					</span>
					<button class="text-xs text-primary-700 dark:text-primary-300 hover:underline disabled:opacity-50" disabled={busy !== ''} onclick={() => retry(post, pub)}>
						{busy === 'retry' + pub.id ? 'Retrying…' : 'Retry'}
					</button>
				</span>
			{/if}
		{/each}
	</div>
{/snippet}

{#if !path}
	<PageHeader title="Microblog" description="Short posts, published on every target of the microblog">
		{#snippet actions()}
			<button class="btn-primary" onclick={() => (composeOpen = true)}>Compose</button>
		{/snippet}
	</PageHeader>
	{#if list?.targets}
		<p class="-mt-4 mb-4 text-sm text-gray-500 dark:text-gray-400">Publishes to {list.targets.join(', ') || 'no target yet'}.</p>
	{/if}
	{#if error}<div class="mb-4 p-3 bg-red-50 dark:bg-red-900/30 text-red-700 dark:text-red-300 rounded-lg">{error}</div>{/if}
	<div class="card mb-4">
		<input class="input" type="search" placeholder="Search posts…" bind:value={q} oninput={typed} aria-label="Search posts" />
	</div>
	{#if !list}
		<p class="text-gray-500">Loading…</p>
	{:else}
		<div class="space-y-4" data-testid="microblog-posts">
			{#each list.items as post (post.id)}
				<article class="card">
					<div class="flex items-start justify-between gap-3 mb-2 text-xs text-gray-500 dark:text-gray-400">
						<a href={mb.href(String(post.id))} class="hover:underline truncate">{post.slug} · {formatDate(post.createdAt)} · {likes(post)} likes</a>
						<button class="text-red-600 dark:text-red-400 hover:underline" onclick={() => (deleteId = post.id)}>Delete</button>
					</div>
					{@render postBody(post)}
					{@render publications(post)}
				</article>
			{:else}
				<div class="card text-center text-gray-500 py-8">{q ? 'No posts match.' : 'No posts yet: compose the first one.'}</div>
			{/each}
		</div>
		{#if list.total > list.limit}
			<div class="flex items-center justify-between mt-4 text-sm text-gray-600 dark:text-gray-300">
				<span>Page {list.page} of {pages}</span>
				<div class="flex gap-2">
					<button class="btn-secondary btn-sm" disabled={page <= 1} onclick={() => { page--; load(); }}>Previous</button>
					<button class="btn-secondary btn-sm" disabled={page >= pages} onclick={() => { page++; load(); }}>Next</button>
				</div>
			</div>
		{/if}
	{/if}
{:else}
	<PageHeader title="Microblog post">
		{#snippet actions()}
			<a href={mb.href()} class="btn-secondary">All posts</a>
		{/snippet}
	</PageHeader>
	{#if error}<div class="mb-4 p-3 bg-red-50 dark:bg-red-900/30 text-red-700 dark:text-red-300 rounded-lg">{error}</div>{/if}
	{#if detail}
		{@const post = detail.post}
		<article class="card mb-6">
			<div class="flex items-start justify-between gap-3 mb-2 text-xs text-gray-500 dark:text-gray-400">
				<span>{post.slug} · {formatDate(post.createdAt)} · {likes(post)} likes ({post.nativeLikes} on the website)</span>
				<button class="text-red-600 dark:text-red-400 hover:underline" onclick={() => (deleteId = post.id)}>Delete</button>
			</div>
			{@render postBody(post)}
			{@render publications(post)}
		</article>
		<section class="card">
			<div class="flex items-center justify-between gap-3 mb-4">
				<h2 class="text-lg font-semibold">Replies</h2>
				<button class="btn-secondary btn-sm" disabled={busy !== ''} onclick={() => refreshReplies(post)}>{busy === 'refresh' ? 'Refreshing…' : 'Refresh replies'}</button>
			</div>
			<div class="space-y-4" data-testid="replies">
				{#each detail.comments ?? [] as c (c.id)}
					<div class="flex gap-3">
						{#if c.avatarUrl}<img src={c.avatarUrl} alt="" class="w-10 h-10 rounded-full shrink-0" />{:else}<div class="w-10 h-10 rounded-full bg-gray-200 dark:bg-gray-700 shrink-0"></div>{/if}
						<div class="min-w-0 flex-1">
							<p class="flex flex-wrap items-center gap-2 text-sm">
								{#if c.authorUrl}<a href={c.authorUrl} target="_blank" rel="noopener" class="font-medium text-primary-700 dark:text-primary-300 hover:underline">{c.author}</a>{:else}<span class="font-medium">{c.author}</span>{/if}
								<PlatformBadge platform={c.platform} showLabel={false} />
								<span class="text-xs text-gray-500">{formatDate(c.postedAt)}</span>
							</p>
							<p class="text-sm mt-1 whitespace-pre-wrap">{stripHtml(c.body)}</p>
						</div>
					</div>
				{:else}
					<p class="text-gray-500 dark:text-gray-400">No replies yet.</p>
				{/each}
			</div>
		</section>
	{:else if !error}
		<p class="text-gray-500">Loading…</p>
	{/if}
{/if}

<Modal open={composeOpen} title="New post" onClose={() => (composeOpen = false)}>
	<div class="space-y-4">
		<div>
			<label for="mb-cw" class="label">Content warning (optional)</label>
			<input id="mb-cw" class="input" bind:value={warning} placeholder="e.g. politics, food" />
		</div>
		<div>
			<label for="mb-body" class="label">Text</label>
			<textarea id="mb-body" class="input min-h-[140px]" bind:value={body} placeholder="What's on your mind?"></textarea>
			<p class="text-xs text-right mt-1 {[...body].length > max ? 'text-red-600' : 'text-gray-500'}">{[...body].length} / {max}</p>
		</div>
		<div>
			<span class="label">Image (optional)</span>
			{#if imageUrl}
				<div class="flex items-center gap-3">
					<img src={imageUrl} alt="" class="w-20 h-20 object-cover rounded" />
					<input class="input flex-1" bind:value={imageAlt} placeholder="Alt text" aria-label="Alt text" />
					<button class="text-red-600 dark:text-red-400 text-sm" onclick={() => (imageUrl = imageAlt = '')}>Remove</button>
				</div>
			{:else}
				<label class="flex items-center gap-2 p-3 border-2 border-dashed border-gray-300 dark:border-gray-700 rounded-lg cursor-pointer hover:bg-gray-50 dark:hover:bg-gray-800">
					{uploading ? 'Uploading…' : 'Choose an image'}
					<input type="file" accept="image/*" class="hidden" disabled={uploading} onchange={upload} />
				</label>
			{/if}
		</div>
		{#if composeError}<div class="p-3 bg-red-50 dark:bg-red-900/30 text-red-700 dark:text-red-300 rounded-lg">{composeError}</div>{/if}
	</div>
	{#snippet actions()}
		<button class="btn-secondary" disabled={composing} onclick={() => (composeOpen = false)}>Cancel</button>
		<button class="btn-primary" disabled={composing} onclick={publish}>{composing ? 'Publishing…' : 'Publish'}</button>
	{/snippet}
</Modal>

<Modal open={deleteId !== null} title="Delete post" onClose={() => (deleteId = null)}>
	<p>The post is deleted here and, as far as the networks allow, on every target.</p>
	<p class="text-sm text-gray-500 dark:text-gray-400 mt-2">Its imported replies are discarded too.</p>
	{#snippet actions()}
		<button class="btn-secondary" onclick={() => (deleteId = null)}>Cancel</button>
		<button class="btn-danger" disabled={busy === 'delete'} onclick={remove}>{busy === 'delete' ? 'Deleting…' : 'Delete'}</button>
	{/snippet}
</Modal>

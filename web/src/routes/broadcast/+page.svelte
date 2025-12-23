<script lang="ts">
	import { api } from '$lib/api';
	import { PageHeader } from '$lib/components';

	let title = $state('');
	let body = $state('');
	let url = $state('');
	let icon = $state('');

	let loading = $state(false);
	let success = $state('');
	let error = $state('');

	async function handleSubmit(e: Event) {
		e.preventDefault();
		error = '';
		success = '';
		loading = true;

		try {
			await api.broadcast({
				title,
				body,
				url: url || undefined,
				icon: icon || undefined
			});
			success = 'Notification broadcast successfully!';
			// Reset form
			title = '';
			body = '';
			url = '';
			icon = '';
		} catch (e) {
			error = 'Failed to broadcast notification. Please try again.';
		}

		loading = false;
	}
</script>

<PageHeader
	title="Broadcast Notification"
	description="Send a push notification to all subscribers"
/>

<div class="max-w-2xl">
	<div class="card">
		<form onsubmit={handleSubmit}>
			<div class="space-y-6">
				<div>
					<label for="title" class="label">Title *</label>
					<input
						id="title"
						type="text"
						bind:value={title}
						class="input"
						placeholder="Notification title"
						required
						disabled={loading}
					/>
				</div>

				<div>
					<label for="body" class="label">Body *</label>
					<textarea
						id="body"
						bind:value={body}
						class="input min-h-[100px]"
						placeholder="Notification message"
						required
						disabled={loading}
					></textarea>
				</div>

				<div>
					<label for="url" class="label">URL (optional)</label>
					<input
						id="url"
						type="url"
						bind:value={url}
						class="input"
						placeholder="https://example.com/page"
						disabled={loading}
					/>
					<p class="text-xs text-gray-500 mt-1">Link to open when the notification is clicked</p>
				</div>

				<div>
					<label for="icon" class="label">Icon URL (optional)</label>
					<input
						id="icon"
						type="url"
						bind:value={icon}
						class="input"
						placeholder="https://example.com/icon.png"
						disabled={loading}
					/>
					<p class="text-xs text-gray-500 mt-1">URL to an image to display with the notification</p>
				</div>

				{#if error}
					<div class="p-3 bg-red-50 text-red-700 rounded-lg">{error}</div>
				{/if}

				{#if success}
					<div class="p-3 bg-green-50 text-green-700 rounded-lg">{success}</div>
				{/if}

				<button type="submit" class="btn-primary w-full" disabled={loading}>
					{#if loading}
						<span class="flex items-center justify-center gap-2">
							<span
								class="w-4 h-4 border-2 border-white border-t-transparent rounded-full animate-spin"
							></span>
							Sending...
						</span>
					{:else}
						Send Broadcast
					{/if}
				</button>
			</div>
		</form>
	</div>

	<div class="card mt-6">
		<h2 class="text-lg font-semibold mb-4">Preview</h2>
		<div class="border rounded-lg p-4 bg-gray-50">
			<div class="flex items-start gap-3">
				{#if icon}
					<img src={icon} alt="Icon" class="w-12 h-12 rounded-lg object-cover" />
				{:else}
					<div class="w-12 h-12 rounded-lg bg-gray-200 flex items-center justify-center text-gray-400">
						?
					</div>
				{/if}
				<div class="flex-1 min-w-0">
					<h3 class="font-semibold">{title || 'Notification Title'}</h3>
					<p class="text-sm text-gray-600 mt-1">{body || 'Notification body text...'}</p>
					{#if url}
						<p class="text-xs text-primary-600 mt-2 truncate">{url}</p>
					{/if}
				</div>
			</div>
		</div>
		<p class="text-xs text-gray-500 mt-2">
			This is an approximate preview. Actual appearance varies by device and browser.
		</p>
	</div>
</div>

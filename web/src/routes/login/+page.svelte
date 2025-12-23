<script lang="ts">
	import { goto } from '$app/navigation';
	import { login } from '$lib/stores/auth';

	let apiKey = $state('');
	let error = $state('');
	let loading = $state(false);

	async function handleSubmit(e: Event) {
		e.preventDefault();
		error = '';
		loading = true;

		try {
			const success = await login(apiKey);

			if (success) {
				goto('/dashboard');
			} else {
				error = 'Invalid API key';
			}
		} catch (err) {
			error = 'Failed to authenticate. Please check your connection.';
		}

		loading = false;
	}
</script>

<div class="min-h-screen flex items-center justify-center bg-gray-100">
	<div class="card max-w-md w-full mx-4">
		<div class="text-center mb-8">
			<h1 class="text-2xl font-bold text-gray-900">HomePageCompanion</h1>
			<p class="text-gray-500 mt-2">Admin Dashboard</p>
		</div>

		<form onsubmit={handleSubmit}>
			<div class="mb-6">
				<label for="apiKey" class="label">API Key</label>
				<input
					id="apiKey"
					type="password"
					bind:value={apiKey}
					class="input"
					placeholder="Enter your API key"
					required
					disabled={loading}
				/>
			</div>

			{#if error}
				<div class="mb-4 p-3 bg-red-50 text-red-700 rounded-lg text-sm">
					{error}
				</div>
			{/if}

			<button type="submit" class="btn-primary w-full" disabled={loading}>
				{#if loading}
					<span class="flex items-center justify-center gap-2">
						<span
							class="w-4 h-4 border-2 border-white border-t-transparent rounded-full animate-spin"
						></span>
						Signing in...
					</span>
				{:else}
					Sign In
				{/if}
			</button>
		</form>
	</div>
</div>

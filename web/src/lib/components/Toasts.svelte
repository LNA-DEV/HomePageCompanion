<script lang="ts">
	import { toasts } from '$lib/shell/toast';

	const colors = {
		info: 'bg-gray-900 text-white dark:bg-gray-100 dark:text-gray-900',
		success: 'bg-green-600 text-white',
		error: 'bg-red-600 text-white'
	};
</script>

<div class="fixed bottom-4 right-4 z-50 flex flex-col gap-2 max-w-sm" aria-live="polite">
	{#each $toasts as t (t.id)}
		<div class="px-4 py-3 rounded-lg shadow-lg text-sm {colors[t.kind]}" role="status">{t.message}</div>
	{/each}
</div>

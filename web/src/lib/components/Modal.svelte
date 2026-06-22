<script lang="ts">
	import type { Snippet } from 'svelte';

	interface Props {
		open: boolean;
		title: string;
		onClose: () => void;
		children: Snippet;
		actions?: Snippet;
		size?: 'md' | 'lg' | 'xl';
	}

	let { open, title, onClose, children, actions, size = 'md' }: Props = $props();

	const widthClass = $derived({ md: 'max-w-md', lg: 'max-w-2xl', xl: 'max-w-4xl' }[size]);

	function handleBackdropClick(e: MouseEvent) {
		if (e.target === e.currentTarget) {
			onClose();
		}
	}

	function handleKeydown(e: KeyboardEvent) {
		if (e.key === 'Escape') {
			onClose();
		}
	}
</script>

<svelte:window onkeydown={handleKeydown} />

{#if open}
	<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
	<div
		class="fixed inset-0 bg-black/60 flex items-center justify-center z-50 p-4"
		onclick={handleBackdropClick}
		onkeydown={handleKeydown}
		role="dialog"
		aria-modal="true"
		tabindex="-1"
	>
		<div class="bg-white dark:bg-gray-900 rounded-lg shadow-xl {widthClass} w-full max-h-[90vh] overflow-y-auto">
			<div class="px-6 py-4 border-b border-gray-200 dark:border-gray-800 flex items-center justify-between">
				<h2 class="text-lg font-semibold text-gray-900 dark:text-white">{title}</h2>
				<button
					onclick={onClose}
					class="text-gray-400 hover:text-gray-600 dark:text-gray-500 dark:hover:text-gray-300 text-xl"
					aria-label="Close"
				>&times;</button>
			</div>
			<div class="px-6 py-4 text-gray-700 dark:text-gray-200">
				{@render children()}
			</div>
			{#if actions}
				<div class="px-6 py-4 border-t border-gray-200 dark:border-gray-800 bg-gray-50 dark:bg-gray-800/50 flex justify-end gap-2 rounded-b-lg">
					{@render actions()}
				</div>
			{/if}
		</div>
	</div>
{/if}

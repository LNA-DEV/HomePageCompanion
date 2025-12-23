<script lang="ts">
	import type { Snippet } from 'svelte';

	interface Props {
		open: boolean;
		title: string;
		onClose: () => void;
		children: Snippet;
		actions?: Snippet;
	}

	let { open, title, onClose, children, actions }: Props = $props();

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
		class="fixed inset-0 bg-black bg-opacity-50 flex items-center justify-center z-50"
		onclick={handleBackdropClick}
		onkeydown={handleKeydown}
		role="dialog"
		aria-modal="true"
		tabindex="-1"
	>
		<div class="bg-white rounded-lg shadow-xl max-w-md w-full mx-4">
			<div class="px-6 py-4 border-b flex items-center justify-between">
				<h2 class="text-lg font-semibold">{title}</h2>
				<button onclick={onClose} class="text-gray-400 hover:text-gray-600 text-xl">&times;</button>
			</div>
			<div class="px-6 py-4">
				{@render children()}
			</div>
			{#if actions}
				<div class="px-6 py-4 border-t bg-gray-50 flex justify-end gap-2 rounded-b-lg">
					{@render actions()}
				</div>
			{/if}
		</div>
	</div>
{/if}

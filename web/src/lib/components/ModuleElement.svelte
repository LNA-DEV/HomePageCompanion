<script lang="ts">
	// Places a building block's custom element: loads its entry.js once,
	// then creates the element with data-module and the given attributes.
	import type { UIModule } from '$lib/shell/core';
	import { loadUI, whenDefined } from '$lib/shell/modules';

	interface Props {
		module: UIModule;
		tag: string;
		attrs?: Record<string, string>;
	}

	let { module, tag, attrs = {} }: Props = $props();
	let container: HTMLDivElement;
	let error = $state('');

	$effect(() => {
		const m = module;
		const t = tag;
		const a = { ...attrs };
		let el: HTMLElement | null = null;
		let gone = false;
		error = '';
		loadUI(m)
			.then(() => whenDefined(t))
			.then((ok) => {
				if (gone) return;
				if (!ok) {
					error = `${m.name}: element <${t}> is not defined by its UI`;
					return;
				}
				el = document.createElement(t);
				el.dataset.module = m.id;
				for (const [k, v] of Object.entries(a)) el.setAttribute(k, v);
				container.replaceChildren(el);
			})
			.catch((e) => {
				if (!gone) error = `${m.name}: ${e instanceof Error ? e.message : e}`;
			});
		return () => {
			gone = true;
			el?.remove();
		};
	});
</script>

<div bind:this={container} class="contents"></div>
{#if error}
	<div class="p-3 rounded-lg bg-amber-50 text-amber-800 dark:bg-amber-900/30 dark:text-amber-200 text-sm">{error}</div>
{/if}

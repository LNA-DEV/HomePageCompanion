<script lang="ts">
	// A building block's page: /b/<id>/<path> places the element its manifest
	// names for that path. Pages ending in "*" get the rest as "path".
	import { page } from '$app/stores';
	import { ModuleElement, Loading } from '$lib/components';
	import { manifest } from '$lib/shell/session';
	import { pageFor } from '$lib/shell/core';

	const moduleId = $derived($page.params.module);
	const path = $derived(($page.params.path ?? '').replace(/\/$/, ''));
	const mod = $derived($manifest?.modules.find((m) => m.id === moduleId));
	const found = $derived(mod ? pageFor(mod, path) : null);
</script>

<svelte:head>
	<title>{found?.page.title ?? mod?.name ?? 'HomePageCompanion'} · HomePageCompanion</title>
</svelte:head>

{#if !$manifest}
	<Loading message="Loading..." />
{:else if !mod}
	<div class="card">No building block <code>{moduleId}</code> is configured.</div>
{:else if !found}
	<div class="card">{mod.name} has no page <code>/{path}</code>.</div>
{:else if !mod.hasUi}
	<div class="card">
		{mod.name} declares this page, but its UI is not part of this build (built with <code>hpc build --no-ui</code>?).
	</div>
{:else}
	{#key `${mod.id}/${found.page.element}`}
		<ModuleElement module={mod} tag={found.page.element} attrs={{ path: found.rest }} />
	{/key}
{/if}

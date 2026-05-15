<script lang="ts">
	import { Cloud, Camera, Image, Send, Heart, AtSign } from 'lucide-svelte';
	import { platformColor } from '$lib/utils';

	interface Props {
		platform: string;
		href?: string | null;
		size?: 'sm' | 'md';
		showLabel?: boolean;
	}

	let { platform, href = null, size = 'sm', showLabel = true }: Props = $props();

	const Icon = $derived.by(() => {
		switch (platform.toLowerCase()) {
			case 'bluesky':
				return Cloud;
			case 'instagram':
				return Camera;
			case 'pixelfed':
				return Image;
			case 'mastodon':
				return AtSign;
			case 'native':
				return Heart;
			default:
				return Send;
		}
	});

	const iconSize = $derived(size === 'sm' ? 12 : 14);
	const classes = $derived(
		[
			'inline-flex items-center gap-1.5 rounded-full font-medium',
			size === 'sm' ? 'px-2 py-0.5 text-xs' : 'px-2.5 py-1 text-sm',
			platformColor(platform)
		].join(' ')
	);
</script>

{#if href}
	<a {href} target="_blank" rel="noopener noreferrer" class="{classes} hover:opacity-80 transition-opacity">
		<Icon size={iconSize} />
		{#if showLabel}<span class="capitalize">{platform}</span>{/if}
	</a>
{:else}
	<span class={classes}>
		<Icon size={iconSize} />
		{#if showLabel}<span class="capitalize">{platform}</span>{/if}
	</span>
{/if}

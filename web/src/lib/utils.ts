export function stripHtml(input: string | null | undefined): string {
	if (!input) return '';
	return input
		.replace(/<[^>]*>/g, ' ')
		.replace(/&nbsp;/g, ' ')
		.replace(/&amp;/g, '&')
		.replace(/&lt;/g, '<')
		.replace(/&gt;/g, '>')
		.replace(/&quot;/g, '"')
		.replace(/&#39;/g, "'")
		.replace(/\s+/g, ' ')
		.trim();
}

const PLATFORM_COLORS: Record<string, string> = {
	pixelfed: 'bg-indigo-100 text-indigo-700 dark:bg-indigo-900/40 dark:text-indigo-200',
	bluesky: 'bg-sky-100 text-sky-700 dark:bg-sky-900/40 dark:text-sky-200',
	instagram: 'bg-pink-100 text-pink-700 dark:bg-pink-900/40 dark:text-pink-200',
	mastodon: 'bg-purple-100 text-purple-700 dark:bg-purple-900/40 dark:text-purple-200',
	threads: 'bg-zinc-900 text-zinc-100 dark:bg-zinc-800 dark:text-zinc-100',
	twitter: 'bg-slate-200 text-slate-800 dark:bg-slate-700 dark:text-slate-100',
	native: 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/40 dark:text-emerald-200'
};

export function platformColor(platform: string | null | undefined): string {
	if (!platform) return 'bg-gray-100 text-gray-700 dark:bg-gray-800 dark:text-gray-200';
	return (
		PLATFORM_COLORS[platform.toLowerCase()] ??
		'bg-gray-100 text-gray-700 dark:bg-gray-800 dark:text-gray-200'
	);
}

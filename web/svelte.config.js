import adapter from '@sveltejs/adapter-static';
import { vitePreprocess } from '@sveltejs/vite-plugin-svelte';

/** @type {import('@sveltejs/kit').Config} */
const config = {
	preprocess: vitePreprocess(),
	kit: {
		adapter: adapter({
			pages: 'build',
			assets: 'build',
			fallback: 'index.html',
			precompress: false,
			strict: true
		}),
		paths: {
			base: ''
		},
		alias: {
			// The longer one first: the first match wins.
			'@companion/ui/components': 'src/sdk/components.ts',
			'@companion/ui': 'src/sdk/index.ts'
		}
	}
};

export default config;

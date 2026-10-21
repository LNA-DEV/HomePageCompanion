import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig, loadEnv } from 'vite';
import { SvelteKitPWA } from '@vite-pwa/sveltekit';

export default defineConfig({
	plugins: [
		sveltekit(),
		SvelteKitPWA({
			registerType: 'autoUpdate',
			strategies: 'generateSW',
			injectRegister: 'auto',
			manifest: {
				name: 'HomePageCompanion Admin',
				short_name: 'HPC Admin',
				description: 'Admin dashboard for HomePageCompanion',
				theme_color: '#2563eb',
				background_color: '#ffffff',
				display: 'standalone',
				start_url: '/',
				scope: '/',
				icons: [
					{ src: '/icon-192.png', sizes: '192x192', type: 'image/png' },
					{ src: '/icon-512.png', sizes: '512x512', type: 'image/png' },
					{ src: '/icon-512.png', sizes: '512x512', type: 'image/png', purpose: 'any maskable' }
				]
			},
			workbox: {
				navigateFallback: '/',
				globPatterns: ['**/*.{js,css,html,svg,png,ico,webp}'],
				navigateFallbackDenylist: [/^\/api\//]
			},
			devOptions: {
				enabled: false
			}
		})
	],
	// The dev server in front of a companion (hpc dev sets COMPANION_DEV_URL):
	// its API, the module and service UIs, health and metrics.
	server: {
		proxy: Object.fromEntries(
			['/api', '/ui/', '/health', '/metrics'].map((p) => [
				p,
				{
					target: loadEnv('development', '.', 'COMPANION_').COMPANION_DEV_URL ?? 'http://localhost:8080',
					changeOrigin: true
				}
			])
		)
	}
});

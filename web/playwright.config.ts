import { defineConfig, devices } from '@playwright/test';

// The UI tests (docs/concepts/modular-architecture.md §6): the shell and the
// module UIs in a browser, against the seeded integration setup with its
// fake platforms (e2e/world, served by e2e/cmd/uiserver -world). Build the
// shell and the module UIs first:
//
//	npm run build && npm run build:modules && npm run test:ui
const port = 18095;

export default defineConfig({
	testDir: 'tests/ui',
	// One world for the whole run, and some tests change it: in order.
	fullyParallel: false,
	workers: 1,
	forbidOnly: !!process.env.CI,
	retries: 0,
	timeout: 60_000,
	reporter: [['list'], ['html', { open: 'never', outputFolder: 'playwright-report' }]],
	outputDir: 'test-results',
	use: {
		baseURL: `http://127.0.0.1:${port}`,
		trace: 'retain-on-failure',
		screenshot: 'only-on-failure'
	},
	projects: [
		{ name: 'desktop', use: { ...devices['Desktop Chrome'], viewport: { width: 1400, height: 900 } }, testIgnore: /mobile/ },
		{ name: 'mobile', use: { ...devices['Pixel 7'] }, testMatch: /mobile/ }
	],
	webServer: {
		command: `go run ./cmd/uiserver -world -listen 127.0.0.1:${port}`,
		cwd: '../e2e',
		url: `http://127.0.0.1:${port}/health`,
		reuseExistingServer: !process.env.CI,
		timeout: 300_000,
		stdout: 'ignore',
		stderr: 'pipe'
	}
});

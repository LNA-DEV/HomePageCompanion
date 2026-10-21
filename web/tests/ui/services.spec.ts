import { test, expect } from '@playwright/test';
import { login, shot, watch } from './helpers';

// A service's UI (docs/concepts/modular-architecture.md §5.7): the notes
// service of the integration setup, plain HTML with @companion/ui, in the
// shell's sandboxed iframe. Its calls, notifications and navigation go
// through the bridge; the shell's page, cookies, storage and the core's API
// stay out of its reach.
test('a service page in the sandboxed frame, through the bridge', async ({ page }, info) => {
	const w = watch(page);
	await login(page);
	await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Notes' }).click();
	await expect(page).toHaveURL(/\/b\/notes$/);
	const el = page.getByTestId('service-frame');
	await expect(el).toHaveAttribute('sandbox', 'allow-scripts allow-forms');
	const frame = page.frameLocator('[data-testid=service-frame]');
	await expect(frame.getByText("A service's page")).toBeVisible();

	const iso = frame.getByTestId('isolation');
	await expect(iso.locator('#parent')).toContainText('blocked');
	await expect(iso.locator('#cookies')).toContainText('blocked');
	await expect(iso.locator('#storage')).toContainText('blocked');
	await expect(iso.locator('#core')).toContainText('blocked (403)');

	// An API call through the bridge, and a notification in the shell.
	await frame.getByLabel('Note').fill('First note');
	await frame.getByRole('button', { name: 'Add note' }).click();
	await expect(page.getByText('Note saved')).toBeVisible();
	await expect(frame.getByTestId('notes')).toContainText('First note');
	await shot(page, info, 'service-page');

	// The shell's theme reaches the frame.
	await expect(iso.locator('#dark')).toHaveText(/on|off/);

	// Navigation: within the service, and to a page of the shell.
	await frame.getByRole('button', { name: 'Open note 1' }).click();
	await expect(page).toHaveURL(/\/b\/notes\/note\/1$/);
	await expect(page.frameLocator('[data-testid=service-frame]').getByTestId('note-detail')).toHaveText('Note note/1');
	await page.frameLocator('[data-testid=service-frame]').getByRole('link', { name: 'Settings' }).click();
	await expect(page).toHaveURL(/\/settings$/);

	// Its dashboard widget, a frame too.
	await page.goto('/');
	await expect(page.frameLocator('[data-testid=service-frame]').getByTestId('notes-count')).toHaveText('1');
	await shot(page, info, 'service-widget');
	w.check();
});

// Opened on its own, the service's page is still sandboxed: the CSP keeps
// it out of the companion's origin.
test('a service page opened outside the shell stays sandboxed', async ({ page }) => {
	await login(page);
	const resp = await page.request.post('/api/core/services/notes/ui-token');
	const { base } = await resp.json();
	const direct = await page.request.get(`${base}index.html`);
	expect(direct.headers()['content-security-policy']).toContain('sandbox allow-scripts allow-forms');
	await page.goto(`${base}index.html`);
	await expect(page.getByTestId('isolation').locator('#cookies')).toContainText('blocked');
});

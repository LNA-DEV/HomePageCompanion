import { test, expect } from '@playwright/test';
import { login, shot, watch } from './helpers';

// On a phone: the posts page shows tiles, the navigation sits behind the
// menu button.
test('posts as tiles and the menu on a phone', async ({ page }, info) => {
	const w = watch(page);
	await login(page);
	await shot(page, info, 'dashboard');
	await page.goto('/b/autoupload');
	await expect(page.getByTestId('posts-tiles')).toBeVisible();
	await expect(page.getByTestId('posts-table')).toBeHidden();
	await expect(page.getByTestId('posts-tiles').locator('button')).toHaveCount(50);
	await shot(page, info, 'posts');

	await page.getByRole('button', { name: 'Open menu' }).click();
	const nav = page.getByRole('navigation', { name: 'Main' });
	await expect(nav).toBeVisible();
	await shot(page, info, 'menu');
	await nav.getByRole('link', { name: 'Schedule' }).click();
	await expect(page).toHaveURL(/\/b\/autoupload\/schedule$/);
	await expect(page.getByTestId('connection-card').first()).toBeVisible();
	await shot(page, info, 'schedule');

	await page.goto('/b/autoupload');
	await page.getByTestId('posts-tiles').locator('button').first().click();
	await expect(page.getByTestId('detail-panel')).toBeVisible();
	await shot(page, info, 'detail');
	w.check();
});

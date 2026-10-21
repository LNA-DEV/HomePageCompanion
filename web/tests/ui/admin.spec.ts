import { test, expect } from '@playwright/test';
import { login, shot, watch } from './helpers';

// Against the seeded integration setup (e2e/world): 60 photos, 8 blog
// posts and 3 microblog posts; one open problem each of account (Bluesky,
// paused), content (Instagram) and transient (Threads). The later tests
// change it ("run now", resume, retry), so the order matters.
test.describe.configure({ mode: 'serial' });

test('login refuses a wrong key and opens the dashboard with the right one', async ({ page }, info) => {
	const w = watch(page);
	await page.goto('/');
	await expect(page).toHaveURL(/\/login$/);
	await page.fill('#apiKey', 'wrong');
	await page.click('button[type=submit]');
	await expect(page.getByText('Invalid admin key')).toBeVisible();
	await shot(page, info, 'login-refused');
	await login(page);
	await expect(page.getByTestId('core-card')).toBeVisible();
	await expect(page.getByTestId('targets-widget')).toContainText('bluesky');
	await expect(page.getByTestId('top-widget')).toBeVisible();
	await expect(page.getByTestId('problems-widget')).toContainText('3');
	await shot(page, info, 'dashboard');
	w.check();
});

test('posts page: paging, filters, search across pages, sort', async ({ page }, info) => {
	const w = watch(page);
	await login(page);
	await page.goto('/b/autoupload');
	const rows = page.getByTestId('post-row');
	await expect(rows).toHaveCount(50);
	await expect(page.getByText('1–50 of 71')).toBeVisible();
	await shot(page, info, 'posts');
	await page.getByRole('button', { name: 'Next' }).click();
	await expect(page.getByText('51–71 of 71')).toBeVisible();
	await expect(rows).toHaveCount(21);
	await page.getByRole('button', { name: 'Previous' }).click();
	await expect(page.getByText('1–50 of 71')).toBeVisible();

	for (const [tab, total] of [
		['Photos', '1–50 of 60'],
		['Blog', '1–8 of 8'],
		['Microblog', '1–3 of 3'],
		['With problems', '1–3 of 3']
	]) {
		await page.getByRole('tab', { name: new RegExp(`^${tab}`) }).click();
		await expect(page.getByText(total)).toBeVisible();
	}
	await shot(page, info, 'posts-problems-filter');
	await page.getByRole('tab', { name: /^All/ }).click();

	// "Zugspitze in fog" (photo-024) and "after rain" (photo-044) are on
	// other pages than the first; the search runs on the server.
	await page.getByLabel('Search').fill('zugspitze');
	await expect(page.getByText('1–3 of 3')).toBeVisible();
	await expect(rows).toHaveCount(3);
	for (const r of await rows.all()) await expect(r).toContainText('Zugspitze');
	await page.getByLabel('Search').fill('');
	await page.getByLabel('Tag').fill('lakes');
	await expect(page.getByText('1–15 of 15')).toBeVisible();
	await page.getByLabel('Tag').fill('');
	await expect(page.getByText('1–50 of 71')).toBeVisible();

	await page.getByLabel('Sort').selectOption('likes');
	await expect(rows.first()).toBeVisible();
	const likes = await rows.evaluateAll((els) => els.slice(0, 5).map((e) => Number(e.lastElementChild?.querySelector('span')?.textContent?.replace('–', '0') ?? 0)));
	expect(likes).toEqual([...likes].sort((a, b) => b - a));
	w.check();
});

test('detail panel: targets, preview, history', async ({ page }, info) => {
	const w = watch(page);
	await login(page);
	await page.goto('/b/autoupload');
	await page.getByTestId('post-row').filter({ hasText: 'Watzmann at dawn' }).click();
	const panel = page.getByTestId('detail-panel');
	await expect(panel).toBeVisible();
	await expect(panel.getByTestId('target-card')).toHaveCount(5);
	await expect(panel).toContainText('History');
	await panel.getByTestId('target-card').filter({ hasText: 'mastodon' }).getByRole('button', { name: 'Preview' }).click();
	await expect(panel.getByTestId('preview')).toContainText('More at https://example.com/gallery');
	await shot(page, info, 'detail');
	await page.getByRole('button', { name: 'Close panel' }).click();
	await expect(panel).toBeHidden();
	w.check();
});

test('schedule: run now shows the real result, a paused connection resumes', async ({ page }, info) => {
	const w = watch(page);
	await login(page);
	await page.goto('/b/autoupload/schedule');
	const cards = page.getByTestId('connection-card');
	await expect(cards).toHaveCount(6);
	const bluesky = cards.filter({ hasText: 'photos-bluesky' });
	await expect(bluesky).toContainText('Paused after an account error');
	await shot(page, info, 'schedule');

	const pixelfed = cards.filter({ hasText: 'photos-pixelfed' });
	await pixelfed.getByRole('button', { name: 'Run now' }).click();
	const result = pixelfed.getByTestId('run-result');
	await expect(result).toContainText('Published', { timeout: 20_000 });
	await expect(result.getByRole('link')).toHaveAttribute('href', /\/@lna\/\d+$/);
	await shot(page, info, 'schedule-run-now');

	await bluesky.getByRole('button', { name: 'Resume' }).click();
	await expect(bluesky).not.toContainText('Paused after an account error');
	w.check();
});

test('problems: grouped by cause, retry, and the attempt log', async ({ page }, info) => {
	const w = watch(page);
	await login(page);
	await page.goto('/b/autoupload/problems');
	await expect(page.getByTestId('problem-group-account')).toContainText('bluesky');
	await expect(page.getByTestId('problem-group-content')).toContainText('too large');
	await expect(page.getByTestId('problem-group-transient')).toContainText('threads');
	await shot(page, info, 'problems');

	await page.getByTestId('problem-group-transient').getByRole('button', { name: 'Retry transient' }).click();
	await expect(page.getByText(/Retrying 1 post/)).toBeVisible();
	await expect(async () => {
		await page.reload();
		await expect(page.getByTestId('problem-group-transient')).toHaveCount(0);
	}).toPass({ timeout: 20_000 });

	await page.getByRole('tab', { name: 'Log' }).click();
	const log = page.getByTestId('attempt-log');
	await expect(log.locator('tbody tr').first()).toBeVisible();
	await page.getByLabel('Status').selectOption('failed');
	await expect(log.locator('tbody tr').first()).toBeVisible();
	for (const r of (await log.locator('tbody tr').all()).slice(0, 5)) await expect(r).not.toContainText('published');
	await shot(page, info, 'problems-log');
	w.check();
});

test('the other pages: feeds, microblog, trips, website, settings, system', async ({ page }, info) => {
	const w = watch(page);
	await login(page);
	const pages: [string, string, (p: typeof page) => Promise<void>][] = [
		['feeds', '/b/rss', async (p) => expect(p.getByTestId('feed-card')).toHaveCount(2)],
		['feed', '/b/rss/photos', async (p) => expect(p.getByTestId('feed-items')).toContainText('Watzmann at dawn')],
		['microblog', '/b/microblog', async (p) => expect(p.getByTestId('microblog-posts').locator('article')).toHaveCount(3)],
		['trips', '/b/trips', async (p) => expect(p.getByTestId('trips')).toContainText('Scandinavia')],
		['webmentions', '/b/webmention', async (p) => expect(p.getByTestId('webmentions').locator('tbody tr')).toHaveCount(3)],
		['subscribers', '/b/webpush', async (p) => expect(p.getByTestId('subscribers').locator('tbody tr')).toHaveCount(3)],
		['settings', '/settings', async (p) => expect(p.locator('a[href="/settings/' + encodeURIComponent('mastodon:main') + '"]').first()).toBeVisible()],
		['instance', '/settings/' + encodeURIComponent('mastodon:main'), async (p) => expect(p.getByTestId('oauth-card')).toContainText('Connect account')],
		['jobs', '/jobs', async (p) => expect(p.getByText('autoupload:photos-mastodon/publish')).toBeVisible()],
		['logs', '/logs', async (p) => expect(p.locator('main')).toBeVisible()],
		['backups', '/backups', async (p) => expect(p.locator('main')).toBeVisible()]
	];
	for (const [name, path, check] of pages) {
		await page.goto(path);
		await check(page);
		await shot(page, info, name);
	}
	// A broadcast reaches the three subscribed browsers of the fake push service.
	await page.goto('/b/webpush/broadcast');
	await page.fill('#wp-title', 'New photos');
	await page.fill('#wp-body', 'Twelve from the Alps');
	await page.getByRole('button', { name: 'Send' }).click();
	await expect(page.getByTestId('broadcast-result')).toContainText('Reached 3 devices');
	await shot(page, info, 'broadcast');
	w.check();
});

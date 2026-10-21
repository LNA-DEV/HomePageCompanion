import { expect, type Page, type TestInfo } from '@playwright/test';

export const adminKey = 'world-admin-key';

// shot keeps a screenshot as a CI artefact (ui-screenshots/).
export async function shot(page: Page, info: TestInfo, name: string) {
	await page.screenshot({ path: `ui-screenshots/${info.project.name}-${name}.png`, fullPage: true });
}

// watch fails the test on a JavaScript error or a failed request to the
// companion; images of the fake platforms do not count.
export function watch(page: Page) {
	const problems: string[] = [];
	page.on('pageerror', (e) => problems.push(`pageerror: ${e.message}`));
	page.on('response', (r) => {
		const u = new URL(r.url());
		if (r.status() >= 500 && u.port === '18095') problems.push(`${r.status()} ${r.url()}`);
	});
	return {
		check() {
			expect(problems, problems.join('\n')).toEqual([]);
		}
	};
}

export async function login(page: Page) {
	await page.goto('/login');
	await page.fill('#apiKey', adminKey);
	await page.click('button[type=submit]');
	await expect(page).toHaveURL('/');
}

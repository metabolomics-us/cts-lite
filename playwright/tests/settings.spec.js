const { test, expect } = require('@playwright/test');

test.beforeEach(async ({ page }) => {
  await page.goto('/');
});

test('settings panel opens on gear click', async ({ page }) => {
  await page.locator('#settings-toggle').click();
  await expect(page.locator('#settings-panel')).toHaveClass(/open/);
  await expect(page.locator('#settings-toggle')).toHaveAttribute('aria-expanded', 'true');
});

test('settings panel closes on second gear click', async ({ page }) => {
  await page.locator('#settings-toggle').click();
  await page.locator('#settings-toggle').click();
  await expect(page.locator('#settings-panel')).not.toHaveClass(/open/);
  await expect(page.locator('#settings-toggle')).toHaveAttribute('aria-expanded', 'false');
});

test('settings panel closes when clicking outside', async ({ page }) => {
  await page.locator('#settings-toggle').click();
  await expect(page.locator('#settings-panel')).toHaveClass(/open/);
  await page.locator('h1').click();
  await expect(page.locator('#settings-panel')).not.toHaveClass(/open/);
});

test('top_hit_only=false sent when toggle is unchecked', async ({ page }) => {
  await page.locator('#settings-toggle').click();
  await page.locator('label[aria-label="Top hit only"]').click();

  const [request] = await Promise.all([
    page.waitForRequest(req => req.url().includes('/match')),
    page.fill('#query-input', 'MYFAKEINCHIKEY-ISRIGHTHER-E'),
    page.click('button[type="submit"]'),
  ]);

  expect(request.url()).toContain('top_hit_only=false');
});

test('first_block_matches=false sent when toggle is unchecked', async ({ page }) => {
  await page.locator('#settings-toggle').click();
  await page.locator('label[aria-label="First block matches"]').click();

  const [request] = await Promise.all([
    page.waitForRequest(req => req.url().includes('/match')),
    page.fill('#query-input', 'MYFAKEINCHIKEY-ISRIGHTHER-E'),
    page.click('button[type="submit"]'),
  ]);

  expect(request.url()).toContain('first_block_matches=false');
});

test('Top Hit Only info icon opens correct docs section', async ({ page, context }) => {
  await page.locator('#settings-toggle').click();

  const [docsPage] = await Promise.all([
    context.waitForEvent('page'),
    page.locator('a[aria-label="Top Hit Only documentation"]').click(),
  ]);

  await docsPage.waitForLoadState();
  expect(docsPage.url()).toContain('#top-hit-only');
  await expect(docsPage.locator('#top-hit-only')).toBeInViewport();
});

test('First Block Matches info icon opens correct docs section', async ({ page, context }) => {
  await page.locator('#settings-toggle').click();

  const [docsPage] = await Promise.all([
    context.waitForEvent('page'),
    page.locator('a[aria-label="Match levels documentation"]').click(),
  ]);

  await docsPage.waitForLoadState();
  expect(docsPage.url()).toContain('#match-levels');
  await expect(docsPage.locator('#match-levels')).toBeInViewport();
});

test('applied settings label hidden before any query', async ({ page }) => {
  await expect(page.locator('#applied-settings-label')).not.toBeVisible();
});

test('applied settings label shows default settings after query', async ({ page }) => {
  await page.fill('#query-input', 'MYFAKEINCHIKEY-ISRIGHTHER-E');
  await page.click('button[type="submit"]');

  await expect(page.locator('#applied-settings-label')).toBeVisible();
  await expect(page.locator('#applied-settings-label')).toContainText('Top Hit Only');
  await expect(page.locator('#applied-settings-label')).toContainText('First Block Matches');
});

test('applied settings label shows All Hits when top hit only disabled', async ({ page }) => {
  await page.locator('#settings-toggle').click();
  await page.locator('label[aria-label="Top hit only"]').click();
  await page.fill('#query-input', 'MYFAKEINCHIKEY-ISRIGHTHER-E');
  await page.click('button[type="submit"]');

  await expect(page.locator('#applied-settings-label')).toContainText('All Hits');
});

test('applied settings label shows Exact Matches Only when first block matches disabled', async ({ page }) => {
  await page.locator('#settings-toggle').click();
  await page.locator('label[aria-label="First block matches"]').click();
  await page.fill('#query-input', 'MYFAKEINCHIKEY-ISRIGHTHER-E');
  await page.click('button[type="submit"]');

  await expect(page.locator('#applied-settings-label')).toContainText('Exact Matches Only');
});

test('page size persists across queries', async ({ page }) => {
  await page.locator('#settings-toggle').click();
  await page.locator('#page-size-select').selectOption('20');

  await page.fill('#query-input', '1 2 3 1 2 3 1 2 3 1 2');
  await page.click('button[type="submit"]');
  await expect(page.locator('.result-item')).toHaveCount(11);

  await page.click('button[type="submit"]');
  await expect(page.locator('.result-item')).toHaveCount(11);
  await expect(page.locator('#pagination-controls')).not.toBeVisible();
});

test('increasing page size collapses pagination', async ({ page }) => {
  // 11 queries exceeds the default page size of 10, triggering pagination
  await page.fill('#query-input', '1 2 3 1 2 3 1 2 3 1 2');
  await page.click('button[type="submit"]');
  await expect(page.locator('#pagination-controls')).toBeVisible();

  // Changing to 20 fits all 11 results on one page
  await page.locator('#settings-toggle').click();
  await page.locator('#page-size-select').selectOption('20');

  await expect(page.locator('.result-item')).toHaveCount(11);
  await expect(page.locator('#pagination-controls')).not.toBeVisible();
});

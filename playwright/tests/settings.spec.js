const { test, expect } = require('@playwright/test');

test.beforeEach(async ({ page }) => {
  // Pin ClassyFire status to up so its toggle is enabled. Otherwise a live "down"
  // response disables the toggle and the ClassyFire settings test cannot click it
  await page.route('**/classyfire/status', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ up: true }) }));
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

test('rdkit_conversion=false sent when toggle is unchecked', async ({ page }) => {
  await page.locator('#settings-toggle').click();
  await page.locator('label[aria-label="RDKit conversion"]').click();

  const [request] = await Promise.all([
    page.waitForRequest(req => req.url().includes('/match')),
    page.fill('#query-input', 'MYFAKEINCHIKEY-ISRIGHTHER-E'),
    page.click('button[type="submit"]'),
  ]);

  expect(request.url()).toContain('rdkit_conversion=false');
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
  await expect(page.locator('#applied-settings-label')).toContainText('RDKit Conversion');
  await expect(page.locator('#applied-settings-label')).not.toContainText('No RDKit Conversion');
  await expect(page.locator('#applied-settings-label')).not.toContainText('ClassyFire');
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

test('applied settings label shows No RDKit Conversion when rdkit disabled', async ({ page }) => {
  await page.locator('#settings-toggle').click();
  await page.locator('label[aria-label="RDKit conversion"]').click();
  await page.fill('#query-input', 'MYFAKEINCHIKEY-ISRIGHTHER-E');
  await page.click('button[type="submit"]');

  await expect(page.locator('#applied-settings-label')).toContainText('No RDKit Conversion');
});

test('applied settings label reflects all toggles disabled at once', async ({ page }) => {
  await page.locator('#settings-toggle').click();
  await page.locator('label[aria-label="Top hit only"]').click();
  await page.locator('label[aria-label="First block matches"]').click();
  await page.locator('label[aria-label="RDKit conversion"]').click();
  await page.fill('#query-input', 'MYFAKEINCHIKEY-ISRIGHTHER-E');
  await page.click('button[type="submit"]');

  await expect(page.locator('#applied-settings-label')).toContainText(
    'All Hits, Exact Matches Only, No RDKit Conversion'
  );
});

// Every label segment renders its non-default text when all toggles are flipped.
// The query matches nothing, so the server never calls the live ClassyFire service
test('applied settings label shows full permutation with ClassyFire enabled', async ({ page }) => {
  await page.locator('#settings-toggle').click();
  await page.locator('label[aria-label="Top hit only"]').click();
  await page.locator('label[aria-label="First block matches"]').click();
  await page.locator('label[aria-label="RDKit conversion"]').click();
  await page.locator('label[aria-label="ClassyFire chemical classes"]').click();
  await page.fill('#query-input', 'ZZZZZZZZZZZZZZ-ZZZZZZZZZZ-Z');
  await page.click('button[type="submit"]');

  await expect(page.locator('#applied-settings-label')).toContainText(
    'All Hits, Exact Matches Only, No RDKit Conversion, ClassyFire'
  );
});

test('applied settings label reflects settings at submit time, not later toggling', async ({ page }) => {
  await page.fill('#query-input', 'MYFAKEINCHIKEY-ISRIGHTHER-E');
  await page.click('button[type="submit"]');
  await expect(page.locator('#applied-settings-label')).toContainText('Top Hit Only');

  // Flipping a toggle after the query must not change the label until the next submit
  await page.locator('#settings-toggle').click();
  await page.locator('label[aria-label="Top hit only"]').click();
  await expect(page.locator('#applied-settings-label')).toContainText('Top Hit Only');

  await page.click('button[type="submit"]');
  await expect(page.locator('#applied-settings-label')).toContainText('All Hits');
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

const { test, expect } = require('@playwright/test');

test.beforeEach(async ({ page }) => {
  await page.goto('/');
  await page.fill('#query-input', 'MYFAKEINCHIKEY-ISRIGHTHER-E ZZZZZZZZZZZZZZ-ZZZZZZZZZZ-Z');
  await page.click('button[type="submit"]');
  await expect(page.locator('.match-status.no-match')).toHaveCount(1);
});

test('filter menu opens from the button and shows counts per match status', async ({ page }) => {
  await expect(page.locator('#filter-panel')).not.toBeVisible();
  await page.click('#filter-toggle');

  await expect(page.locator('#filter-panel')).toBeVisible();
  await expect(page.locator('.filter-option[data-filter="all"] .filter-count')).toHaveText('2');
  await expect(page.locator('.filter-option[data-filter="exact-match"] .filter-count')).toHaveText('1');
  await expect(page.locator('.filter-option[data-filter="no-match"] .filter-count')).toHaveText('1');
});

test('No Match filter shows only no-match rows and keeps query numbers', async ({ page }) => {
  await page.click('#filter-toggle');
  await page.click('.filter-option[data-filter="no-match"]');

  await expect(page.locator('#filter-panel')).not.toBeVisible();
  await expect(page.locator('.result-item')).toHaveCount(1);
  await expect(page.locator('.result-item h3')).toContainText('Query 2:');
  await expect(page.locator('#filter-toggle')).toHaveAttribute('data-filter', 'no-match');
});

test('selecting All clears the filter', async ({ page }) => {
  await page.click('#filter-toggle');
  await page.click('.filter-option[data-filter="no-match"]');
  await page.click('#filter-toggle');
  await page.click('.filter-option[data-filter="all"]');

  await expect(page.locator('.result-item')).toHaveCount(2);
  await expect(page.locator('#filter-toggle')).not.toHaveClass(/filtered/);
});

test('empty filter shows a message', async ({ page }) => {
  await page.click('#filter-toggle');
  await page.click('.filter-option[data-filter="first-block-match"]');

  await expect(page.locator('.result-item')).toHaveCount(0);
  await expect(page.locator('.filter-empty')).toBeVisible();

  await page.click('.filter-empty-clear');
  await expect(page.locator('.result-item')).toHaveCount(2);
  await expect(page.locator('#filter-toggle')).not.toHaveClass(/filtered/);
});

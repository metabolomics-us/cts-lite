const { test, expect } = require('@playwright/test');

test.beforeEach(async ({ page }) => {
  await page.goto('/');
});

test('pagination controls hidden when results fit on one page', async ({ page }) => {
  await page.fill('#query-input', '1 2 3');
  await page.click('button[type="submit"]');
  await expect(page.locator('.result-item').first()).toBeVisible();

  await expect(page.locator('#pagination-controls')).not.toBeVisible();
});

test('pagination controls appear when results exceed page size of 10', async ({ page }) => {
  await page.fill('#query-input', '1 2 3 1 2 3 1 2 3 1 2');
  await page.click('button[type="submit"]');

  await expect(page.locator('#pagination-controls')).toBeVisible();
  await expect(page.locator('.result-item')).toHaveCount(10);
});

test('next page button shows remaining results', async ({ page }) => {
  await page.fill('#query-input', '1 2 3 1 2 3 1 2 3 1 2');
  await page.click('button[type="submit"]');
  await expect(page.locator('#pagination-controls')).toBeVisible();

  await page.locator('#next-page').click();

  await expect(page.locator('.result-item')).toHaveCount(1);
  await expect(page.locator('#prev-page')).not.toBeDisabled();
  await expect(page.locator('#next-page')).toBeDisabled();
});

test('prev page button returns to first page', async ({ page }) => {
  await page.fill('#query-input', '1 2 3 1 2 3 1 2 3 1 2');
  await page.click('button[type="submit"]');
  await page.locator('#next-page').click();
  await page.locator('#prev-page').click();

  await expect(page.locator('.result-item')).toHaveCount(10);
  await expect(page.locator('#prev-page')).toBeDisabled();
});

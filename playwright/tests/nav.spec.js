const { test, expect } = require('@playwright/test');

test('docs link navigates to documentation page', async ({ page }) => {
  await page.goto('/');
  await page.locator('a[href="/docs"]').first().click();

  await expect(page).toHaveURL(/\/docs/);
  await expect(page.locator('body')).not.toBeEmpty();
});

test('documentation page loads without error', async ({ page }) => {
  await page.goto('/docs');

  await expect(page).toHaveURL(/\/docs/);
  await expect(page.locator('body')).not.toBeEmpty();
});

test('logo on docs page navigates back to home', async ({ page }) => {
  await page.goto('/docs');
  await page.locator('#navbar-logo').click();

  await expect(page).toHaveURL(/\/$/);
  await expect(page.locator('#query-form')).toBeVisible();
});

test('docs heading anchor button updates URL hash', async ({ page }) => {
  await page.goto('/docs');
  await page.locator('#rest-api .heading-anchor').click({ force: true });

  expect(page.url()).toContain('#rest-api');
});

const { test, expect } = require('@playwright/test');

test.beforeEach(async ({ page }) => {
  await page.goto('/');
});

test('empty submit shows error', async ({ page }) => {
  await page.click('button[type="submit"]');
  await expect(page.locator('#output-label')).toHaveText('Error');
  await expect(page.locator('#output-text')).toHaveText('Please enter a query');
});

test('InChIKey query returns exact match for Water', async ({ page }) => {
  await page.fill('#query-input', 'MYFAKEINCHIKEY-ISRIGHTHER-E');
  await page.click('button[type="submit"]');

  await expect(page.locator('.result-item')).toBeVisible();
  await expect(page.locator('.match-status')).toContainText('Match Found');
  await expect(page.locator('.match-item')).toContainText('WATER');
  await expect(page.locator('.match-item')).toContainText('MYFAKEINCHIKEY-ISRIGHTHER-E');
  await expect(page.locator('.match-item')).toContainText('H2O');
});

test('InChI query returns match for Methane', async ({ page }) => {
  await page.fill('#query-input', 'InChI=1S/CH4/h1H4');
  await page.click('button[type="submit"]');

  await expect(page.locator('.match-item')).toContainText('METHANE');
});

test('SMILES query returns match for Formaldehyde', async ({ page }) => {
  await page.fill('#query-input', 'C=O');
  await page.click('button[type="submit"]');

  await expect(page.locator('.match-item')).toContainText('FORMALDEHYDE');
});

test('formula query returns match for Water', async ({ page }) => {
  await page.fill('#query-input', 'H2O');
  await page.click('button[type="submit"]');

  await expect(page.locator('.match-item')).toContainText('WATER');
});

test('PubChem ID query returns match for Water', async ({ page }) => {
  await page.fill('#query-input', '1');
  await page.click('button[type="submit"]');

  await expect(page.locator('.match-item')).toContainText('WATER');
});

test('unknown InChIKey returns no match', async ({ page }) => {
  await page.fill('#query-input', 'ZZZZZZZZZZZZZZ-ZZZZZZZZZZ-Z');
  await page.click('button[type="submit"]');

  await expect(page.locator('.match-status')).toHaveText('✗ No match');
});

test('output label shows match count after query', async ({ page }) => {
  await page.fill('#query-input', 'MYFAKEINCHIKEY-ISRIGHTHER-E');
  await page.click('button[type="submit"]');

  await expect(page.locator('#output-label')).toContainText('1 / 1');
});

test('multi-query shows count for each result', async ({ page }) => {
  await page.fill('#query-input', 'MYFAKEINCHIKEY-ISRIGHTHER-E MYFAKEINCHIKEY-ANOTHERONE-E');
  await page.click('button[type="submit"]');

  await expect(page.locator('.result-item')).toHaveCount(2);
  await expect(page.locator('#output-label')).toContainText('2 / 2');
});

test('InChIKey match status shows Exact', async ({ page }) => {
  await page.fill('#query-input', 'MYFAKEINCHIKEY-ISRIGHTHER-E');
  await page.click('button[type="submit"]');

  await expect(page.locator('.match-status')).toHaveText('✓ Match Found: Exact');
});

test('malformed InChIKey shows error message', async ({ page }) => {
  await page.fill('#query-input', 'MYFAKEINCHIKEY-ISRIGHTHER-EE');
  await page.click('button[type="submit"]');

  await expect(page.locator('.error-message')).toContainText('Malformed InChIKey');
});

test('unidentified query type shows error message', async ({ page }) => {
  await page.fill('#query-input', 'junk');
  await page.click('button[type="submit"]');

  await expect(page.locator('.error-message')).toContainText('could not identify');
});

test('mixed query renders both match and no-match results', async ({ page }) => {
  await page.fill('#query-input', 'MYFAKEINCHIKEY-ISRIGHTHER-E ZZZZZZZZZZZZZZ-ZZZZZZZZZZ-Z');
  await page.click('button[type="submit"]');

  await expect(page.locator('.match-status.exact-match')).toHaveCount(1);
  await expect(page.locator('.match-status.no-match')).toHaveCount(1);
});

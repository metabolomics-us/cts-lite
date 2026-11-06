const { test, expect } = require('@playwright/test');

test.beforeEach(async ({ page }) => {
  await page.goto('/');
});

test('empty submit shows error', async ({ page }) => {
  await page.click('button[type="submit"]');
  await expect(page.locator('#output-label')).toHaveText('Error');
  await expect(page.locator('#output-text')).toHaveText('Please enter a query');
});

test('quotes-only submit shows empty query error', async ({ page }) => {
  await page.fill('#query-input', '" "" """');
  await page.click('button[type="submit"]');
  await expect(page.locator('#output-label')).toHaveText('Error');
  await expect(page.locator('#output-text')).toHaveText('Please enter a query');
});

// A line over 10 words splits per word, so one long line counts toward the limit
test('more than 100,000 identifiers on one line is blocked client-side', async ({ page }) => {
  let requested = false;
  await page.route('**/match*', (route) => { requested = true; route.fulfill({ status: 200, body: '' }); });

  await page.fill('#query-input', Array.from({ length: 100001 }, () => 'O').join(' '));
  await page.click('button[type="submit"]');

  await expect(page.locator('#output-label')).toHaveText('Error');
  await expect(page.locator('#output-text')).toHaveText(
    'Query contains 100,001 identifiers - please limit to 100,000 per submission'
  );
  expect(requested).toBe(false);
});

// Short lines count once client-side, but lines of HMDB IDs split on the server
test('lines splitting over 100,000 identifiers show the server error', async ({ page }) => {
  await page.fill('#query-input', Array.from({ length: 50001 }, () => 'HMDB1 HMDB2').join('\n'));
  await page.click('button[type="submit"]');

  await expect(page.locator('#output-label')).toHaveText('Error');
  await expect(page.locator('#output-text')).toContainText(
    'Server returned 400 - Query contains more than 100000 identifiers (limit 100,000)'
  );
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

// Padding keeps the match exact, the dropdown shows the ID that was looked up
test('short HMDB ID matches exactly and shows the padded ID', async ({ page }) => {
  await page.fill('#query-input', 'hmdb:02714');
  await page.click('button[type="submit"]');

  await expect(page.locator('.match-item')).toContainText('METHANE');
  await expect(page.locator('.match-status')).toHaveText('✓ Match Found: Exact');
  await page.click('.query-type-expandable-btn');
  await expect(page.locator('.query-type-conversion')).toContainText('HMDB0002714');
});

test('HMDB ID typed in full shows no read-as dropdown', async ({ page }) => {
  await page.fill('#query-input', 'HMDB0002111');
  await page.click('button[type="submit"]');

  await expect(page.locator('.match-item')).toContainText('WATER');
  await expect(page.locator('.query-type')).toHaveText('Type: HMDB ID');
  await expect(page.locator('.query-type-expandable-btn')).toHaveCount(0);
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

const { test, expect } = require('@playwright/test');
const fs = require('fs');

async function submitAndWaitForResults(page, query) {
  await page.goto('/');
  await page.fill('#query-input', query);
  await page.click('button[type="submit"]');
  await expect(page.locator('.result-item').first()).toBeVisible();
}

// Covers every query type the backend can parse against the test dataset:
//   inchikey, inchi, smiles, formula, pubchem_id, smiles_or_formula
const QUERY_ALL_TYPES = [
  'MYFAKEINCHIKEY-ISRIGHTHER-E',   // inchikey      → Water
  'InChI=1S/CH4/h1H4',             // inchi         → Methane
  'C=O',                            // smiles        → Formaldehyde
  'H2O',                            // formula       → Water
  '3',                              // pubchem_id    → Formaldehyde
  'CH4',                            // smiles_or_formula → Methane
].join(' ');

const SETTINGS_CASES = [
  {
    label: 'default settings',
    apiParams: '',
    uiSetup: async (_page) => {},
  },
  {
    label: 'top_hit_only=false',
    apiParams: 'top_hit_only=false',
    uiSetup: async (page) => {
      await page.locator('#settings-toggle').click();
      await page.locator('label[aria-label="Top hit only"]').click();
    },
  },
  {
    label: 'first_block_matches=false',
    apiParams: 'first_block_matches=false',
    uiSetup: async (page) => {
      await page.locator('#settings-toggle').click();
      await page.locator('label[aria-label="First block matches"]').click();
    },
  },
  {
    label: 'top_hit_only=false, first_block_matches=false',
    apiParams: 'top_hit_only=false&first_block_matches=false',
    uiSetup: async (page) => {
      await page.locator('#settings-toggle').click();
      await page.locator('label[aria-label="Top hit only"]').click();
      await page.locator('label[aria-label="First block matches"]').click();
    },
  },
];

async function submitWithSettings(page, query, uiSetup) {
  await page.goto('/');
  await uiSetup(page);
  await page.fill('#query-input', query);
  await page.click('button[type="submit"]');
  await expect(page.locator('.result-item').first()).toBeVisible();
}

test('download buttons hidden before first query', async ({ page }) => {
  await page.goto('/');
  await expect(page.locator('#download-buttons')).not.toBeVisible();
});

test('download buttons appear after successful query', async ({ page }) => {
  await submitAndWaitForResults(page, 'MYFAKEINCHIKEY-ISRIGHTHER-E');
  await expect(page.locator('#download-buttons')).toBeVisible();
});

test('CSV download contains correct headers', async ({ page }) => {
  await submitAndWaitForResults(page, 'MYFAKEINCHIKEY-ISRIGHTHER-E');

  const [download] = await Promise.all([
    page.waitForEvent('download'),
    page.locator('#download-csv').click(),
  ]);

  const filePath = await download.path();
  const content = fs.readFileSync(filePath, 'utf-8');
  const header = content.split('\n')[0];

  expect(header).toBe(
    'query,query_type,translated_query,found_match,match_level,error_message,pubchem_cid,inchikey,inchi,smiles,compound_name,molecular_formula,exact_mass,literature_count,patent_count'
  );
});

test('CSV download contains correct data for matched compound', async ({ page }) => {
  await submitAndWaitForResults(page, 'MYFAKEINCHIKEY-ISRIGHTHER-E');

  const [download] = await Promise.all([
    page.waitForEvent('download'),
    page.locator('#download-csv').click(),
  ]);

  const filePath = await download.path();
  const lines = fs.readFileSync(filePath, 'utf-8').trim().split('\n');

  expect(lines).toHaveLength(2); // header + 1 data row
  expect(lines[1]).toContain('MYFAKEINCHIKEY-ISRIGHTHER-E');
  expect(lines[1]).toContain('inchikey');
  expect(lines[1]).toContain('true');
  expect(lines[1]).toContain('Water');
  expect(lines[1]).toContain('H2O');
});

test('CSV download has empty compound fields for no-match', async ({ page }) => {
  await submitAndWaitForResults(page, 'ZZZZZZZZZZZZZZ-ZZZZZZZZZZ-Z');
  await expect(page.locator('.match-status')).toHaveText('✗ No match');

  const [download] = await Promise.all([
    page.waitForEvent('download'),
    page.locator('#download-csv').click(),
  ]);

  const filePath = await download.path();
  const lines = fs.readFileSync(filePath, 'utf-8').trim().split('\n');

  expect(lines).toHaveLength(2);
  const headerCols = lines[0].split(',').length;
  const dataCols = lines[1].split(',').length;
  expect(dataCols).toBe(headerCols);
  expect(lines[1]).toContain('false');
  // pubchem_cid and compound fields are empty — row ends with many commas
  expect(lines[1]).toMatch(/false,[^,]*,[^,]*,,,,,,,,,$/);
});

// Mix of matches and no-matches — exercises the no-match CSV branch in script.js
// against the server's CSV output. The bug this catches: the no-match branch was
// missing the translated_query column, producing 14 fields vs the 15-field header.
const QUERY_WITH_NOMATCH = [
  'MYFAKEINCHIKEY-ISRIGHTHER-E',  // inchikey → match
  'ZZZZZZZZZZZZZZ-ZZZZZZZZZZ-Z',  // inchikey → no match
  'C=O',                           // smiles   → match
].join(' ');

test('CSV download matches API response including no-match rows', async ({ page }) => {
  const apiResponse = await page.request.post('/match?format=csv', {
    data: { queries: QUERY_WITH_NOMATCH },
    headers: { 'Content-Type': 'application/json' },
  });
  const apiCsv = await apiResponse.text();

  await submitAndWaitForResults(page, QUERY_WITH_NOMATCH);

  const [download] = await Promise.all([
    page.waitForEvent('download'),
    page.locator('#download-csv').click(),
  ]);
  const csv = fs.readFileSync(await download.path(), 'utf-8');

  expect(csv).toBe(apiCsv);
});

test('JSON download matches API response structure', async ({ page }) => {
  await submitAndWaitForResults(page, 'MYFAKEINCHIKEY-ISRIGHTHER-E MYFAKEINCHIKEY-ANOTHERONE-E');

  const [download] = await Promise.all([
    page.waitForEvent('download'),
    page.locator('#download-json').click(),
  ]);

  const filePath = await download.path();
  const data = JSON.parse(fs.readFileSync(filePath, 'utf-8'));

  expect(data).toHaveLength(2);
  expect(data[0].query).toBe('MYFAKEINCHIKEY-ISRIGHTHER-E');
  expect(data[0].found_match).toBe(true);
  expect(data[0].matches[0].compound_name).toBe('Water');
  expect(data[1].query).toBe('MYFAKEINCHIKEY-ANOTHERONE-E');
  expect(data[1].matches[0].compound_name).toBe('Methane');
});

for (const { label, apiParams, uiSetup } of SETTINGS_CASES) {
  test(`JSON download matches API response (${label})`, async ({ page }) => {
    const url = apiParams ? `/match?${apiParams}` : '/match';
    const apiResponse = await page.request.post(url, {
      data: { queries: QUERY_ALL_TYPES },
      headers: { 'Content-Type': 'application/json' },
    });
    const apiData = await apiResponse.json();

    await submitWithSettings(page, QUERY_ALL_TYPES, uiSetup);

    const [download] = await Promise.all([
      page.waitForEvent('download'),
      page.locator('#download-json').click(),
    ]);
    const data = JSON.parse(fs.readFileSync(await download.path(), 'utf-8'));

    expect(data).toEqual(apiData);
  });

  test(`CSV download matches API CSV response (${label})`, async ({ page }) => {
    const csvParams = apiParams ? `${apiParams}&format=csv` : 'format=csv';
    const apiResponse = await page.request.post(`/match?${csvParams}`, {
      data: { queries: QUERY_ALL_TYPES },
      headers: { 'Content-Type': 'application/json' },
    });
    const apiCsv = await apiResponse.text();

    await submitWithSettings(page, QUERY_ALL_TYPES, uiSetup);

    const [download] = await Promise.all([
      page.waitForEvent('download'),
      page.locator('#download-csv').click(),
    ]);
    const csv = fs.readFileSync(await download.path(), 'utf-8');

    expect(csv).toBe(apiCsv);
  });
}

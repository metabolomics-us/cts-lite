const { test, expect } = require('@playwright/test');

// These tests never reach the real PubChem API: /match is intercepted with
// page.route and answered with a canned JSON array carrying the match_level
// the backend would emit for a live fallback hit

const COMPOUND = {
  identifier: '2519',
  inchikey: 'RYYVLZVUVIJVGH-UHFFFAOYSA-N',
  inchi: 'InChI=1S/C8H10N4O2',
  smiles: 'CN1C=NC2=C1C(=O)N(C(=O)N2C)C',
  compound_name: 'Caffeine',
  molecular_formula: 'C8H10N4O2',
  exact_mass: 194.08,
  literature_count: 100,
  patent_count: 50,
  annotation_type_count: 30,
};

function result(matchLevel, over = {}) {
  return {
    query: 'RYYVLZVUVIJVGH-UHFFFAOYSA-N',
    query_type: 'inchikey',
    found_match: true,
    match_level: matchLevel,
    matches: [COMPOUND],
    error_message: '',
    ...over,
  };
}

// mockMatch answers /match with a fixed JSON array (the non-streaming path)
async function mockMatch(page, results) {
  await page.route('**/match*', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(results) }));
}

test.beforeEach(async ({ page }) => {
  await page.route('**/classyfire/status', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ up: true }) }));
});

async function submit(page) {
  await page.goto('/');
  await page.fill('#query-input', 'RYYVLZVUVIJVGH-UHFFFAOYSA-N');
  await page.click('button[type="submit"]');
}

// The live-lookup levels also contain "Exact", so without an explicit check
// ahead of the exact one they render as an ordinary local match
test('a PubChem Live match is not reported as a plain local match', async ({ page }) => {
  await mockMatch(page, [result('PubChem Live: Exact InChIKey')]);
  await submit(page);

  await expect(page.locator('.match-status')).toHaveText('✓ Match Found: PubChem Live');
  await expect(page.locator('.match-status.pubchem-live-match')).toHaveCount(1);
  await expect(page.locator('.match-status.exact-match')).toHaveCount(0);
});

test('a PubChem Live formula match carries the same badge', async ({ page }) => {
  await mockMatch(page, [result('PubChem Live: Formula (top hit)', { query_type: 'formula' })]);
  await submit(page);

  await expect(page.locator('.match-status')).toHaveText('✓ Match Found: PubChem Live');
  await expect(page.locator('.match-status.pubchem-live-match')).toHaveCount(1);
});

test('a local match still reports as Exact', async ({ page }) => {
  await mockMatch(page, [result('Exact InChIKey')]);
  await submit(page);

  await expect(page.locator('.match-status')).toHaveText('✓ Match Found: Exact');
  await expect(page.locator('.match-status.exact-match')).toHaveCount(1);
  await expect(page.locator('.match-status.pubchem-live-match')).toHaveCount(0);
});

// Local and live hits in one response each keep their own badge
test('local and PubChem Live matches are distinguishable in the same result set', async ({ page }) => {
  await mockMatch(page, [
    result('Exact InChIKey'),
    result('PubChem Live: Exact InChIKey', { query: 'BSYNRYMUTXBXSQ-UHFFFAOYSA-N' }),
  ]);
  await submit(page);

  await expect(page.locator('.match-status.exact-match')).toHaveCount(1);
  await expect(page.locator('.match-status.pubchem-live-match')).toHaveCount(1);
});

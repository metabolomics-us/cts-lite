const { test, expect } = require('@playwright/test');
const fs = require('fs');

// These tests never touch the real ClassyFire service: every /match call is
// intercepted with page.route and answered with a canned NDJSON / CSV / JSON body.
// "Mid-stream" UI states are observed as stable end states by deliberately
// omitting the trailing `done` line or a key's `classyfire` line.

const CAPPED_NOTE = 'Only the top 3 hits of each query are classified for latency considerations';

const CF = {
  kingdom: 'Organic compounds',
  superclass: 'Organoheterocyclic compounds',
  class: 'Imidazopyrimidines',
  subclass: 'Purines and purine derivatives',
  direct_parent: 'Xanthines',
  description: 'A xanthine alkaloid',
};

// match builds a compound matching the JSON shape the backend emits
function match(over = {}) {
  return {
    identifier: '2519',
    inchikey: 'RYYVLZVUVIJVGH-UHFFFAOYSA-N',
    inchi: 'InChI=1S/C8H10N4O2',
    smiles: 'CN1C=NC2=C1C(=O)N(C(=O)N2C)C',
    compound_name: 'Caffeine',
    molecular_formula: 'C8H10N4O2',
    exact_mass: 194.08,
    literature_count: 100,
    patent_count: 50,
    ...over,
  };
}

// cts-lite matches by identifier (no name lookup), so the query is the InChIKey itself
function result(matches, over = {}) {
  return { query: 'RYYVLZVUVIJVGH-UHFFFAOYSA-N', query_type: 'inchikey', found_match: true, match_level: 'exact', matches, error_message: '', ...over };
}

// nd joins objects into an NDJSON body
const nd = (...objs) => objs.map((o) => JSON.stringify(o)).join('\n') + '\n';

// mockMatch intercepts every /match request with a fixed NDJSON body
async function mockMatch(page, body) {
  await page.route('**/match*', (route) =>
    route.fulfill({ status: 200, contentType: 'application/x-ndjson', body }));
}

// enableClassyFireAndSubmit opens settings, turns on ClassyFire and submits a query
async function enableClassyFireAndSubmit(page, query = 'RYYVLZVUVIJVGH-UHFFFAOYSA-N') {
  await page.goto('/');
  await page.locator('#settings-toggle').click();
  await page.locator('label[aria-label="ClassyFire chemical classes"]').click();
  await page.fill('#query-input', query);
  await page.click('button[type="submit"]');
}

// A failed classification surfaces an error message on the match card
test('classyfire service-down error is shown on the card', async ({ page }) => {
  await mockMatch(page, nd(
    { type: 'matches', results: [result([match()])], unique: 1, queue: 1 },
    { type: 'classyfire', inchikey: 'RYYVLZVUVIJVGH-UHFFFAOYSA-N', info: { error: 'ClassyFire is currently unavailable' }, queue: 1 },
    { type: 'done' },
  ));

  await enableClassyFireAndSubmit(page);

  const card = page.locator('.result-item').first();
  await expect(card).toContainText('Error:');
  await expect(card).toContainText('ClassyFire is currently unavailable');
});

// Matches past the top 3 carry a "Skipped" warning instead of a classification
test('matches beyond the top 3 show the capped warning', async ({ page }) => {
  const keys = ['AAAAAAAAAAAAAA-AAAAAAAAAA-A', 'BBBBBBBBBBBBBB-BBBBBBBBBB-B', 'CCCCCCCCCCCCCC-CCCCCCCCCC-C', 'DDDDDDDDDDDDDD-DDDDDDDDDD-D'];
  const matches = keys.map((k, i) =>
    i < 3 ? match({ inchikey: k }) : match({ inchikey: k, classyfire: { error: CAPPED_NOTE } }));

  await mockMatch(page, nd(
    { type: 'matches', results: [result(matches)], unique: 3, queue: 1 },
    ...keys.slice(0, 3).map((k) => ({ type: 'classyfire', inchikey: k, info: CF, queue: 1 })),
    { type: 'done' },
  ));

  await enableClassyFireAndSubmit(page);

  const card = page.locator('.result-item').first();
  await expect(card).toContainText('Skipped:');
  await expect(card).toContainText(CAPPED_NOTE);
});

// A match still awaiting classification shows the "Queued" spinner
test('queued spinner shows while a match is unclassified', async ({ page }) => {
  // matches arrive, but the classyfire line for the inchikey never does, so it stays queued
  await mockMatch(page, nd(
    { type: 'matches', results: [result([match()])], unique: 1, queue: 1 },
    { type: 'done' },
  ));

  await enableClassyFireAndSubmit(page);

  const card = page.locator('.result-item').first();
  await expect(card.locator('.cf-queued')).toBeVisible();
  await expect(card.locator('.cf-queued .inline-spinner')).toBeVisible();
});

// Downloads stay disabled while classification is still running (no done line)
test('downloads are disabled while classyfire is working', async ({ page }) => {
  await mockMatch(page, nd(
    { type: 'matches', results: [result([match()])], unique: 1, queue: 1 },
    { type: 'classyfire', inchikey: 'RYYVLZVUVIJVGH-UHFFFAOYSA-N', info: CF, queue: 1 },
    // no `done` line: the run never signals completion
  ));

  await enableClassyFireAndSubmit(page);

  await expect(page.locator('#download-buttons')).toBeVisible();
  await expect(page.locator('#download-buttons')).toHaveClass(/loading/);
  await expect(page.locator('#download-csv')).toBeDisabled();
  await expect(page.locator('#download-json')).toBeDisabled();
});

// Once classification finishes, downloads become available again
test('downloads are re-enabled after classyfire finishes', async ({ page }) => {
  await mockMatch(page, nd(
    { type: 'matches', results: [result([match()])], unique: 1, queue: 1 },
    { type: 'classyfire', inchikey: 'RYYVLZVUVIJVGH-UHFFFAOYSA-N', info: CF, queue: 1 },
    { type: 'done' },
  ));

  await enableClassyFireAndSubmit(page);

  await expect(page.locator('#download-buttons')).not.toHaveClass(/loading/);
  await expect(page.locator('#download-csv')).toBeEnabled();
  await expect(page.locator('#download-json')).toBeEnabled();
});

// The progress bar denominator is the unique-InChIKey count, not the match-row count
test('progress bar counts unique inchikeys', async ({ page }) => {
  // 4 match rows but only 3 unique inchikeys (the caffeine inchikey appears twice), so total = 3
  const dupKey = 'EEEEEEEEEEEEEE-EEEEEEEEEE-E';
  const matches = [match(), match(), match({ inchikey: dupKey }), match()]; // caffeine inchikey x3 + dupKey
  await mockMatch(page, nd(
    { type: 'matches', results: [result(matches)], unique: 3, queue: 1 },
    { type: 'classyfire', inchikey: 'RYYVLZVUVIJVGH-UHFFFAOYSA-N', info: CF, queue: 1 },
    // no done: progress freezes at 1 / 3
  ));

  await enableClassyFireAndSubmit(page);

  await expect(page.locator('#cf-progress')).toBeVisible();
  await expect(page.locator('.cf-progress-label')).toContainText('1 / 3');
  await expect(page.locator('.cf-progress-label')).not.toContainText('/ 4');
});

// The queue indicator appears when more than one request is contending
test('queue indicator shows when multiple requests are queued', async ({ page }) => {
  await mockMatch(page, nd(
    { type: 'matches', results: [result([match()])], unique: 1, queue: 2 },
    // no done: indicator stays visible
  ));

  await enableClassyFireAndSubmit(page);

  await expect(page.locator('#cf-queue')).toBeVisible();
  await expect(page.locator('#cf-queue')).toContainText('1'); // 1 other request
});

// When the queue depth drops to 1 (e.g. another client closed its tab) the indicator hides.
// The backend depth bookkeeping on disconnect is covered by TestClassyFireQueueDepthDropsWhenClientDisconnects.
test('queue indicator hides when the queue drops to one', async ({ page }) => {
  await mockMatch(page, nd(
    { type: 'matches', results: [result([match()])], unique: 1, queue: 2 },
    { type: 'classyfire', inchikey: 'RYYVLZVUVIJVGH-UHFFFAOYSA-N', info: CF, queue: 1 },
    { type: 'done' },
  ));

  await enableClassyFireAndSubmit(page);

  await expect(page.locator('#cf-queue')).not.toBeVisible();
});

// Mirrors the real two-tab scenario from the request-B page: B opens while request
// A is still classifying (queue depth 2) so the multi-request warning shows, then A finishes
// mid-stream (a later line reports depth 1) and the warning must clear even though
// B keeps classifying. Fed line-by-line so the test controls when each line lands,
// the way real streamed timing would
test('queue warning clears when the other request finishes mid-stream', async ({ page }) => {
  // Replace fetch for /match with a hand-fed NDJSON stream so the test decides when
  // each line arrives and can observe the warning between lines
  await page.addInitScript(() => {
    const realFetch = window.fetch.bind(window);
    window.fetch = (input, init) => {
      const url = typeof input === 'string' ? input : input.url;
      if (!url.includes('/match')) return realFetch(input, init);
      const stream = new ReadableStream({
        start(controller) {
          const enc = new TextEncoder();
          window.__push = (obj) => controller.enqueue(enc.encode(JSON.stringify(obj) + '\n'));
          window.__close = () => controller.close();
        },
      });
      return Promise.resolve(new Response(stream, { status: 200, headers: { 'Content-Type': 'application/x-ndjson' } }));
    };
  });

  const KEY_A = 'RYYVLZVUVIJVGH-UHFFFAOYSA-N';
  const KEY_B = 'AAAAAAAAAAAAAA-AAAAAAAAAA-A';
  const matches = [match({ inchikey: KEY_A }), match({ inchikey: KEY_B })];

  await enableClassyFireAndSubmit(page, `${KEY_A} ${KEY_B}`);
  await page.waitForFunction(() => typeof window.__push === 'function');

  const push = (obj) => page.evaluate((o) => window.__push(o), obj);

  // Request B opens while request A is still in the queue
  await push({ type: 'matches', results: [result(matches)], unique: 2, queue: 2 });
  await expect(page.locator('#cf-queue')).toBeVisible();
  await expect(page.locator('#cf-queue')).toContainText('1'); // 1 other request

  // B classifies its first key, request A is still present so the warning stays
  await push({ type: 'classyfire', inchikey: KEY_A, info: CF, queue: 2 });
  await expect(page.locator('#cf-queue')).toBeVisible();

  // Request A finishes: the next line B receives reports the lower depth, warning clears
  await push({ type: 'classyfire', inchikey: KEY_B, info: CF, queue: 1 });
  await expect(page.locator('#cf-queue')).not.toBeVisible();

  await page.evaluate(() => window.__close());
});

// The applied-settings label reflects that ClassyFire was enabled for the request
test('applied settings label notes ClassyFire', async ({ page }) => {
  await mockMatch(page, nd(
    { type: 'matches', results: [result([match()])], unique: 1, queue: 1 },
    { type: 'classyfire', inchikey: 'RYYVLZVUVIJVGH-UHFFFAOYSA-N', info: CF, queue: 1 },
    { type: 'done' },
  ));

  await enableClassyFireAndSubmit(page);

  await expect(page.locator('#applied-settings-label')).toContainText('ClassyFire');
});

// ClassyFire-supplied strings are escaped, not injected as markup
test('classyfire description is HTML-escaped', async ({ page }) => {
  const xss = '<img src=x onerror=alert(1)>';
  await mockMatch(page, nd(
    { type: 'matches', results: [result([match()])], unique: 1, queue: 1 },
    { type: 'classyfire', inchikey: 'RYYVLZVUVIJVGH-UHFFFAOYSA-N', info: { ...CF, description: xss }, queue: 1 },
    { type: 'done' },
  ));

  await enableClassyFireAndSubmit(page);

  // The literal text is present (escaped) and no <img> element was injected from it
  await expect(page.locator('.result-item').first()).toContainText(xss);
  await expect(page.locator('.result-item img[onerror]')).toHaveCount(0);
});

// With ClassyFire off, the request is a normal JSON match with no ClassyFire UI
test('classyfire is off by default', async ({ page }) => {
  await page.route('**/match*', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify([result([match()])]) }));

  await page.goto('/');
  await expect(page.locator('#classyfire-enabled')).not.toBeChecked();
  await page.fill('#query-input', 'RYYVLZVUVIJVGH-UHFFFAOYSA-N');
  await page.click('button[type="submit"]');

  await expect(page.locator('.result-item').first()).toBeVisible();
  await expect(page.locator('.classyfire-heading')).toHaveCount(0);
  await expect(page.locator('#cf-progress')).not.toBeVisible();
});

// The client rejects >1000 identifiers before sending a ClassyFire request
test('classyfire blocks more than 1,000 identifiers client-side', async ({ page }) => {
  let requested = false;
  await page.route('**/match*', (route) => { requested = true; route.fulfill({ status: 200, body: '' }); });

  await page.goto('/');
  await page.locator('#settings-toggle').click();
  await page.locator('label[aria-label="ClassyFire chemical classes"]').click();
  await page.fill('#query-input', Array.from({ length: 1001 }, () => 'O').join(' '));
  await page.click('button[type="submit"]');

  await expect(page.locator('#output-text')).toContainText('limit to 1,000 identifiers');
  expect(requested).toBe(false);
});

// The CSV/JSON the browser downloads matches what the API returns for the same data.
test('downloaded CSV and JSON match the API response', async ({ page }) => {
  const FINAL = [result([match({ classyfire: CF })])];

  // The matches line carries the compound without classyfire; the classyfire line attaches it.
  const streamBody = nd(
    { type: 'matches', results: [result([match()])], unique: 1, queue: 1 },
    { type: 'classyfire', inchikey: 'RYYVLZVUVIJVGH-UHFFFAOYSA-N', info: CF, queue: 1 },
    { type: 'done' },
  );

  // Server-format CSV, authored to match api/handler.go writeResultsAsCSV exactly.
  const header = 'query,query_type,converted_query,found_match,match_level,error_message,pubchem_cid,inchikey,inchi,smiles,compound_name,molecular_formula,exact_mass,literature_count,patent_count,classyfire_kingdom,classyfire_superclass,classyfire_class,classyfire_subclass,classyfire_direct_parent,classyfire_description,classyfire_error';
  const dataRow = 'RYYVLZVUVIJVGH-UHFFFAOYSA-N,inchikey,,true,exact,,2519,RYYVLZVUVIJVGH-UHFFFAOYSA-N,InChI=1S/C8H10N4O2,CN1C=NC2=C1C(=O)N(C(=O)N2C)C,Caffeine,C8H10N4O2,194.08,100,50,Organic compounds,Organoheterocyclic compounds,Imidazopyrimidines,Purines and purine derivatives,Xanthines,A xanthine alkaloid,';
  const apiCsv = header + '\n' + dataRow + '\n';

  await page.route('**/match*', (route) => {
    const url = route.request().url();
    if (url.includes('format=csv')) {
      return route.fulfill({ status: 200, contentType: 'text/csv', body: apiCsv });
    }
    if (url.includes('stream=true')) {
      return route.fulfill({ status: 200, contentType: 'application/x-ndjson', body: streamBody });
    }
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(FINAL) });
  });

  await enableClassyFireAndSubmit(page);
  await expect(page.locator('#download-buttons')).not.toHaveClass(/loading/); // wait for done

  // Fetch the API responses from the page context (also intercepted by the route)
  const apiJson = await page.evaluate(async () => {
    const r = await fetch('/match?classyfire=true', {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ queries: 'RYYVLZVUVIJVGH-UHFFFAOYSA-N' }),
    });
    return r.json();
  });
  const apiCsvFetched = await page.evaluate(async () => {
    const r = await fetch('/match?classyfire=true&format=csv', {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ queries: 'RYYVLZVUVIJVGH-UHFFFAOYSA-N' }),
    });
    return r.text();
  });

  // Client CSV download === API CSV
  const [csvDownload] = await Promise.all([
    page.waitForEvent('download'),
    page.locator('#download-csv').click(),
  ]);
  const clientCsv = fs.readFileSync(await csvDownload.path(), 'utf-8');
  expect(clientCsv).toBe(apiCsvFetched);

  // Client JSON download === API JSON (compare parsed, key order independent)
  const [jsonDownload] = await Promise.all([
    page.waitForEvent('download'),
    page.locator('#download-json').click(),
  ]);
  const clientJson = JSON.parse(fs.readFileSync(await jsonDownload.path(), 'utf-8'));
  expect(clientJson).toEqual(apiJson);
});

// The /classyfire/status endpoint drives whether the toggle is usable. These tests
// mock it (registered before goto, since initClassyfireStatus fetches it on load)
// so the real ClassyFire backend is never probed.

// mockStatus answers /classyfire/status with the given reachability
async function mockStatus(page, up) {
  await page.route('**/classyfire/status', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ up }) }));
}

// The ClassyFire toggle label, the clickable surface of the row
const cfLabel = 'label[aria-label="ClassyFire chemical classes"]';

// When the service is up the toggle behaves normally and never shows the modal
test('toggle works normally when ClassyFire is up', async ({ page }) => {
  await mockStatus(page, true);
  await page.goto('/');
  await page.locator('#settings-toggle').click();

  await expect(page.locator('#classyfire-enabled')).toBeEnabled();
  await expect(page.locator('#classyfire-row')).not.toHaveClass(/cf-disabled/);

  await page.locator(cfLabel).click();
  await expect(page.locator('#classyfire-enabled')).toBeChecked();
  await expect(page.locator('#cf-down-modal')).toBeHidden();
});

// When the service is down the toggle is locked off and cannot be enabled
test('toggle is disabled and stays off when ClassyFire is down', async ({ page }) => {
  await mockStatus(page, false);
  await page.goto('/');
  await page.locator('#settings-toggle').click();

  await expect(page.locator('#classyfire-row')).toHaveClass(/cf-disabled/);
  await expect(page.locator('#classyfire-enabled')).toBeDisabled();
  await expect(page.locator('#classyfire-enabled')).not.toBeChecked();

  // Clicking the toggle does not enable it (force past Playwright's disabled-control check)
  await page.locator(cfLabel).click({ force: true });
  await expect(page.locator('#classyfire-enabled')).not.toBeChecked();
});

// Clicking the down toggle opens the explainer modal with a link to the docs
test('clicking the down toggle opens the modal with a docs link', async ({ page }) => {
  await mockStatus(page, false);
  await page.goto('/');
  await page.locator('#settings-toggle').click();

  await expect(page.locator('#cf-down-modal')).toBeHidden();
  await page.locator(cfLabel).click({ force: true });

  const modal = page.locator('#cf-down-modal');
  await expect(modal).toBeVisible();
  await expect(modal).toContainText('ClassyFire is currently down');
  await expect(modal.locator('.cf-modal-link')).toHaveAttribute('href', '/docs#classyfire');
});

// The modal closes via the close button and via the Escape key
test('the down modal closes via button and Escape', async ({ page }) => {
  await mockStatus(page, false);
  await page.goto('/');
  await page.locator('#settings-toggle').click();

  // Close button
  await page.locator(cfLabel).click({ force: true });
  await expect(page.locator('#cf-down-modal')).toBeVisible();
  await page.locator('#cf-modal-close').click();
  await expect(page.locator('#cf-down-modal')).toBeHidden();

  // Escape key (the close-button click above also closed the settings panel, so reopen it)
  await page.locator('#settings-toggle').click();
  await page.locator(cfLabel).click({ force: true });
  await expect(page.locator('#cf-down-modal')).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(page.locator('#cf-down-modal')).toBeHidden();
});

// The docs question-mark link in the row navigates instead of opening the modal
test('the row docs link does not trigger the down modal', async ({ page }) => {
  await mockStatus(page, false);
  await page.goto('/');
  await page.locator('#settings-toggle').click();

  const link = page.locator('#classyfire-row a[href="/docs#classyfire"]');
  const [popup] = await Promise.all([
    page.waitForEvent('popup'),
    link.click(),
  ]);
  await expect(page.locator('#cf-down-modal')).toBeHidden();
  await popup.close();
});

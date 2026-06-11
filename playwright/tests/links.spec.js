const { test, expect } = require('@playwright/test');

// Collect every anchor href on a page, deduplicated
async function collectHrefs(page, path) {
  await page.goto(path);
  const hrefs = await page
    .locator('a[href]')
    .evaluateAll((els) => els.map((el) => el.getAttribute('href')));
  return [...new Set(hrefs)];
}

function isExternal(href) {
  return /^https?:\/\//.test(href);
}

// Check that each internal link on the page returns a 200 from the server
async function checkInternalLinks(page, path) {
  const hrefs = (await collectHrefs(page, path)).filter((h) => !isExternal(h));
  expect(hrefs.length).toBeGreaterThan(0);

  for (const href of hrefs) {
    const target = new URL(href, page.url());
    const response = await page.request.get(target.pathname);
    expect(response.status(), `${href} on ${path}`).toBe(200);
  }
}

// Check that each link with a fragment points to an element that exists on the target page
async function checkAnchorTargets(page, path) {
  const withFragments = (await collectHrefs(page, path)).filter(
    (h) => !isExternal(h) && h.includes('#') && !h.endsWith('#')
  );

  for (const href of withFragments) {
    const target = new URL(href, page.url());
    const id = decodeURIComponent(target.hash.slice(1));

    if (target.pathname !== new URL(page.url()).pathname) {
      await page.goto(target.pathname);
    }
    await expect(page.locator(`#${id}`), `${href} on ${path}`).toHaveCount(1);

    // Return to the source page so relative hrefs keep resolving correctly
    await page.goto(path);
  }
}

test.describe('internal links', () => {
  test('home page links return 200', async ({ page }) => {
    await checkInternalLinks(page, '/');
  });

  test('docs page links return 200', async ({ page }) => {
    await checkInternalLinks(page, '/docs');
  });

  test('home page anchor links point to existing elements', async ({ page }) => {
    await checkAnchorTargets(page, '/');
  });

  test('docs page anchor links point to existing elements', async ({ page }) => {
    await checkAnchorTargets(page, '/docs');
  });
});

test.describe('external links', () => {
  // These hit real third-party sites, so give them room to respond
  test.setTimeout(120_000);

  for (const path of ['/', '/docs']) {
    test(`external links on ${path} respond successfully`, async ({ page }) => {
      const hrefs = (await collectHrefs(page, path)).filter(isExternal);
      expect(hrefs.length).toBeGreaterThan(0);

      for (const href of hrefs) {
        const response = await page.request.get(href, { timeout: 30_000 });
        expect(response.status(), `${href} on ${path}`).toBeLessThan(400);
      }
    });
  }
});

test('result compound name links to its PubChem compound page', async ({ page }) => {
  await page.goto('/');
  await page.fill('#query-input', '1');
  await page.click('button[type="submit"]');

  const link = page.locator('.match-item a');
  await expect(link).toHaveAttribute(
    'href',
    /^https:\/\/pubchem\.ncbi\.nlm\.nih\.gov\/compound\/1/
  );
  await expect(link).toHaveAttribute('target', '_blank');
});

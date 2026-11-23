// Regenerates docs/images from a throwaway server with fictional demo data.
// Run with: make screenshots
const { test } = require('@playwright/test');
const OUT = '../docs/images';
const ORIGIN = 'http://127.0.0.1:8099';
test.skip(!process.env.SCREENSHOTS, 'set SCREENSHOTS=1 (make screenshots)');

const base = {
  mode: 'in_person', service_count: 1, notary_name: 'Alex Rivera', commission_id: '01XX0000000',
  identity_method: 'credential', credential_type: 'New York driver license', witnesses: [],
  verification: 'Reviewed an unexpired credential and confirmed the signer appeared in person.',
};
const entries = [
  { ...base, source: 'contemporaneous', contemporaneous_attestation: true, occurred_at: '2026-11-20T14:15:00Z',
    act_type: 'Acknowledgment', principals: [{ name: 'Jordan Sample', address: '12 Example St, Albany, NY 12207' }] },
  { ...base, source: 'contemporaneous', contemporaneous_attestation: true, occurred_at: '2026-11-21T16:40:00Z',
    act_type: 'Jurat', service_count: 2, principals: [{ name: 'Casey Demo', address: '48 Sample Ave, Troy, NY 12180' }, { name: 'Morgan Test', address: '48 Sample Ave, Troy, NY 12180' }] },
  { ...base, source: 'paper_import', occurred_at: '2025-06-03', time_not_recorded: true, source_reference: 'Book 2, page 14, row 3',
    act_type: 'Acknowledgment', principals: [{ name: 'Taylor Placeholder', address: '7 Fictional Rd, Schenectady, NY 12305' }] },
];

async function login(page) {
  await page.goto('/');
  await page.getByLabel('Journal password').fill('e2e-test-password-not-secret');
  await page.getByRole('button', { name: 'Unlock journal' }).click();
  await page.waitForSelector('#application:not([hidden])');
}

test('capture', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.goto('/');
  await page.screenshot({ path: `${OUT}/login.png` });
  await login(page);
  for (const e of entries) {
    const r = await page.request.post('/api/entries', { data: e, headers: { Origin: ORIGIN } });
    if (!r.ok()) throw new Error(await r.text());
  }
  await page.reload(); await page.waitForSelector('#application:not([hidden])');
  await page.waitForSelector('#entries tr');
  await page.screenshot({ path: `${OUT}/journal.png` });
  await page.locator('#entries tr button').first().click();
  await page.waitForSelector('#detail[open]');
  await page.screenshot({ path: `${OUT}/record-detail.png` });
  await page.keyboard.press('Escape');
  await page.locator('nav [data-view="entry"]').click();
  await page.screenshot({ path: `${OUT}/new-entry.png`, fullPage: true });
  await page.locator('nav [data-view="archive"]').click();
  await page.getByRole('button', { name: 'Verify journal integrity' }).click();
  await page.waitForFunction(() => /Verified/.test(document.querySelector('#integrity-result').textContent));
  await page.screenshot({ path: `${OUT}/archive.png` });

  await page.setViewportSize({ width: 390, height: 844 });
  await page.locator('[data-view="journal"]').first().click();
  await page.screenshot({ path: `${OUT}/mobile-journal.png` });
  await page.locator('#entries tr button').first().click();
  await page.waitForSelector('#detail[open]');
  await page.screenshot({ path: `${OUT}/mobile-record-detail.png` });
  await page.keyboard.press('Escape');
  await page.locator('[data-view="entry"]').first().click();
  await page.screenshot({ path: `${OUT}/mobile-new-entry.png` });
  await page.locator('[data-view="archive"]').first().click();
  await page.screenshot({ path: `${OUT}/mobile-archive.png`, fullPage: true });
});

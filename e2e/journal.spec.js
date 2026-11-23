const { test, expect } = require('@playwright/test');
const PASSWORD = 'e2e-test-password-not-secret';

test.describe.configure({ mode: 'serial' });

async function unlock(page) {
  await page.goto('/');
  await page.getByLabel('Journal password').fill(PASSWORD);
  await page.getByRole('button', { name: 'Unlock journal' }).click();
  await expect(page.locator('#application')).toBeVisible();
}

test('rejects a wrong password', async ({ page }) => {
  await page.goto('/');
  await page.getByLabel('Journal password').fill('wrong-wrong-wrong-wrong');
  await page.getByRole('button', { name: 'Unlock journal' }).click();
  await expect(page.locator('#login-error')).toContainText('incorrect');
});

test('create entry, attach file, correct it', async ({ page }) => {
  const errors = [];
  page.on('pageerror', e => errors.push(e.message));
  await unlock(page);
  await expect(page.locator('#empty-state')).toBeVisible();

  await page.locator('nav [data-view="entry"]').click();
  await page.getByLabel(/Date & approximate time/).fill('2026-11-23T10:30');
  await page.getByLabel('Type of notarial service').fill('Acknowledgment');
  await expect(page.getByLabel('Notary name')).toHaveValue('Alex Rivera');
  await expect(page.getByLabel('Commission / registration ID')).toHaveValue('01XX0000000');
  await page.getByLabel('Full name').fill('<b>Jane Doe</b>');
  await page.getByLabel('Full address').fill('1 Main St, Albany NY');
  await page.getByLabel('Credential type').fill('NY driver license');
  await page.getByLabel('Verification procedures').fill('Viewed credential; confirmed appearance.');
  await page.getByLabel(/making this record contemporaneously/).check();
  await page.getByRole('button', { name: 'Save journal entry' }).click();

  const row = page.locator('#entries tr').first();
  await expect(row).toContainText('<b>Jane Doe</b>'); // rendered as text, not HTML
  await expect(row.locator('b')).toHaveCount(0);

  await row.getByRole('button', { name: 'Open' }).click();
  const dialog = page.locator('#detail');
  await expect(dialog).toBeVisible();
  await expect(dialog).toContainText('Identification credential');
  await dialog.locator('input[type=file]').setInputFiles({
    name: 'scan.pdf', mimeType: 'application/pdf', buffer: Buffer.from('%PDF-1.4 test'),
  });
  await dialog.getByRole('button', { name: 'Attach to record' }).click();
  await expect(page.locator('#detail-content')).toContainText('Attachment');

  await page.emulateMedia({ media: 'print' });
  await expect(page.locator('#application')).toBeHidden();
  await expect(dialog).toBeVisible();
  await expect(dialog.getByRole('button', { name: 'Close' })).toBeHidden();
  await page.emulateMedia({ media: 'screen' });
  await dialog.getByRole('button', { name: 'Add correction' }).click();
  await page.getByLabel('Full address').fill('2 Main St, Albany NY');
  await page.getByLabel('Reason for correction').fill('Address typo');
  await page.getByRole('button', { name: 'Save correction' }).click();
  await expect(page.locator('#toast')).toContainText('Correction saved');
  await page.locator('nav [data-view="archive"]').click();
  await expect(page.locator('#toast')).toBeHidden();

  await page.locator('nav [data-view="archive"]').click();
  await expect(page.locator('nav [data-view="archive"]')).toHaveAttribute('aria-current', 'page');
  await page.getByRole('button', { name: 'Verify journal integrity' }).click();
  await expect(page.locator('#integrity-result')).toContainText('Verified 3 events');
  expect(errors).toEqual([]);
});

test('paper import requires a book reference and keeps date-only', async ({ page }) => {
  await unlock(page);
  await page.locator('nav [data-view="import"]').click();
  await page.getByLabel(/does not record an approximate time/).check();
  await expect(page.locator('input[name=occurred_at]')).toHaveAttribute('type', 'date');
  await expect(page.locator('input[name=source_reference]')).toBeVisible();
});

test('phone layout has no horizontal scroll', async ({ page }) => {
  await page.setViewportSize({ width: 375, height: 800 });
  await unlock(page);
  for (const view of ['journal', 'entry', 'archive', 'guidance']) {
    await page.locator(`[data-view="${view}"]`).first().click();
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
    expect(overflow, view).toBeLessThanOrEqual(0);
  }
  await page.locator('[data-view="journal"]').first().click();
  expect((await page.locator('#search').boundingBox()).width).toBeGreaterThan(200);
});

test('sorts newest first by default, toggles, and searches', async ({ page }) => {
  await unlock(page);
  const post = async (act, when, name) => { const r = await page.request.post('/api/entries', {
    headers: { Origin: 'http://127.0.0.1:8099' },
    data: { source: 'contemporaneous', contemporaneous_attestation: true, occurred_at: when, mode: 'in_person', act_type: act,
      service_count: 1, notary_name: 'Alex Rivera', commission_id: '01XX0000000', identity_method: 'personal_knowledge',
      verification: 'Known to notary.', witnesses: [], principals: [{ name, address: '1 Test St' }] },
  }); expect(r.ok(), await r.text()).toBeTruthy(); };
  await post('Jurat', '2020-01-05T15:00:00Z', 'Older Person');
  await post('Jurat', '2026-11-22T15:00:00Z', 'Newer Person');
  await page.reload();
  await page.waitForSelector('#entries tr');
  const names = () => page.locator('#entries tr td:nth-child(2) strong').allTextContents();
  const newerFirst = async () => { const n = await names(); return n.indexOf('Newer Person') < n.indexOf('Older Person'); };
  await expect(page.locator('#sort-order')).toHaveValue('newest');
  expect(await newerFirst()).toBe(true);
  await page.locator('#sort-order').selectOption('oldest');
  expect(await newerFirst()).toBe(false);
  await page.locator('#search').fill('older person');
  await expect(page.locator('#entries tr')).toHaveCount(1);
  await expect(page.locator('#entries tr')).toContainText('Older Person');
  await page.locator('#search').fill('zzz-no-match');
  await expect(page.locator('#empty-state')).toBeVisible();
});

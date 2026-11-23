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
  await page.getByLabel('Notary name').fill('Test Notary');
  await page.getByLabel('Commission / registration ID').fill('01TE0000000');
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
  await dialog.locator('input[type=file]').setInputFiles({
    name: 'scan.pdf', mimeType: 'application/pdf', buffer: Buffer.from('%PDF-1.4 test'),
  });
  await dialog.getByRole('button', { name: 'Attach to record' }).click();
  await expect(page.locator('#detail-content')).toContainText('Attachment');

  await dialog.getByRole('button', { name: 'Add correction' }).click();
  await page.getByLabel('Full address').fill('2 Main St, Albany NY');
  await page.getByLabel('Reason for correction').fill('Address typo');
  await page.getByRole('button', { name: 'Save correction' }).click();
  await expect(page.locator('#toast')).toContainText('Correction saved');

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
});

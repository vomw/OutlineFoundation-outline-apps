import { test, expect } from '@playwright/test';

test('Add and manage Outline servers', async ({ page }) => {
  // 1. Navigate to the app shell
  await page.goto('/index_cordova.html');

  // 2. Acknowledge privacy overlay
  await page.getByText('GOT IT').first().click();

  // 3. Add first server
  await page.getByRole('textbox').fill('ssconf://example1.invalid#TestServer1');
  // Wait for the Confirm button to be attached and visible
  await page.getByText('Confirm', { exact: true }).click();

  // 4. Add second server
  await page.getByLabel('Add server').click();
  await page.getByRole('textbox').fill('ssconf://example2.invalid#TestServer2');
  await page.getByText('Confirm', { exact: true }).click();

  // 5. Delete the first server (TestServer1) by finding its specific card first
  const serverOptionsBtn = page.locator('server-row-card')
    .filter({ hasText: 'TestServer1' })
    .getByRole('button').first();
  await serverOptionsBtn.click();

  // Click 'Forget' from the dropdown menu
  await page.getByText('Forget').first().click();

  // 6. Navigate to the About Page and verify the version string 
  await page.getByLabel('Menu').click();
  await page.getByText('About').click();

  // Assert that text containing "Version" is visible
  await expect(page.getByText(/Version .*/)).toBeVisible();

  // Return to the main page
  await page.getByText('Back').click();
});

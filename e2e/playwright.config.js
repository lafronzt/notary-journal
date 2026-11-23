const { defineConfig } = require('@playwright/test');
const fs = require('fs'), os = require('os'), path = require('path');
const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'notary-e2e-'));
module.exports = defineConfig({
  testDir: '.',
  workers: 1,
  use: { baseURL: 'http://127.0.0.1:8099', screenshot: 'only-on-failure' },
  webServer: {
    command: `../bin/notary-journal --db ${dir}/e2e.sqlite3 --listen 127.0.0.1:8099 serve`,
    url: 'http://127.0.0.1:8099/healthz',
    env: { NOTARY_PASSWORD: 'e2e-test-password-not-secret' },
    reuseExistingServer: false,
  },
});

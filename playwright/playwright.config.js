const { defineConfig } = require('@playwright/test');
const path = require('path');

const repoRoot = path.join(__dirname, '..');
const testDbPath = '/tmp/cts-lite-playwright-test.db';
const csvPath = 'dataset/test_datasets/unittest_data.csv';

module.exports = defineConfig({
  testDir: './tests',
  retries: 1,
  reporter: process.env.CI ? 'playwright-teamcity-reporter' : 'list',
  use: {
    baseURL: 'http://localhost:8081',
    channel: 'chrome',
  },
  webServer: {
    command: `rm -f ${testDbPath} && go run ./dataset/cmd/build-db/ ${csvPath} ${testDbPath} && DB_PATH=${testDbPath} PORT=8081 go run ./server`,
    port: 8081,
    cwd: repoRoot,
    reuseExistingServer: false,
    timeout: 120_000,
  },
});

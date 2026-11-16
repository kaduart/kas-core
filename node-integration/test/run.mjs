import { spawnSync } from 'node:child_process';
import { existsSync, mkdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const integrationDir = path.dirname(fileURLToPath(import.meta.url));
const pilotDir = path.resolve(integrationDir, '..', '..');
const toolsDir = path.join(pilotDir, '.tools');
const localGo = path.join(toolsDir, 'go', 'bin', process.platform === 'win32' ? 'go.exe' : 'go');
const goCommand = existsSync(localGo) ? localGo : 'go';
const binaryPath = path.join(toolsDir, process.platform === 'win32' ? 'kas-core-pilot.exe' : 'kas-core-pilot');

for (const dir of ['gocache', 'modcache', 'tmp']) mkdirSync(path.join(toolsDir, dir), { recursive: true });
const env = {
  ...process.env,
  GOTOOLCHAIN: 'local',
  GOCACHE: path.join(toolsDir, 'gocache'),
  GOMODCACHE: path.join(toolsDir, 'modcache'),
  GOTMPDIR: path.join(toolsDir, 'tmp'),
};

const build = spawnSync(goCommand, ['build', '-o', binaryPath, './cmd/kas-core'], {
  cwd: pilotDir,
  env,
  encoding: 'utf8',
  timeout: 120_000,
});
if (build.stdout) process.stdout.write(build.stdout);
if (build.stderr) process.stderr.write(build.stderr);
if (build.error) throw build.error;
if (build.status !== 0) {
  process.stderr.write(`Go build failed (status=${build.status}, signal=${build.signal ?? 'none'}).\n`);
  process.exit(build.status ?? 1);
}

for (const testFile of [
  path.join(integrationDir, 'kasConfigShadowClient.test.mjs'),
  path.join(integrationDir, 'processIntegration.test.mjs'),
  path.join(integrationDir, 'auditProcessIntegration.test.mjs'),
  path.join(integrationDir, 'tenantRegistryProcessIntegration.test.mjs'),
]) {
  const tests = spawnSync(process.execPath, ['--test', testFile], {
    cwd: integrationDir,
    env: { ...env, KAS_PILOT_BINARY: binaryPath },
    stdio: 'inherit',
    timeout: 120_000,
  });
  if (tests.error) throw tests.error;
  if (tests.status !== 0) process.exit(tests.status ?? 1);
}

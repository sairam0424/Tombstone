import { Command } from 'commander';
import chalk from 'chalk';

const apiUrl = process.env['TOMBSTONE_API_URL'] ?? 'http://localhost:8081';
const token = process.env['TOMBSTONE_TOKEN'] ?? '';

function authHeaders(): Record<string, string> {
  return { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' };
}

export function buildFlagsCommand(): Command {
  const cmd = new Command('flags').description('Manage feature flags');

  cmd
    .command('list')
    .description('List flags')
    .option('--project <id>', 'Project ID')
    .option('--env <env>', 'Environment filter')
    .action(async (opts: { project?: string; env?: string }) => {
      const url = new URL(`${apiUrl}/api/v1/flags`);
      if (opts.project) url.searchParams.set('project_id', opts.project);
      if (opts.env) url.searchParams.set('environment', opts.env);

      const res = await fetch(url.toString(), { headers: authHeaders() });
      if (!res.ok) { console.error(chalk.red(`Error: ${res.status}`)); process.exit(1); }

      const { flags } = await res.json() as { flags: Array<Record<string, unknown>> };
      if (!flags?.length) { console.log(chalk.dim('No flags found.')); return; }

      console.log(chalk.bold('\nFLAGS'));
      console.log(chalk.dim('─'.repeat(70)));
      for (const f of flags) {
        const state = f['state'] === 'ACTIVE' ? chalk.green('ACTIVE') : chalk.dim(String(f['state']));
        console.log(`${chalk.cyan(String(f['key']).padEnd(40))} ${state}`);
      }
      console.log(chalk.dim(`\n${flags.length} flag(s) total\n`));
    });

  cmd
    .command('get <key>')
    .description('Get flag details')
    .action(async (key: string) => {
      const res = await fetch(`${apiUrl}/api/v1/flags/${key}`, { headers: authHeaders() });
      if (!res.ok) { console.error(chalk.red(`Flag '${key}' not found`)); process.exit(1); }
      const flag = await res.json() as Record<string, unknown>;
      console.log(JSON.stringify(flag, null, 2));
    });

  cmd
    .command('enable <key>')
    .description('Enable a flag in an environment')
    .requiredOption('--env <env>', 'Environment (development|staging|production)')
    .action(async (key: string, opts: { env: string }) => {
      const res = await fetch(`${apiUrl}/api/v1/flags/${key}/environments/${opts.env}`, {
        method: 'PATCH',
        headers: authHeaders(),
        body: JSON.stringify({ enabled: true, rollout_pct: 100 }),
      });
      if (!res.ok) { console.error(chalk.red(`Failed: ${res.status}`)); process.exit(1); }
      console.log(chalk.green(`✓ ${key} enabled in ${opts.env}`));
    });

  cmd
    .command('disable <key>')
    .description('Disable (kill switch) a flag in an environment')
    .requiredOption('--env <env>', 'Environment')
    .action(async (key: string, opts: { env: string }) => {
      const res = await fetch(`${apiUrl}/api/v1/flags/${key}/kill`, {
        method: 'POST',
        headers: authHeaders(),
        body: JSON.stringify({ environment: opts.env, reason: 'manual_kill_switch' }),
      });
      if (!res.ok) { console.error(chalk.red(`Failed: ${res.status}`)); process.exit(1); }
      console.log(chalk.red(`✓ Kill switch activated: ${key} disabled in ${opts.env}`));
    });

  cmd
    .command('flip <key>')
    .description('Set rollout percentage for a flag')
    .requiredOption('--env <env>', 'Environment')
    .requiredOption('--pct <n>', 'Rollout percentage (0-100)')
    .option('--dry-run', 'Preview without making changes')
    .action(async (key: string, opts: { env: string; pct: string; dryRun?: boolean }) => {
      const pct = parseInt(opts.pct, 10);
      if (isNaN(pct) || pct < 0 || pct > 100) {
        console.error(chalk.red('--pct must be 0-100'));
        process.exit(1);
      }
      if (opts.dryRun) {
        console.log(chalk.yellow(`[DRY RUN] Would set ${key} to ${pct}% in ${opts.env}`));
        return;
      }
      const res = await fetch(`${apiUrl}/api/v1/flags/${key}/environments/${opts.env}`, {
        method: 'PATCH',
        headers: authHeaders(),
        body: JSON.stringify({ enabled: pct > 0, rollout_pct: pct }),
      });
      if (!res.ok) { console.error(chalk.red(`Failed: ${res.status}`)); process.exit(1); }
      console.log(chalk.green(`✓ ${key} set to ${pct}% in ${opts.env}`));
    });

  return cmd;
}

#!/usr/bin/env node
import { program } from 'commander';
import { buildFlagsCommand } from './commands/flags.js';

program
  .name('flagmind')
  .description('Tombstone CLI — manage feature flags from the terminal')
  .version('0.1.0');

program.addCommand(buildFlagsCommand());

program.parseAsync(process.argv).catch((err: unknown) => {
  console.error(err);
  process.exit(1);
});

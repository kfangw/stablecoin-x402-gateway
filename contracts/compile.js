// Compiles the demo contracts with solc-js and writes each one's ABI and
// bytecode to the files embedded by its Go package. Add a contract by extending
// the TARGETS list below.
// Usage: node contracts/compile.js
const fs = require('fs');
const path = require('path');
const solc = require('solc');

// Each target names its source file, the contract to extract, and the package
// directory that embeds abi.json and bytecode.hex.
const TARGETS = [
  { source: 'KRWTestStablecoin.sol', contract: 'KRWTestStablecoin', dest: 'token' },
  { source: 'IdentityRegistry.sol', contract: 'IdentityRegistry', dest: 'registry' },
  { source: 'RWATestAsset.sol', contract: 'RWATestAsset', dest: 'asset' },
  { source: 'EligibilityRegistry.sol', contract: 'EligibilityRegistry', dest: 'eligibility' },
];

const sources = {};
for (const t of TARGETS) {
  sources[t.source] = { content: fs.readFileSync(path.join(__dirname, t.source), 'utf8') };
}

const input = {
  language: 'Solidity',
  sources,
  settings: {
    optimizer: { enabled: true, runs: 200 },
    evmVersion: 'paris',
    outputSelection: { '*': { '*': ['abi', 'evm.bytecode.object'] } },
  },
};

const out = JSON.parse(solc.compile(JSON.stringify(input)));
const errors = (out.errors || []).filter((e) => e.severity === 'error');
if (errors.length) {
  console.error(errors.map((e) => e.formattedMessage).join('\n'));
  process.exit(1);
}

for (const t of TARGETS) {
  const c = out.contracts[t.source][t.contract];
  const dst = path.join(__dirname, '..', t.dest);
  fs.mkdirSync(dst, { recursive: true });
  fs.writeFileSync(path.join(dst, 'abi.json'), JSON.stringify(c.abi, null, 2));
  fs.writeFileSync(path.join(dst, 'bytecode.hex'), c.evm.bytecode.object);
  console.log(`compiled: ${t.dest}/abi.json, ${t.dest}/bytecode.hex`);
}

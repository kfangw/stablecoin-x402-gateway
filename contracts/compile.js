// Compiles KRWTestStablecoin.sol with solc-js and writes the ABI and bytecode
// to the files embedded by the Go token package (token/abi.json, token/bytecode.hex).
// Usage: node contracts/compile.js
const fs = require('fs');
const path = require('path');
const solc = require('solc');

const src = fs.readFileSync(path.join(__dirname, 'KRWTestStablecoin.sol'), 'utf8');
const input = {
  language: 'Solidity',
  sources: { 'KRWTestStablecoin.sol': { content: src } },
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
const c = out.contracts['KRWTestStablecoin.sol']['KRWTestStablecoin'];
const dst = path.join(__dirname, '..', 'token');
fs.mkdirSync(dst, { recursive: true });
fs.writeFileSync(path.join(dst, 'abi.json'), JSON.stringify(c.abi, null, 2));
fs.writeFileSync(path.join(dst, 'bytecode.hex'), c.evm.bytecode.object);
console.log('compiled: token/abi.json, token/bytecode.hex');

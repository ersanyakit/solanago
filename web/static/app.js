"use strict";

const LOADER_PROGRAM_ID = "BPFLoaderUpgradeab1e11111111111111111111111";
const DEVNET_RPC_URL = "https://api.devnet.solana.com";
const WRITE_CHUNK_SIZE = 800; // must match deploy.DefaultChunkSize
const WRITE_BATCH_SIZE = 15; // transactions per signAllTransactions batch

const state = {
  network: "devnet",
  customRpcUrl: "",
  wallet: null, // web3.PublicKey once connected
  examples: [],
  activeExampleId: null,
};

function currentRpcUrl() {
  return state.network === "custom" ? state.customRpcUrl.trim() : DEVNET_RPC_URL;
}

function currentConnection() {
  const url = currentRpcUrl();
  if (!url) throw new Error("enter a custom RPC URL first");
  return new solanaWeb3.Connection(url, "confirmed");
}

// withRpcRetry retries fn on rate-limit (429) responses with exponential
// backoff. Public RPC endpoints like Devnet's default one rate-limit
// aggressively under the burst of reads/sends a deploy or Call does; every
// call site this wraps is either read-only or resubmits an
// already-identically-signed transaction, so retrying is always safe.
async function withRpcRetry(fn, attempts = 5, baseDelayMs = 400) {
  let delay = baseDelayMs;
  for (let attempt = 0; attempt < attempts; attempt++) {
    try {
      return await fn();
    } catch (err) {
      const message = String((err && err.message) || err);
      const isRateLimited = message.includes("429") || /too many requests/i.test(message);
      if (!isRateLimited || attempt === attempts - 1) throw err;
      await new Promise((resolve) => setTimeout(resolve, delay));
      delay = Math.min(delay * 2, 5000);
    }
  }
}

function explorerLink(address) {
  if (state.network === "custom") {
    return `https://explorer.solana.com/address/${address}?cluster=custom&customUrl=${encodeURIComponent(currentRpcUrl())}`;
  }
  return `https://explorer.solana.com/address/${address}?cluster=devnet`;
}

function explorerTxLink(signature) {
  if (state.network === "custom") {
    return `https://explorer.solana.com/tx/${signature}?cluster=custom&customUrl=${encodeURIComponent(currentRpcUrl())}`;
  }
  return `https://explorer.solana.com/tx/${signature}?cluster=devnet`;
}

async function api(path, options) {
  const response = await fetch(`/api${path}`, {
    headers: { "content-type": "application/json" },
    ...options,
  });
  const body = await response.json().catch(() => ({}));
  if (!response.ok) {
    throw new Error(body.error || `request to ${path} failed (${response.status})`);
  }
  return body;
}

// --- wallet -----------------------------------------------------------

async function connectWallet() {
  const provider = window.solana;
  if (!provider || !provider.isPhantom) {
    alert("Phantom wallet extension not found.");
    return;
  }
  const result = await provider.connect();
  state.wallet = result.publicKey;
  document.getElementById("wallet-address").textContent = state.wallet.toBase58();
  document.getElementById("connect-button").textContent = "Connected";
}

// --- tabs / examples ----------------------------------------------------

async function loadExamples() {
  state.examples = await api("/examples");
  await Promise.all(
    state.examples.map(async (example) => {
      try {
        example.schema = await api(`/examples/${example.id}/schema`);
      } catch {
        example.schema = { instructions: [], states: [] };
      }
    })
  );
  const tabs = document.getElementById("tabs");
  const panels = document.getElementById("panels");
  tabs.innerHTML = "";
  panels.innerHTML = "";
  state.examples.forEach((example, index) => {
    const tabButton = document.createElement("button");
    tabButton.className = "tab-button" + (index === 0 ? " active" : "");
    tabButton.textContent = example.name;
    tabButton.addEventListener("click", () => selectTab(example.id));
    tabs.appendChild(tabButton);

    panels.appendChild(buildPanel(example, index === 0));
  });
  state.activeExampleId = state.examples[0] && state.examples[0].id;
}

function buildPanel(example, active) {
  const panel = document.createElement("section");
  panel.className = "panel" + (active ? " active" : "");
  panel.id = `panel-${example.id}`;
  panel.innerHTML = `
    <h2>${example.name}</h2>
    <p class="description">${example.description}</p>
    <div class="actions">
      <button data-action="build">Build</button>
      <button data-action="deploy">Deploy</button>
    </div>
    <div class="status-line" data-role="status">not built yet</div>
    <div class="log" data-role="log"></div>
    <div class="result-box" data-role="result" hidden></div>
    ${methodsPanelHTML(example)}
  `;
  panel.querySelector('[data-action="build"]').addEventListener("click", () => buildExample(example.id));
  panel.querySelector('[data-action="deploy"]').addEventListener("click", () => deployExample(example.id));
  wireMethodsPanel(panel, example);
  return panel;
}

function selectTab(id) {
  state.activeExampleId = id;
  document.querySelectorAll(".tab-button").forEach((button, index) => {
    button.classList.toggle("active", state.examples[index].id === id);
  });
  document.querySelectorAll(".panel").forEach((panel) => {
    panel.classList.toggle("active", panel.id === `panel-${id}`);
  });
}

function panelEl(exampleId, role) {
  return document.querySelector(`#panel-${exampleId} [data-role="${role}"]`);
}

function log(exampleId, message, kind) {
  const el = panelEl(exampleId, "log");
  const line = document.createElement("div");
  line.className = "log-line" + (kind ? ` ${kind}` : "");
  line.textContent = message;
  el.appendChild(line);
  el.scrollTop = el.scrollHeight;
}

function setStatus(exampleId, text) {
  panelEl(exampleId, "status").textContent = text;
}

// --- build --------------------------------------------------------------

async function buildExample(exampleId) {
  setStatus(exampleId, "building…");
  try {
    const result = await api(`/examples/${exampleId}/build`, { method: "POST" });
    setStatus(exampleId, `built: ${result.sizeBytes} bytes, sha256 ${result.sha256.slice(0, 12)}…`);
    log(exampleId, `build ok: ${result.sizeBytes} bytes`, "ok");
  } catch (err) {
    setStatus(exampleId, "build failed");
    log(exampleId, String(err.message || err), "err");
  }
}

// --- deploy ---------------------------------------------------------------

function base64ToBytes(base64) {
  const binary = atob(base64);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
  return bytes;
}

function writeInstructionData(offset, chunk) {
  const data = new Uint8Array(4 + 4 + 8 + chunk.length);
  const view = new DataView(data.buffer);
  view.setUint32(0, 1, true); // Write discriminator
  view.setUint32(4, offset, true);
  view.setBigUint64(8, BigInt(chunk.length), true);
  data.set(chunk, 16);
  return data;
}

function buildWriteInstruction(bufferId, authority, offset, chunk) {
  return new solanaWeb3.TransactionInstruction({
    programId: new solanaWeb3.PublicKey(LOADER_PROGRAM_ID),
    keys: [
      { pubkey: bufferId, isSigner: false, isWritable: true },
      { pubkey: authority, isSigner: true, isWritable: false },
    ],
    data: writeInstructionData(offset, chunk),
  });
}

async function sendAndConfirm(connection, signedTx, blockhashInfo) {
  const signature = await withRpcRetry(() => connection.sendRawTransaction(signedTx.serialize(), { skipPreflight: false }));
  await withRpcRetry(() => connection.confirmTransaction({ signature, ...blockhashInfo }, "confirmed"));
  return signature;
}

async function getLatestBlockhash(connection) {
  return withRpcRetry(() => connection.getLatestBlockhash("confirmed"));
}

// phantomSignAndSend deserializes a base64 partially-signed versioned
// transaction (fee-payer slot zeroed by the server), has Phantom fill and
// sign that slot, submits it, and waits for confirmation.
async function phantomSignAndSend(txBase64) {
  const connection = currentConnection();
  const tx = solanaWeb3.VersionedTransaction.deserialize(base64ToBytes(txBase64));
  const signed = await window.solana.signTransaction(tx);
  const blockhashInfo = await getLatestBlockhash(connection);
  return sendAndConfirm(connection, signed, blockhashInfo);
}

async function deployExample(exampleId) {
  if (!state.wallet) {
    alert("Connect Phantom first.");
    return;
  }
  const provider = window.solana;
  const connection = currentConnection();
  setStatus(exampleId, "preparing…");

  try {
    // 1. server-side session: builds the program, generates the ephemeral
    //    buffer/program keypairs, computes rent.
    const session = await api("/deploy/session", {
      method: "POST",
      body: JSON.stringify({ exampleId, feePayer: state.wallet.toBase58(), rpcUrl: currentRpcUrl() }),
    });
    log(exampleId, `session ${session.sessionId}: program ${session.programId}, buffer ${session.bufferId}, ${session.elfLength} bytes`);

    // 2. create + initialize the loader buffer account. Server pre-signed
    //    with the buffer's own ephemeral key; Phantom fills the fee-payer
    //    slot.
    setStatus(exampleId, "creating buffer (approve in Phantom)…");
    const createBufferTx = solanaWeb3.VersionedTransaction.deserialize(
      base64ToBytes((await api(`/deploy/session/${session.sessionId}/create-buffer-tx`, { method: "POST" })).tx)
    );
    let signed = await provider.signTransaction(createBufferTx);
    let blockhashInfo = await getLatestBlockhash(connection);
    let signature = await sendAndConfirm(connection, signed, blockhashInfo);
    log(exampleId, `create-buffer confirmed: ${signature}`, "ok");

    // 3. write the artifact into the buffer in chunks. None of these need
    //    an ephemeral local signer — only Phantom, as the loader
    //    "authority" account — so they're built and signed entirely
    //    client-side, batched through signAllTransactions with a fresh
    //    blockhash per batch to stay inside its ~60-90s validity window
    //    across dozens of chunks.
    setStatus(exampleId, "fetching artifact…");
    const artifactResponse = await fetch(`/api/examples/${exampleId}/artifact`);
    if (!artifactResponse.ok) throw new Error("failed to fetch built artifact");
    const artifact = new Uint8Array(await artifactResponse.arrayBuffer());

    const chunkOffsets = [];
    for (let offset = 0; offset < artifact.length; offset += WRITE_CHUNK_SIZE) chunkOffsets.push(offset);

    const bufferPubkey = new solanaWeb3.PublicKey(session.bufferId);
    for (let batchStart = 0; batchStart < chunkOffsets.length; batchStart += WRITE_BATCH_SIZE) {
      const batchOffsets = chunkOffsets.slice(batchStart, batchStart + WRITE_BATCH_SIZE);
      setStatus(exampleId, `writing bytes ${batchOffsets[0]}–${Math.min(
        batchOffsets[batchOffsets.length - 1] + WRITE_CHUNK_SIZE,
        artifact.length
      )} of ${artifact.length}…`);
      const batchBlockhash = await getLatestBlockhash(connection);
      const batchTxs = batchOffsets.map((offset) => {
        const chunk = artifact.subarray(offset, Math.min(offset + WRITE_CHUNK_SIZE, artifact.length));
        const instruction = buildWriteInstruction(bufferPubkey, state.wallet, offset, chunk);
        const message = new solanaWeb3.TransactionMessage({
          payerKey: state.wallet,
          recentBlockhash: batchBlockhash.blockhash,
          instructions: [instruction],
        }).compileToV0Message();
        return new solanaWeb3.VersionedTransaction(message);
      });
      const signedBatch = await provider.signAllTransactions(batchTxs);
      const signatures = await Promise.all(
        signedBatch.map((tx) => withRpcRetry(() => connection.sendRawTransaction(tx.serialize())))
      );
      await Promise.all(
        signatures.map((sig) => withRpcRetry(() => connection.confirmTransaction({ signature: sig, ...batchBlockhash }, "confirmed")))
      );
      log(exampleId, `wrote ${batchOffsets.length} chunk(s) (offsets ${batchOffsets[0]}…${batchOffsets[batchOffsets.length - 1]})`, "ok");
    }

    // 4. finalize: create the program account and deploy the buffer's
    //    contents as its code. Server pre-signed with the program's own
    //    ephemeral key; Phantom fills the fee-payer slot, fetched fresh
    //    now that all writes are confirmed.
    setStatus(exampleId, "finalizing deploy (approve in Phantom)…");
    const deployTx = solanaWeb3.VersionedTransaction.deserialize(
      base64ToBytes((await api(`/deploy/session/${session.sessionId}/deploy-tx`, { method: "POST" })).tx)
    );
    signed = await provider.signTransaction(deployTx);
    blockhashInfo = await getLatestBlockhash(connection);
    signature = await sendAndConfirm(connection, signed, blockhashInfo);
    log(exampleId, `deploy confirmed: ${signature}`, "ok");

    setStatus(exampleId, "deployed");
    const result = panelEl(exampleId, "result");
    result.hidden = false;
    result.innerHTML = `Program ID: <a href="${explorerLink(session.programId)}" target="_blank" rel="noopener">${session.programId}</a><br/>` +
      `Final transaction: <a href="${explorerTxLink(signature)}" target="_blank" rel="noopener">${signature}</a>`;
    const programIdField = document.querySelector(`#panel-${exampleId} [data-field="programId"]`);
    if (programIdField) programIdField.value = session.programId;
  } catch (err) {
    setStatus(exampleId, "deploy failed");
    log(exampleId, String((err && err.message) || err), "err");
  }
}

// --- generic "Methods" panel -----------------------------------------------
//
// Renders every example's instructions and readable account states from
// its schema (GET /api/examples/:id/schema) — one form per instruction, one
// viewer per state layout, the same way a Remix-style IDE turns a
// contract's ABI into a UI, instead of bespoke code per example. Account
// roles the backend resolves on its own (new accounts it generates, your
// wallet, the System Program, values derived by reading another account)
// are shown as hints, not inputs — only roles that genuinely need an
// existing pubkey you supply get a text field.

const NUMERIC_FIELD_TYPES = new Set(["u8", "u16", "u32", "u64"]);

function methodsPanelHTML(example) {
  const schema = example.schema || { instructions: [], states: [] };
  const instructions = schema.instructions || [];
  const states = schema.states || [];
  if (!instructions.length && !states.length) return "";

  let html = `
    <hr />
    <h3>Use it</h3>
    <p class="description">Paste the program id from a successful Deploy above (or one you deployed earlier).</p>
    <label>Program ID<br /><input data-field="programId" type="text" placeholder="deployed program id" /></label>
  `;

  if (instructions.length) {
    html += `<h4>Methods</h4>`;
    instructions.forEach((ix) => {
      html += `<div class="method-card">`;
      html += `<strong>${ix.name}</strong>`;
      if (ix.help) html += `<p class="description">${ix.help}</p>`;
      (ix.accounts || []).forEach((role) => {
        if (role.newAccount) {
          html += `<div class="hint">${role.name}: new account (generated for you)</div>`;
        } else if (role.default === "wallet") {
          html += `<div class="hint">${role.name}: your wallet</div>`;
        } else if (role.default === "system") {
          html += `<div class="hint">${role.name}: System Program</div>`;
        } else if (role.derivedFromAccount) {
          html += `<div class="hint">${role.name}: read from ${role.derivedFromAccount}</div>`;
        } else {
          html += `<label>${role.name}${role.help ? ` <small>(${role.help})</small>` : ""}<br /><input data-account="${role.name}" type="text" placeholder="${role.name} pubkey" /></label>`;
        }
      });
      (ix.fields || []).forEach((field) => {
        if (field.type === "bool") {
          html += `<label><input data-field-input="${field.name}" type="checkbox" /> ${field.name}${field.help ? ` <small>(${field.help})</small>` : ""}</label>`;
        } else {
          const inputType = NUMERIC_FIELD_TYPES.has(field.type) ? "number" : "text";
          html += `<label>${field.name}${field.help ? ` <small>(${field.help})</small>` : ""}<br /><input data-field-input="${field.name}" type="${inputType}" /></label>`;
        }
      });
      html += `<button data-call="${ix.name}">Call ${ix.name}</button>`;
      html += `<div class="log" data-role="log-${ix.name}"></div>`;
      html += `</div>`;
    });
  }

  if (states.length) {
    html += `<h4>Read state</h4>`;
    states.forEach((layout) => {
      html += `<div class="method-card">
        <strong>${layout.name}</strong><br/>
        <input data-state-address="${layout.name}" type="text" placeholder="account address" />
        <button data-read="${layout.name}" class="secondary">Read</button>
        <pre data-role="read-${layout.name}"></pre>
      </div>`;
    });
  }

  if (example.id === "phonebook") {
    html += `
      <h4>Contacts</h4>
      <div class="method-card">
        <input data-field="phonebookForContacts" type="text" placeholder="phonebook account" />
        <button data-action="refresh-contacts" class="secondary">List contacts</button>
        <div class="result-box" data-role="contacts" hidden></div>
      </div>
    `;
  }

  return html;
}

function wireMethodsPanel(panel, example) {
  const schema = example.schema || { instructions: [], states: [] };
  (schema.instructions || []).forEach((ix) => {
    const button = panel.querySelector(`[data-call="${ix.name}"]`);
    if (button) button.addEventListener("click", () => callInstruction(panel, example, ix));
  });
  (schema.states || []).forEach((layout) => {
    const button = panel.querySelector(`[data-read="${layout.name}"]`);
    if (button) button.addEventListener("click", () => readState(panel, example, layout));
  });
  if (example.id === "phonebook") {
    const button = panel.querySelector('[data-action="refresh-contacts"]');
    if (button) button.addEventListener("click", () => refreshPhonebookContacts(panel));
  }
}

function requireProgramId(panel) {
  if (!state.wallet) {
    alert("Connect Phantom first.");
    return null;
  }
  const field = panel.querySelector('[data-field="programId"]');
  const programId = field ? field.value.trim() : "";
  if (!programId) {
    alert("Enter the deployed program id first.");
    return null;
  }
  return programId;
}

function appendLog(el, message, kind) {
  if (!el) return;
  const line = document.createElement("div");
  line.className = "log-line" + (kind ? ` ${kind}` : "");
  line.textContent = message;
  el.appendChild(line);
  el.scrollTop = el.scrollHeight;
}

async function callInstruction(panel, example, ix) {
  const programId = requireProgramId(panel);
  if (!programId) return;
  const logEl = panel.querySelector(`[data-role="log-${ix.name}"]`);

  const accounts = {};
  (ix.accounts || []).forEach((role) => {
    if (role.newAccount || role.default === "wallet" || role.default === "system" || role.derivedFromAccount) return;
    const input = panel.querySelector(`[data-account="${role.name}"]`);
    if (input) accounts[role.name] = input.value.trim();
  });
  const fields = {};
  (ix.fields || []).forEach((field) => {
    const input = panel.querySelector(`[data-field-input="${field.name}"]`);
    if (!input) return;
    fields[field.name] = field.type === "bool" ? String(input.checked) : input.value;
  });

  try {
    const body = { programId, feePayer: state.wallet.toBase58(), rpcUrl: currentRpcUrl(), accounts, fields };
    const result = await api(`/examples/${example.id}/call/${ix.name}`, { method: "POST", body: JSON.stringify(body) });
    const signature = await phantomSignAndSend(result.tx);
    let message = `${ix.name} confirmed (tx ${signature})`;
    if (result.accounts && Object.keys(result.accounts).length) {
      message += " — " + Object.entries(result.accounts).map(([name, pubkey]) => `${name}=${pubkey}`).join(", ");
    }
    appendLog(logEl, message, "ok");
  } catch (err) {
    appendLog(logEl, `${ix.name} failed: ${(err && err.message) || err}`, "err");
  }
}

async function readState(panel, example, layout) {
  const addressInput = panel.querySelector(`[data-state-address="${layout.name}"]`);
  const address = addressInput ? addressInput.value.trim() : "";
  const out = panel.querySelector(`[data-role="read-${layout.name}"]`);
  if (!address) {
    alert("Enter an account address.");
    return;
  }
  try {
    const params = new URLSearchParams({ rpcUrl: currentRpcUrl(), address });
    const result = await api(`/examples/${example.id}/read/${layout.name}?${params.toString()}`);
    out.textContent = JSON.stringify(result, null, 2);
  } catch (err) {
    out.textContent = `error: ${(err && err.message) || err}`;
  }
}

async function refreshPhonebookContacts(panel) {
  const input = panel.querySelector('[data-field="phonebookForContacts"]');
  const phonebook = input ? input.value.trim() : "";
  const box = panel.querySelector('[data-role="contacts"]');
  if (!phonebook) {
    alert("Enter a phonebook account address.");
    return;
  }
  try {
    const params = new URLSearchParams({ rpcUrl: currentRpcUrl(), phonebook });
    const result = await api(`/phonebook/contacts?${params.toString()}`);
    box.hidden = false;
    if (!result.contacts || !result.contacts.length) {
      box.textContent = "No contacts yet.";
      return;
    }
    box.innerHTML = result.contacts.map((c) => `${c.name} &rarr; <code>${c.address}</code>`).join("<br/>");
  } catch (err) {
    box.hidden = false;
    box.textContent = `error: ${(err && err.message) || err}`;
  }
}

// --- boot -----------------------------------------------------------------

document.getElementById("network-select").addEventListener("change", (event) => {
  state.network = event.target.value;
  document.getElementById("custom-rpc").hidden = state.network !== "custom";
});
document.getElementById("custom-rpc").addEventListener("input", (event) => {
  state.customRpcUrl = event.target.value;
});
document.getElementById("connect-button").addEventListener("click", connectWallet);

loadExamples().catch((err) => {
  document.getElementById("panels").textContent = `failed to load examples: ${err.message || err}`;
});

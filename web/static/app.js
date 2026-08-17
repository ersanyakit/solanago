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
  activeKind: "example", // "example" | "token2022"
  selectedBuild: {}, // example id -> build id Deploy should use
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

// --- project tree / examples ----------------------------------------------
//
// Remix-style layout: a file tree on the left (project root -> examples/ ->
// one folder per example -> its actual guest source files), a toolbar with
// Compile/Deploy acting on whichever example is currently selected in the
// tree, and one content panel per example in the main area (only the
// selected one visible).

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
  const panels = document.getElementById("panels");
  panels.innerHTML = "";
  state.examples.forEach((example) => {
    panels.appendChild(buildPanel(example));
  });
  panels.appendChild(buildToken2022Panel());
  buildFileTree(state.examples);
  selectNode(state.examples[0] && state.examples[0].id, "example");
  state.examples.forEach((example) => renderBuildList(example.id));
}

function buildPanel(example) {
  const panel = document.createElement("section");
  panel.className = "panel";
  panel.id = `panel-${example.id}`;
  panel.innerHTML = `
    <h2>${example.name}</h2>
    <p class="description">${example.description}</p>
    ${sourceViewerHTML()}
    <div class="status-line" data-role="status">not built yet</div>
    <h4>Builds</h4>
    <p class="description">Build as many times as you like — every build is kept below. Click one to select it as the build Deploy will use.</p>
    <div class="build-list" data-role="builds"><span class="hint">no builds yet</span></div>
    <div class="log" data-role="log"></div>
    <div class="result-box" data-role="result" hidden></div>
    ${methodsPanelHTML(example)}
  `;
  wireSourceViewer(panel);
  wireMethodsPanel(panel, example);
  return panel;
}

// renderBuildList re-fetches exampleId's build history and redraws its
// build-list, keeping (or defaulting to the newest as) the selected build
// that Deploy will use.
async function renderBuildList(exampleId) {
  const container = document.querySelector(`#panel-${exampleId} [data-role="builds"]`);
  if (!container) return;
  let history = [];
  try {
    history = await api(`/examples/${exampleId}/builds`);
  } catch {
    // leave "no builds yet" showing
    return;
  }
  if (!history.length) {
    container.innerHTML = `<span class="hint">no builds yet</span>`;
    return;
  }
  if (!state.selectedBuild[exampleId] || !history.some((b) => b.id === state.selectedBuild[exampleId])) {
    state.selectedBuild[exampleId] = history[history.length - 1].id;
  }
  container.innerHTML = "";
  history
    .slice()
    .reverse()
    .forEach((build) => {
      const row = document.createElement("div");
      row.className = "build-row" + (build.id === state.selectedBuild[exampleId] ? " selected" : "");
      const when = new Date(build.builtAt).toLocaleTimeString();
      row.innerHTML = `<span>${build.id}</span><span class="hint">${build.sizeBytes} bytes · sha256 ${build.sha256.slice(0, 10)}… · ${when}</span>`;
      row.addEventListener("click", () => {
        state.selectedBuild[exampleId] = build.id;
        container.querySelectorAll(".build-row").forEach((el) => el.classList.remove("selected"));
        row.classList.add("selected");
        setStatus(exampleId, `selected build ${build.id} (${build.sizeBytes} bytes) for Deploy`);
      });
      container.appendChild(row);
    });
  setStatus(exampleId, `${history.length} build(s) — using ${state.selectedBuild[exampleId]} for Deploy`);
}

// sourceViewerHTML is the (initially hidden) read-only code view every
// panel gets, populated by showSource() when a file leaf is clicked in the
// tree.
function sourceViewerHTML() {
  return `
    <div class="source-viewer" data-role="source" hidden>
      <div class="source-viewer-header">
        <span>📄 <span data-role="source-path"></span></span>
        <button class="secondary" data-action="close-source">✕</button>
      </div>
      <pre data-role="source-content"></pre>
    </div>
  `;
}

function wireSourceViewer(panel) {
  const closeButton = panel.querySelector('[data-action="close-source"]');
  if (closeButton) {
    closeButton.addEventListener("click", () => {
      panel.querySelector('[data-role="source"]').hidden = true;
    });
  }
}

// showSource brings exampleId's panel into view and fills its source
// viewer with path's content, fetched read-only from GET /api/source
// (server-side allowlisted — see web/source.go).
async function showSource(exampleId, path) {
  selectNode(exampleId, exampleId === "token2022" ? "token2022" : "example");
  const panel = document.getElementById(`panel-${exampleId}`);
  if (!panel) return;
  const viewer = panel.querySelector('[data-role="source"]');
  const pathEl = panel.querySelector('[data-role="source-path"]');
  const contentEl = panel.querySelector('[data-role="source-content"]');
  viewer.hidden = false;
  pathEl.textContent = path;
  contentEl.textContent = "loading…";
  try {
    const response = await fetch(`/api/source?path=${encodeURIComponent(path)}`);
    if (!response.ok) {
      const body = await response.json().catch(() => ({}));
      throw new Error(body.error || `failed to load ${path} (${response.status})`);
    }
    contentEl.textContent = await response.text();
  } catch (err) {
    contentEl.textContent = `error: ${(err && err.message) || err}`;
  }
}

// buildFileTree renders the project structure two ways:
//   gosbf -> examples -> <example id> -> <source file>   (compiled examples this
//     server can build/deploy, using each Example's real Sources paths)
//   gosbf -> sdk -> token2022                              (the official,
//     already-deployed Token-2022 program this server never compiles or
//     deploys — see buildToken2022Panel)
function buildFileTree(examples) {
  const root = document.getElementById("file-tree");
  root.innerHTML = "";

  const exampleNodes = examples.map((example) => {
    const fileChildren = (example.sources || []).map((path) =>
      treeNode({ icon: "📄", label: path.split("/").pop(), title: path, filePath: path, fileExampleId: example.id })
    );
    return treeNode({
      icon: "📁",
      label: example.id,
      nodeId: example.id,
      kind: "example",
      children: fileChildren,
      collapsed: true,
    });
  });
  const examplesFolder = treeNode({ icon: "📁", label: "examples", children: exampleNodes });

  const token2022Files = ["token2022.go", "instruction.go", "state.go", "extension.go"].map((name) =>
    treeNode({
      icon: "📄",
      label: name,
      title: `sdk/token2022/${name}`,
      filePath: `sdk/token2022/${name}`,
      fileExampleId: "token2022",
    })
  );
  const sdkFolder = treeNode({
    icon: "📁",
    label: "sdk",
    children: [
      treeNode({
        icon: "📁",
        label: "token2022",
        nodeId: "token2022",
        kind: "token2022",
        children: token2022Files,
        collapsed: true,
      }),
    ],
  });

  const projectRoot = treeNode({ icon: "📦", label: "gosbf", children: [examplesFolder, sdkFolder] });
  root.appendChild(projectRoot);
}

// treeNode builds one collapsible tree row plus its children container.
// Clicking a folder that carries a nodeId selects that node (and toggles
// its children); clicking a file leaf that carries a filePath shows that
// file's source instead.
function treeNode({ icon, label, title, nodeId, kind, filePath, fileExampleId, children, collapsed }) {
  const node = document.createElement("div");
  node.className = "tree-node";

  const row = document.createElement("div");
  row.className = "tree-label";
  if (nodeId) row.dataset.nodeId = nodeId;
  if (title) row.title = title;

  const hasChildren = Array.isArray(children) && children.length > 0;
  const caret = document.createElement("span");
  caret.className = "tree-caret";
  caret.textContent = hasChildren ? (collapsed ? "▶" : "▼") : "";
  row.appendChild(caret);

  const iconEl = document.createElement("span");
  iconEl.className = "tree-icon";
  iconEl.textContent = icon;
  row.appendChild(iconEl);

  const labelEl = document.createElement("span");
  labelEl.textContent = label;
  row.appendChild(labelEl);

  node.appendChild(row);

  let childrenContainer = null;
  if (hasChildren) {
    childrenContainer = document.createElement("div");
    childrenContainer.className = "tree-children" + (collapsed ? " collapsed" : "");
    children.forEach((child) => childrenContainer.appendChild(child));
    node.appendChild(childrenContainer);
  }

  row.addEventListener("click", () => {
    if (childrenContainer) childrenContainer.classList.toggle("collapsed");
    caret.textContent = childrenContainer ? (childrenContainer.classList.contains("collapsed") ? "▶" : "▼") : "";
    if (nodeId) selectNode(nodeId, kind);
    if (filePath) showSource(fileExampleId, filePath);
  });

  return node;
}

function selectNode(id, kind) {
  if (!id) return;
  state.activeExampleId = id;
  state.activeKind = kind || "example";
  document.querySelectorAll(".tree-label").forEach((row) => {
    row.classList.toggle("active", row.dataset.nodeId === id);
  });
  document.querySelectorAll(".panel").forEach((panel) => {
    panel.classList.toggle("active", panel.id === `panel-${id}`);
  });
  if (state.activeKind === "token2022") {
    document.getElementById("toolbar-active").textContent = "sdk/token2022 — official pre-deployed program, nothing to compile/deploy";
    return;
  }
  const example = state.examples.find((candidate) => candidate.id === id);
  document.getElementById("toolbar-active").textContent = example
    ? `${example.id} — ${(example.sources || []).join(", ")}`
    : id;
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
    state.selectedBuild[exampleId] = result.id;
    await renderBuildList(exampleId);
    log(exampleId, `build ok: ${result.id} (${result.sizeBytes} bytes, sha256 ${result.sha256.slice(0, 12)}…)`, "ok");
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
    // 1. server-side session: uses the selected build (or builds fresh if
    //    none picked), generates the ephemeral buffer/program keypairs,
    //    computes rent.
    const session = await api("/deploy/session", {
      method: "POST",
      body: JSON.stringify({
        exampleId,
        feePayer: state.wallet.toBase58(),
        rpcUrl: currentRpcUrl(),
        buildId: state.selectedBuild[exampleId] || "",
      }),
    });
    state.selectedBuild[exampleId] = session.buildId;
    await renderBuildList(exampleId);
    log(exampleId, `session ${session.sessionId}: build ${session.buildId}, program ${session.programId}, buffer ${session.bufferId}, ${session.elfLength} bytes`);

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
    // Must be the exact same build the session above sized rent/buffers
    // for — never re-resolve to "whatever the latest build happens to be
    // right now," which could differ if another build happened meanwhile.
    const artifactResponse = await fetch(`/api/examples/${exampleId}/artifact?buildId=${encodeURIComponent(session.buildId)}`);
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

// --- token2022 panel --------------------------------------------------------
//
// Unlike every example panel, this one talks to the official, already
// on-chain Token-2022 program (see web/token2022.go/token2022_handlers.go)
// — there is nothing to Compile or Deploy here, only to call: create a
// mint, create/derive an associated token account, mint, transfer, and
// read back a mint/account's decoded state.

function buildToken2022Panel() {
  const panel = document.createElement("section");
  panel.className = "panel";
  panel.id = "panel-token2022";
  panel.innerHTML = `
    <h2>token2022</h2>
    <p class="description">The official, already-deployed Token-2022 program (TokenzQdBNbLqP5VEhdkAS6EPFLC1PHnBqCXEpPxuEb). Nothing to compile or deploy — these actions submit real instructions against it.</p>
    ${sourceViewerHTML()}

    <div class="method-card">
      <strong>Create mint</strong>
      <p class="description">Creates a bare (no-extension) mint with you as mint authority.</p>
      <label>Decimals <input data-t22="decimals" type="number" value="6" style="width:6rem" /></label>
      <label>Freeze authority (optional) <input data-t22="freezeAuthority" type="text" placeholder="defaults to none" /></label>
      <button data-t22-action="create-mint">Create mint</button>
      <div class="log" data-role="t22-log-mint"></div>
    </div>

    <div class="method-card">
      <strong>Create associated token account</strong>
      <p class="description">Derives and creates the ATA for (owner, mint) — idempotent, safe to click again.</p>
      <label>Mint <input data-t22="ataMint" type="text" placeholder="mint address" /></label>
      <label>Owner (optional) <input data-t22="ataOwner" type="text" placeholder="defaults to your wallet" /></label>
      <button data-t22-action="create-ata">Create ATA</button>
      <div class="log" data-role="t22-log-ata"></div>
    </div>

    <div class="method-card">
      <strong>Mint to</strong>
      <label>Mint <input data-t22="mintToMint" type="text" placeholder="mint address" /></label>
      <label>Token account <input data-t22="mintToAccount" type="text" placeholder="associated token account" /></label>
      <label>Amount (raw units) <input data-t22="mintToAmount" type="number" /></label>
      <button data-t22-action="mint-to">Mint to</button>
      <div class="log" data-role="t22-log-mintto"></div>
    </div>

    <div class="method-card">
      <strong>Transfer</strong>
      <label>Mint <input data-t22="transferMint" type="text" placeholder="mint address" /></label>
      <label>Source token account <input data-t22="transferSource" type="text" /></label>
      <label>Destination token account <input data-t22="transferDestination" type="text" /></label>
      <label>Amount (raw units) <input data-t22="transferAmount" type="number" /></label>
      <label>Decimals <input data-t22="transferDecimals" type="number" value="6" style="width:6rem" /></label>
      <button data-t22-action="transfer">Transfer</button>
      <div class="log" data-role="t22-log-transfer"></div>
    </div>

    <h4>Read state</h4>
    <div class="method-card">
      <strong>mint</strong><br/>
      <input data-t22="readMint" type="text" placeholder="mint address" />
      <button data-t22-action="read-mint" class="secondary">Read</button>
      <pre data-role="t22-read-mint"></pre>
    </div>
    <div class="method-card">
      <strong>token account</strong><br/>
      <input data-t22="readAccount" type="text" placeholder="token account address" />
      <button data-t22-action="read-account" class="secondary">Read</button>
      <pre data-role="t22-read-account"></pre>
    </div>
  `;
  wireSourceViewer(panel);
  wireToken2022Panel(panel);
  return panel;
}

function t22Field(panel, name) {
  const el = panel.querySelector(`[data-t22="${name}"]`);
  return el ? el.value.trim() : "";
}

function wireToken2022Panel(panel) {
  panel.querySelector('[data-t22-action="create-mint"]').addEventListener("click", () => token2022CreateMint(panel));
  panel.querySelector('[data-t22-action="create-ata"]').addEventListener("click", () => token2022CreateATA(panel));
  panel.querySelector('[data-t22-action="mint-to"]').addEventListener("click", () => token2022MintTo(panel));
  panel.querySelector('[data-t22-action="transfer"]').addEventListener("click", () => token2022Transfer(panel));
  panel.querySelector('[data-t22-action="read-mint"]').addEventListener("click", () => token2022ReadMint(panel));
  panel.querySelector('[data-t22-action="read-account"]').addEventListener("click", () => token2022ReadAccount(panel));
}

function requireWallet() {
  if (!state.wallet) {
    alert("Connect Phantom first.");
    return false;
  }
  return true;
}

async function token2022CreateMint(panel) {
  if (!requireWallet()) return;
  const logEl = panel.querySelector('[data-role="t22-log-mint"]');
  try {
    const body = {
      feePayer: state.wallet.toBase58(),
      rpcUrl: currentRpcUrl(),
      decimals: Number(t22Field(panel, "decimals") || 6),
      freezeAuthority: t22Field(panel, "freezeAuthority"),
    };
    const result = await api("/token2022/create-mint", { method: "POST", body: JSON.stringify(body) });
    const signature = await phantomSignAndSend(result.tx);
    panel.querySelector('[data-t22="ataMint"]').value = result.mint;
    panel.querySelector('[data-t22="mintToMint"]').value = result.mint;
    panel.querySelector('[data-t22="transferMint"]').value = result.mint;
    panel.querySelector('[data-t22="readMint"]').value = result.mint;
    appendLog(logEl, `mint created: ${result.mint} (tx ${signature})`, "ok");
  } catch (err) {
    appendLog(logEl, `create-mint failed: ${(err && err.message) || err}`, "err");
  }
}

async function token2022CreateATA(panel) {
  if (!requireWallet()) return;
  const logEl = panel.querySelector('[data-role="t22-log-ata"]');
  const mint = t22Field(panel, "ataMint");
  if (!mint) {
    alert("Enter (or create) a mint first.");
    return;
  }
  try {
    const body = { feePayer: state.wallet.toBase58(), rpcUrl: currentRpcUrl(), mint, owner: t22Field(panel, "ataOwner") };
    const result = await api("/token2022/create-ata", { method: "POST", body: JSON.stringify(body) });
    const signature = await phantomSignAndSend(result.tx);
    panel.querySelector('[data-t22="mintToAccount"]').value = result.associatedTokenAccount;
    panel.querySelector('[data-t22="transferSource"]').value = result.associatedTokenAccount;
    panel.querySelector('[data-t22="readAccount"]').value = result.associatedTokenAccount;
    appendLog(logEl, `ATA ready: ${result.associatedTokenAccount} (tx ${signature})`, "ok");
  } catch (err) {
    appendLog(logEl, `create-ata failed: ${(err && err.message) || err}`, "err");
  }
}

async function token2022MintTo(panel) {
  if (!requireWallet()) return;
  const logEl = panel.querySelector('[data-role="t22-log-mintto"]');
  const mint = t22Field(panel, "mintToMint");
  const tokenAccount = t22Field(panel, "mintToAccount");
  const amount = Number(t22Field(panel, "mintToAmount"));
  if (!mint || !tokenAccount || !amount) {
    alert("Fill mint, token account, and amount.");
    return;
  }
  try {
    const body = { feePayer: state.wallet.toBase58(), rpcUrl: currentRpcUrl(), mint, tokenAccount, amount };
    const result = await api("/token2022/mint-to", { method: "POST", body: JSON.stringify(body) });
    const signature = await phantomSignAndSend(result.tx);
    appendLog(logEl, `minted ${amount} raw units (tx ${signature})`, "ok");
  } catch (err) {
    appendLog(logEl, `mint-to failed: ${(err && err.message) || err}`, "err");
  }
}

async function token2022Transfer(panel) {
  if (!requireWallet()) return;
  const logEl = panel.querySelector('[data-role="t22-log-transfer"]');
  const body = {
    feePayer: state.wallet.toBase58(),
    rpcUrl: currentRpcUrl(),
    mint: t22Field(panel, "transferMint"),
    source: t22Field(panel, "transferSource"),
    destination: t22Field(panel, "transferDestination"),
    amount: Number(t22Field(panel, "transferAmount")),
    decimals: Number(t22Field(panel, "transferDecimals") || 6),
  };
  if (!body.mint || !body.source || !body.destination || !body.amount) {
    alert("Fill mint, source, destination, and amount.");
    return;
  }
  try {
    const result = await api("/token2022/transfer", { method: "POST", body: JSON.stringify(body) });
    const signature = await phantomSignAndSend(result.tx);
    appendLog(logEl, `transferred ${body.amount} raw units (tx ${signature})`, "ok");
  } catch (err) {
    appendLog(logEl, `transfer failed: ${(err && err.message) || err}`, "err");
  }
}

async function token2022ReadMint(panel) {
  const address = t22Field(panel, "readMint");
  const out = panel.querySelector('[data-role="t22-read-mint"]');
  if (!address) {
    alert("Enter a mint address.");
    return;
  }
  try {
    const params = new URLSearchParams({ rpcUrl: currentRpcUrl(), address });
    const result = await api(`/token2022/mint?${params.toString()}`);
    out.textContent = JSON.stringify(result, null, 2);
  } catch (err) {
    out.textContent = `error: ${(err && err.message) || err}`;
  }
}

async function token2022ReadAccount(panel) {
  const address = t22Field(panel, "readAccount");
  const out = panel.querySelector('[data-role="t22-read-account"]');
  if (!address) {
    alert("Enter a token account address.");
    return;
  }
  try {
    const params = new URLSearchParams({ rpcUrl: currentRpcUrl(), address });
    const result = await api(`/token2022/account?${params.toString()}`);
    out.textContent = JSON.stringify(result, null, 2);
  } catch (err) {
    out.textContent = `error: ${(err && err.message) || err}`;
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

document.getElementById("toolbar-compile").addEventListener("click", () => {
  if (state.activeKind === "token2022") {
    alert("Token-2022 is the official pre-deployed program — nothing to compile. Use the actions in the panel.");
    return;
  }
  if (!state.activeExampleId) {
    alert("Select an example in the file tree first.");
    return;
  }
  buildExample(state.activeExampleId);
});
document.getElementById("toolbar-deploy").addEventListener("click", () => {
  if (state.activeKind === "token2022") {
    alert("Token-2022 is the official pre-deployed program — nothing to deploy. Use the actions in the panel.");
    return;
  }
  if (!state.activeExampleId) {
    alert("Select an example in the file tree first.");
    return;
  }
  deployExample(state.activeExampleId);
});

loadExamples().catch((err) => {
  document.getElementById("panels").textContent = `failed to load examples: ${err.message || err}`;
});

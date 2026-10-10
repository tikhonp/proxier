// The template editor: CodeMirror 6 over the form's text areas.
//
// The page is a plain form. Every text file is a <textarea name="file[path]">,
// every other file a hidden <input name="bin[path]"> holding base64, so the
// form posts the whole draft and works without this script. This script puts
// one CodeMirror view on top: it shows the file selected in the tree, writes
// every change back to that file's text area, and handles add, rename and
// delete in the tree. Nothing is saved until the form is submitted.
//
// CSP: style-src is 'self', so inline <style> elements are blocked. CodeMirror
// writes its styles through style-mod, which build.sh patches to use
// constructable stylesheets (document.adoptedStyleSheets) on the document as
// well as in shadow roots; everything else here is set through the CSSOM.
// The views live in the page itself, not in a shadow root: focus must land on
// a plain contenteditable, or browser extensions with their own key maps (a
// vim mode in Safari) take the keys typed in the editor as commands.
// Keys typed in the editor stop at its host so the page's key map
// (g d, j k, /, ?) never sees them.
import { EditorState } from "@codemirror/state";
import { EditorView, keymap, lineNumbers, highlightActiveLine, highlightActiveLineGutter, drawSelection, highlightSpecialChars } from "@codemirror/view";
import { defaultKeymap, history, historyKeymap, indentWithTab } from "@codemirror/commands";
import { indentUnit, syntaxHighlighting, HighlightStyle, StreamLanguage, bracketMatching, indentOnInput } from "@codemirror/language";
import { linter, lintGutter, setDiagnostics, lintKeymap } from "@codemirror/lint";
import { search, searchKeymap, highlightSelectionMatches } from "@codemirror/search";
import { yaml } from "@codemirror/lang-yaml";
import { json } from "@codemirror/lang-json";
import { shell } from "@codemirror/legacy-modes/mode/shell";
import { nginx } from "@codemirror/legacy-modes/mode/nginx";
import { tags as t } from "@lezer/highlight";

// ---- Rosé Pine, from the page's tokens.css variables -------------------------

const theme = EditorView.theme({
  "&": { color: "var(--text)", backgroundColor: "var(--black)", fontSize: "12.5px", flex: "1 1 auto", minWidth: "0", minHeight: "480px", maxHeight: "78vh" },
  ".cm-scroller": { fontFamily: "var(--font)", lineHeight: "20px", overflow: "auto" },
  ".cm-content": { caretColor: "var(--text)", padding: "8px 0 16px" },
  ".cm-cursor, .cm-dropCursor": { borderLeftColor: "var(--text)" },
  "&.cm-focused": { outline: "none" },
  "&.cm-focused > .cm-scroller > .cm-selectionLayer .cm-selectionBackground, .cm-selectionBackground, .cm-content ::selection": { backgroundColor: "var(--hl-med)" },
  ".cm-activeLine": { backgroundColor: "var(--overlay)" },
  ".cm-gutters": { backgroundColor: "var(--black)", color: "var(--muted)", border: "none" },
  ".cm-activeLineGutter": { backgroundColor: "var(--overlay)", color: "var(--text)", fontWeight: "600" },
  ".cm-lineNumbers .cm-gutterElement": { padding: "0 14px 0 8px", minWidth: "34px" },
  ".cm-matchingBracket": { backgroundColor: "var(--hl-med)", outline: "none" },
  ".cm-searchMatch": { backgroundColor: "var(--hl-med)", outline: "1px solid var(--subtle)" },
  ".cm-searchMatch.cm-searchMatch-selected": { backgroundColor: "var(--overlay)", outline: "1px solid var(--text)" },
  ".cm-selectionMatch": { backgroundColor: "var(--overlay)" },
  ".cm-panels": { backgroundColor: "var(--black)", color: "var(--text)", borderColor: "var(--overlay)" },
  ".cm-panels input, .cm-panels button": { font: "inherit", color: "var(--text)" },
  ".cm-panels input": { backgroundColor: "var(--base)", border: "1px solid var(--hl-med)", padding: "2px 6px" },
  ".cm-panels button": { backgroundColor: "transparent", border: "1px solid var(--hl-high)", backgroundImage: "none", padding: "2px 8px" },
  ".cm-tooltip": { backgroundColor: "var(--black)", border: "1px solid var(--hl-high)", color: "var(--text)" },
  ".cm-diagnostic": { padding: "4px 8px", borderLeft: "3px solid var(--subtle)" },
  ".cm-diagnostic-error": { borderLeftColor: "var(--love)", color: "var(--love)" },
  ".cm-diagnostic-warning": { borderLeftColor: "var(--gold)", color: "var(--gold)" },
  ".cm-lintRange-error": { backgroundImage: "none", textDecoration: "underline wavy var(--love)", textUnderlineOffset: "4px" },
  ".cm-lintRange-warning": { backgroundImage: "none", textDecoration: "underline wavy var(--gold)", textUnderlineOffset: "4px" },
  ".cm-lint-marker-error": { content: "none" },
}, { dark: true });

const highlight = syntaxHighlighting(HighlightStyle.define([
  { tag: [t.propertyName, t.attributeName, t.tagName], color: "var(--foam)", fontStyle: "italic" },
  { tag: [t.string, t.special(t.string)], color: "var(--gold)" },
  { tag: [t.number, t.bool, t.null, t.atom], color: "var(--rose)" },
  { tag: [t.keyword, t.operatorKeyword, t.definitionKeyword, t.controlKeyword], color: "var(--iris)" },
  { tag: [t.punctuation, t.separator, t.bracket, t.operator, t.meta], color: "var(--subtle)" },
  { tag: [t.comment, t.lineComment, t.blockComment], color: "var(--subtle)", fontStyle: "italic" },
  { tag: [t.variableName, t.labelName, t.typeName, t.className], color: "var(--rose)" },
  { tag: [t.invalid], color: "var(--iris)", textDecoration: "underline wavy var(--love)" },
]));

// ---- files ---------------------------------------------------------------------

const PATH_PART = /^[^\\\x00]+$/;

// validPath mirrors the server's rule (pages/editor.go validDraftPath).
function validPath(p) {
  if (!p || p.length > 200 || !PATH_PART.test(p) || p.startsWith("/") || p.endsWith("/")) return false;
  return p.split("/").every((s) => s !== "" && s !== "." && s !== ".." && s !== ".git");
}

function langOf(path) {
  let base = path.split("/").pop().toLowerCase();
  if (base.endsWith(".template")) base = base.slice(0, -".template".length);
  const dot = base.lastIndexOf(".");
  const ext = dot > 0 ? base.slice(dot) : "";
  if (ext === ".yaml" || ext === ".yml") return "yaml";
  if (ext === ".json") return "json";
  if (ext === ".sh" || ext === ".bash") return "bash";
  if (ext === ".conf") return "nginx";
  return "";
}

function languageExt(lang) {
  switch (lang) {
    case "yaml": return yaml();
    case "json": return json();
    case "bash": return StreamLanguage.define(shell);
    case "nginx": return StreamLanguage.define(nginx);
  }
  return [];
}

function pathLess(a, b) {
  if ((a === "manifest.yaml") !== (b === "manifest.yaml")) return a === "manifest.yaml";
  const la = a.toLowerCase(), lb = b.toLowerCase();
  return la !== lb ? la < lb : a < b;
}

const $ = (sel, root) => (root || document).querySelector(sel);
const $$ = (sel, root) => Array.from((root || document).querySelectorAll(sel));

class Editor {
  constructor(form) {
    this.form = form;
    this.files = [];       // {path, el, binary, state}
    this.active = null;
    this.findings = {};    // path -> [{line, severity, message}]
    this.dirty = false;
    this.submitting = false;
    this.t = (k) => form.dataset[k] || "";
    this.host = $("#ed-host");
    this.view = new EditorView({ parent: this.host, state: EditorState.create({ doc: "" }) });
    this.host.addEventListener("keydown", (e) => {
      if (e.key === "Escape" && !e.defaultPrevented) this.view.contentDOM.blur();
      e.stopPropagation();
    });
    // the note for a binary file sits beside the view, which is hidden then
    this.binaryNote = document.createElement("p");
    this.binaryNote.className = "ed-hint subtle hidden";
    this.host.insertAdjacentElement("afterend", this.binaryNote);
    this.load($("#ed-files").dataset.active);
    form.classList.add("ed-on");
    this.bind();
  }

  // load reads the files from the form fields. A file whose text did not
  // change keeps its editor state, so an upload does not wipe the undo history.
  load(activePath) {
    const old = new Map(this.files.map((f) => [f.path, f]));
    this.files = [];
    for (const el of $$("[data-ed-src],[data-ed-bin]", this.form)) {
      const binary = el.hasAttribute("data-ed-bin");
      const path = el.name.slice(binary ? 4 : 5, -1);
      const f = { path, el, binary, state: null };
      if (!binary) {
        const prev = old.get(path);
        f.state = prev && !prev.binary && prev.state.doc.toString() === el.value ? prev.state : this.makeState(f);
      }
      this.files.push(f);
    }
    this.files.sort((a, b) => pathLess(a.path, b.path));
    this.renderTree();
    const want = this.files.find((f) => f.path === activePath) || this.files.find((f) => f.path === "manifest.yaml") || this.files[0];
    this.select(want ? want.path : null);
  }

  makeState(file) {
    return EditorState.create({
      doc: file.el.value,
      extensions: [
        lineNumbers(), highlightActiveLineGutter(), highlightSpecialChars(), history(), drawSelection(), indentOnInput(), bracketMatching(),
        highlightActiveLine(), highlightSelectionMatches(), search({ top: true }), lintGutter(), linter(null),
        EditorState.tabSize.of(2), indentUnit.of("  "),
        keymap.of([
          { key: "Mod-s", preventDefault: true, run: () => { this.save(); return true; } },
          indentWithTab, ...searchKeymap, ...historyKeymap, ...lintKeymap, ...defaultKeymap,
        ]),
        languageExt(langOf(file.path)),
        theme, highlight,
        EditorView.updateListener.of((u) => {
          if (u.docChanged) {
            file.el.value = u.state.doc.toString();
            this.markDirty();
          }
          if (u.docChanged || u.selectionSet) this.status();
        }),
      ],
    });
  }

  file(path) { return this.files.find((f) => f.path === path); }

  markDirty() { this.dirty = true; }

  save() {
    const btn = $('[data-action="tpl.save"]');
    if (btn && !btn.disabled) this.form.requestSubmit(btn);
  }

  select(path) {
    this.active = this.file(path) || null;
    const f = this.active;
    $("#ed-active").value = f ? f.path : "";
    $("#ed-name").textContent = f ? f.path : this.t("tNofile");
    $("#ed-dir").value = f && f.path.includes("/") ? f.path.slice(0, f.path.lastIndexOf("/")) : "";
    $("#ed-lang").textContent = f ? (f.binary ? this.t("tBinary") : langOf(f.path) || "text") : "";
    for (const a of $$("#ed-tree .ed-file-row")) {
      if (f && a.dataset.path === f.path) a.setAttribute("aria-current", "true"); else a.removeAttribute("aria-current");
    }
    const cm = this.view.dom;
    if (!f || f.binary) {
      cm.style.display = "none";
      this.binaryNote.textContent = f ? this.t("tBinary") : this.t("tNofile");
      this.binaryNote.classList.remove("hidden");
    } else {
      cm.style.display = "";
      this.binaryNote.classList.add("hidden");
      this.view.setState(f.state);
      this.diagnose();
    }
    this.status();
  }

  // ---- tree ---------------------------------------------------------------------

  renderTree() {
    const tree = $("#ed-tree");
    tree.textContent = "";
    const seen = new Set();
    const base = this.form.action.replace(/\/draft$/, "/edit");
    for (const f of this.files) {
      const parts = f.path.split("/");
      for (let i = 0; i < parts.length - 1; i++) {
        const dir = parts.slice(0, i + 1).join("/");
        if (seen.has(dir)) continue;
        seen.add(dir);
        const d = document.createElement("div");
        d.className = "ed-row ed-dir d" + Math.min(i, 8);
        d.dataset.dir = dir;
        d.textContent = parts[i] + "/";
        tree.appendChild(d);
      }
      const a = document.createElement("a");
      a.className = "ed-row ed-file-row pc-row d" + Math.min(parts.length - 1, 8);
      a.dataset.path = f.path;
      a.href = base + "?file=" + encodeURIComponent(f.path);
      const name = document.createElement("span");
      name.className = "ed-name";
      name.textContent = parts[parts.length - 1];
      const count = document.createElement("span");
      count.className = "ed-count sm";
      a.append(name, count);
      tree.appendChild(a);
    }
    this.markTree();
  }

  markTree() {
    for (const a of $$("#ed-tree .ed-file-row")) {
      const fs = this.findings[a.dataset.path] || [];
      const errs = fs.filter((x) => x.severity === "error").length;
      const warns = fs.length - errs;
      a.classList.toggle("has-err", errs > 0);
      a.classList.toggle("has-warn", errs === 0 && warns > 0);
      $(".ed-count", a).textContent = fs.length ? String(fs.length) : "";
    }
  }

  // ---- findings -----------------------------------------------------------------

  // collect reads the validation report in the side panel.
  collect() {
    this.findings = {};
    for (const el of $$("#ed-report [data-finding]")) {
      const path = el.dataset.path;
      if (!path) continue;
      (this.findings[path] = this.findings[path] || []).push({
        line: parseInt(el.dataset.line, 10) || 0, severity: el.dataset.sev, message: el.dataset.msg,
      });
    }
    this.markTree();
    this.diagnose();
    this.status();
  }

  diagnose() {
    const f = this.active;
    if (!f || f.binary) return;
    const doc = this.view.state.doc;
    const diags = (this.findings[f.path] || []).map((x) => {
      const line = doc.line(Math.min(Math.max(x.line, 1), doc.lines));
      return { from: line.from, to: line.to, severity: x.severity === "error" ? "error" : "warning", message: x.message };
    });
    this.view.dispatch(setDiagnostics(this.view.state, diags));
  }

  status() {
    const f = this.active;
    const pos = $("#ed-pos");
    if (f && !f.binary) {
      const head = this.view.state.selection.main.head;
      const line = this.view.state.doc.lineAt(head);
      pos.textContent = this.t("tLine").replace("{line}", line.number).replace("{col}", head - line.from + 1);
    } else {
      pos.textContent = "";
    }
    const fs = f ? this.findings[f.path] || [] : [];
    const errors = fs.filter((x) => x.severity === "error").length;
    $("#ed-issues").textContent = fs.length ? this.t("tIssues").replace("{errors}", errors).replace("{warnings}", fs.length - errors) : "";
  }

  goto(path, line) {
    const f = this.file(path) || this.file("manifest.yaml");
    if (!f) return;
    this.select(f.path);
    if (f.binary || !line) return;
    const doc = this.view.state.doc;
    const l = doc.line(Math.min(Math.max(line, 1), doc.lines));
    this.view.dispatch({ selection: { anchor: l.from }, effects: EditorView.scrollIntoView(l.from, { y: "center" }) });
    this.view.focus();
  }

  // ---- add, rename, delete ---------------------------------------------------------

  askPath(title, value, done) {
    const dlg = $("#ed-name-dlg"), input = $("#ed-name-input"), err = $("#ed-name-err"), form = $("#ed-name-form");
    $("#ed-name-dlg-h").textContent = title;
    input.value = value;
    err.classList.add("hidden");
    this.nameDone = done;
    form.onsubmit = (e) => {
      e.preventDefault();
      const p = input.value.trim();
      const bad = !validPath(p) ? this.t("tInvalid") : (this.file(p) && this.file(p) !== this.nameTarget ? this.t("tExists") : "");
      if (bad) { err.textContent = bad; err.classList.remove("hidden"); return; }
      dlg.close();
      this.nameDone(p);
    };
    dlg.showModal();
    input.focus();
    input.select();
  }

  add() {
    const dir = this.active && this.active.path.includes("/") ? this.active.path.slice(0, this.active.path.lastIndexOf("/") + 1) : "";
    this.nameTarget = null;
    this.askPath(this.t("tAdd"), dir, (p) => {
      const ta = document.createElement("textarea");
      ta.name = "file[" + p + "]";
      ta.className = "ed-src";
      ta.setAttribute("data-ed-src", "");
      ta.spellcheck = false;
      ta.tabIndex = -1;
      $("#ed-sources").appendChild(ta);
      const f = { path: p, el: ta, binary: false, state: null };
      f.state = this.makeState(f);
      this.files.push(f);
      this.files.sort((a, b) => pathLess(a.path, b.path));
      this.renderTree();
      this.select(p);
      this.markDirty();
      this.view.focus();
    });
  }

  rename() {
    const f = this.active;
    if (!f) return;
    this.nameTarget = f;
    this.askPath(this.t("tRename"), f.path, (p) => {
      if (p === f.path) return;
      f.el.name = (f.binary ? "bin[" : "file[") + p + "]";
      if (this.findings[f.path]) { this.findings[p] = this.findings[f.path]; delete this.findings[f.path]; }
      f.path = p;
      this.files.sort((a, b) => pathLess(a.path, b.path));
      this.renderTree();
      this.select(p);
      this.markDirty();
    });
  }

  remove() {
    const f = this.active;
    if (!f) return;
    if (!window.confirm(this.t("tDeleteConfirm").replace("{path}", f.path))) return;
    f.el.remove();
    delete this.findings[f.path];
    this.files = this.files.filter((x) => x !== f);
    this.renderTree();
    this.select(this.files.length ? (this.files.find((x) => x.path === "manifest.yaml") || this.files[0]).path : null);
    this.markDirty();
  }

  // ---- wiring -------------------------------------------------------------------

  copyDraft(btn) {
    const text = this.files.map((f) => "=== " + f.path + " ===\n" + (f.binary ? "(binary)" : f.state.doc.toString())).join("\n");
    if (!navigator.clipboard) return;
    navigator.clipboard.writeText(text).then(() => {
      const was = btn.textContent;
      btn.textContent = this.t("tCopied");
      setTimeout(() => { btn.textContent = was; }, 2000);
    });
  }

  bind() {
    const form = this.form;
    form.addEventListener("submit", () => { this.submitting = true; });
    $("#ed-tree").addEventListener("click", (e) => {
      const a = e.target.closest("a[data-path]");
      if (a) { e.preventDefault(); this.select(a.dataset.path); }
    });
    document.addEventListener("click", (e) => {
      const t = e.target.closest("[data-ed-add],[data-ed-rename],[data-ed-delete],[data-ed-goto],[data-ed-reload],[data-copy-draft]");
      if (!t) return;
      if (t.hasAttribute("data-ed-add")) this.add();
      else if (t.hasAttribute("data-ed-rename")) this.rename();
      else if (t.hasAttribute("data-ed-delete")) this.remove();
      else if (t.hasAttribute("data-copy-draft")) this.copyDraft(t);
      else if (t.hasAttribute("data-ed-reload")) { e.preventDefault(); this.submitting = true; location.reload(); }
      else if (t.hasAttribute("data-ed-goto")) {
        e.preventDefault();
        const row = t.closest("[data-finding]");
        this.goto(row.dataset.path, parseInt(row.dataset.line, 10) || 0);
      }
    });
    document.addEventListener("keydown", (e) => {
      // CodeMirror's own Mod-s has already handled (and prevented) the key in the editor
      if (!e.defaultPrevented && (e.metaKey || e.ctrlKey) && !e.altKey && e.key.toLowerCase() === "s") { e.preventDefault(); this.save(); }
    });
    window.addEventListener("beforeunload", (e) => {
      if (this.dirty && !this.submitting) { e.preventDefault(); e.returnValue = this.t("dirtyMsg"); }
    });
    document.body.addEventListener("htmx:afterSwap", (e) => {
      const id = e.detail.target && e.detail.target.id;
      if (id === "ed-side") {
        if ($("#ed-report")) { this.dirty = false; this.collect(); }
      } else if (id === "ed-files") {
        this.load($("#ed-files").dataset.active);
        this.markDirty();
      }
    });
  }
}

// ---- single code fields: textarea[data-code] --------------------------------------

// A small mode for Shadowrocket configs: [Section] headers, comment lines
// (# and ;), key = value lines and rule lines TYPE,value[,policy].
const shadowrocketMode = {
  name: "shadowrocket",
  startState: () => ({ inRule: false, pos: 0 }),
  token(stream, state) {
    if (stream.sol()) {
      state.pos = 0;
      stream.eatSpace();
      if (stream.peek() === "#" || stream.peek() === ";") { stream.skipToEnd(); return "comment"; }
      if (stream.match(/^\[[^\]]+\]\s*$/)) {
        state.inRule = /^\[rule\]/i.test(stream.current().trim());
        return "heading";
      }
    }
    if (stream.eatSpace()) return null;
    if (state.pos === 0 && stream.match(/^[A-Za-z0-9_-]+(?=\s*=)/)) { state.pos = 1; return "propertyName"; }
    if (state.pos === 0 && stream.match(/^[A-Z][A-Z0-9-]*(?=,)/)) { state.pos = 2; return "keyword"; }
    if (stream.eat("=") || stream.eat(",")) { if (state.pos === 0) state.pos = 1; return "punctuation"; }
    if (stream.match(/^[^,]+/)) {
      if (state.pos >= 2) { state.pos++; return state.pos === 3 ? "string" : "atom"; }
      return "string";
    }
    stream.next();
    return null;
  },
};

// A mode for RouterOS scripts, coloured like the server's routeros lexer
// (ui/routeros.go): comment lines, "# @…" annotations, :commands, strings
// with escapes, $variables, numbers, and a /menu path at the start of a
// statement.
const routerosMode = {
  name: "routeros",
  startState: () => ({ inString: false, stmtStart: true }),
  token(stream, state) {
    if (state.inString) return routerosString(stream, state);
    if (stream.sol()) {
      state.stmtStart = true;
      if (stream.match(/^[ \t]*#[ \t]*@.*/)) return "keyword";
      if (stream.match(/^[ \t]*#.*/)) return "comment";
    }
    if (stream.eatSpace()) return null;
    if (stream.peek() === '"') {
      stream.next();
      state.inString = true;
      state.stmtStart = false;
      return routerosString(stream, state);
    }
    if (stream.match(/^\$"[^"]*"/) || stream.match(/^\$[A-Za-z_][A-Za-z0-9_]*/)) { state.stmtStart = false; return "variableName"; }
    if (stream.match(/^:[a-z][a-z-]*/)) { state.stmtStart = false; return "propertyName"; }
    if (state.stmtStart && stream.match(/^\/[A-Za-z0-9\/-]+/)) { state.stmtStart = false; return "atom"; }
    if (stream.match(/^[0-9]+(?![A-Za-z0-9_])/)) { state.stmtStart = false; return "number"; }
    if (stream.match(/^[[;{]/)) { state.stmtStart = true; return "punctuation"; }
    if (stream.match(/^[=\]()}.]/)) { state.stmtStart = false; return "punctuation"; }
    if (stream.match(/^[A-Za-z_][A-Za-z0-9_-]*/)) { state.stmtStart = false; return null; }
    stream.next();
    state.stmtStart = false;
    return null;
  },
};

// routerosString reads a string up to its closing quote; a backslash at the
// end of a line continues it on the next.
function routerosString(stream, state) {
  while (!stream.eol()) {
    const c = stream.next();
    if (c === "\\") { stream.next(); continue; }
    if (c === '"') { state.inString = false; break; }
  }
  return "string";
}

const codeModes = {
  shadowrocket: () => StreamLanguage.define(shadowrocketMode),
  routeros: () => StreamLanguage.define(routerosMode),
};

// codeFindings shows the rows of [data-code-findings="<textarea id>"] (the
// router script's parameters panel) as lint markers on their lines.
function codeFindings(view, ta) {
  const panel = ta.id && document.querySelector('[data-code-findings="' + CSS.escape(ta.id) + '"]');
  if (!panel) return;
  const doc = view.state.doc;
  const diags = Array.from(panel.querySelectorAll("[data-code-finding]")).map((el) => {
    const line = doc.line(Math.min(Math.max(parseInt(el.dataset.line, 10) || 1, 1), doc.lines));
    return { from: line.from, to: line.to, severity: el.dataset.sev === "error" ? "error" : "warning", message: el.dataset.msg || "" };
  });
  view.dispatch(setDiagnostics(view.state, diags));
}

// saveCode submits the form of the page's [data-code-save] button (⌘S).
function saveCode() {
  const btn = document.querySelector("[data-code-save]");
  if (btn && !btn.disabled && btn.form) btn.form.requestSubmit(btn);
}

// initCodeAreas puts CodeMirror on each textarea[data-code] and copies the
// text back into the textarea on every change and before submit, so the form
// posts what is shown.
function initCodeAreas() {
  for (const ta of document.querySelectorAll("textarea[data-code]")) {
    if (ta.dataset.codeOn) continue;
    ta.dataset.codeOn = "1";
    const host = document.createElement("div");
    host.className = "code-host";
    ta.insertAdjacentElement("afterend", host);
    const lang = codeModes[ta.dataset.code];
    // htmx follows the text area (hx-trigger="input …"): tell it when the
    // text changed, at most every 300 ms
    let inputTimer = 0;
    const view = new EditorView({
      parent: host,
      state: EditorState.create({
        doc: ta.value,
        extensions: [
          lineNumbers(), highlightActiveLineGutter(), highlightSpecialChars(), history(), drawSelection(),
          highlightActiveLine(), highlightSelectionMatches(), search({ top: true }), lintGutter(), linter(null),
          keymap.of([
            { key: "Mod-s", preventDefault: true, run: () => { saveCode(); return true; } },
            indentWithTab, ...searchKeymap, ...historyKeymap, ...lintKeymap, ...defaultKeymap,
          ]),
          lang ? lang() : [], theme, highlight,
          EditorView.updateListener.of((u) => {
            if (!u.docChanged) return;
            ta.value = u.state.doc.toString();
            clearTimeout(inputTimer);
            inputTimer = setTimeout(() => ta.dispatchEvent(new Event("input", { bubbles: true })), 300);
          }),
        ],
      }),
    });
    codeFindings(view, ta);
    document.body.addEventListener("htmx:afterSettle", () => codeFindings(view, ta));
    host.addEventListener("keydown", (e) => {
      if (e.key === "Escape" && !e.defaultPrevented) view.contentDOM.blur();
      // ⌘↵ still reaches the page and submits the form
      if (!((e.metaKey || e.ctrlKey) && e.key === "Enter")) e.stopPropagation();
    });
    ta.style.display = "none";
    if (ta.form) ta.form.addEventListener("submit", () => { ta.value = view.state.doc.toString(); });
  }
}

function init() {
  const form = document.querySelector("form[data-ed]");
  if (form && !form.classList.contains("ed-on")) window.proxierEditor = new Editor(form);
  initCodeAreas();
  if (!form && document.querySelector("[data-code-save]")) {
    document.addEventListener("keydown", (e) => {
      if (!e.defaultPrevented && (e.metaKey || e.ctrlKey) && !e.altKey && e.key.toLowerCase() === "s") { e.preventDefault(); saveCode(); }
    });
  }
}

if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", init); else init();

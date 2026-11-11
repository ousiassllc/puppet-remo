// 配布物に同梱する第三者のライセンス表記（licenses/THIRD_PARTY_NOTICES.txt）を作る（Issue #133。docs/architecture/frontend.md §8.5）。
// このファイルは Git に入れる生成物で、アプリの「ライセンス表記」の画面（/licenses。go:embed）と、インストーラ（build/windows/nsis）が使う。
//
//   bun scripts/third-party-notices.mjs           licenses/THIRD_PARTY_NOTICES.txt を作り直す
//   bun scripts/third-party-notices.mjs --check   作り直した内容が Git の内容と同じかを確かめる（違えば終了コード 1。CI が使う）
//
// 載せるのは、配布物に実際に入るものだけ:
//   - フォント（dist/fonts。@fontsource-variable/* の LICENSE をそのまま）
//   - アイコン（Iconoir。scripts/third-party/iconoir-LICENSE）
//   - フロントエンドのライブラリ（htmx・拡張・Lit・Tailwind CSS。node_modules の LICENSE をそのまま）
//   - Go の標準ライブラリと、Windows 向けの本番ビルド（-tags production）に入る Go のモジュール（LICENSE・COPYING をそのまま）
// 同じ本文のものは 1 つにまとめて、対象の名前を並べる。フォントや依存を足したら、このスクリプトに足して作り直す。
import { spawnSync } from "node:child_process";
import { existsSync, readdirSync, readFileSync, statSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "../..");
const outPath = join(root, "licenses/THIRD_PARTY_NOTICES.txt");
const nm = join(root, "static/node_modules");

const fonts = [
  { pkg: "sora", name: "Sora" },
  {
    pkg: "noto-sans-jp",
    name: "Noto Sans JP",
    note:
      "同梱するのは、JIS X 0208 の字（かな・カナ・第1/第2水準漢字・記号）と英数字に絞った改変版（サブセット）です。\n" +
      "フォントの著作権表示: (c) 2014-2021 Adobe (http://www.adobe.com/), with Reserved Font Name 'Source'.\n" +
      "改変版は 'Noto Sans JP' の名前で配り、Reserved Font Name の 'Source' は名前に使っていません。",
  },
  { pkg: "jetbrains-mono", name: "JetBrains Mono" },
];
const nodeLibs = ["htmx.org", "htmx-ext-response-targets", "htmx-ext-sse", "lit", "lit-html", "lit-element", "@lit/reactive-element", "tailwindcss"];

const read = (path) => readFileSync(path, "utf8").replace(/\r\n/g, "\n").trim();
const compare = (a, b) => (a < b ? -1 : a > b ? 1 : 0);
const licenseFile = /^(LICEN[CS]E|COPYING|UNLICENSE)([-._].*)?$/i;

function licenseFiles(dir) {
  const names = readdirSync(dir).filter((n) => licenseFile.test(n) && statSync(join(dir, n)).isFile()).sort(compare);
  if (names.length === 0) {
    throw new Error(`ライセンスのファイルが見つからない: ${dir}`);
  }
  return names.map((n) => [n, read(join(dir, n))]);
}

function nodeVersion(pkg) {
  return JSON.parse(readFileSync(join(nm, pkg, "package.json"), "utf8")).version;
}

function run(cmd, args, env = {}) {
  const r = spawnSync(cmd, args, { cwd: root, encoding: "utf8", env: { ...process.env, ...env }, maxBuffer: 64 * 1024 * 1024 });
  if (r.status !== 0) {
    throw new Error(`${cmd} ${args.join(" ")} が失敗した:\n${r.stderr}`);
  }
  return r.stdout;
}

// Windows 向けの本番ビルドに入る Go のモジュール（自分自身を除く）。
function goModules() {
  const out = run(
    "go",
    ["list", "-e", "-deps", "-tags", "production", "-f", "{{with .Module}}{{.Path}}\t{{.Version}}\t{{.Dir}}{{end}}", "./cmd/puppet-remo"],
    { GOOS: "windows", GOARCH: "amd64", CGO_ENABLED: "0" },
  );
  const mods = new Map();
  for (const line of out.split("\n")) {
    const [path, version, dir] = line.split("\t");
    if (path && version && dir) {
      mods.set(path, { version, dir });
    }
  }
  return [...mods].sort(([a], [b]) => compare(a, b));
}

// 同じ本文を 1 つにまとめる: section.groups は Map<本文, 名前[]>（登録順）。
const section = (title) => ({ title, groups: new Map() });
function add(sec, name, text) {
  const names = sec.groups.get(text) ?? [];
  names.push(name);
  sec.groups.set(text, names);
}

const fontSec = section("フォント（SIL Open Font License 1.1）");
for (const { pkg, name, note } of fonts) {
  const label = `${name}（@fontsource-variable/${pkg} ${nodeVersion(`@fontsource-variable/${pkg}`)}）`;
  add(fontSec, note ? `${label}\n${note}` : label, read(join(nm, "@fontsource-variable", pkg, "LICENSE")));
}

const iconSec = section("アイコン（MIT License）");
add(iconSec, "Iconoir（https://iconoir.com。regular の SVG の path を internal/web/atoms/icon.templ に取り込んでいる）", read(join(root, "static/scripts/third-party/iconoir-LICENSE")));

const libSec = section("フロントエンドのライブラリ");
for (const pkg of nodeLibs) {
  add(libSec, `${pkg} ${nodeVersion(pkg)}`, read(join(nm, pkg, "LICENSE")));
}

const goSec = section("Go の標準ライブラリとモジュール");
add(goSec, "Go 標準ライブラリ", read(join(run("go", ["env", "GOROOT"]).trim(), "LICENSE")));
for (const [path, { version, dir }] of goModules()) {
  const files = licenseFiles(dir);
  for (const [file, text] of files) {
    add(goSec, `${path} ${version}${files.length > 1 ? `（${file}）` : ""}`, text);
  }
}

const rule = "=".repeat(78);
const thin = "-".repeat(78);
let out = [
  "第三者のライセンス表記（THIRD PARTY NOTICES）",
  rule,
  "",
  "puppet-remo は、次のソフトウェアを同梱しています。それぞれのライセンスの全文を、著作権表示とあわせて以下に載せます。",
  "このファイルは static/scripts/third-party-notices.mjs が作ります（手で編集しません）。",
  "",
].join("\n");
for (const sec of [fontSec, iconSec, libSec, goSec]) {
  out += `\n${rule}\n${sec.title}\n${rule}\n`;
  for (const [text, names] of sec.groups) {
    out += `\n${names.join("\n")}\n${thin}\n${text}\n`;
  }
}

if (process.argv.includes("--check")) {
  const current = existsSync(outPath) ? readFileSync(outPath, "utf8") : "";
  if (current !== out) {
    console.error("licenses/THIRD_PARTY_NOTICES.txt が古い。`cd static && bun scripts/third-party-notices.mjs` で作り直して、コミットすること。");
    process.exit(1);
  }
  console.log("licenses/THIRD_PARTY_NOTICES.txt: OK");
} else {
  writeFileSync(outPath, out);
  console.log(`${outPath} を作った（${out.length} 文字）`);
}

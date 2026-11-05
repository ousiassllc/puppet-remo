// フォント（Sora・Noto Sans JP・JetBrains Mono。いずれも SIL OFL。Ousiass デザインガイド T1）を dist/fonts にコピーし、
// それを読み込む @font-face を src/css/fonts.generated.css に生成する（app.css が @import する。生成物は Git に入れない）。
// フォントファイルは @fontsource-variable/* の可変フォント（woff2）で、版は bun.lock で固定する。
// 外部の CDN は使わない（CSP は同一オリジンのみ。docs/architecture/frontend.md §7.1）。
// 'Sora Variable' などの Fontsource の名前は、ガイドの font-family（Sora・Noto Sans JP・JetBrains Mono）に付け替える。
// latin-ext・cyrillic・greek・vietnamese など日本語の UI で使わない文字セットは、サイズを抑えるため同梱しない。
import { copyFileSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";

const families = [
  { pkg: "sora", from: "Sora Variable", to: "Sora" },
  { pkg: "noto-sans-jp", from: "Noto Sans JP Variable", to: "Noto Sans JP" },
  { pkg: "jetbrains-mono", from: "JetBrains Mono Variable", to: "JetBrains Mono" },
];
// 同梱するサブセット: ファイル名（Fontsource の命名）が、これらの文字セットのものだけ。
// Noto Sans JP は、日本語の文字を 100 余りの unicode-range の断片（数字のファイル名）に分けており、すべて同梱する。
const skipSubsets = /-(?:latin-ext|cyrillic-ext|cyrillic|greek-ext|greek|vietnamese)-/;

mkdirSync("dist/fonts", { recursive: true });
let css = "/* scripts/copy-fonts.mjs が生成する。手で編集しない。 */\n";
for (const { pkg, from, to } of families) {
  const dir = `node_modules/@fontsource-variable/${pkg}`;
  const source = readFileSync(`${dir}/index.css`, "utf8");
  // index.css は `/* 名前 */ @font-face { ... }` の並び。italic は使わない。
  for (const block of source.split(/(?=\/\* )/)) {
    const file = /url\(\.\/files\/([^)]+\.woff2)\)/.exec(block)?.[1];
    if (!file || skipSubsets.test(file) || /italic/.test(file)) {
      continue;
    }
    copyFileSync(`${dir}/files/${file}`, `dist/fonts/${file}`);
    css += block
      .replace(`'${from}'`, `"${to}"`)
      .replace(`./files/${file}`, `../fonts/${file}`)
      .trimEnd() + "\n";
  }
}
writeFileSync("src/css/fonts.generated.css", css);

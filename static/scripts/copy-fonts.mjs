// フォント（Sora・Noto Sans JP・JetBrains Mono。いずれも SIL OFL。Ousiass デザインガイド T1）を dist/fonts に置き、
// それを読み込む @font-face を src/css/fonts.generated.css に生成する（app.css が @import する。生成物は Git に入れない）。
// フォントファイルは @fontsource-variable/* の可変フォント（woff2）で、版は bun.lock で固定する。
// 外部の CDN は使わない（CSP は同一オリジンのみ。docs/architecture/frontend.md §7.1）。
// 'Sora Variable' などの Fontsource の名前は、ガイドの font-family（Sora・Noto Sans JP・JetBrains Mono）に付け替える。
// latin-ext・cyrillic・greek・vietnamese など日本語の UI で使わない文字セットは、サイズを抑えるため同梱しない。
// Noto Sans JP の日本語の断片（数字のファイル名。unicode-range 分割）は、JIS X 0208（かな・カナ・第1/第2水準漢字・記号）
// の字だけに絞って作り直す（subset-font = HarfBuzz の wasm。bun だけで決定的に動く。docs/architecture/frontend.md §8.5）。
// 範囲外の字（𠮷 など）は OS のフォントで表示する。
import { copyFileSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import subsetFont from "subset-font";

const families = [
  { pkg: "sora", from: "Sora Variable", to: "Sora" },
  { pkg: "noto-sans-jp", from: "Noto Sans JP Variable", to: "Noto Sans JP", subsetToJIS: true },
  { pkg: "jetbrains-mono", from: "JetBrains Mono Variable", to: "JetBrains Mono" },
];
// 同梱するサブセット: ファイル名（Fontsource の命名）が、これらの文字セットのものだけ。
const skipSubsets = /-(?:latin-ext|cyrillic-ext|cyrillic|greek-ext|greek|vietnamese)-/;

// JIS X 0208 の 6879 字（1〜8 区の記号・英数・かな・カナ・ギリシャ・キリル・罫線、16〜84 区の第1/第2水準漢字）を、
// EUC-JP の復号（WHATWG Encoding の index。JIS X 0208 は改訂されないので結果は不変）で求める。
// 復号器は 〜 ‖ − ¢ £ ¬ を Windows 系の字（～ ∥ － ￠ ￡ ￢）にするので、本来の字を足す。半角カナも足す。
function jisX0208Codepoints() {
  const decoder = new TextDecoder("euc-jp");
  const set = new Set();
  for (const [firstRow, lastRow] of [[1, 8], [16, 84]]) {
    for (let row = firstRow; row <= lastRow; row++) {
      for (let cell = 1; cell <= 94; cell++) {
        const s = decoder.decode(new Uint8Array([0xa0 + row, 0xa0 + cell]));
        if (s !== "\uFFFD") {
          set.add(s.codePointAt(0));
        }
      }
    }
  }
  if (set.size !== 6879) {
    throw new Error(`JIS X 0208 の字数が 6879 ではない: ${set.size}`);
  }
  for (const cp of [0x2014, 0x2016, 0x2212, 0x301c, 0xa2, 0xa3, 0xac]) {
    set.add(cp);
  }
  for (let cp = 0xff61; cp <= 0xff9f; cp++) {
    set.add(cp);
  }
  return set;
}

// `U+25ee8,U+26ff6-26ff8,...` を符号位置の配列にする。
function parseUnicodeRange(range) {
  const cps = [];
  for (const part of range.replace(/U\+/g, "").split(",")) {
    const [lo, hi = lo] = part.trim().split("-").map((h) => Number.parseInt(h, 16));
    for (let cp = lo; cp <= hi; cp++) {
      cps.push(cp);
    }
  }
  return cps;
}

// 昇順の符号位置の配列を `U+3041-3096,U+309d-309f` の形にする。
function formatUnicodeRange(sortedCps) {
  const ranges = [];
  for (const cp of sortedCps) {
    const last = ranges.at(-1);
    if (last && last[1] === cp - 1) {
      last[1] = cp;
    } else {
      ranges.push([cp, cp]);
    }
  }
  return ranges
    .map(([lo, hi]) => (lo === hi ? `U+${lo.toString(16)}` : `U+${lo.toString(16)}-${hi.toString(16)}`))
    .join(",");
}

const jis = jisX0208Codepoints();
mkdirSync("dist/fonts", { recursive: true });
let css = "/* scripts/copy-fonts.mjs が生成する。手で編集しない。 */\n";
for (const { pkg, from, to, subsetToJIS } of families) {
  const dir = `node_modules/@fontsource-variable/${pkg}`;
  const source = readFileSync(`${dir}/index.css`, "utf8");
  // index.css は `/* 名前 */ @font-face { ... }` の並び。italic は使わない。
  for (const block of source.split(/(?=\/\* )/)) {
    const file = /url\(\.\/files\/([^)]+\.woff2)\)/.exec(block)?.[1];
    if (!file || skipSubsets.test(file) || /italic/.test(file)) {
      continue;
    }
    let unicodeRange = /unicode-range:\s*([^;]+);/.exec(block)?.[1];
    // 数字のファイル名が日本語の断片。latin の断片（ASCII など）は、そのまま同梱する。
    if (subsetToJIS && /-\d+-wght-/.test(file)) {
      const kept = parseUnicodeRange(unicodeRange).filter((cp) => jis.has(cp)).sort((a, b) => a - b);
      if (kept.length === 0) {
        continue;
      }
      const subset = await subsetFont(readFileSync(`${dir}/files/${file}`), String.fromCodePoint(...kept), {
        targetFormat: "woff2",
      });
      writeFileSync(`dist/fonts/${file}`, subset);
      unicodeRange = formatUnicodeRange(kept);
    } else {
      copyFileSync(`${dir}/files/${file}`, `dist/fonts/${file}`);
    }
    css += block
      .replace(`'${from}'`, `"${to}"`)
      .replace(`./files/${file}`, `../fonts/${file}`)
      .replace(/unicode-range:\s*[^;]+;/, `unicode-range: ${unicodeRange};`)
      .trimEnd() + "\n";
  }
}
writeFileSync("src/css/fonts.generated.css", css);

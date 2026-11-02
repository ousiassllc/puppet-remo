// build/appicon.svg（大きいサイズ）と build/appicon-small.svg（小さいサイズ）から、アイコンの生成物を作る。
// 出力: build/appicon.png（512px）・build/windows/icon.ico（7 サイズ）・internal/app/tray.png（64px）・見本 docs/screenshots/icon/app-icon-sizes.png
// 使い方: cd build/icons && bun install --frozen-lockfile && bun render.mjs（task common:generate:icons）
import { Resvg } from "@resvg/resvg-js";
import { readFileSync, writeFileSync, mkdirSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "../..");
const at = (p) => resolve(root, p);
const large = readFileSync(at("build/appicon.svg"));
const small = readFileSync(at("build/appicon-small.svg"));

const ICO_SIZES = [16, 24, 32, 48, 64, 128, 256];
const SMALL_MAX = 64; // この大きさ以下は小さい版から直接描く（縮小で作らない）。大きい版の細い線は 64px 以下では灰色に薄れる
const TRAY_SIZE = 64;

const svgFor = (size) => (size <= SMALL_MAX ? small : large);
const iconPng = (size) => new Resvg(svgFor(size), { fitTo: { mode: "width", value: size } }).render().asPng();

// ICO: ヘッダ 6 バイト + エントリ 16 バイト × 枚数 + PNG 本体（256px は幅・高さを 0 で表す）
function ico(sizes) {
  const images = sizes.map(iconPng);
  const head = Buffer.alloc(6 + 16 * sizes.length);
  head.writeUInt16LE(1, 2); // type = icon
  head.writeUInt16LE(sizes.length, 4);
  let offset = head.length;
  sizes.forEach((size, i) => {
    const e = 6 + 16 * i;
    head[e] = head[e + 1] = size === 256 ? 0 : size;
    head.writeUInt16LE(1, e + 4); // planes
    head.writeUInt16LE(32, e + 6); // bit count
    head.writeUInt32LE(images[i].length, e + 8);
    head.writeUInt32LE(offset, e + 12);
    offset += images[i].length;
  });
  return Buffer.concat([head, ...images]);
}

// 見本: 16・32・256px の実寸（明るい背景・暗い背景 #202020）と、16・32px を最近傍補間で 256px に拡大した行。
function preview() {
  const dataUri = (size) => `data:image/png;base64,${iconPng(size).toString("base64")}`;
  const pad = 24;
  const gap = 48;
  const rowH = 256 + pad * 2;
  const W = pad * 2 + 256 * 2 + gap;
  const cell = (size, x, y, scale = 1) =>
    `<image x="${x}" y="${y}" width="${size * scale}" height="${size * scale}" href="${dataUri(size)}" image-rendering="optimizeSpeed"/>`;
  const realRow = (y, bg) => {
    const sizes = [16, 32, 256];
    let x = pad;
    let out = `<rect y="${y}" width="${W}" height="${rowH}" fill="${bg}"/>`;
    for (const s of sizes) {
      out += cell(s, x, y + pad + 256 - s); // 下端をそろえる
      x += s + gap;
    }
    return out;
  };
  const zoomRow = (y) =>
    `<rect y="${y}" width="${W}" height="${rowH}" fill="#202020"/>` + cell(16, pad, y + pad, 16) + cell(32, pad + 256 + gap, y + pad, 8);
  const svg =
    `<svg xmlns="http://www.w3.org/2000/svg" width="${W}" height="${rowH * 3}">` +
    realRow(0, "#e8e8e8") + realRow(rowH, "#202020") + zoomRow(rowH * 2) +
    `</svg>`;
  return new Resvg(svg).render().asPng();
}

writeFileSync(at("build/appicon.png"), iconPng(512));
writeFileSync(at("build/windows/icon.ico"), ico(ICO_SIZES));
writeFileSync(at("internal/app/tray.png"), iconPng(TRAY_SIZE));
mkdirSync(at("docs/screenshots/icon"), { recursive: true });
writeFileSync(at("docs/screenshots/icon/app-icon-sizes.png"), preview());

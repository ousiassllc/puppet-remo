// htmx の設定（docs/architecture/frontend.md §7.1）。classic スクリプトとして htmx.min.js の直後に読み込み、
// htmx の初期化（DOMContentLoaded）より前に適用する。<meta name="htmx-config"> は使わない。
declare const htmx: {
  config: {
    allowEval: boolean;
    includeIndicatorStyles: boolean;
  };
};

// CSP（script-src 'self'）と整合させる: eval を使う機能（hx-on:*、js: 接頭辞、hx-trigger のフィルター）を無効にする。
htmx.config.allowEval = false;
// htmx が既定で挿入するインラインの <style> を作らせない。.htmx-indicator は app.css に定義する。
htmx.config.includeIndicatorStyles = false;

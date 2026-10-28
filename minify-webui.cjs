// minify-webui.cjs — perkecil file WebUI hasil salinan (module/webui) tanpa mengubah
// perilaku: JS/CSS di-minify dengan esbuild, blok <style>/<script> inline di HTML
// juga, komentar HTML dibuang. Spasi di antara tag HTML TIDAK disentuh (aman untuk
// layout inline). Setiap file diproses terpisah: bila gagal, file asli dibiarkan.
// Pakai:  node minify-webui.cjs <folder-webui>
const fs = require('fs'), path = require('path');
let esbuild;
try { esbuild = require('esbuild'); } catch (e) { console.log('esbuild tidak ada — minify dilewati'); process.exit(0); }
const root = process.argv[2];
if (!root) { console.log('pakai: node minify-webui.cjs <folder-webui>'); process.exit(1); }
const TARGET = 'chrome87';
const js = s => esbuild.transformSync(s, { loader: 'js', minify: true, target: TARGET, legalComments: 'none' }).code.trim();
const css = s => esbuild.transformSync(s, { loader: 'css', minify: true, target: TARGET, legalComments: 'none' }).code.trim();
function html(s) {
  s = s.replace(/<!--(?!\[if)[\s\S]*?-->/g, '');
  s = s.replace(/<style([^>]*)>([\s\S]*?)<\/style>/gi, (m, a, b) => b.trim() ? `<style${a}>${css(b)}</style>` : m);
  s = s.replace(/<script([^>]*)>([\s\S]*?)<\/script>/gi, (m, a, b) => {
    if (/\bsrc\s*=/i.test(a) || !b.trim()) return m;
    if (/\btype\s*=\s*["']?(?!module|text\/javascript|application\/javascript)/i.test(a)) return m;
    return `<script${a}>${js(b)}</script>`;
  });
  return s;
}
function walk(d, out) { for (const e of fs.readdirSync(d, { withFileTypes: true })) { const p = path.join(d, e.name); e.isDirectory() ? walk(p, out) : out.push(p); } return out; }
let before = 0, after = 0;
for (const f of walk(root, [])) {
  const ext = path.extname(f).toLowerCase();
  const fn = { '.js': js, '.css': css, '.html': html }[ext];
  if (!fn) continue;
  const src = fs.readFileSync(f, 'utf8');
  try {
    const out = fn(src);
    if (out && out.length < src.length) { fs.writeFileSync(f, out); before += src.length; after += out.length; console.log(path.relative(root, f) + ': ' + src.length + ' -> ' + out.length); }
  } catch (e) { console.log('dilewati ' + path.relative(root, f) + ': ' + String(e.message).split('\n')[0]); }
}
console.log('total ' + before + ' -> ' + after);

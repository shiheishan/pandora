// 产物大小：首屏（index.html 引用的文件加它们的静态 import 闭包）与全部 chunk 的原始 / gzip-9 / brotli-11 大小（KiB）。
// 用法：node sizes.mjs <dist/admin 或 dist/portal>
// 注意：nginx 实际用的是 gzip_comp_level（现为 5），线上传输量会比这里的 gzip-9 略大。
import fs from 'node:fs'; import path from 'node:path'; import zlib from 'node:zlib'
const dir = process.argv[2]; const A = path.join(dir,'assets')
const sz = f => { const b = fs.readFileSync(path.join(A,f)); return { raw:b.length, gz: zlib.gzipSync(b,{level:9}).length, br: zlib.brotliCompressSync(b,{params:{[zlib.constants.BROTLI_PARAM_QUALITY]:11}}).length } }
const files = fs.readdirSync(A)
const staticDeps = f => { if(!f.endsWith('.js')) return []; const s = fs.readFileSync(path.join(A,f),'utf8'); const out=new Set()
  for (const m of s.matchAll(/(?:^|[;}\s])import\s*(?:[\w${},*\s]+from\s*)?["']\.\/([^"']+)["']/g)) out.add(m[1])
  return [...out] }
const dynDeps = f => { const s = fs.readFileSync(path.join(A,f),'utf8'); return [...new Set([...s.matchAll(/import\(["']\.\/([^"']+)["']\)/g)].map(m=>m[1]))] }
const html = fs.readFileSync(path.join(dir,'index.html'),'utf8')
const initial = new Set([...html.matchAll(/(?:src|href)="\.\/assets\/([^"]+)"/g)].map(m=>m[1]))
const q=[...initial]; while(q.length){const f=q.pop(); for(const d of staticDeps(f)) if(!initial.has(d)){initial.add(d);q.push(d)}}
const sum = (list) => list.reduce((a,f)=>{const s=sz(f);a.raw+=s.raw;a.gz+=s.gz;a.br+=s.br;return a},{raw:0,gz:0,br:0})
const k = n => (n/1024).toFixed(1)
const js=[...initial].filter(f=>f.endsWith('.js')), css=[...initial].filter(f=>f.endsWith('.css'))
console.log('== initial (html-referenced + static imports) ==')
for (const f of initial){const s=sz(f);console.log(f.padEnd(40),k(s.raw),k(s.gz),k(s.br))}
for (const [n,l] of [['JS',js],['CSS',css]]){const s=sum(l);console.log(`TOTAL ${n}`.padEnd(40),k(s.raw),k(s.gz),k(s.br))}
const all = files.filter(f=>/\.(js|css)$/.test(f)); const sa=sum(all); console.log('ALL js+css'.padEnd(40),k(sa.raw),k(sa.gz),k(sa.br))
const fonts=files.filter(f=>f.endsWith('.woff2')); console.log('fonts', fonts.map(f=>`${f} ${k(sz(f).raw)}`).join(', '))
console.log('== top 10 chunks ==')
all.map(f=>({f,...sz(f)})).sort((a,b)=>b.raw-a.raw).slice(0,10).forEach(x=>console.log(x.f.padEnd(40),k(x.raw),k(x.gz),k(x.br), 'static:', staticDeps(x.f).join(',')))
console.log('== entry dynamic imports ==')
for (const f of js) console.log(f, '->', dynDeps(f).join(' '))

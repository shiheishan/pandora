// 路由分包：入口里每个 import(`./x.js`) 连同 __vite__mapDeps 带出的依赖文件，合计多大。
// 用法：node routes.mjs <dist/admin 或 dist/portal>
// 依赖 Vite 产物里 `m.f=[...]` 与 `import(`./…`),__vite__mapDeps([…])` 的写法；Vite 升级改了写法时这里会抛错。
import fs from 'node:fs'; import path from 'node:path'; import zlib from 'node:zlib'
const dir=process.argv[2]; const A=path.join(dir,'assets')
const entry=fs.readdirSync(A).find(f=>/^index-.*\.js$/.test(f)); const s=fs.readFileSync(path.join(A,entry),'utf8')
const arr=JSON.parse(s.match(/m\.f=(\[[^\]]*\])/)[1]).map(x=>x.slice(2))
const sz=f=>{const b=fs.readFileSync(path.join(A,f));return{raw:b.length,gz:zlib.gzipSync(b,{level:9}).length,br:zlib.brotliCompressSync(b).length}}
const k=n=>(n/1024).toFixed(1)
// 同一个分包可能被引用多次（如后台的路由表与 prefetch.ts），只列一次
const seen=new Set()
for (const m of s.matchAll(/import\(`\.\/([^`]+)`\),__vite__mapDeps\(\[([\d,]*)\]\)/g)){
  if (seen.has(m[1])) continue; seen.add(m[1])
  const files=[m[1],...m[2].split(',').filter(Boolean).map(i=>arr[+i])].filter((v,i,a)=>a.indexOf(v)===i)
  const t=files.reduce((a,f)=>{const x=sz(f);a.raw+=x.raw;a.gz+=x.gz;a.br+=x.br;return a},{raw:0,gz:0,br:0})
  console.log(m[1].padEnd(28),`+${files.length} files`, k(t.raw),k(t.gz),k(t.br), files.filter(f=>f!==m[1]).join(' '))
}

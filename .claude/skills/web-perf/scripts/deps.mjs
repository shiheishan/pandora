// 依赖构成：读 source-map-explorer 的 --json 输出，按 npm 包（自家代码按 src/ 下两级目录）汇总字节数，列前 14。
// 用法：node deps.mjs <sme.json> [只看文件名含这个子串的 bundle，如 index-]
import fs from 'node:fs'
const j=JSON.parse(fs.readFileSync(process.argv[2],'utf8'))
const byPkg={}, byFile={}
for (const r of j.results){ for (const [f,v] of Object.entries(r.files)){ const sz=v.size??v
  const m=f.match(/node_modules\/((?:@[^/]+\/)?[^/]+)/); const key=m?m[1]:(f.startsWith('[')?f:'(app) '+f.replace(/^.*?src\//,'src/').split('/').slice(0,3).join('/'))
  byPkg[key]=(byPkg[key]||0)+sz
  if (process.argv[3] && r.bundleName.includes(process.argv[3])) byFile[key]=(byFile[key]||0)+sz }}
const show=o=>Object.entries(o).sort((a,b)=>b[1]-a[1]).slice(0,14).forEach(([k,v])=>console.log((v/1024).toFixed(1).padStart(8),k))
show(byPkg); if(process.argv[3]){console.log('--- in',process.argv[3]); show(byFile)}

package server

var dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>k8s-doctor</title>
<style>
:root{
  --bg:#0b0d14;--s1:#12151f;--s2:#1a1e2e;--s3:#222840;--s4:#2a3050;
  --b1:#252a3d;--b2:#353c58;
  --t1:#e2e8f0;--t2:#8892b0;--t3:#4a5568;
  --red:#f87171;--red-bg:#2d0a0a;--red-br:#5c1a1a;
  --amb:#fbbf24;--amb-bg:#2d1f00;--amb-br:#5c3d00;
  --grn:#4ade80;--grn-bg:#0d2b1a;--grn-br:#1a5c35;
  --blu:#60a5fa;--blu-bg:#0d1f3c;--blu-br:#1a3a6e;
  --r:8px;--rl:12px;
}
*{margin:0;padding:0;box-sizing:border-box}
body{font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',sans-serif;background:var(--bg);color:var(--t1);font-size:13px;line-height:1.5;min-height:100vh}
#topbar{height:52px;background:var(--s1);border-bottom:1px solid var(--b1);display:flex;align-items:center;padding:0 20px;gap:14px;position:sticky;top:0;z-index:300}
.logo{font-size:15px;font-weight:700;color:var(--blu);letter-spacing:-.4px;flex-shrink:0}
.cpill{background:var(--s2);border:1px solid var(--b1);border-radius:20px;padding:4px 12px;font-size:12px;color:var(--t2);display:flex;align-items:center;gap:6px}
.cdot{width:7px;height:7px;border-radius:50%;flex-shrink:0}
.nav{display:flex;gap:2px;background:var(--s2);border:1px solid var(--b1);border-radius:8px;padding:3px}
.nb{padding:4px 12px;border-radius:5px;font-size:12px;cursor:pointer;color:var(--t2);background:none;border:none;transition:all .15s;white-space:nowrap}
.nb:hover{color:var(--t1)}.nb.on{background:var(--s3);color:var(--t1);font-weight:500}
.nc{font-size:10px;border-radius:8px;padding:1px 5px;margin-left:3px}
.nc.r{background:var(--red-bg);color:var(--red)}.nc.a{background:var(--amb-bg);color:var(--amb)}.nc.g{background:var(--grn-bg);color:var(--grn)}
.tright{margin-left:auto;display:flex;align-items:center;gap:10px}
.hbadge{display:flex;align-items:center;gap:6px;padding:5px 13px;border-radius:20px;font-size:12px;font-weight:600;border:1px solid}
.hbadge.ok{background:var(--grn-bg);color:var(--grn);border-color:var(--grn-br)}.hbadge.warn{background:var(--amb-bg);color:var(--amb);border-color:var(--amb-br)}.hbadge.crit{background:var(--red-bg);color:var(--red);border-color:var(--red-br)}
.ldot{width:7px;height:7px;border-radius:50%;animation:pulse 2s infinite}
.ldot.ok{background:var(--grn)}.ldot.warn{background:var(--amb)}.ldot.crit{background:var(--red);animation:pulse .7s infinite}
@keyframes pulse{0%,100%{opacity:1}50%{opacity:.2}}
.btn{background:var(--s2);border:1px solid var(--b2);color:var(--t2);padding:5px 12px;border-radius:6px;cursor:pointer;font-size:12px;transition:all .15s}
.btn:hover{color:var(--t1);border-color:var(--t3)}
.utime{font-size:11px;color:var(--t3)}
#app{display:flex;min-height:calc(100vh - 52px)}
#main{flex:1;overflow-y:auto;padding:16px;min-width:0}
#drawer{width:360px;flex-shrink:0;background:var(--s1);border-left:1px solid var(--b1);overflow-y:auto;transition:width .2s}
#drawer.col{width:0;overflow:hidden}
.stats{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:10px;margin-bottom:16px}
.stat{background:var(--s1);border:1px solid var(--b1);border-radius:var(--r);padding:14px 16px;cursor:pointer;transition:all .15s;position:relative;overflow:hidden}
.stat::before{content:'';position:absolute;left:0;top:0;bottom:0;width:3px;border-radius:0}
.stat.r::before{background:var(--red)}.stat.a::before{background:var(--amb)}.stat.g::before{background:var(--grn)}.stat.b::before{background:var(--blu)}
.stat:hover{border-color:var(--b2);background:var(--s2)}.stat.on{border-color:var(--blu);background:var(--blu-bg)}
.slbl{font-size:11px;color:var(--t3);text-transform:uppercase;letter-spacing:.07em;margin-bottom:5px}
.sval{font-size:30px;font-weight:700;line-height:1;margin-bottom:4px}
.sval.r{color:var(--red)}.sval.a{color:var(--amb)}.sval.g{color:var(--grn)}.sval.b{color:var(--blu)}
.ssub{font-size:11px;color:var(--t3)}
.rccard{background:var(--s1);border:1px solid var(--b1);border-radius:var(--rl);margin-bottom:14px;overflow:hidden;border-top-width:2px}
.rccard.ok{border-top-color:var(--grn)}.rccard.warn{border-top-color:var(--amb)}.rccard.crit{border-top-color:var(--red)}
.rch{display:flex;align-items:center;gap:12px;padding:14px 16px;border-bottom:1px solid var(--b1)}
.rcico{width:36px;height:36px;border-radius:50%;display:flex;align-items:center;justify-content:center;font-size:15px;flex-shrink:0}
.rcico.ok{background:var(--grn-bg);color:var(--grn)}.rcico.warn{background:var(--amb-bg);color:var(--amb)}.rcico.crit{background:var(--red-bg);color:var(--red)}
.rctitle{font-size:13px;font-weight:600}.rcsub{font-size:11px;color:var(--t3);margin-top:2px}
.cpill2{margin-left:auto;padding:3px 10px;border-radius:12px;font-size:11px;font-weight:600;border:1px solid}
.cpill2.h{background:var(--grn-bg);color:var(--grn);border-color:var(--grn-br)}.cpill2.m{background:var(--amb-bg);color:var(--amb);border-color:var(--amb-br)}.cpill2.l{background:var(--red-bg);color:var(--red);border-color:var(--red-br)}
.rcb{padding:14px 16px}
.rcsentence{font-size:14px;color:var(--t1);line-height:1.65;margin-bottom:12px}
.rcfacts{display:grid;grid-template-columns:1fr 1fr;gap:8px;margin-bottom:12px}
.rcfact{background:var(--s2);border-radius:6px;padding:9px 11px}
.rcfl{font-size:10px;color:var(--t3);text-transform:uppercase;letter-spacing:.06em;margin-bottom:3px}
.rcfv{font-size:12px;color:var(--t1);font-weight:500}
.rcfv.b{color:var(--blu)}
.cmdbox{background:var(--bg);border:1px solid var(--b1);border-radius:6px;padding:10px 12px;display:flex;align-items:center;gap:10px;margin-top:4px}
.cmdtxt{font-family:'SF Mono',monospace;font-size:12px;color:var(--blu);flex:1;word-break:break-all}
.copybtn{background:var(--s2);border:1px solid var(--b2);color:var(--t2);padding:4px 10px;border-radius:5px;cursor:pointer;font-size:11px;white-space:nowrap;flex-shrink:0}
.copybtn:hover{color:var(--t1)}
.cbar{display:flex;align-items:center;gap:8px;margin-top:10px}
.ctrack{height:3px;background:var(--s3);border-radius:2px;flex:1;max-width:180px}
.cfill{height:3px;border-radius:2px;transition:width .5s}
.clbl{font-size:11px;color:var(--t3)}
.sec{background:var(--s1);border:1px solid var(--b1);border-radius:var(--rl);margin-bottom:12px;overflow:hidden}
.seh{display:flex;align-items:center;padding:11px 16px;cursor:pointer;user-select:none;transition:background .1s}
.seh:hover{background:var(--s2)}
.setitle{font-size:13px;font-weight:600;flex:1}
.semeta{display:flex;align-items:center;gap:6px}
.chev{font-size:9px;color:var(--t3);transition:transform .2s;margin-left:8px}
.seh.op .chev{transform:rotate(90deg)}
.seb{border-top:1px solid var(--b1)}.seb.hd{display:none}
.fbar{display:flex;align-items:center;gap:8px;padding:9px 14px;border-bottom:1px solid var(--b1);background:var(--s2)}
.fbar input{flex:1;background:var(--bg);border:1px solid var(--b1);color:var(--t1);padding:5px 10px;border-radius:6px;font-size:12px;outline:none}
.fbar input:focus{border-color:var(--b2)}
.fbar select{background:var(--bg);border:1px solid var(--b1);color:var(--t2);padding:5px 8px;border-radius:6px;font-size:12px;outline:none;cursor:pointer}
.tw{overflow-x:auto}
table{width:100%;border-collapse:collapse;font-size:12px;table-layout:fixed}
th{text-align:left;padding:8px 14px;font-size:10px;font-weight:600;color:var(--t3);text-transform:uppercase;letter-spacing:.07em;background:var(--s2);border-bottom:1px solid var(--b1);white-space:nowrap}
td{padding:10px 14px;border-bottom:1px solid var(--b1);vertical-align:middle;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
tr:last-child td{border-bottom:none}
.tr{cursor:pointer;transition:background .1s}.tr:hover td{background:var(--s2)}.tr.op td{background:var(--s2)}
.exr td{padding:0;background:var(--bg)}
.exin{padding:12px 16px;border-left:3px solid var(--blu);margin:0 14px 12px;background:var(--s2);border-radius:0 var(--r) var(--r) 0}
.exgrid{display:grid;grid-template-columns:1fr 1fr;gap:8px;margin-bottom:8px}
.exitem{background:var(--s3);border-radius:5px;padding:8px 10px}
.exlbl{font-size:10px;color:var(--t3);text-transform:uppercase;letter-spacing:.06em;margin-bottom:2px}
.exval{font-size:12px;color:var(--t1);white-space:normal;word-break:break-word}
.exval.mono{font-family:monospace;font-size:11px;color:var(--blu)}
.badge{display:inline-flex;align-items:center;gap:3px;padding:2px 7px;border-radius:9px;font-size:11px;font-weight:500;white-space:nowrap;flex-shrink:0;border:1px solid}
.badge.r{background:var(--red-bg);color:var(--red);border-color:var(--red-br)}.badge.a{background:var(--amb-bg);color:var(--amb);border-color:var(--amb-br)}.badge.g{background:var(--grn-bg);color:var(--grn);border-color:var(--grn-br)}.badge.b{background:var(--blu-bg);color:var(--blu);border-color:var(--blu-br)}.badge.n{background:var(--s3);color:var(--t2);border-color:var(--b2)}
.sdot{width:6px;height:6px;border-radius:50%;display:inline-block}
.sdot.r{background:var(--red)}.sdot.a{background:var(--amb)}.sdot.g{background:var(--grn)}
.mbar{display:flex;align-items:center;gap:6px}
.mbt{flex:1;height:3px;background:var(--s3);border-radius:2px;min-width:40px}
.mbf{height:3px;border-radius:2px}
.mbf.r{background:var(--red)}.mbf.a{background:var(--amb)}.mbf.g{background:var(--grn)}
.mbp{font-size:10px;color:var(--t3);width:30px;text-align:right;flex-shrink:0}
.ditabs{display:flex;border-bottom:1px solid var(--b1);padding:0 14px;background:var(--s2)}
.ditab{padding:8px 12px;font-size:12px;cursor:pointer;color:var(--t2);border-bottom:2px solid transparent;transition:all .15s;white-space:nowrap}
.ditab:hover{color:var(--t1)}.ditab.on{color:var(--blu);border-bottom-color:var(--blu)}
.dipane{display:none}.dipane.on{display:block}
.diffval{display:flex;align-items:center;gap:5px;margin-top:3px}
.dold{font-family:monospace;font-size:11px;color:var(--red);text-decoration:line-through;max-width:150px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.darr{color:var(--t3);font-size:11px}
.dnew{font-family:monospace;font-size:11px;color:var(--grn);max-width:150px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.drblock{border-bottom:1px solid var(--b1)}
.drhead{display:flex;align-items:center;justify-content:space-between;padding:12px 16px;cursor:pointer;user-select:none}
.drhead:hover{background:var(--s2)}
.drtitle{font-size:11px;font-weight:600;color:var(--t2);text-transform:uppercase;letter-spacing:.08em;display:flex;align-items:center;gap:8px}
.drbody{padding:0 16px 12px}.drbody.hd{display:none}
.evrow{display:grid;grid-template-columns:52px 1fr;gap:8px;padding:8px 0;border-bottom:1px solid var(--b1);cursor:pointer}
.evrow:last-child{border-bottom:none}.evrow:hover .evmsg{color:var(--t1)}
.evtime{font-size:10px;color:var(--t3);font-family:monospace;padding-top:2px}
.evreason{font-size:12px;font-weight:600}
.evreason.r{color:var(--red)}.evreason.a{color:var(--amb)}.evreason.g{color:var(--t2)}
.evobj{font-size:11px;color:var(--t3);margin-top:1px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.evmsg{font-size:11px;color:var(--t3);margin-top:2px;display:-webkit-box;-webkit-line-clamp:2;-webkit-box-orient:vertical;overflow:hidden;white-space:normal}
.chrow{padding:8px 0;border-bottom:1px solid var(--b1);cursor:pointer}
.chrow:last-child{border-bottom:none}.chrow:hover{background:var(--s2);margin:0 -16px;padding:8px 16px}
.chname{font-size:12px;font-weight:500;color:var(--t1);white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.chmeta{display:flex;justify-content:space-between;align-items:center;margin-top:3px;gap:8px}
.chby{font-size:11px;color:var(--blu)}.chtime{font-size:10px;color:var(--t3)}
.chfield{font-size:11px;color:var(--t3);margin-top:2px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.chcorr{font-size:11px;color:var(--red);margin-top:3px}
.nrow{padding:9px 0;border-bottom:1px solid var(--b1)}
.nrow:last-child{border-bottom:none}
.nnrow{display:flex;justify-content:space-between;align-items:center;margin-bottom:6px}
.nname{font-size:12px;color:var(--t1);white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.empty{padding:28px;text-align:center;color:var(--t3);font-size:12px}
.ei{font-size:18px;color:var(--grn);margin-bottom:6px}
.toast{position:fixed;bottom:20px;right:20px;background:var(--s2);border:1px solid var(--b2);color:var(--t1);padding:9px 16px;border-radius:var(--r);font-size:12px;z-index:999;opacity:0;transform:translateY(6px);transition:all .2s;pointer-events:none}
.toast.show{opacity:1;transform:translateY(0)}
.swrap{display:flex;align-items:center;justify-content:center;gap:10px;padding:48px;color:var(--t3);font-size:13px}
.spin{width:18px;height:18px;border:2px solid var(--b2);border-top-color:var(--blu);border-radius:50%;animation:sp .7s linear infinite}
@keyframes sp{to{transform:rotate(360deg)}}
</style>
</head>
<body>
<div id="topbar">
  <span class="logo">k8s-doctor</span>
  <div class="cpill"><span class="cdot" id="cdot" style="background:var(--grn)"></span><span id="cname">loading...</span></div>
  <nav class="nav">
    <button class="nb on" onclick="setV('overview',this)">Overview</button>
    <button class="nb" onclick="setV('workloads',this)">Workloads</button>
    <button class="nb" onclick="setV('changes',this)">Changes <span class="nc a" id="nc-diff" style="display:none">0</span></button>
    <button class="nb" onclick="setV('events',this)">Events <span class="nc r" id="nc-warn" style="display:none">0</span></button>
    <button class="nb" onclick="setV('risks',this)">Risks <span class="nc r" id="nc-risk" style="display:none">0</span></button>
    <button class="nb" onclick="setV('inventory',this)">Inventory <span class="nc a" id="nc-inv" style="display:none">0</span></button>
  </nav>
  <div class="tright">
    <div class="hbadge ok" id="hb"><span class="ldot ok" id="ld"></span><span id="ht">—</span></div>
    <span class="utime" id="ut">—</span>
    <button class="btn" onclick="loadAll(true)">↻ Refresh</button>
    <button class="btn" onclick="togDrawer()">◫ Sidebar</button>
  </div>
</div>
<div id="app">
  <div id="main"><div class="swrap"><div class="spin"></div>Connecting...</div></div>
  <div id="drawer"><div id="dri"><div class="swrap"><div class="spin"></div></div></div></div>
</div>
<div class="toast" id="toast"></div>
<script>
const $=id=>document.getElementById(id);
let S={};let V='overview';let rt;
async function api(u){const r=await fetch(u);if(!r.ok)throw new Error(await r.text());return r.json()}
async function loadAll(m){
  if(m)toast('Refreshing...');
  try{
    const[snap,diag,evts,diffs,pred]=await Promise.all([api('/api/snapshot'),api('/api/diagnose?window=1h'),api('/api/events?window=2h'),api('/api/diff?window=1h'),api('/api/predict')]);
    S={snap,diag,events:evts,diffs,predict:pred};
    render();
    $('ut').textContent=new Date().toLocaleTimeString([],{hour:'2-digit',minute:'2-digit',second:'2-digit'});
    if(m)toast('Updated ✓');
  }catch(e){$('main').innerHTML='<div style="padding:20px;color:var(--red)">Error: '+esc(e.message)+'</div>'}
  clearTimeout(rt);rt=setTimeout(()=>loadAll(false),30000);
}
function render(){
  const sc=S.snap?.HealthScore||0;
  const cl=sc>=80?'ok':sc>=60?'warn':'crit';
  $('hb').className='hbadge '+cl;$('ld').className='ldot '+cl;
  $('ht').textContent=(sc>=80?'Healthy':sc>=60?'Degraded':'Critical')+' · '+sc+'/100';
  $('cname').textContent=S.snap?.ContextName||'cluster';
  $('cdot').style.background=sc>=80?'var(--grn)':sc>=60?'var(--amb)':'var(--red)';
  const faults=(S.diag?.active_faults||[]).filter(f=>f.score>0);
  const diffs=S.diffs||[];const warns=(S.events||[]).filter(e=>e.Type==='Warning');
  const risks=(S.predict||[]).filter(f=>f.score>0&&f.severity!=='INFO');
  function nc(id,n,cl){const e=$(id);if(n>0){e.textContent=n;e.className='nc '+cl;e.style.display='inline'}else e.style.display='none'}
  nc('nc-diff',diffs.length,'a');nc('nc-warn',warns.length,'r');
  nc('nc-risk',risks.length,risks.some(f=>f.severity==='CRITICAL')?'r':'a');
  renderMain();renderDrawer();
}
function renderMain(){
  const faults=(S.diag?.active_faults||[]).filter(f=>f.score>0);
  const critF=faults.filter(f=>f.severity==='CRITICAL').length,warnF=faults.filter(f=>f.severity==='WARNING').length;
  const diffs=S.diffs||[];
  const risks=(S.predict||[]).filter(f=>f.score>0&&f.severity!=='INFO');
  const critR=risks.filter(f=>f.severity==='CRITICAL').length;
  const nodes=S.snap?.Nodes||[];const badN=nodes.filter(n=>n.Status!=='Ready').length;
  let h='<div class="stats">';
  h+=st('Active faults',critF+warnF,critF>0?'r':warnF>0?'a':'g',critF+' crit · '+warnF+' warn','overview');
  h+=st('Changes (1h)',diffs.length,diffs.length>0?'a':'g','deployments · configs · services','changes');
  h+=st('Predicted risks',risks.length,critR>0?'r':risks.length>0?'a':'g',critR+' critical · '+(risks.length-critR)+' warning','risks');
  h+=st('Nodes',nodes.length,badN>0?'r':'g',badN+' not ready · '+(nodes.length-badN)+' ready','workloads');
  h+='</div>';
  if(V==='overview')h+=ovView(faults);
  else if(V==='workloads')h+=wlView();
  else if(V==='changes')h+=chView();
  else if(V==='events')h+=evView();
  else if(V==='risks')h+=rkView();
  else if(V==='inventory')h+=invView();
  $('main').innerHTML=h;
}
function st(lbl,val,col,sub,v){return '<div class="stat '+col+(V===v?' on':'')+'" onclick="setV(\''+v+'\',null)"><div class="slbl">'+lbl+'</div><div class="sval '+col+'">'+val+'</div><div class="ssub">'+sub+'</div></div>'}
function ovView(faults){
  const rc=S.diag?.root_cause||{};const noF=!faults.length;
  const sev=noF?'ok':rc.confidence>=70?'crit':'warn';
  let h='<div class="rccard '+sev+'">';
  h+='<div class="rch"><div class="rcico '+(noF?'ok':rc.confidence>=70?'crit':'warn')+'">'+(noF?'✓':'⚡')+'</div>';
  h+='<div><div class="rctitle">Root cause analysis</div><div class="rcsub">correlates faults with recent changes automatically</div></div>';
  if(rc.confidence&&!noF){const cc=rc.confidence>=70?'h':rc.confidence>=40?'m':'l';h+='<div class="cpill2 '+cc+'">'+rc.confidence+'% confidence</div>'}
  h+='</div><div class="rcb">';
  if(noF){h+='<div class="rcsentence" style="color:var(--grn)">✓ No active pod faults. Cluster is healthy.</div>'}
  else{
    h+='<div class="rcsentence">'+esc(rc.conclusion||'Analyzing...')+'</div>';
    if(rc.changed_by||rc.changed_at||rc.evidence){
      h+='<div class="rcfacts">';
      if(rc.changed_by)h+='<div class="rcfact"><div class="rcfl">Changed by</div><div class="rcfv b">'+esc(rc.changed_by)+'</div></div>';
      if(rc.changed_at)h+='<div class="rcfact"><div class="rcfl">Changed at</div><div class="rcfv">'+esc(rc.changed_at)+'</div></div>';
      if(rc.evidence)h+='<div class="rcfact" style="grid-column:1/-1"><div class="rcfl">Evidence</div><div class="rcfv">'+esc(rc.evidence)+'</div></div>';
      h+='</div>';
    }
    if(rc.remedy)h+='<div class="cmdbox"><span class="cmdtxt">'+esc(rc.remedy)+'</span><button class="copybtn" onclick="cp(\''+esc(rc.remedy).replace(/'/g,"\\'")+'\')" >Copy</button></div>';
    if(rc.confidence){const bc=rc.confidence>=70?'var(--grn)':rc.confidence>=40?'var(--amb)':'var(--red)';h+='<div class="cbar"><div class="ctrack"><div class="cfill" style="width:'+rc.confidence+'%;background:'+bc+'"></div></div><span class="clbl">'+rc.confidence+'% confidence</span></div>'}
  }
  h+='</div></div>';
  h+=sec('sf','Active faults',faults.length?bdg(faults.length+' active','r'):bdg('All clear','g'),buildFaults(faults),true);
  h+=sec('sns','Namespace summary',bdg((S.snap?.Namespaces||[]).length+' namespaces','n'),buildNs(S.snap?.Namespaces||[]),true);
  return h;
}
function wlView(){
  const nodes=S.snap?.Nodes||[],nss=S.snap?.Namespaces||[],pvcs=S.snap?.PVCs||[],top=S.snap?.TopConsumers||[];
  let h=sec('snd','Nodes',bdg(nodes.length,'n'),buildNodes(nodes),true);
  h+=sec('sns2','Namespaces',bdg(nss.length,'n'),buildNs(nss),true);
  if(top.length)h+=sec('stp','Top consumers',bdg('by memory','b'),buildTop(top),false);
  if(pvcs.length)h+=sec('spv','PVCs',pvcs.filter(p=>p.Status!=='Bound').length>0?bdg(pvcs.filter(p=>p.Status!=='Bound').length+' unbound','r'):bdg('all bound','g'),buildPvc(pvcs),false);
  return h;
}
function chView(){
  const diffs=S.diffs||[],audit=S.diag?.audit_entries||[];
  return '<div class="ditabs"><div class="ditab on" onclick="ditab(this,\'pd\')">What changed ('+diffs.length+')</div><div class="ditab" onclick="ditab(this,\'pa\')">Who changed it ('+audit.length+')</div></div>'+
    '<div id="pd" class="dipane on">'+buildDiff(diffs)+'</div><div id="pa" class="dipane">'+buildAudit(audit)+'</div>';
}
function evView(){
  const all=S.events||[],warns=all.filter(e=>e.Type==='Warning'),norm=all.filter(e=>e.Type!=='Warning');
  return '<div class="ditabs"><div class="ditab on" onclick="ditab(this,\'pea\')">All ('+all.length+')</div>'+
    '<div class="ditab" onclick="ditab(this,\'pew\')" style="color:var(--amb)">Warnings ('+warns.length+')</div>'+
    '<div class="ditab" onclick="ditab(this,\'pen\')">Normal ('+norm.length+')</div></div>'+
    '<div id="pea" class="dipane on">'+buildEvents(all)+'</div>'+
    '<div id="pew" class="dipane">'+buildEvents(warns)+'</div>'+
    '<div id="pen" class="dipane">'+buildEvents(norm)+'</div>';
}
function rkView(){
  const risks=(S.predict||[]).filter(f=>f.score>0);
  if(!risks.length)return '<div class="empty"><div class="ei">✓</div>No predictive risks detected.</div>';
  const crit=risks.filter(f=>f.severity==='CRITICAL'),warn=risks.filter(f=>f.severity==='WARNING'),info=risks.filter(f=>f.severity==='INFO');
  let h='';
  if(crit.length)h+=sec('src','Critical risks',bdg(crit.length,'r'),buildFaults(crit),true);
  if(warn.length)h+=sec('srw','Warnings',bdg(warn.length,'a'),buildFaults(warn),true);
  if(info.length)h+=sec('sri','Observations',bdg(info.length,'n'),buildFaults(info),false);
  return h;
}
function buildFaults(faults){
  if(!faults.length)return '<div class="empty"><div class="ei">✓</div>All clear</div>';
  const uid='ft'+rnd();
  let h='<div class="fbar"><input placeholder="Filter faults..." oninput="filt(this,\''+uid+'\')" /></div>';
  h+='<div class="tw"><table><colgroup><col style="width:90px"><col><col style="width:130px"><col style="width:110px"><col style="width:55px"></colgroup>';
  h+='<thead><tr><th>Severity</th><th>Title</th><th>Object</th><th>Namespace</th><th>Score</th></tr></thead><tbody id="'+uid+'">';
  faults.forEach((f,i)=>{
    const s=sv(f.severity),rid='fr'+i+uid;
    h+='<tr class="tr" onclick="togR(\''+rid+'\',this)"><td>'+bdg(f.severity,s)+'</td><td style="font-weight:500;white-space:normal;word-break:break-word">'+esc(f.title)+'</td><td style="font-family:monospace;font-size:11px;color:var(--t2)">'+esc(tr(f.object,22))+'</td><td style="color:var(--t2)">'+esc(f.namespace||'—')+'</td><td>'+bdg(f.score,f.score>=80?'r':f.score>=50?'a':'n')+'</td></tr>';
    h+='<tr id="'+rid+'" class="exr" style="display:none"><td colspan="5"><div class="exin">';
    if(f.detail)h+='<div style="color:var(--t2);font-size:12px;margin-bottom:8px;white-space:normal">'+esc(f.detail)+'</div>';
    if(f.remedy)h+='<div class="cmdbox"><span class="cmdtxt">'+esc(f.remedy)+'</span><button class="copybtn" onclick="cp(\''+esc(f.remedy).replace(/'/g,"\\'")+'\');event.stopPropagation()">Copy</button></div>';
    h+='</div></td></tr>';
  });
  return h+'</tbody></table></div>';
}
function buildNodes(nodes){
  if(!nodes.length)return '<div class="empty">No node data</div>';
  let h='<div class="tw"><table><colgroup><col><col style="width:80px"><col style="width:90px"><col style="width:90px"><col style="width:90px"><col style="width:90px"></colgroup>';
  h+='<thead><tr><th>Node</th><th>Status</th><th>CPU req</th><th>CPU cap</th><th>Mem req</th><th>Mem cap</th></tr></thead><tbody>';
  nodes.forEach((n,i)=>{
    const ok=n.Status==='Ready',rid='nr'+i;
    h+='<tr class="tr" onclick="togR(\''+rid+'\',this)"><td style="font-family:monospace;font-size:11px">'+esc(tr(n.Name,36))+'</td><td>'+bdg(n.Status,ok?'g':'r')+'</td><td style="color:var(--t2)">'+esc(n.CPURequested||'—')+'</td><td style="color:var(--t2)">'+esc(n.CPUCapacity||'—')+'</td><td style="color:var(--t2)">'+esc(n.MemRequested||'—')+'</td><td style="color:var(--t2)">'+esc(n.MemCapacity||'—')+'</td></tr>';
    h+='<tr id="'+rid+'" class="exr" style="display:none"><td colspan="6"><div class="exin"><div class="exgrid"><div class="exitem"><div class="exlbl">Full name</div><div class="exval mono">'+esc(n.Name)+'</div></div><div class="exitem"><div class="exlbl">Status</div><div class="exval">'+esc(n.Status)+'</div></div></div></div></td></tr>';
  });
  return h+'</tbody></table></div>';
}
function buildNs(nss){
  if(!nss.length)return '<div class="empty">No namespaces</div>';
  const uid='ns'+rnd();
  let h='<div class="fbar"><input placeholder="Filter namespaces..." oninput="filt(this,\''+uid+'\')" /></div>';
  h+='<div class="tw"><table><colgroup><col><col style="width:80px"><col style="width:60px"><col style="width:60px"><col style="width:65px"><col style="width:80px"></colgroup>';
  h+='<thead><tr><th>Namespace</th><th>Deployments</th><th>Pods</th><th>Running</th><th>Failing</th><th>StatefulSets</th></tr></thead><tbody id="'+uid+'">';
  nss.forEach(n=>{const hf=n.FailingPods>0;h+='<tr class="tr"><td style="font-weight:500">'+esc(n.Name)+'</td><td style="color:var(--t2)">'+n.Deployments+'</td><td style="color:var(--t2)">'+n.TotalPods+'</td><td style="color:var(--grn)">'+n.RunningPods+'</td><td>'+bdg(n.FailingPods,hf?'r':'g')+'</td><td style="color:var(--t2)">'+n.StatefulSets+'</td></tr>'});
  return h+'</tbody></table></div>';
}
function buildTop(top){
  let h='<div class="tw"><table><colgroup><col><col style="width:110px"><col style="width:80px"><col style="width:80px"></colgroup>';
  h+='<thead><tr><th>Pod</th><th>Namespace</th><th>CPU req</th><th>Mem req</th></tr></thead><tbody>';
  top.slice(0,15).forEach(c=>{h+='<tr class="tr"><td style="font-size:11px">'+esc(tr(c.Name,36))+'</td><td style="color:var(--t2)">'+esc(tr(c.Namespace,16))+'</td><td style="color:var(--amb)">'+esc(c.CPURequest||'—')+'</td><td style="color:var(--amb)">'+esc(c.MemRequest||'—')+'</td></tr>'});
  return h+'</tbody></table></div>';
}
function buildPvc(pvcs){
  let h='<div class="tw"><table><colgroup><col><col style="width:110px"><col style="width:80px"><col style="width:80px"></colgroup>';
  h+='<thead><tr><th>Name</th><th>Namespace</th><th>Capacity</th><th>Status</th></tr></thead><tbody>';
  pvcs.forEach(p=>{const ok=p.Status==='Bound';h+='<tr class="tr"><td style="font-family:monospace;font-size:11px">'+esc(tr(p.Name,36))+'</td><td style="color:var(--t2)">'+esc(p.Namespace)+'</td><td style="color:var(--t2)">'+esc(p.Capacity||'—')+'</td><td>'+bdg(p.Status,ok?'g':'r')+'</td></tr>'});
  return h+'</tbody></table></div>';
}
function buildDiff(diffs){
  if(!diffs.length)return '<div class="empty"><div class="ei">✓</div>No changes in the last hour</div>';
  const uid='df'+rnd();
  let h='<div class="fbar"><input placeholder="Filter by name, field, author..." oninput="filt(this,\''+uid+'\')" /><select onchange="filtS(this,\''+uid+'\',\'dc\')"><option value="">All changes</option><option value="dc">Correlated faults only</option></select></div>';
  h+='<div class="tw"><table><colgroup><col style="width:80px"><col style="width:130px"><col style="width:130px"><col><col style="width:110px"><col style="width:75px"></colgroup>';
  h+='<thead><tr><th>Kind</th><th>Name</th><th>Field</th><th>Change</th><th>By</th><th>Status</th></tr></thead><tbody id="'+uid+'">';
  diffs.forEach((d,i)=>{
    const ic=!!d.CorrelatedFault,rid='dfr'+i+uid;
    h+='<tr class="tr'+(ic?' dc':'')+'" onclick="togR(\''+rid+'\',this)">';
    h+='<td>'+bdg(d.Kind,'n')+'</td><td><div style="font-weight:500">'+esc(tr(d.Name,18))+'</div><div style="font-size:10px;color:var(--t3)">'+esc(d.Namespace)+'</div></td>';
    h+='<td style="font-size:11px;color:var(--t2)">'+esc(tr(d.Field,20))+'</td>';
    h+='<td>'+(d.OldValue&&!d.OldValue.includes('use --save')?'<div class="diffval"><span class="dold">'+esc(tr(d.OldValue,22))+'</span><span class="darr">→</span><span class="dnew">'+esc(tr(d.NewValue,22))+'</span></div>':'<span style="font-size:11px;color:var(--grn);font-family:monospace">'+esc(tr(d.NewValue,30))+'</span>')+'</td>';
    h+='<td style="color:var(--blu);font-size:11px">'+esc(tr(d.ChangedBy,16))+'</td><td>'+bdg(ic?'⚠ fault':'ok',ic?'r':'g')+'</td></tr>';
    h+='<tr id="'+rid+'" class="exr" style="display:none"><td colspan="6"><div class="exin"><div class="exgrid">';
    h+='<div class="exitem"><div class="exlbl">Changed by</div><div class="exval" style="color:var(--blu)">'+esc(d.ChangedBy||'—')+'</div></div>';
    h+='<div class="exitem"><div class="exlbl">Timestamp</div><div class="exval">'+ft(d.Timestamp)+'</div></div>';
    if(d.OldValue&&!d.OldValue.includes('use --save'))h+='<div class="exitem"><div class="exlbl">Before</div><div class="exval mono">'+esc(tr(d.OldValue,60))+'</div></div>';
    h+='<div class="exitem"><div class="exlbl">After</div><div class="exval mono">'+esc(tr(d.NewValue,60))+'</div></div>';
    if(d.Risk)h+='<div class="exitem" style="grid-column:1/-1"><div class="exlbl">Risk</div><div class="exval" style="color:var(--amb)">'+esc(d.Risk)+'</div></div>';
    h+='</div>';
    if(ic){h+='<div style="color:var(--red);font-size:12px;margin-top:6px">⚠ Correlated with: '+esc(d.CorrelatedFault)+'</div>';if(d.Mitigation)h+='<div class="cmdbox" style="margin-top:6px"><span class="cmdtxt">'+esc(d.Mitigation)+'</span><button class="copybtn" onclick="cp(\''+esc(d.Mitigation).replace(/'/g,"\\'")+'\');event.stopPropagation()">Copy</button></div>'}
    h+='</div></td></tr>';
  });
  return h+'</tbody></table></div>';
}
function buildAudit(audit){
  if(!audit||!audit.length)return '<div class="empty">No audit entries in the last hour</div>';
  const uid='au'+rnd();
  let h='<div class="fbar"><input placeholder="Filter by name, user, kind..." oninput="filt(this,\''+uid+'\')" /></div>';
  h+='<div class="tw"><table><colgroup><col style="width:80px"><col style="width:80px"><col><col style="width:110px"><col style="width:70px"><col style="width:80px"></colgroup>';
  h+='<thead><tr><th>Time</th><th>Kind</th><th>Name</th><th>Changed by</th><th>Action</th><th>Correlated</th></tr></thead><tbody id="'+uid+'">';
  audit.forEach((a,i)=>{
    const ic=!!a.CorrelatedFault,rid='aur'+i+uid,ac=a.Action==='DELETE'?'r':a.Action==='CREATE'?'g':'a';
    h+='<tr class="tr" onclick="togR(\''+rid+'\',this)"><td style="font-family:monospace;font-size:11px;color:var(--t3)">'+ft(a.Timestamp)+'</td><td>'+bdg(a.Kind,'n')+'</td><td><div style="font-weight:500">'+esc(tr(a.Name,22))+'</div><div style="font-size:10px;color:var(--t3)">'+esc(a.Namespace)+'</div></td><td style="color:var(--blu);font-size:11px">'+esc(tr(a.FieldManager,18))+'</td><td>'+bdg(a.Action,ac)+'</td><td>'+bdg(ic?'⚠ yes':'—',ic?'r':'n')+'</td></tr>';
    h+='<tr id="'+rid+'" class="exr" style="display:none"><td colspan="6"><div class="exin">';
    if(a.Detail)h+='<div style="color:var(--t2);font-size:12px;margin-bottom:8px;white-space:normal">'+esc(a.Detail)+'</div>';
    if(ic){h+='<div style="color:var(--red);font-size:12px;margin-bottom:6px">⚠ Correlated: '+esc(a.CorrelatedFault)+'</div>';if(a.Mitigation)h+='<div class="cmdbox"><span class="cmdtxt">'+esc(a.Mitigation)+'</span><button class="copybtn" onclick="cp(\''+esc(a.Mitigation).replace(/'/g,"\\'")+'\');event.stopPropagation()">Copy</button></div>'}
    h+='</div></td></tr>';
  });
  return h+'</tbody></table></div>';
}
function buildEvents(evts){
  if(!evts||!evts.length)return '<div class="empty"><div class="ei">✓</div>No events found</div>';
  const uid='ev'+rnd();
  let h='<div class="fbar"><input placeholder="Filter events..." oninput="filt(this,\''+uid+'\')" /></div>';
  h+='<div class="tw"><table><colgroup><col style="width:78px"><col style="width:70px"><col style="width:110px"><col style="width:140px"><col><col style="width:50px"></colgroup>';
  h+='<thead><tr><th>Time</th><th>Type</th><th>Reason</th><th>Object</th><th>Message</th><th>×</th></tr></thead><tbody id="'+uid+'">';
  evts.forEach((ev,i)=>{
    const iw=ev.Type==='Warning',rid='evr'+i+uid;
    h+='<tr class="tr" onclick="togR(\''+rid+'\',this)"><td style="font-family:monospace;font-size:11px;color:var(--t3)">'+ft(ev.LastSeen)+'</td><td>'+bdg(ev.Type,iw?'a':'g')+'</td><td style="font-weight:500;'+(iw?'color:var(--amb)':'')+'">'+esc(ev.Reason)+'</td><td style="font-family:monospace;font-size:11px;color:var(--t2)">'+esc(tr(ev.ObjectName,22))+'</td><td style="color:var(--t2);white-space:normal;word-break:break-word;font-size:12px">'+esc(tr(ev.Message,70))+'</td><td style="text-align:center">'+(ev.Count>1?bdg('×'+ev.Count,'n'):'')+'</td></tr>';
    h+='<tr id="'+rid+'" class="exr" style="display:none"><td colspan="6"><div class="exin"><div class="exgrid"><div class="exitem"><div class="exlbl">Object kind</div><div class="exval">'+esc(ev.ObjectKind||'—')+'</div></div><div class="exitem"><div class="exlbl">Namespace</div><div class="exval">'+esc(ev.Namespace||'—')+'</div></div></div><div style="font-size:12px;color:var(--t2);margin-top:6px;white-space:normal">'+esc(ev.Message)+'</div></div></td></tr>';
  });
  return h+'</tbody></table></div>';
}
async function loadInventory(ns){
  S.invLoading=true;S.invErr=null;renderMain();
  try{
    const rep=await api('/api/inventory?ns='+encodeURIComponent(ns||'*'));
    S.inv=rep;S.invNs=ns;
  }catch(e){S.invErr=e.message}
  S.invLoading=false;renderMain();
  const n=(S.inv?.suspicious?.length||0)+(S.inv?.stuck?.length||0);
  const e=$('nc-inv');if(e){if(n>0){e.textContent=n;e.className='nc a';e.style.display='inline'}else e.style.display='none'}
}
function invScanBar(){
  return '<div class="fbar"><input id="invnsin" placeholder="namespace (blank = all namespaces)" value="'+esc(S.invNs||'')+'" onkeydown="if(event.key===\'Enter\')invScan()" /><button class="btn" onclick="invScan()">Scan</button></div>';
}
function invScan(){const v=$('invnsin').value.trim();loadInventory(v)}
function invStat(lbl,val,col,sub){return '<div class="stat '+col+'"><div class="slbl">'+esc(lbl)+'</div><div class="sval '+col+'">'+val+'</div><div class="ssub">'+esc(sub)+'</div></div>'}
function invView(){
  if(S.invLoading)return invScanBar()+'<div class="swrap"><div class="spin"></div>Scanning namespace'+(S.invNs?': '+esc(S.invNs):'s (all — can take 30s+)')+'...</div>';
  if(S.invErr)return invScanBar()+'<div style="padding:20px;color:var(--red)">Error: '+esc(S.invErr)+'</div>';
  if(!S.inv){loadInventory(S.invNs||'');return invScanBar()+'<div class="swrap"><div class="spin"></div>Starting scan...</div>';}
  const r=S.inv;
  const susp=r.suspicious||[],stuck=r.stuck||[];
  const owned=r.totalResources-susp.length-stuck.length;
  let h=invScanBar();
  h+='<div class="stats">';
  h+=invStat('Total resources',r.totalResources,'b',r.scannedTypes+' types scanned · '+r.skippedTypes+' skipped (noise profile)');
  h+=invStat('Owned / GitOps',owned,'g','has ownerReference or known GitOps signal');
  h+=invStat('Suspicious',susp.length,susp.length>0?'a':'g','no owner, no GitOps signal — investigate');
  h+=invStat('Stuck',stuck.length,stuck.length>0?'r':'g','finalizer-blocked > 2m');
  h+='</div>';
  h+='<div style="font-size:11px;color:var(--t3);margin-bottom:12px">ns='+esc(r.namespace||'all')+' · scanned in '+((r.durationMs||0)/1000).toFixed(1)+'s</div>';
  h+=sec('sig','Resource groups',bdg((r.groups||[]).length+' groups','n'),buildInvGroups(r.groups||[]),true);
  h+=sec('sis','Suspicious resources',bdg(susp.length,susp.length>0?'a':'g'),buildInvEntries(susp,'a'),susp.length>0);
  h+=sec('ist','Stuck resources',bdg(stuck.length,stuck.length>0?'r':'g'),buildInvEntries(stuck,'r'),stuck.length>0);
  return h;
}
function buildInvGroups(groups){
  if(!groups.length)return '<div class="empty">No resources found</div>';
  let h='<div class="tw"><table><colgroup><col style="width:170px"><col><col style="width:70px"><col style="width:100px"></colgroup>';
  h+='<thead><tr><th>API group</th><th>Kind</th><th>Count</th><th>Suspicious</th></tr></thead><tbody>';
  groups.forEach(g=>{
    const label=g.group||'core';
    (g.resources||[]).forEach((rc,i)=>{
      h+='<tr class="tr"><td style="color:var(--t2)">'+(i===0?esc(label):'')+'</td><td style="font-weight:500">'+esc(rc.kind)+'</td><td>'+rc.count+'</td><td>'+(rc.suspicious>0?bdg(rc.suspicious,'a'):'—')+'</td></tr>';
    });
  });
  return h+'</tbody></table></div>';
}
function buildInvEntries(entries,col){
  if(!entries.length)return '<div class="empty"><div class="ei">✓</div>None found</div>';
  const uid='iv'+rnd();
  let h='<div class="fbar"><input placeholder="Filter..." oninput="filt(this,\''+uid+'\')" /></div>';
  h+='<div class="tw"><table><colgroup><col><col style="width:130px"><col style="width:100px"></colgroup>';
  h+='<thead><tr><th>Resource</th><th>Namespace</th><th>Status</th></tr></thead><tbody id="'+uid+'">';
  entries.forEach((e,i)=>{
    const o=e.object||{},c=e.classification||{},rid='ivr'+i+uid;
    h+='<tr class="tr" onclick="togR(\''+rid+'\',this)"><td style="font-weight:500">'+esc((o.kind||'').toLowerCase())+'/'+esc(tr(o.name,32))+'</td><td style="color:var(--t2)">'+esc(o.namespace||'—')+'</td><td>'+bdg(c.status,col)+'</td></tr>';
    h+='<tr id="'+rid+'" class="exr" style="display:none"><td colspan="3"><div class="exin">';
    (c.reasons||[]).forEach(rs=>{h+='<div style="font-size:12px;color:var(--t2);margin-bottom:4px">• '+esc(rs)+'</div>'});
    h+='</div></td></tr>';
  });
  return h+'</tbody></table></div>';
}
function renderDrawer(){
  const nodes=S.snap?.Nodes||[];
  const warns=(S.events||[]).filter(e=>e.Type==='Warning').slice(0,20);
  const diffs=(S.diffs||[]).slice(0,15);
  let h='';
  h+=drBlock('drn','Nodes',nodes.map(n=>{const ok=n.Status==='Ready';return '<div class="nrow"><div class="nnrow"><span class="nname">'+esc(tr(n.Name,26))+'</span>'+bdg(n.Status,ok?'g':'r')+'</div><div class="mbar"><span style="font-size:10px;color:var(--t3);width:28px">CPU</span><div class="mbt"><div class="mbf g" style="width:35%"></div></div><span class="mbp">'+esc(n.CPURequested||'—')+'</span></div><div class="mbar" style="margin-top:4px"><span style="font-size:10px;color:var(--t3);width:28px">Mem</span><div class="mbt"><div class="mbf g" style="width:50%"></div></div><span class="mbp">'+esc(n.MemRequested||'—')+'</span></div></div>';}).join(''),true);
  h+=drBlock('drw','Warning events',warns.map(ev=>'<div class="evrow" onclick="setV(\'events\',null)"><div class="evtime">'+ft(ev.LastSeen)+'</div><div><div class="evreason a">'+esc(tr(ev.Reason,18))+'</div><div class="evobj">'+esc(tr(ev.ObjectName,30))+'</div><div class="evmsg">'+esc(tr(ev.Message,100))+'</div></div></div>').join('')||'<div class="empty" style="padding:16px"><div class="ei">✓</div>No warnings</div>',true);
  h+=drBlock('drc','Change log',diffs.map(d=>{const ic=!!d.CorrelatedFault;return '<div class="chrow" onclick="setV(\'changes\',null)"><div class="chname">'+bdg(d.Kind,'n')+' '+esc(tr(d.Name,22))+'</div><div class="chmeta"><span class="chby">'+esc(tr(d.ChangedBy,20))+'</span><span class="chtime">'+ft(d.Timestamp)+'</span></div><div class="chfield">'+esc(tr(d.Field,36))+'</div>'+(ic?'<div class="chcorr">⚠ '+esc(tr(d.CorrelatedFault,36))+'</div>':'')+'</div>';}).join('')||'<div class="empty" style="padding:16px">No changes in last hour</div>',true);
  $('dri').innerHTML=h;
}
function drBlock(id,title,body,open){return '<div class="drblock"><div class="drhead" onclick="togDB(\''+id+'\',this)"><span class="drtitle">'+title+'</span><span style="font-size:10px;color:var(--t3)">'+(open?'▲':'▼')+'</span></div><div class="drbody'+(open?'':' hd')+'" id="'+id+'">'+body+'</div></div>'}
function sec(id,title,bge,body,open){return '<div class="sec"><div class="seh'+(open?' op':'')+'" onclick="togS(\''+id+'\',this)"><span class="setitle">'+title+'</span><div class="semeta">'+bge+'<span class="chev">▶</span></div></div><div class="seb'+(open?'':' hd')+'" id="'+id+'">'+body+'</div></div>'}
function bdg(txt,cls){return '<span class="badge '+cls+'">'+esc(String(txt))+'</span>'}
function sv(s){return s==='CRITICAL'?'r':s==='WARNING'?'a':'g'}
function esc(s){return String(s||'').replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;')}
function tr(s,n){s=String(s||'');return s.length>n?s.slice(0,n-1)+'…':s}
function ft(ts){if(!ts)return '—';return new Date(ts).toLocaleTimeString([],{hour:'2-digit',minute:'2-digit',second:'2-digit'})}
function rnd(){return Math.random().toString(36).slice(2,8)}
function togS(id,h){const b=$(id);const o=!b.classList.contains('hd');b.classList.toggle('hd',o);h.classList.toggle('op',!o)}
function togR(id,row){const r=$(id);if(!r)return;const o=r.style.display!=='none';r.style.display=o?'none':'table-row';row.classList.toggle('op',!o)}
function togDB(id,h){const b=$(id);b.classList.toggle('hd');h.querySelector('span:last-child').textContent=b.classList.contains('hd')?'▼':'▲'}
function togDrawer(){$('drawer').classList.toggle('col')}
function filt(inp,tbid){const q=inp.value.toLowerCase();const tb=$(tbid);if(!tb)return;tb.querySelectorAll('tr:not(.exr)').forEach(r=>{r.style.display=r.textContent.toLowerCase().includes(q)?'':'none';const nx=r.nextElementSibling;if(nx&&nx.classList.contains('exr'))nx.style.display='none'})}
function filtS(sel,tbid,cls){const v=sel.value;const tb=$(tbid);if(!tb)return;tb.querySelectorAll('tr:not(.exr)').forEach(r=>{r.style.display=(!v||r.classList.contains(cls))?'':'none'})}
function ditab(el,pid){const wrap=el.closest('[id^=main],[id^=sec],[class*=sec]')||$('main');wrap.querySelectorAll('.ditab').forEach(t=>t.classList.remove('on'));wrap.querySelectorAll('.dipane').forEach(p=>p.classList.remove('on'));el.classList.add('on');const p=$(pid);if(p)p.classList.add('on')}
function setV(v,el){V=v;document.querySelectorAll('.nb').forEach(b=>b.classList.remove('on'));if(el)el.classList.add('on');else{const vs=['overview','workloads','changes','events','risks','inventory'];document.querySelectorAll('.nb').forEach((b,i)=>{if(vs[i]===v)b.classList.add('on')})}renderMain()}
function cp(txt){navigator.clipboard.writeText(txt).then(()=>toast('Copied!')).catch(()=>toast('Failed'))}
function toast(msg){const t=$('toast');t.textContent=msg;t.classList.add('show');setTimeout(()=>t.classList.remove('show'),2000)}
loadAll(false);
</script>
</body>
</html>`

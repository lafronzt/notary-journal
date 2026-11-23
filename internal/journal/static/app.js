'use strict';
const $ = s => document.querySelector(s);
const form = $('#entry-form');
let entries = [], defaults = {notary_name:'',commission_id:''}, selected = null, currentView = 'journal', toastTimer;
const text = (tag, value, cls) => { const el=document.createElement(tag); el.textContent=value; if(cls)el.className=cls; return el; };
const actDate = value => new Date(/^\d{4}-\d{2}-\d{2}$/.test(value)?value+'T12:00:00':value);
const localDate = value => { if(/^\d{4}-\d{2}-\d{2}$/.test(value))return actDate(value).toLocaleDateString()+' · time not recorded'; const d=new Date(value); return Number.isNaN(d.getTime()) ? value : d.toLocaleString(undefined,{dateStyle:'medium',timeStyle:'short'}); };
function toast(message) { $('#toast').textContent=message; $('#toast').hidden=false; clearTimeout(toastTimer); toastTimer=setTimeout(()=>$('#toast').hidden=true,8000); }
async function api(path, data) {
  const res=await fetch(path,{credentials:'same-origin', ...(data===undefined?{}:{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(data)})});
  const result=await res.json();
  if(!res.ok){if(res.status===401 && path!='/api/login')lock();throw new Error(result.error||'Request failed');}
  return result;
}
function lock(){entries=[];selected=null;$('#entries').replaceChildren();$('#detail-content').replaceChildren();$('#detail').close();form.reset();$('#application').hidden=true;$('#unlock').hidden=false;$('#login-form').reset();}
async function start(){ $('#unlock').hidden=true;$('#application').hidden=false;await refresh(); }
async function refresh(){const result=await Promise.all([api('/api/entries'),api('/api/status'),api('/api/config')]);entries=result[0];defaults=result[2];renderEntries();renderStatus(result[1]);}
function renderStatus(s){
  $('#entry-count').textContent=entries.length;$('#pending-count').textContent=s.pending;$('#archive-pending').textContent=s.pending;
  $('#cloud-pill').textContent=!s.gcs_configured?'GCS not configured':s.archive_error?'Archive needs attention':s.pending?'GCS · uploads pending':s.last_archive?'GCS · up to date':'GCS · no records yet';
  $('#cloud-pill').classList.toggle('pending',!s.gcs_configured||s.pending>0||!!s.archive_error);
  $('#journal-id').textContent=s.journal_id;$('#last-archive').textContent=s.last_archive?localDate(s.last_archive):'No successful upload yet';
  $('#archive-status').textContent=s.archive_error||s.last_error||(!s.gcs_configured?'Not connected. Set GCS_BUCKET and configure Google Application Default Credentials. Your records are currently stored locally only.':'Automatic sync runs every 60 seconds while the app is running. Pending events remain local until verified in GCS.');
}
function renderEntries(){
  const q=$('#search').value.toLocaleLowerCase().trim(), source=$('#source-filter').value;
  const sign=$('#sort-order').value==='oldest'?1:-1;
  const filtered=entries.filter(e=>(source==='all'||e.data.source===source)&&(JSON.stringify(e.data)+' '+localDate(e.data.occurred_at)).toLocaleLowerCase().includes(q))
    .sort((a,b)=>sign*(actDate(a.data.occurred_at)-actDate(b.data.occurred_at)||a.created_at.localeCompare(b.created_at)));
  $('#entries').replaceChildren();$('#empty-state').hidden=filtered.length>0;
  if(!filtered.length){$('#empty-state h2').textContent=entries.length?'No matching records.':'A new chapter for your records.';$('#empty-state p').textContent=entries.length?'Try another search or entry filter.':'Record a notarial act or bring an existing paper journal into Ledger.';}
  for(const e of filtered){const row=document.createElement('tr');
    const date=document.createElement('td');date.append(text('strong',actDate(e.data.occurred_at).toLocaleDateString(undefined,{month:'short',day:'numeric',year:'numeric'})),text('small',e.data.time_not_recorded?'Time not recorded':actDate(e.data.occurred_at).toLocaleTimeString(undefined,{hour:'2-digit',minute:'2-digit'})));row.append(date);
    const principal=document.createElement('td');principal.append(text('strong',e.data.principals.map(p=>p.name).join(', ')),text('small',`${e.data.act_type} · ${e.data.service_count} service${e.data.service_count===1?'':'s'}`));row.append(principal);
    row.append(text('td',e.data.source==='paper_import'?'Paper import':'New act'));
    const archive=document.createElement('td');archive.append(text('span',e.archived?'Archived':'Pending',`badge${e.archived?'':' pending'}`));row.append(archive);
    const open=document.createElement('td'),button=text('button','Open','secondary');button.addEventListener('click',()=>showDetail(e).catch(err=>toast(err.message)));open.append(button);row.append(open);$('#entries').append(row);
  }
}
function rowInputs(container, fields, values={}){
  const row=document.createElement('div');row.className='person-row';
  for(const [key,label] of fields){const l=text('label',label),i=document.createElement('input');i.dataset.key=key;i.value=values[key]||'';i.required=true;l.append(i);row.append(l);}
  const remove=text('button','Remove','secondary');remove.type='button';remove.addEventListener('click',()=>{if(container.id==='principals'&&container.children.length===1){toast('At least one person is required.');return;}row.remove();});row.append(remove);container.append(row);
}
const addPrincipal=p=>rowInputs($('#principals'),[['name','Full name'],['address','Full address']],p);
const addWitness=w=>rowInputs($('#witnesses'),[['name','Witness name'],['identity_basis','Credential type / personal knowledge']],w);
function rows(container){return [...$(container).children].map(r=>Object.fromEntries([...r.querySelectorAll('input')].map(i=>[i.dataset.key,i.value.trim()])));}
function dateInput(value){const d=new Date(value);return new Date(d.getTime()-d.getTimezoneOffset()*60000).toISOString().slice(0,16);}
function dependencies(){
  const electronic=form.elements.mode.value==='electronic', witness=form.elements.identity_method.value==='witness',credential=form.elements.identity_method.value==='credential',paper=form.elements.source.value==='paper_import',correction=!!form.elements.entry_id.value;
  $('#electronic-section').hidden=!electronic;for(const n of ['communication_technology','certification_authority','verification_provider','recording_reference','recording_retention_attestation'])form.elements[n].required=electronic;
  $('#witness-section').hidden=!witness;for(const i of $('#witnesses').querySelectorAll('input'))i.required=witness;
  if(witness&&!$('#witnesses').children.length)addWitness();
  $('#credential-label').hidden=!credential;form.elements.credential_type.required=credential;
  $('#unknown-time-label').hidden=!paper;
  const unknown=paper&&form.elements.time_not_recorded.checked;
  const timeInput=form.elements.occurred_at;
  if(unknown&&timeInput.type!=='date'){const value=timeInput.value.slice(0,10);timeInput.type='date';timeInput.value=value;}
  if(!unknown&&timeInput.type!=='datetime-local'){const value=timeInput.value;timeInput.type='datetime-local';timeInput.value=value?value+'T12:00':'';}
  $('#paper-reference').hidden=!paper;form.elements.source_reference.required=paper;$('#import-note').hidden=!paper;
  $('#contemporaneous-label').hidden=paper||correction;form.elements.contemporaneous_attestation.required=!paper&&!correction;
  $('#reason-label').hidden=!correction;form.elements.reason.required=correction;
}
function setupForm(source='contemporaneous',entry=null){
  form.reset();form.elements.occurred_at.type='datetime-local';form.elements.entry_id.value=entry?.id||'';form.elements.source.value=source;$('#principals').replaceChildren();$('#witnesses').replaceChildren();
  form.elements.occurred_at.value=dateInput(new Date());
  $('#form-title').textContent=entry?'A correction, with context.':source==='paper_import'?'Give paper a lasting home.':'A place for every detail.';
  $('#entry-eyebrow').textContent=entry?'PRESERVE THE ORIGINAL · ADD A CORRECTION':source==='paper_import'?'TRANSCRIBE YOUR EXISTING JOURNAL':'RECORD A NOTARIAL ACT';
  $('#form-description').textContent=entry?'The original stays unchanged. This adds a dated correction and the reason.':source==='paper_import'?'Transcribe a paper record, then attach its scan to the saved entry.':'Record the act as it happens. Required fields follow 19 NYCRR §182.9.';
  $('#save-button').textContent=entry?'Save correction':'Save journal entry';
  if(entry){for(const [key,value]of Object.entries(entry.data)){const input=form.elements[key];if(!input||key==='occurred_at')continue;if(input.type==='checkbox')input.checked=!!value;else if(typeof value!=='object')input.value=value;}if(entry.data.time_not_recorded){form.elements.occurred_at.type='date';form.elements.occurred_at.value=entry.data.occurred_at;}else form.elements.occurred_at.value=dateInput(entry.data.occurred_at);for(const p of entry.data.principals)addPrincipal(p);for(const w of entry.data.witnesses||[])addWitness(w);}
  else{addPrincipal();form.elements.notary_name.value=defaults.notary_name||'';form.elements.commission_id.value=defaults.commission_id||'';}
  dependencies();
}
function navigate(view,entry=null,keepToast=false){
  if(!keepToast){clearTimeout(toastTimer);$('#toast').hidden=true;}
  if(view==='import'){setupForm('paper_import');currentView='entry';}else if(view==='entry'){setupForm(entry?.data.source||'contemporaneous',entry);currentView='entry';}else currentView=view;
  document.querySelectorAll('.view').forEach(v=>v.hidden=v.id!==`view-${currentView}`);
  document.querySelectorAll('.nav-item').forEach(n=>{const on=n.dataset.view===view;n.classList.toggle('active',on);on?n.setAttribute('aria-current','page'):n.removeAttribute('aria-current');});
  window.scrollTo({top:0,behavior:'smooth'});
}
function collect(){
  const data={};for(const input of form.elements){if(!input.name||['entry_id','reason'].includes(input.name))continue;data[input.name]=input.type==='checkbox'?input.checked:input.value.trim();}
  data.occurred_at=data.source==='paper_import'&&data.time_not_recorded?form.elements.occurred_at.value:new Date(form.elements.occurred_at.value).toISOString();data.service_count=Number(data.service_count);data.principals=rows('#principals');data.witnesses=data.identity_method==='witness'?rows('#witnesses'):[];
  return data;
}
function field(grid,label,value){const block=document.createElement('div');block.append(text('dt',label),text('dd',String(value||'—')));grid.append(block);}
async function showDetail(entry){
  selected=entry;const events=await api(`/api/history/${entry.id}`),d=entry.data,c=$('#detail-content');c.replaceChildren();
  c.append(text('h1',d.principals.map(p=>p.name).join(', ')),text('p',`${d.act_type} · ${localDate(d.occurred_at)}`,'muted'));
  const grid=document.createElement('dl');grid.className='detail-grid';
  for(const p of d.principals)field(grid,'Principal',`${p.name} · ${p.address}`);
  const values=[['Entry identifier',entry.id],['Appearance',d.mode==='electronic'?'Electronic':'In person'],['Services',d.service_count],['Notary',`${d.notary_name} · ${d.commission_id}`],['Identity basis',({credential:'Identification credential',personal_knowledge:'Personal knowledge',witness:'Credible / identifying witness'})[d.identity_method]||d.identity_method],['Credential type',d.credential_type],['Verification procedures',d.verification],['First recorded / digitized',localDate(entry.created_at)],['Source',d.source==='paper_import'?`Paper import · ${d.source_reference}`:'Contemporaneous entry (notary attestation)'],['Retain at least until',localDate(d.retain_until)],['Document description',d.document_description],['Notes',d.notes],['Archive',entry.archived?'All entry events archived':'Some events await GCS upload']];
  if(d.mode==='electronic')for(const key of ['communication_technology','certification_authority','verification_provider','recording_reference'])values.push([key.replaceAll('_',' '),d[key]]);
  for(const [label,value]of values)field(grid,label,value);for(const w of d.witnesses||[])field(grid,'Witness',`${w.name} · ${w.identity_basis}`);c.append(grid,text('h2','Preserved record history'));
  for(const e of events){const card=document.createElement('details');card.className='event-card';card.append(text('summary',`${e.kind==='entry'?'Original record':e.kind==='correction'?'Correction':'Attachment'} · ${localDate(e.recorded_at)}`));
    if(e.kind==='attachment'){const a=text('a',`Download ${e.payload.name} (${Math.ceil(e.payload.size/1024)} KB)`);a.href=`/api/attachment/${e.seq}`;card.append(a,text('p',`SHA-256: ${e.payload.sha256}`,'mono'));}
    card.append(text('pre',JSON.stringify(e,null,2)));c.append(card);
  }
  $('#attachment-form').reset();if(!$('#detail').open)$('#detail').showModal();
}
document.querySelectorAll('[data-view]').forEach(b=>b.addEventListener('click',()=>navigate(b.dataset.view)));
$('#search').addEventListener('input',renderEntries);$('#sort-order').addEventListener('change',renderEntries);$('#source-filter').addEventListener('change',renderEntries);
$('#add-principal').addEventListener('click',()=>addPrincipal());$('#add-witness').addEventListener('click',()=>addWitness());
form.elements.time_not_recorded.addEventListener('change',dependencies);form.elements.mode.addEventListener('change',dependencies);form.elements.identity_method.addEventListener('change',dependencies);
$('#timezone-label').textContent=`(${Intl.DateTimeFormat().resolvedOptions().timeZone})`;
$('#login-form').addEventListener('submit',async e=>{e.preventDefault();const b=e.target.querySelector('button');b.disabled=true;$('#login-error').textContent='';try{await api('/api/login',{password:e.target.elements.password.value});e.target.reset();await start();}catch(err){$('#login-error').textContent=err.message;}finally{b.disabled=false;}});
$('#logout').addEventListener('click',async()=>{try{await api('/api/logout',{});lock();}catch(err){toast(err.message);}});
form.addEventListener('submit',async e=>{e.preventDefault();const button=$('#save-button');button.disabled=true;try{const id=form.elements.entry_id.value,data=collect();await api(id?'/api/corrections':'/api/entries',id?{entry_id:id,reason:form.elements.reason.value,entry:data}:data);form.reset();await refresh();navigate('journal');toast(id?'Correction saved. Original record preserved.':'Entry saved locally and queued for archive. Open it to attach a source scan.');}catch(err){toast(err.message);}finally{button.disabled=false;}});
$('#close-detail').addEventListener('click',()=>$('#detail').close());$('#correct').addEventListener('click',()=>{const e=selected;$('#detail').close();navigate('entry',e);});$('#print').addEventListener('click',()=>window.print());
$('#verify').addEventListener('click',async()=>{try{const r=await api('/api/verify');$('#integrity-result').textContent=`Verified ${r.events} events. Head: ${r.head}`;}catch(err){$('#integrity-result').textContent=err.message;}});
$('#sync').addEventListener('click',async()=>{const button=$('#sync');button.disabled=true;$('#sync-result').textContent='Checking GCS and uploading pending events…';try{await api('/api/sync',{});$('#sync-result').textContent='Archive verified. All pending events uploaded.';}catch(err){$('#sync-result').textContent=err.message;}finally{button.disabled=false;await refresh().catch(err=>toast(err.message));}});
$('#attachment-form').addEventListener('submit',async e=>{e.preventDefault();const file=e.target.elements.file.files[0],button=e.target.querySelector('button');if(!file||file.size>64*1024*1024){toast('Choose a file up to 64 MiB.');return;}button.disabled=true;try{const bytes=new Uint8Array(await file.arrayBuffer());let binary='';for(let i=0;i<bytes.length;i+=32768)binary+=String.fromCharCode(...bytes.subarray(i,i+32768));const mime=file.type||({pdf:'application/pdf',jpg:'image/jpeg',jpeg:'image/jpeg',png:'image/png',mp4:'video/mp4',m4a:'audio/mp4',mp3:'audio/mpeg'}[file.name.split('.').pop().toLowerCase()]);await api('/api/attachments',{entry_id:selected.id,name:file.name,mime,data:btoa(binary)});const id=selected.id;await refresh();await showDetail(entries.find(x=>x.id===id));toast('Source file preserved and queued for archive.');}catch(err){toast(err.message);}finally{button.disabled=false;}});
api('/api/session').then(s=>s.authenticated?start():null).catch(err=>{$('#login-error').textContent=err.message;});
setInterval(()=>{if(!$('#application').hidden)refresh().catch(()=>{});},30000);

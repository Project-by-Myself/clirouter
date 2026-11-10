import{u as G,G as Y,b as J,w as A,r as h,H as W,i as q,j as e,I as x,y as V,C as Q}from"./index-g-6HIOA9.js";const X=[{id:"claude",name:"Claude Code",proto:"/v1/messages",icon:"playground"},{id:"codex",name:"Codex CLI",proto:"/v1/responses",icon:"playground"},{id:"openai",name:"Cursor · Cline · opencode",proto:"/v1/chat/completions",icon:"integrations"},{id:"gemini",name:"Gemini CLI",proto:"/v1beta",icon:"playground"},{id:"curl",name:"curl / SDK",proto:"HTTP",icon:"send"}];function Z(s,d,l){return s==="windows"?`$env:${d} = "${l}"`:`export ${d}=${l}`}function te(){const s=G(),{status:d}=Y(),{config:l,models:c}=J(),[b,D]=A("clr.intTool","claude"),[g,S]=A("clr.intOS",/Win/i.test(navigator.userAgent)?"windows":"unix"),[,H]=A("clr.visitedIntegrations","0"),[N,R]=h.useState(""),[j,F]=h.useState(""),[O,L]=h.useState(""),[P,w]=h.useState(""),[m,$]=h.useState({state:"idle"});h.useEffect(()=>H("1"),[H]);const _=(d==null?void 0:d.listen)||(l==null?void 0:l.listen)||"";h.useEffect(()=>{_&&!N&&R(W(_))},[_,N]),h.useEffect(()=>{if(!O&&c.length){L(c[0].id);const t=c.find(u=>/haiku|mini|flash|small|lite/i.test(u.id));w((t||c[0]).id)}},[c,O]);const a=l==null?void 0:l.api_keys[0],v=(d==null?void 0:d.auth_required)??(l?l.api_keys.length>0:!0),k=h.useMemo(()=>j.trim()?{key:j.trim(),note:"",real:!0}:a?a.key.startsWith("env:")?{key:g==="windows"?`$env:${a.key.slice(4)}`:`$${a.key.slice(4)}`,note:s("int.envKey",{name:a.name,env:a.key.slice(4)}),real:!1}:q(a.key)?{key:"<YOUR_CLIROUTER_KEY>",note:s("int.maskedKey",{name:a.name}),real:!1}:{key:a.key,note:"",real:!0}:{key:v?"<YOUR_CLIROUTER_KEY>":"clirouter",note:v?"":s("int.noAuth"),real:!v},[j,a,v,s,g]),o=`http://${N||"127.0.0.1:8317"}`,i=k.key,n=O||"<model>",U=P||n,r=g==="windows",p=(t,u)=>Z(g,t,u),T=r?"`":"\\",B={claude:[{title:r?"PowerShell":"Shell (macOS / Linux)",note:s("int.claudeShell"),code:[p("ANTHROPIC_BASE_URL",o),p("ANTHROPIC_AUTH_TOKEN",i),p("ANTHROPIC_MODEL",n),p("ANTHROPIC_DEFAULT_OPUS_MODEL",n),p("ANTHROPIC_DEFAULT_SONNET_MODEL",n),p("ANTHROPIC_DEFAULT_HAIKU_MODEL",U),"claude"].join(`
`)},{title:r?"%USERPROFILE%\\.claude\\settings.json":"~/.claude/settings.json",note:s("int.claudeSettings"),code:JSON.stringify({env:{ANTHROPIC_BASE_URL:o,ANTHROPIC_AUTH_TOKEN:i,ANTHROPIC_MODEL:n,ANTHROPIC_DEFAULT_OPUS_MODEL:n,ANTHROPIC_DEFAULT_SONNET_MODEL:n,ANTHROPIC_DEFAULT_HAIKU_MODEL:U}},null,2)}],codex:[{title:r?"%USERPROFILE%\\.codex\\config.toml":"~/.codex/config.toml",code:`model = "${n}"
model_provider = "clirouter"

[model_providers.clirouter]
name = "clirouter"
base_url = "${o}/v1"
env_key = "CLIROUTER_API_KEY"
wire_api = "responses"`},{title:r?"PowerShell":"Shell (macOS / Linux)",code:`${p("CLIROUTER_API_KEY",i)}
codex`}],openai:[{title:s("int.genericOpenAI"),note:s("int.openaiNote"),code:`Base URL:  ${o}/v1
API key:   ${i}
Model:     ${n}`},{title:"opencode — opencode.json",code:JSON.stringify({$schema:"https://opencode.ai/config.json",provider:{clirouter:{npm:"@ai-sdk/openai-compatible",name:"clirouter",options:{baseURL:`${o}/v1`,apiKey:i},models:Object.fromEntries((c.length?c.slice(0,8).map(t=>t.id):[n]).map(t=>[t,{name:t}]))}}},null,2)},{title:"Continue — ~/.continue/config.yaml",code:`models:
  - name: clirouter ${n}
    provider: openai
    model: ${n}
    apiBase: ${o}/v1
    apiKey: ${i}`}],gemini:[{title:r?"PowerShell":"Shell (macOS / Linux)",note:s("int.geminiNote"),code:`${p("GOOGLE_GEMINI_BASE_URL",o)}
${p("GEMINI_API_KEY",i)}
gemini -m ${n}`}],curl:[{title:"POST /v1/chat/completions",code:r?`$body = @{
  model = "${n}"
  messages = @(@{ role = "user"; content = "Hello!" })
} | ConvertTo-Json -Depth 5
Invoke-RestMethod -Method Post -Uri "${o}/v1/chat/completions" ${T}
  -Headers @{ Authorization = "Bearer ${i}" } ${T}
  -ContentType "application/json" -Body $body`:`curl ${o}/v1/chat/completions \\
  -H "Authorization: Bearer ${i}" \\
  -H "Content-Type: application/json" \\
  -d '{
    "model": "${n}",
    "stream": true,
    "messages": [{"role": "user", "content": "Hello!"}]
  }'`},{title:"POST /v1/messages",code:r?`$body = @{
  model = "${n}"
  max_tokens = 1024
  messages = @(@{ role = "user"; content = "Hello!" })
} | ConvertTo-Json -Depth 5
Invoke-RestMethod -Method Post -Uri "${o}/v1/messages" ${T}
  -Headers @{ "x-api-key" = "${i}"; "anthropic-version" = "2023-06-01" } ${T}
  -ContentType "application/json" -Body $body`:`curl ${o}/v1/messages \\
  -H "x-api-key: ${i}" \\
  -H "anthropic-version: 2023-06-01" \\
  -H "Content-Type: application/json" \\
  -d '{
    "model": "${n}",
    "max_tokens": 1024,
    "messages": [{"role": "user", "content": "Hello!"}]
  }'`},{title:"Python — openai SDK",code:`from openai import OpenAI

client = OpenAI(base_url="${o}/v1", api_key="${i}")
r = client.chat.completions.create(
    model="${n}",
    messages=[{"role": "user", "content": "Hello!"}],
)
print(r.choices[0].message.content)`},{title:"GET /v1/models",code:`${r?"curl.exe":"curl"} ${o}/v1/models -H "Authorization: Bearer ${i}"`}]},z=async()=>{var u;$({state:"busy"});const t=performance.now();try{const f={};k.real&&(f.Authorization=`Bearer ${i}`);const I=await fetch("/v1/models",{headers:f}),E=await I.text();if(!I.ok){let K=E;try{const y=JSON.parse(E);K=((u=y==null?void 0:y.error)==null?void 0:u.message)||(y==null?void 0:y.error)||E}catch{}$({state:"err",msg:`HTTP ${I.status}: ${String(K).slice(0,200)}`});return}const C=JSON.parse(E);$({state:"ok",models:Array.isArray(C==null?void 0:C.data)?C.data.length:0,ms:performance.now()-t})}catch(f){$({state:"err",msg:f.message})}},M=v&&!k.real;return e.jsxs("div",{children:[e.jsxs("div",{className:"page-head",children:[e.jsxs("div",{className:"grow",children:[e.jsx("h1",{children:s("nav.integrations")}),e.jsx("div",{className:"desc",children:s("int.desc")})]}),e.jsxs("div",{className:"seg",role:"group","aria-label":s("int.os"),children:[e.jsx("button",{"aria-pressed":!r,onClick:()=>S("unix"),children:"macOS / Linux"}),e.jsx("button",{"aria-pressed":r,onClick:()=>S("windows"),children:"Windows"})]})]}),e.jsx("div",{className:"tool-grid",role:"tablist","aria-label":s("int.tool"),children:X.map(t=>e.jsxs("button",{role:"tab",className:"tool","aria-selected":b===t.id,onClick:()=>D(t.id),children:[e.jsxs("span",{className:"t-name",children:[e.jsx(x,{name:t.icon,size:14}),t.name]}),e.jsx("span",{className:"t-proto",children:t.proto})]},t.id))}),e.jsxs("section",{className:"card",style:{marginBottom:18},children:[e.jsxs("div",{className:"card-head",children:[e.jsx("h2",{className:"grow",children:s("int.params")}),e.jsxs("div",{className:"test-result",role:"status",children:[m.state==="ok"&&e.jsxs("span",{className:"badge ok",children:[e.jsx(x,{name:"check",size:12,strokeWidth:2.5}),s("int.testOk",{n:m.models,ms:V(m.ms)})]}),m.state==="err"&&e.jsxs("span",{className:"badge err",title:m.msg,children:[e.jsx(x,{name:"alert",size:12}),s("int.testFail")]})]}),e.jsxs("button",{className:"btn sm",onClick:z,disabled:m.state==="busy"||M,title:M?s("int.testNeedKey"):void 0,children:[e.jsx(x,{name:"bolt",size:13}),m.state==="busy"?s("int.testing"):s("int.test")]})]}),e.jsxs("div",{className:"card-body grid-2",children:[e.jsxs("div",{className:"field",children:[e.jsx("label",{htmlFor:"int-host",children:s("int.host")}),e.jsx("input",{id:"int-host",className:"input mono",value:N,onChange:t=>R(t.target.value),"aria-describedby":"int-host-h"}),e.jsx("span",{className:"hint",id:"int-host-h",children:s("int.hostHint",{listen:_||"–"})})]}),e.jsxs("div",{className:"field",children:[e.jsx("label",{htmlFor:"int-key",children:s("int.key")}),e.jsx("input",{id:"int-key",className:"input mono",type:"password",autoComplete:"off",value:j,placeholder:a?a.name:s("int.keyPh"),onChange:t=>F(t.target.value),"aria-describedby":"int-key-h"}),e.jsx("span",{className:"hint",id:"int-key-h",children:k.note||s("int.keyHint")})]}),e.jsxs("div",{className:"field",children:[e.jsx("label",{htmlFor:"int-model",children:s("int.model")}),e.jsxs("select",{id:"int-model",className:"input mono",value:O,onChange:t=>L(t.target.value),children:[c.length===0&&e.jsx("option",{value:"",children:s("int.noModels")}),c.map(t=>e.jsxs("option",{value:t.id,children:[t.id," (",t.owner,")"]},t.id))]})]}),b==="claude"&&e.jsxs("div",{className:"field",children:[e.jsx("label",{htmlFor:"int-fast",children:s("int.fastModel")}),e.jsx("select",{id:"int-fast",className:"input mono",value:P,onChange:t=>w(t.target.value),children:c.map(t=>e.jsxs("option",{value:t.id,children:[t.id," (",t.owner,")"]},t.id))})]})]}),m.state==="err"&&e.jsx("div",{style:{padding:"0 18px 18px"},children:e.jsxs("div",{className:"alert err",role:"alert",children:[e.jsx(x,{name:"alert",style:{flex:"none",marginTop:2}}),e.jsx("pre",{children:m.msg})]})})]}),e.jsx("div",{className:"stack",children:B[b].map(t=>e.jsxs("div",{className:"snippet",children:[e.jsxs("div",{className:"snippet-head",children:[e.jsx(x,{name:t.title.includes("/")||t.title.includes("\\")||t.title.includes(".")?"edit":"playground",size:14,className:"faint"}),e.jsx("span",{className:"s-title grow ellipsis",children:t.title}),e.jsx(Q,{text:t.code,className:"btn xs"})]}),t.note&&e.jsx("p",{className:"snippet-note",children:t.note}),e.jsx("pre",{className:"code",tabIndex:0,"aria-label":t.title,children:t.code})]},t.title))})]})}export{te as default};

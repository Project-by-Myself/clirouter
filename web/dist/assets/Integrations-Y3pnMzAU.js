import{u as G,J as W,b as q,x as H,r as u,K as V,k as Q,j as e,I as y,z as X,C as Z}from"./index-BDpo0heW.js";const ee=[{id:"claude",name:"Claude Code",proto:"/v1/messages",icon:"playground"},{id:"codex",name:"Codex CLI",proto:"/v1/responses",icon:"playground"},{id:"openai",name:"Cursor · Cline · opencode",proto:"/v1/chat/completions",icon:"integrations"},{id:"gemini",name:"Gemini CLI",proto:"/v1beta",icon:"playground"},{id:"curl",name:"curl / SDK",proto:"HTTP",icon:"send"}];function te(s,d,i){return s==="windows"?`$env:${d} = "${i}"`:`export ${d}=${i}`}function ne(){const s=G(),{status:d}=W(),{config:i,models:c}=q(),[C,B]=H("clr.intTool","claude"),[g,L]=H("clr.intOS",/Win/i.test(navigator.userAgent)?"windows":"unix"),[,P]=H("clr.visitedIntegrations","0"),[N,I]=u.useState(""),[j,z]=u.useState(""),[$,U]=u.useState(""),[w,M]=u.useState(""),[m,O]=u.useState({state:"idle"});u.useEffect(()=>P("1"),[P]);const _=(d==null?void 0:d.listen)||(i==null?void 0:i.listen)||"",A=((i==null?void 0:i.public_url)||"").trim().replace(/\/+$/,"");u.useEffect(()=>{N||(A?I(A):_&&I(V(_)))},[_,N,A]),u.useEffect(()=>{if(!$&&c.length){U(c[0].id);const t=c.find(h=>/haiku|mini|flash|small|lite/i.test(h.id));M((t||c[0]).id)}},[c,$]);const l=i==null?void 0:i.api_keys[0],v=(d==null?void 0:d.auth_required)??(i?i.api_keys.length>0:!0),k=u.useMemo(()=>j.trim()?{key:j.trim(),note:"",real:!0}:l?l.key.startsWith("env:")?{key:g==="windows"?`$env:${l.key.slice(4)}`:`$${l.key.slice(4)}`,note:s("int.envKey",{name:l.name,env:l.key.slice(4)}),real:!1}:Q(l.key)?{key:"<YOUR_CLIROUTER_KEY>",note:s("int.maskedKey",{name:l.name}),real:!1}:{key:l.key,note:"",real:!0}:{key:v?"<YOUR_CLIROUTER_KEY>":"clirouter",note:v?"":s("int.noAuth"),real:!v},[j,l,v,s,g]),S=N.trim().replace(/\/+$/,""),a=/^https?:\/\//.test(S)?S:`http://${S||"127.0.0.1:8317"}`,o=k.key,n=$||"<model>",K=w||n,r=g==="windows",p=(t,h)=>te(g,t,h),T=r?"`":"\\",J={claude:[{title:r?"PowerShell":"Shell (macOS / Linux)",note:s("int.claudeShell"),code:[p("ANTHROPIC_BASE_URL",a),p("ANTHROPIC_AUTH_TOKEN",o),p("ANTHROPIC_MODEL",n),p("ANTHROPIC_DEFAULT_OPUS_MODEL",n),p("ANTHROPIC_DEFAULT_SONNET_MODEL",n),p("ANTHROPIC_DEFAULT_HAIKU_MODEL",K),"claude"].join(`
`)},{title:r?"%USERPROFILE%\\.claude\\settings.json":"~/.claude/settings.json",note:s("int.claudeSettings"),code:JSON.stringify({env:{ANTHROPIC_BASE_URL:a,ANTHROPIC_AUTH_TOKEN:o,ANTHROPIC_MODEL:n,ANTHROPIC_DEFAULT_OPUS_MODEL:n,ANTHROPIC_DEFAULT_SONNET_MODEL:n,ANTHROPIC_DEFAULT_HAIKU_MODEL:K}},null,2)}],codex:[{title:r?"%USERPROFILE%\\.codex\\config.toml":"~/.codex/config.toml",code:`model = "${n}"
model_provider = "clirouter"

[model_providers.clirouter]
name = "clirouter"
base_url = "${a}/v1"
env_key = "CLIROUTER_API_KEY"
wire_api = "responses"`},{title:r?"PowerShell":"Shell (macOS / Linux)",code:`${p("CLIROUTER_API_KEY",o)}
codex`}],openai:[{title:s("int.genericOpenAI"),note:s("int.openaiNote"),code:`Base URL:  ${a}/v1
API key:   ${o}
Model:     ${n}`},{title:"opencode — opencode.json",code:JSON.stringify({$schema:"https://opencode.ai/config.json",provider:{clirouter:{npm:"@ai-sdk/openai-compatible",name:"clirouter",options:{baseURL:`${a}/v1`,apiKey:o},models:Object.fromEntries((c.length?c.slice(0,8).map(t=>t.id):[n]).map(t=>[t,{name:t}]))}}},null,2)},{title:"Continue — ~/.continue/config.yaml",code:`models:
  - name: clirouter ${n}
    provider: openai
    model: ${n}
    apiBase: ${a}/v1
    apiKey: ${o}`}],gemini:[{title:r?"PowerShell":"Shell (macOS / Linux)",note:s("int.geminiNote"),code:`${p("GOOGLE_GEMINI_BASE_URL",a)}
${p("GEMINI_API_KEY",o)}
gemini -m ${n}`}],curl:[{title:"POST /v1/chat/completions",code:r?`$body = @{
  model = "${n}"
  messages = @(@{ role = "user"; content = "Hello!" })
} | ConvertTo-Json -Depth 5
Invoke-RestMethod -Method Post -Uri "${a}/v1/chat/completions" ${T}
  -Headers @{ Authorization = "Bearer ${o}" } ${T}
  -ContentType "application/json" -Body $body`:`curl ${a}/v1/chat/completions \\
  -H "Authorization: Bearer ${o}" \\
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
Invoke-RestMethod -Method Post -Uri "${a}/v1/messages" ${T}
  -Headers @{ "x-api-key" = "${o}"; "anthropic-version" = "2023-06-01" } ${T}
  -ContentType "application/json" -Body $body`:`curl ${a}/v1/messages \\
  -H "x-api-key: ${o}" \\
  -H "anthropic-version: 2023-06-01" \\
  -H "Content-Type: application/json" \\
  -d '{
    "model": "${n}",
    "max_tokens": 1024,
    "messages": [{"role": "user", "content": "Hello!"}]
  }'`},{title:"Python — openai SDK",code:`from openai import OpenAI

client = OpenAI(base_url="${a}/v1", api_key="${o}")
r = client.chat.completions.create(
    model="${n}",
    messages=[{"role": "user", "content": "Hello!"}],
)
print(r.choices[0].message.content)`},{title:"GET /v1/models",code:`${r?"curl.exe":"curl"} ${a}/v1/models -H "Authorization: Bearer ${o}"`}]},Y=async()=>{var h;O({state:"busy"});const t=performance.now();try{const f={};k.real&&(f.Authorization=`Bearer ${o}`);const R=await fetch("/v1/models",{headers:f}),E=await R.text();if(!R.ok){let F=E;try{const x=JSON.parse(E);F=((h=x==null?void 0:x.error)==null?void 0:h.message)||(x==null?void 0:x.error)||E}catch{}O({state:"err",msg:`HTTP ${R.status}: ${String(F).slice(0,200)}`});return}const b=JSON.parse(E);O({state:"ok",models:Array.isArray(b==null?void 0:b.data)?b.data.length:0,ms:performance.now()-t})}catch(f){O({state:"err",msg:f.message})}},D=v&&!k.real;return e.jsxs("div",{children:[e.jsxs("div",{className:"page-head",children:[e.jsxs("div",{className:"grow",children:[e.jsx("h1",{children:s("nav.integrations")}),e.jsx("div",{className:"desc",children:s("int.desc")})]}),e.jsxs("div",{className:"seg",role:"group","aria-label":s("int.os"),children:[e.jsx("button",{"aria-pressed":!r,onClick:()=>L("unix"),children:"macOS / Linux"}),e.jsx("button",{"aria-pressed":r,onClick:()=>L("windows"),children:"Windows"})]})]}),e.jsx("div",{className:"tool-grid",role:"tablist","aria-label":s("int.tool"),children:ee.map(t=>e.jsxs("button",{role:"tab",className:"tool","aria-selected":C===t.id,onClick:()=>B(t.id),children:[e.jsxs("span",{className:"t-name",children:[e.jsx(y,{name:t.icon,size:14}),t.name]}),e.jsx("span",{className:"t-proto",children:t.proto})]},t.id))}),e.jsxs("section",{className:"card",style:{marginBottom:18},children:[e.jsxs("div",{className:"card-head",children:[e.jsx("h2",{className:"grow",children:s("int.params")}),e.jsxs("div",{className:"test-result",role:"status",children:[m.state==="ok"&&e.jsxs("span",{className:"badge ok",children:[e.jsx(y,{name:"check",size:12,strokeWidth:2.5}),s("int.testOk",{n:m.models,ms:X(m.ms)})]}),m.state==="err"&&e.jsxs("span",{className:"badge err",title:m.msg,children:[e.jsx(y,{name:"alert",size:12}),s("int.testFail")]})]}),e.jsxs("button",{className:"btn sm",onClick:Y,disabled:m.state==="busy"||D,title:D?s("int.testNeedKey"):void 0,children:[e.jsx(y,{name:"bolt",size:13}),m.state==="busy"?s("int.testing"):s("int.test")]})]}),e.jsxs("div",{className:"card-body grid-2",children:[e.jsxs("div",{className:"field",children:[e.jsx("label",{htmlFor:"int-host",children:s("int.host")}),e.jsx("input",{id:"int-host",className:"input mono",value:N,onChange:t=>I(t.target.value),"aria-describedby":"int-host-h"}),e.jsx("span",{className:"hint",id:"int-host-h",children:s("int.hostHint",{listen:_||"–"})})]}),e.jsxs("div",{className:"field",children:[e.jsx("label",{htmlFor:"int-key",children:s("int.key")}),e.jsx("input",{id:"int-key",className:"input mono",type:"password",autoComplete:"off",value:j,placeholder:l?l.name:s("int.keyPh"),onChange:t=>z(t.target.value),"aria-describedby":"int-key-h"}),e.jsx("span",{className:"hint",id:"int-key-h",children:k.note||s("int.keyHint")})]}),e.jsxs("div",{className:"field",children:[e.jsx("label",{htmlFor:"int-model",children:s("int.model")}),e.jsxs("select",{id:"int-model",className:"input mono",value:$,onChange:t=>U(t.target.value),children:[c.length===0&&e.jsx("option",{value:"",children:s("int.noModels")}),c.map(t=>e.jsxs("option",{value:t.id,children:[t.id," (",t.owner,")"]},t.id))]})]}),C==="claude"&&e.jsxs("div",{className:"field",children:[e.jsx("label",{htmlFor:"int-fast",children:s("int.fastModel")}),e.jsx("select",{id:"int-fast",className:"input mono",value:w,onChange:t=>M(t.target.value),children:c.map(t=>e.jsxs("option",{value:t.id,children:[t.id," (",t.owner,")"]},t.id))})]})]}),m.state==="err"&&e.jsx("div",{style:{padding:"0 18px 18px"},children:e.jsxs("div",{className:"alert err",role:"alert",children:[e.jsx(y,{name:"alert",style:{flex:"none",marginTop:2}}),e.jsx("pre",{children:m.msg})]})})]}),e.jsx("div",{className:"stack",children:J[C].map(t=>e.jsxs("div",{className:"snippet",children:[e.jsxs("div",{className:"snippet-head",children:[e.jsx(y,{name:t.title.includes("/")||t.title.includes("\\")||t.title.includes(".")?"edit":"playground",size:14,className:"faint"}),e.jsx("span",{className:"s-title grow ellipsis",children:t.title}),e.jsx(Z,{text:t.code,className:"btn xs"})]}),t.note&&e.jsx("p",{className:"snippet-note",children:t.note}),e.jsx("pre",{className:"code",tabIndex:0,"aria-label":t.title,children:t.code})]},t.title))})]})}export{ne as default};

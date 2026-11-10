import{u as I,G as T,b as A,w as R,r as d,i as H,j as t,I as b,H as L}from"./index-DCkMsJf2.js";function S(s){if(!s)return window.location.host;let i=s,o="";const n=s.match(/^(.*):(\d+)$/);return n&&(i=n[1],o=n[2]),i=i.replace(/^\[|\]$/g,""),(!i||i==="0.0.0.0"||i==="::")&&(i=window.location.hostname||"127.0.0.1"),i.includes(":")&&(i=`[${i}]`),o?`${i}:${o}`:i}function M(){const s=I(),{status:i}=T(),{config:o,models:n}=A(),[v,k]=R("clr.intTool","claude"),[m,j]=d.useState(""),[u,C]=d.useState(""),[h,_]=d.useState(""),[f,N]=d.useState(""),p=(i==null?void 0:i.listen)||(o==null?void 0:o.listen)||"";d.useEffect(()=>{p&&!m&&j(S(p))},[p,m]),d.useEffect(()=>{if(!h&&n.length){_(n[0].id);const e=n.find(y=>/haiku|mini|flash|small/i.test(y.id));N((e||n[0]).id)}},[n,h]);const l=o==null?void 0:o.api_keys[0],x=(i==null?void 0:i.auth_required)??(o?o.api_keys.length>0:!0),g=d.useMemo(()=>u.trim()?{key:u.trim(),note:""}:l?l.key.startsWith("env:")?{key:`$${l.key.slice(4)}`,note:s("int.envKey",{name:l.name,env:l.key.slice(4)})}:H(l.key)?{key:"<YOUR_CLIROUTER_KEY>",note:s("int.maskedKey",{name:l.name})}:{key:l.key,note:""}:{key:x?"<YOUR_CLIROUTER_KEY>":"clirouter",note:x?"":s("int.noAuth")},[u,l,x,s]),c=`http://${m||"127.0.0.1:8317"}`,r=g.key,a=h||"<model>",$=f||a,O={claude:[{title:s("int.shell"),code:`export ANTHROPIC_BASE_URL=${c}
export ANTHROPIC_AUTH_TOKEN=${r}
export ANTHROPIC_MODEL=${a}
export ANTHROPIC_SMALL_FAST_MODEL=${$}
claude`},{title:"~/.claude/settings.json",note:s("int.claudeSettings"),code:JSON.stringify({env:{ANTHROPIC_BASE_URL:c,ANTHROPIC_AUTH_TOKEN:r,ANTHROPIC_MODEL:a,ANTHROPIC_SMALL_FAST_MODEL:$}},null,2)}],codex:[{title:"~/.codex/config.toml",code:`model = "${a}"
model_provider = "clirouter"

[model_providers.clirouter]
name = "clirouter"
base_url = "${c}/v1"
env_key = "CLIROUTER_API_KEY"
wire_api = "responses"`},{title:s("int.shell"),code:`export CLIROUTER_API_KEY=${r}
codex`}],openai:[{title:s("int.genericOpenAI"),note:s("int.openaiNote"),code:`Base URL:  ${c}/v1
API key:   ${r}
Model:     ${a}`},{title:"opencode — opencode.json",code:JSON.stringify({$schema:"https://opencode.ai/config.json",provider:{clirouter:{npm:"@ai-sdk/openai-compatible",name:"clirouter",options:{baseURL:`${c}/v1`,apiKey:r},models:Object.fromEntries((n.length?n.slice(0,8).map(e=>e.id):[a]).map(e=>[e,{name:e}]))}}},null,2)},{title:"Continue — ~/.continue/config.yaml",code:`models:
  - name: clirouter ${a}
    provider: openai
    model: ${a}
    apiBase: ${c}/v1
    apiKey: ${r}`}],gemini:[{title:s("int.shell"),note:s("int.geminiNote"),code:`export GOOGLE_GEMINI_BASE_URL=${c}
export GEMINI_API_KEY=${r}
gemini -m ${a}`}],curl:[{title:"POST /v1/chat/completions",code:`curl ${c}/v1/chat/completions \\
  -H "Authorization: Bearer ${r}" \\
  -H "Content-Type: application/json" \\
  -d '{
    "model": "${a}",
    "stream": true,
    "messages": [{"role": "user", "content": "Hello!"}]
  }'`},{title:"POST /v1/messages",code:`curl ${c}/v1/messages \\
  -H "x-api-key: ${r}" \\
  -H "anthropic-version: 2023-06-01" \\
  -H "Content-Type: application/json" \\
  -d '{
    "model": "${a}",
    "max_tokens": 1024,
    "messages": [{"role": "user", "content": "Hello!"}]
  }'`},{title:"GET /v1/models",code:`curl ${c}/v1/models -H "Authorization: Bearer ${r}"`}]},E=[["claude","Claude Code"],["codex","Codex CLI"],["openai","Cursor / Cline / Continue / opencode"],["gemini","Gemini CLI"],["curl","curl"]];return t.jsxs("div",{children:[t.jsx("div",{className:"page-head",children:t.jsxs("div",{className:"grow",children:[t.jsx("h1",{children:s("nav.integrations")}),t.jsx("div",{className:"desc",children:s("int.desc")})]})}),t.jsx("div",{className:"card card-pad",style:{marginBottom:16},children:t.jsxs("div",{className:"grid-2",children:[t.jsxs("div",{className:"field",children:[t.jsx("label",{htmlFor:"int-host",children:s("int.host")}),t.jsx("input",{id:"int-host",className:"input mono",value:m,onChange:e=>j(e.target.value),"aria-describedby":"int-host-h"}),t.jsx("span",{className:"hint",id:"int-host-h",children:s("int.hostHint",{listen:p||"–"})})]}),t.jsxs("div",{className:"field",children:[t.jsx("label",{htmlFor:"int-key",children:s("int.key")}),t.jsx("input",{id:"int-key",className:"input mono",type:"password",autoComplete:"off",value:u,placeholder:l?l.name:s("int.keyPh"),onChange:e=>C(e.target.value),"aria-describedby":"int-key-h"}),t.jsx("span",{className:"hint",id:"int-key-h",children:g.note||s("int.keyHint")})]}),t.jsxs("div",{className:"field",children:[t.jsx("label",{htmlFor:"int-model",children:s("int.model")}),t.jsxs("select",{id:"int-model",className:"input mono",value:h,onChange:e=>_(e.target.value),children:[n.length===0&&t.jsx("option",{value:"",children:s("int.noModels")}),n.map(e=>t.jsxs("option",{value:e.id,children:[e.id," (",e.owner,")"]},e.id))]})]}),v==="claude"&&t.jsxs("div",{className:"field",children:[t.jsx("label",{htmlFor:"int-fast",children:s("int.fastModel")}),t.jsx("select",{id:"int-fast",className:"input mono",value:f,onChange:e=>N(e.target.value),children:n.map(e=>t.jsxs("option",{value:e.id,children:[e.id," (",e.owner,")"]},e.id))})]})]})}),t.jsx("div",{className:"tabs",role:"tablist",children:E.map(([e,y])=>t.jsx("button",{role:"tab","aria-selected":v===e,onClick:()=>k(e),children:y},e))}),t.jsx("div",{className:"stack",children:O[v].map(e=>t.jsxs("div",{className:"stack",style:{marginTop:0},children:[t.jsxs("h3",{className:"row",children:[t.jsx(b,{name:"playground",size:14})," ",e.title]}),e.note&&t.jsx("p",{className:"muted small",children:e.note}),t.jsx(L,{code:e.code,label:e.title})]},e.title))})]})}export{M as default};

package asar

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const inlineSavedModule = `manager(e,t,n){async function unrelated(){return null}let padding="PADDING";n.d(t,{},{read:async function(model){return async function(e,t,n,r){try{if(!await r())return null;let note="} async function decoy(){",requirements=await e.sendRequest("configRequirements/read"),{config:c}=await e.sendRequest("config/read");return resolve(null,c.service_tier,accessFor(requirements))}catch(e){return t.error("Failed to read saved service tier for request",{safe:{},sensitive:{error:e}}),null}}(client,logger,model,async()=>false)}})}`

const inlinePolicyModule = `policy(e,t,n){var identity=n("identity"),requirements=n("requirements");n.d(t,{},{read:async function(e,t,n,r){try{let allowed=await async function(e,t){let auth=await(0,identity.read)(e,t);if("chatgpt"!==auth&&"personalAccessToken"!==auth&&"amazonBedrock"!==auth)return null;let response=await(0,requirements.read)(e,t);return e.query.setData(requirements.query,{authMethod:auth,hostId:t},response),accessFor(response)}(e,t);let{config:c}=await e.client.sendRequest("config/read"),model=await async function(e,t,n){return{model:n}}(e,t,n);return resolve(model,c.service_tier,allowed)}catch(e){return logger.error("Failed to read service tier for request",{safe:{},sensitive:{error:e}}),null}}})}`

const inlineIdentityModule = `identity(e,t,n){n.d(t,{},{read:async function(e,t){let[f,m]=await Promise.all([e.client.rpc.getAccount(),e.client.rpc.getAuthMethod()]);if("personalAccessToken"===m)return m;var account=f.account;if(null==account)return null;let kind=account.type;return"apiKey"===kind?"apikey":"amazonBedrock"===kind||"chatgpt"===kind||"gcp"===kind?kind:null}})}`

const inlineAccessModule = `permissions(e,t,n){n.d(t,{},{read:function(i){let a=i?.authMethod==="chatgpt"||i?.authMethod==="personalAccessToken"||i?.authMethod==="amazonBedrock",pending=!!i?.isLoading,c={requirements:{featureRequirements:{fast_mode:state.fastAllowed,ultrafast_mode:state.ultraAllowed}}};return{serviceTierAccess:a&&!pending?accessFor(c):null,isLoading:pending}}})}`

const inlineCatalogModule = `catalog(e,t,n){var cfg=n("config");let query=makeQuery(({additionalAvailableModels:e,authMethod:t,enabledReasoningEfforts:n,hostId:i,includeUltraReasoningEffort:a=!0,limit:s=100},{get:d,queryClient:f,scope:p})=>{let config=d(configQuery).data.config,custom=(0,cfg.cc)(config),provider="string"==typeof config?.model_provider?config.model_provider:null,catalogPath="string"==typeof config?.model_catalog_json?config.model_catalog_json:null,enabled=n==null?d(preferences):new Set([...n,"persistent"]),x=a&&d(flag.JW,"1186680773");return{queryFn:()=>client.sendRequest("model/list"),select:({data:models})=>selectModels({authMethod:t,enabledReasoningEfforts:enabled,includeUltraReasoningEffort:x,isCustomModelProvider:custom,models})}});n.d(t,{},{query})}`

func rspackChunk(modules ...string) []byte {
	return []byte("export const __webpack_modules__={" + strings.Join(modules, ",") + "};")
}

func inlineRspackAssets() map[string][]byte {
	saved := strings.Replace(inlineSavedModule, "PADDING", strings.Repeat("x", 11000), 1)
	return map[string][]byte{
		"webview/assets/async/readers.js": rspackChunk(saved, inlinePolicyModule, inlineAccessModule, inlineCatalogModule),
		"webview/assets/async/copy.js":    rspackChunk(inlinePolicyModule, inlineAccessModule, inlineCatalogModule),
		"webview/assets/async/auth.js":    rspackChunk(inlineIdentityModule),
		"webview/assets/async/auth-copy.js": rspackChunk(inlineIdentityModule,
			strings.Replace(inlineIdentityModule, "identity(", "unrelatedIdentity(", 1),
		),
		"webview/assets/descriptions.js": []byte(`let description="modelDescription.copyV2.sol61";let table=new Map([["gpt-6.1-sol",labels.sol],["gpt-6.1-sol-ultrafast",labels.sol],["gpt-5.6-sol",labels.previous],["gpt-6-astra",labels.astra]]);`),
		"zz-payload.bin":                 []byte("unchanged"),
	}
}

func TestRspackInlineReadersAndCopiedModules(t *testing.T) {
	checkRspackRuntime(t, inlineRspackAssets(), "")
}

func checkRspackRuntime(t *testing.T, assets map[string][]byte, extraChecks string) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("Node.js is required for native Rspack regression checks")
	}
	source := filepath.Join(t.TempDir(), "app.asar")
	writeMultiFixture(t, source, assets)
	a, err := Open(source)
	if err != nil {
		t.Fatal(err)
	}
	probe, err := a.Probe()
	if err != nil || probe.Status != StatusOriginal {
		t.Fatalf("inline Rspack source must be patchable: %#v, %v", probe, err)
	}
	if got := strings.Join(probe.UltrafastModels, ","); got != "gpt-5.6-sol,gpt-6-astra,gpt-6.1-sol" {
		t.Fatalf("double-quoted model declarations: %s", got)
	}
	dest := filepath.Join(t.TempDir(), "patched.asar")
	if _, err := PatchCopy(source, dest); err != nil {
		t.Fatal(err)
	}
	a, err = Open(dest)
	if err != nil {
		t.Fatal(err)
	}
	probe, err = a.Probe()
	if err != nil || probe.Status != StatusPatched || len(probe.Candidates) != 0 {
		t.Fatalf("inline patch must be complete and idempotent: %#v, %v", probe, err)
	}
	var chunks []string
	for _, entry := range a.Entries() {
		if !isJavaScript(entry) {
			continue
		}
		data, err := a.ReadEntry(entry)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte("__webpack_modules__")) {
			chunks = append(chunks, string(data))
		}
	}
	encoded, err := json.Marshal(chunks)
	if err != nil {
		t.Fatal(err)
	}
	checks := `
const assert=require("node:assert/strict"),vm=require("node:vm");
const state={account:{account:null,requiresOpenaiAuth:false},loginMethod:null,fastAllowed:true,ultraAllowed:true};
const all=["low","medium","high","xhigh","max","ultra"];
const client={rpc:{getAccount:async()=>state.account,getAuthMethod:async()=>state.loginMethod},sendRequest:async method=>{
 if(method==="config/read")return{config:{service_tier:"priority"}};
 if(method==="configRequirements/read")return{requirements:{featureRequirements:{fast_mode:state.fastAllowed,ultrafast_mode:state.ultraAllowed}}};
 if(method==="model/list")return{data:[]};
 throw Error(method);
}};
const accessFor=x=>({fast:x.requirements?.featureRequirements?.fast_mode!==false,ultrafast:x.requirements?.featureRequirements?.ultrafast_mode!==false});
const resolve=(model,tier,access)=>access?.fast?tier??"priority":null;
const configQuery={},preferences={},flag={JW:{}},logger={error(){}};
const selectModels=({enabledReasoningEfforts,includeUltraReasoningEffort,models})=>({models:models.map(m=>({...m,supportedReasoningEfforts:m.supportedReasoningEfforts.filter(e=>enabledReasoningEfforts.has(e.reasoningEffort)&&(includeUltraReasoningEffort||e.reasoningEffort!=="ultra"))}))});
const context={state,client,logger,accessFor,resolve,configQuery,preferences,flag,selectModels,makeQuery:x=>x};
const definitions=Object.assign({},...CHUNKS.map(source=>vm.runInNewContext("("+source.slice(source.indexOf("=")+1).replace(/;$/,"")+")",context)));
const cache={};
function load(id){
 if(id==="requirements")return{read:()=>client.sendRequest("configRequirements/read"),query:{}};
 if(id==="config")return{cc:(config,provider)=> (provider??config.model_provider)!=="openai"};
 if(cache[id])return cache[id];
 const exports=cache[id]={};definitions[id]({},exports,load);return exports;
}
load.d=(target,getters,values={})=>{for(const[k,get]of Object.entries(getters))Object.defineProperty(target,k,{get});Object.assign(target,values)};
const scope={client,query:{setData(){}}};
function efforts(auth=null,include=true,enabled=all,modelEfforts=all){
 const get=key=>key===configQuery?{data:{config:{model_provider:"custom"}}}:key===preferences?new Set(enabled):false;
 const query=load("catalog").query({authMethod:auth,enabledReasoningEfforts:enabled,includeUltraReasoningEffort:include,hostId:"local"},{get,queryClient:{},scope:{}});
 return Array.from(query.select({data:[{model:"supported",supportedReasoningEfforts:modelEfforts.map(reasoningEffort=>({reasoningEffort}))}]}).models[0].supportedReasoningEfforts,e=>e.reasoningEffort);
}
(async()=>{
 assert.equal(load("permissions").read({authMethod:null,requiresAuth:false}).serviceTierAccess.fast,true);
 assert.equal(load("permissions").read({authMethod:null,requiresAuth:true}).serviceTierAccess,null);
 assert.equal(load("permissions").read({authMethod:"unknown",requiresAuth:false}).serviceTierAccess,null);
 assert.equal(load("permissions").read({authMethod:null,requiresAuth:false,isLoading:true}).serviceTierAccess,null);
 assert.equal(await load("identity").read(scope,"local"),"apikey");
 assert.equal(await load("unrelatedIdentity").read(scope,"local"),null,"unreferenced account readers must not change");
 for(const path of ["first-turn","reasoning-continuation","derived-thread"]){
  assert.equal(await load("policy").read(scope,"local",null),"priority",path);
 }
 assert.equal(await load("manager").read(null),"priority","saved tier must retain API access");
 state.fastAllowed=false;
 assert.equal(await load("policy").read(scope,"local",null),null);
 assert.equal(await load("manager").read(null),null);
 state.fastAllowed=true;
 for(const account of [{account:null,requiresOpenaiAuth:true},{account:null},{account:{type:"unknown"},requiresOpenaiAuth:false}]){
  state.account=account;assert.equal(await load("policy").read(scope,"local",null),null);
 }
 for(const type of ["apiKey","chatgpt","amazonBedrock"]){
  state.account={account:{type},requiresOpenaiAuth:true};
  assert.equal(await load("policy").read(scope,"local",null),"priority",type+" retains its native contract");
 }
 state.account={account:null,requiresOpenaiAuth:true};state.loginMethod="personalAccessToken";
 assert.equal(await load("policy").read(scope,"local",null),"priority");
 assert.deepEqual(efforts(),all);
 assert.deepEqual(efforts("apikey"),all);
 assert.deepEqual(efforts(null,false),all.slice(0,5));
 assert.deepEqual(efforts(null,true,all.filter(e=>e!=="ultra")),all.slice(0,5));
 assert.deepEqual(efforts(null,true,all,all.slice(0,4)),all.slice(0,4));
 assert.deepEqual(efforts("chatgpt"),all.slice(0,5));
 assert.deepEqual(efforts("unknown"),all.slice(0,5));
EXTRA_CHECKS
})().catch(error=>{console.error(error.stack);process.exitCode=1});
`
	program := strings.Replace(checks, "CHUNKS", string(encoded), 1)
	program = strings.Replace(program, "EXTRA_CHECKS", extraChecks, 1)
	if output, err := exec.Command(node, "-e", program).CombinedOutput(); err != nil {
		t.Fatalf("native inline reader behavior: %s", output)
	}
}

func TestRspackProviderOverrideAndWrappedModules(t *testing.T) {
	assets := inlineRspackAssets()
	assets["webview/assets/async/copy.js"] = rspackChunk(wrappedRspackModule(inlinePolicyModule), wrappedRspackModule(inlineAccessModule), wrappedRspackModule(inlineCatalogModule))
	assets["webview/assets/async/auth-copy.js"] = rspackChunk(wrappedRspackModule(inlineIdentityModule), wrappedRspackModule(strings.Replace(inlineIdentityModule, "identity(", "unrelatedIdentity(", 1)))
	for name, path := range map[string]string{
		"hostManager":      ".vite/build/app-server-host.js",
		"bootstrapManager": ".vite/build/early-bootstrap-fixture.js",
	} {
		assets[path] = rspackChunk(strings.Replace(inlineSavedModule, "manager(", name+"(", 1))
	}
	for path, data := range assets {
		content := string(data)
		content = strings.ReplaceAll(content, "async function(e,t,n,r){try{if(!await r())", "async function(e,t,n,r,provider){try{if(!await r())")
		content = strings.ReplaceAll(content, `return resolve(null,c.service_tier,accessFor(requirements))`, `await e.sendRequest("model/list",{modelProvider:provider});return resolve(null,c.service_tier,accessFor(requirements))`)
		content = strings.ReplaceAll(content, "model,async()=>false)", `model,async()=>false,"custom")`)
		content = strings.ReplaceAll(content, "custom=(0,cfg.cc)(config)", `override=state.providerOverride,custom=(0,cfg.cc)(config,override)`)
		content = strings.ReplaceAll(content, `client.sendRequest("model/list")`, `client.sendRequest("model/list",{modelProvider:override})`)
		assets[path] = []byte(content)
	}
	checkRspackRuntime(t, assets, `
 state.providerOverride="openai";
 assert.deepEqual(efforts(),all.slice(0,5),"provider override retains the default-provider gate");
 state.providerOverride="custom";
 assert.deepEqual(efforts(),all,"custom provider override restores Ultra");
 let requestedProvider;
 const originalRequest=client.sendRequest;
 client.sendRequest=async(method,args)=>{if(method==="model/list")requestedProvider=args?.modelProvider;return originalRequest(method,args)};
 for(const id of ["manager","hostManager","bootstrapManager"]){
  assert.equal(await load(id).read(null),"priority",id+" retains saved tier");
  assert.equal(requestedProvider,"custom",id+" forwards the selected provider");
 }
 await load("catalog").query({authMethod:null,hostId:"local"},{get:key=>key===configQuery?{data:{config:{model_provider:"openai"}}}:key===preferences?new Set(all):false,queryClient:{},scope:{}}).queryFn();
 assert.equal(requestedProvider,"custom","query forwards the selected provider");
 `)
}

func wrappedRspackModule(module string) string {
	start := strings.IndexByte(module, '(')
	return module[:start] + ":(function" + module[start:] + ")"
}

func TestSavedReaderCopiesRejectUnrelatedOrAdditionalTargets(t *testing.T) {
	for _, name := range []string{"unrelated third reader", "fourth reader"} {
		t.Run(name, func(t *testing.T) {
			assets := inlineRspackAssets()
			assets[".vite/build/early-bootstrap-fixture.js"] = rspackChunk(inlineSavedModule)
			if name == "unrelated third reader" {
				assets["webview/assets/unrelated.js"] = rspackChunk(inlineSavedModule)
			} else {
				assets[".vite/build/app-server-host.js"] = rspackChunk(inlineSavedModule)
				assets["webview/assets/unrelated.js"] = rspackChunk(inlineSavedModule)
			}
			path := filepath.Join(t.TempDir(), "app.asar")
			writeMultiFixture(t, path, assets)
			a, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			probe, err := a.Probe()
			if err == nil && probe.Status != StatusUnknown {
				t.Fatalf("ambiguous saved-reader copies accepted: %#v", probe)
			}
		})
	}
}

func TestRspackRejectsMissingOrMismatchedAccountDependency(t *testing.T) {
	for _, change := range []string{"missing", "different account binding", "different policy variable"} {
		t.Run(change, func(t *testing.T) {
			assets := inlineRspackAssets()
			switch change {
			case "missing":
				delete(assets, "webview/assets/async/auth.js")
				delete(assets, "webview/assets/async/auth-copy.js")
			case "different account binding":
				for _, path := range []string{"webview/assets/async/auth.js", "webview/assets/async/auth-copy.js"} {
					assets[path] = bytes.ReplaceAll(assets[path], []byte("account=f.account"), []byte("account=unrelated.account"))
				}
			case "different policy variable":
				assets["webview/assets/async/readers.js"] = bytes.ReplaceAll(assets["webview/assets/async/readers.js"], []byte(`"amazonBedrock"!==auth`), []byte(`"amazonBedrock"!==other`))
			}
			path := filepath.Join(t.TempDir(), "app.asar")
			writeMultiFixture(t, path, assets)
			a, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := a.Probe(); err == nil {
				t.Fatal("unsupported Rspack account chain was accepted")
			}
		})
	}
}

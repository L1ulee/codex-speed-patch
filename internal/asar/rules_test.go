package asar

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The relevant function bodies preserve the calling convention of the packaged
// desktop code. The assertions execute patched JavaScript, rather than checking
// only that a replacement string is present.
const requestFixture = `
let method = async () => "apikey";
let policyEnabled = true;
let config = {service_tier:"priority"};
let logger = {error(){}};
let client = {sendRequest:async(name) => {
 if(name==="configRequirements/read")return {requirements:{featureRequirements:{fast_mode:policyEnabled}}};
 if(name==="config/read")return {config};
 if(name==="model/list")return {data:[{model:"supported",isDefault:true,defaultServiceTier:"fast",serviceTiers:[{id:"priority",name:"Fast"}]}]};
 throw Error(name);
}};
let flatten = x => x;
let resolve = (model,tier,allowed=true) => !allowed?null:tier??model?.serviceTiers?.[0]?.id??null;
let loadRequirements = async () => client.sendRequest("configRequirements/read");
async function saved(e,t,n,r){try{if(!await r()||(await e.sendRequest(` + "`configRequirements/read`" + `,void 0,{priority:` + "`critical`" + `})).requirements?.featureRequirements?.fast_mode===!1)return null;let{config:t}=await e.sendRequest(` + "`config/read`" + `,{includeLayers:!1,cwd:null},{priority:` + "`critical`" + `}),i=flatten(t);if(i.service_tier!=null)return resolve(null,i.service_tier);let{data:a}=await e.sendRequest(` + "`model/list`" + `,{includeHidden:!0});return resolve(a[0],null)}catch(e){return t.error(` + "`Failed to read saved service tier for request`" + `),null}}
async function request(e,t,n){try{let r=await allowed(e,t);let{config:a}=await client.sendRequest(` + "`config/read`" + `),o=flatten(a);return resolve(null,o.service_tier,r)}catch(e){return logger.error(` + "`Failed to read service tier for request`" + `),null}}
async function allowed(e,t){let n=await method(e,t);if(n!==` + "`chatgpt`" + `&&n!==` + "`personalAccessToken`" + `)return!1;let r=await loadRequirements(e,t,{priority:` + "`critical`" + `});return r.requirements?.featureRequirements?.fast_mode!==!1}
`

func TestRequestTierSurvivesAPIContinuationAndChildCreation(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is required for JavaScript runtime regression tests")
	}
	source := filepath.Join(t.TempDir(), "app.asar")
	content := `let a=true,u=false,c={requirements:{featureRequirements:{fast_mode:true}}},d=a&&!u&&c!=null&&c?.requirements?.featureRequirements?.fast_mode!==!1;` + requestFixture
	writeFixture(t, source, []byte(content))
	dest := filepath.Join(t.TempDir(), "patched.asar")
	if _, err := PatchCopy(source, dest); err != nil {
		t.Fatal(err)
	}
	archive, err := Open(dest)
	if err != nil {
		t.Fatal(err)
	}
	data, err := archive.ReadEntry(archive.Entries()[0])
	if err != nil {
		t.Fatal(err)
	}
	assertions := `
const assert=require("node:assert/strict");
(async()=>{
 assert.equal(config.service_tier,"priority");
 for(const path of ["reasoning-continuation","child-thread","resume"]){
  assert.equal(await request({},"local",null),"priority",path);
 }
 assert.equal(await saved(client,logger,null,async()=>false),"priority","queued execution");
 policyEnabled=false;
 assert.equal(await request({},"local",null),null,"disabled policy");
 assert.equal(await saved(client,logger,null,async()=>false),null,"disabled queued policy");
 method=async()=>"unknown";
 assert.equal(await request({},"local",null),null,"unrecognized auth mode");
})().catch(e=>{console.error(e.message);process.exitCode=1});`
	if output, err := exec.Command(node, "-e", string(data)+assertions).CombinedOutput(); err != nil {
		t.Fatalf("patched request paths failed: %s", output)
	}
}

func TestUIOnlyPatchIsPartial(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.asar")
	writeFixture(t, path, []byte(`let d=!u&&(!a||!!c&&c?.requirements?.featureRequirements?.fast_mode!==!1);`+requestFixture))
	archive, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	probe, err := archive.Probe()
	if err != nil {
		t.Fatal(err)
	}
	if probe.Status != ProbeStatus("partial") {
		t.Fatalf("UI-only patch reported %s; expected partial", probe.Status)
	}
}

func TestModernSpeedAccessKeepsPolicyAndEnablesAPI(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js runtime test")
	}
	content := `function nativeAccess(e){return {fast:e.requirements?.featureRequirements?.fast_mode!==false,ultrafast:e.requirements?.featureRequirements?.ultrafast_mode!==false}}
function permissions(i){let a=i?.authMethod===` + "`chatgpt`" + `||i?.authMethod===` + "`personalAccessToken`" + `,u=false,c={requirements:{featureRequirements:{fast_mode:policyEnabled,ultrafast_mode:false}}},d=a&&!u&&c!=null?nativeAccess(c):null;return{serviceTierAccess:d,isLoading:u}}` + requestFixture
	source := filepath.Join(t.TempDir(), "app.asar")
	writeFixture(t, source, []byte(content))
	dest := filepath.Join(t.TempDir(), "patched.asar")
	if _, err := PatchCopy(source, dest); err != nil {
		t.Fatal(err)
	}
	a, err := Open(dest)
	if err != nil {
		t.Fatal(err)
	}
	data, err := a.ReadEntry(a.Entries()[0])
	if err != nil {
		t.Fatal(err)
	}
	checks := `const assert=require("node:assert/strict");assert.deepEqual(permissions({authMethod:"apikey"}).serviceTierAccess,{fast:true,ultrafast:false});policyEnabled=false;assert.equal(permissions({authMethod:"apikey"}).serviceTierAccess.fast,false);assert.equal(permissions({authMethod:"unknown"}).serviceTierAccess,null);`
	if output, err := exec.Command(node, "-e", string(data)+checks).CombinedOutput(); err != nil {
		t.Fatalf("modern speed access: %s", output)
	}
}

func TestPartialPatchUpgradesAllRequestPaths(t *testing.T) {
	source := filepath.Join(t.TempDir(), "app.asar")
	writeFixture(t, source, []byte(`let d=!u&&(!a||!!c&&c?.requirements?.featureRequirements?.fast_mode!==!1);`+requestFixture))
	dest := filepath.Join(t.TempDir(), "patched.asar")
	if _, err := PatchCopy(source, dest); err != nil {
		t.Fatal(err)
	}
	a, err := Open(dest)
	if err != nil {
		t.Fatal(err)
	}
	p, err := a.Probe()
	if err != nil || p.Status != StatusPatched {
		t.Fatalf("partial upgrade = %v, %v", p, err)
	}
}

func TestReaderWithDifferentCallbackIsRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.asar")
	bad := strings.Replace(requestFixture, "!await r()", "!await unrelated()", 1)
	writeFixture(t, path, []byte(`let d=a&&!u&&c!=null&&c?.requirements?.featureRequirements?.fast_mode!==!1;`+bad))
	a, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Probe(); err == nil {
		t.Fatal("unsupported authentication callback was accepted")
	}
}

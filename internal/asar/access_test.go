package asar

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const optionalAuthMethodFixture = "let provider=`client`;let accountResult={account:null,requiresOpenaiAuth:false};let loginMethod=null;" +
	"function accountMethod(e){return e==null?null:e.type===`apiKey`?`apikey`:e.type===`chatgpt`?`chatgpt`:null}" +
	"async function method(e,t){let r=e.get(provider,t);let[s,c]=await Promise.all([r.rpc.getAccount(),r.rpc.getAuthMethod().catch(()=>null)]);return c===`personalAccessToken`?c:accountMethod(s.account)}"

const optionalAuthUIFixture = "function nativeAccess(e){let t=e.requirements?.featureRequirements,n=t?.fast_mode!==!1;return{fast:n,ultrafast:(`supportsIndependentSpeedModes`in e&&e.supportsIndependentSpeedModes===!0||n)&&t?.ultrafast_mode!==!1}}" +
	"function permissions(i){let a=i?.authMethod===`chatgpt`||i?.authMethod===`personalAccessToken`||i?.authMethod===`amazonBedrock`,u=!!i?.isLoading,c={requirements:{featureRequirements:{fast_mode:policyEnabled,ultrafast_mode:false}},supportsIndependentSpeedModes:true},d=a&&!u&&c!=null?nativeAccess(c):null;return{serviceTierAccess:d,isLoading:u}}"

func optionalAuthFixture() string {
	return strings.Replace(requestFixture, `let method = async () => "apikey";`, optionalAuthMethodFixture, 1) + optionalAuthUIFixture
}

func TestSpeedControlsAndRequestsSupportExplicitlyOptionalOpenAIAuth(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("Node.js is required to verify native Speed access")
	}
	source := filepath.Join(t.TempDir(), "app.asar")
	writeFixture(t, source, []byte(optionalAuthFixture()))
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
	checks := `
const assert=require("node:assert/strict");
client.rpc={getAccount:async()=>accountResult,getAuthMethod:async()=>loginMethod};
const scope={get:()=>client};
(async()=>{
 const custom={authMethod:null,requiresAuth:false,isLoading:false};
 assert.deepEqual(permissions(custom).serviceTierAccess,{fast:true,ultrafast:false},"native controls for a provider with optional OpenAI authentication");
 assert.equal(await request(scope,"local",null),"priority","native request policy for optional authentication");
 assert.equal(await saved(client,logger,null,async()=>false),"priority","queued request retains Fast");
 assert.equal(permissions({...custom,isLoading:true}).serviceTierAccess,null,"loading still hides controls");
 assert.equal(permissions({authMethod:null,requiresAuth:true}).serviceTierAccess,null,"logged-out OpenAI account");
 assert.equal(permissions({authMethod:null}).serviceTierAccess,null,"missing auth-required flag");
 assert.equal(permissions({authMethod:"unknown",requiresAuth:false}).serviceTierAccess,null,"unknown auth method is not treated as a custom provider");
 for(const authMethod of ["chatgpt","personalAccessToken","apikey","amazonBedrock"]){
  assert.equal(permissions({authMethod,requiresAuth:true}).serviceTierAccess.fast,true,authMethod+" existing control access");
 }
 policyEnabled=false;
 assert.equal(permissions(custom).serviceTierAccess.fast,false,"explicit Fast policy denial");
 assert.equal(await request(scope,"local",null),null,"request policy denial");
 policyEnabled=true;
 for(const value of [{account:null,requiresOpenaiAuth:true},{account:null},{account:{type:"unknown"},requiresOpenaiAuth:false}]){
  accountResult=value;
  assert.equal(await request(scope,"local",null),null,"required or unknown authentication stays denied");
 }
 accountResult={account:{type:"apiKey"},requiresOpenaiAuth:true};
 assert.equal(await request(scope,"local",null),"priority","API-key authentication remains supported");
 accountResult={account:{type:"chatgpt"},requiresOpenaiAuth:true};
 assert.equal(await request(scope,"local",null),"priority","ChatGPT authentication remains supported");
 accountResult={account:null,requiresOpenaiAuth:true};loginMethod="personalAccessToken";
 assert.equal(await request(scope,"local",null),"priority","personal access token retains its contract");
})().catch(e=>{console.error(e.stack);process.exitCode=1});`
	if output, err := exec.Command(node, "-e", string(data)+checks).CombinedOutput(); err != nil {
		t.Fatalf("Speed controls and request access: %s", output)
	}
	probe, err := a.Probe()
	if err != nil || probe.Status != StatusPatched || len(probe.Candidates) != 0 {
		t.Fatalf("optional-auth patch is not idempotent: %#v, %v", probe, err)
	}
}

func TestV4PatchWithOptionalAuthProviderNeedsTwoAccessRepairs(t *testing.T) {
	legacy := optionalAuthFixture()
	legacy = strings.ReplaceAll(legacy, "!await r()", "!1")
	legacy = strings.ReplaceAll(legacy, "n!==`chatgpt`&&n!==`personalAccessToken`", "!/^(chatgpt|personalAccessToken|apikey)$/.test(n)")
	legacy = strings.ReplaceAll(legacy, "i?.authMethod===`chatgpt`||i?.authMethod===`personalAccessToken`", "/^(chatgpt|personalAccessToken|apikey)$/.test(i?.authMethod)")
	path := filepath.Join(t.TempDir(), "v4.asar")
	writeFixture(t, path, []byte(legacy))
	a, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	probe, err := a.Probe()
	if err != nil || probe.Status != StatusPartial || len(probe.Candidates) != 2 {
		t.Fatalf("v4 must need the UI and request-auth repairs: %#v, %v", probe, err)
	}
}

func TestOptionalAuthReaderRejectsInconsistentAccountBinding(t *testing.T) {
	content := strings.Replace(optionalAuthFixture(), "let[s,c]=await Promise.all", "let[wrong,c]=await Promise.all", 1)
	path := filepath.Join(t.TempDir(), "app.asar")
	writeFixture(t, path, []byte(content))
	a, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Probe(); err == nil {
		t.Fatal("inconsistent account-reader bindings accepted")
	}
}

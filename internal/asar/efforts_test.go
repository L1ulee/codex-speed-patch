package asar

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Preserve the packaged model query's inputs and selector boundary. Preferences
// and model capabilities are applied by the native selector after the gate.
const effortQueryFixture = "const query=({additionalAvailableModels:e,authMethod:t,enabledReasoningEfforts:n,hasConfiguredModelCatalog:r,hostId:i,includeUltraReasoningEffort:a,isCustomModelProvider:o,limit:s,modelCatalogPath:c,modelProvider:l,teamCatalog:u},{get:d,queryClient:f,scope:p})=>{let y=n==null?d(vvo):new Set([...n,`persistent`]),b=d(svo),x=a&&d(rg,`1186680773`);return{queryKey:[`models`,i],queryFn:()=>Vh(p,i).sendRequest(`model/list`,{includeHidden:!0,cursor:null,limit:s}),select:({data:n})=>selectModels({authMethod:t,enabledReasoningEfforts:y,includeUltraReasoningEffort:x,models:n})}};"

const effortSelectorFixture = `
function selectModels({authMethod,enabledReasoningEfforts,includeUltraReasoningEffort,models}){
 return {models:models.map(model=>({...model,supportedReasoningEfforts:model.supportedReasoningEfforts.filter(e=>
  (includeUltraReasoningEffort||e.reasoningEffort!=="ultra")&&
  (authMethod!=="copilot"||e.reasoningEffort==="medium")&&enabledReasoningEfforts.has(e.reasoningEffort))}))};
}`

func writeEffortFixture(t *testing.T, path, query string) {
	t.Helper()
	writeMultiFixture(t, path, map[string][]byte{
		"webview/assets/app.js":         []byte("let a=true,u=false,c={},d=a&&!u&&c!=null&&c?.requirements?.featureRequirements?.fast_mode!==!1;" + requestFixture),
		"webview/assets/app-initial.js": []byte(query + effortSelectorFixture),
		"zz-payload.bin":                []byte("unchanged"),
	})
}

func TestAPIEffortQueryPreservesNativePreferencesAndModelCapabilities(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("Node.js is required to verify native effort controls")
	}
	source := filepath.Join(t.TempDir(), "app.asar")
	writeEffortFixture(t, source, effortQueryFixture)
	dest := filepath.Join(t.TempDir(), "patched.asar")
	if _, err := PatchCopy(source, dest); err != nil {
		t.Fatal(err)
	}
	a, err := Open(dest)
	if err != nil {
		t.Fatal(err)
	}
	var data []byte
	for _, entry := range a.Entries() {
		if entry.Path == "webview/assets/app-initial.js" {
			data, err = a.ReadEntry(entry)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	assertions := `
const assert=require("node:assert/strict");
const vvo="preferences",svo="catalog",rg="flag";
const all=["low","medium","high","xhigh","max","ultra"];
const model={model:"supported",supportedReasoningEfforts:all.map(reasoningEffort=>({reasoningEffort}))};
function options(auth="apikey",enabled=all,include=true,gate=false,info=model,custom=false){
 const get=k=>k===vvo?new Set(enabled):k===rg?gate:{};
 const q=query({authMethod:auth,enabledReasoningEfforts:enabled,includeUltraReasoningEffort:include,isCustomModelProvider:custom,hostId:"local"},{get,queryClient:{},scope:{}});
 return q.select({data:[info]}).models[0].supportedReasoningEfforts.map(e=>e.reasoningEffort);
}
assert.deepEqual(options(),all,"API model query must expose native Ultra when preferences enable it");
assert.deepEqual(options("apikey",all,false),all.slice(0,5),"explicit Ultra opt-out");
assert.deepEqual(options("apikey",all.filter(e=>e!=="max")),all.filter(e=>e!=="max"),"Max preference opt-out");
assert.deepEqual(options("apikey",all.filter(e=>e!=="ultra")),all.slice(0,5),"Ultra preference opt-out");
assert.deepEqual(options("chatgpt"),all.slice(0,5),"ChatGPT remote gate remains effective");
assert.deepEqual(options("chatgpt",all,true,true),all,"ChatGPT enabled gate");
assert.deepEqual(options("copilot"),["medium"],"Copilot effort policy remains effective");
assert.deepEqual(options("unknown"),all.slice(0,5),"unknown authentication retains its gate");
assert.deepEqual(options("apikey",all,true,false,{...model,supportedReasoningEfforts:model.supportedReasoningEfforts.slice(0,4)}),all.slice(0,4),"do not invent unsupported model efforts");
assert.deepEqual(options("apikey",["low","medium","high","xhigh","ultra"]),["low","medium","high","xhigh","ultra"],"Max remains an explicit native preference");
assert.deepEqual(options(null,all,true,false,model,true),all,"custom provider without an OpenAI login must expose Ultra");
assert.deepEqual(options(null,all,false,false,model,true),all.slice(0,5),"custom provider explicit Ultra opt-out");
assert.deepEqual(options(null,all.filter(e=>e!=="ultra"),true,false,model,true),all.slice(0,5),"custom provider Ultra preference opt-out");
assert.deepEqual(options(null,all,true,false,{...model,supportedReasoningEfforts:model.supportedReasoningEfforts.slice(0,4)},true),all.slice(0,4),"custom provider model capabilities remain effective");
assert.deepEqual(options(null),all.slice(0,5),"logged-out default provider retains its gate");
assert.deepEqual(options(null,all,true,false,model,1),all.slice(0,5),"custom provider flag must be explicitly true");
assert.deepEqual(options("unknown",all,true,false,model,true),all.slice(0,5),"custom provider does not override unknown identities");
assert.deepEqual(options("chatgpt",all,true,false,model,true),all.slice(0,5),"custom provider does not override ChatGPT gating");
assert.equal(model.supportedReasoningEfforts.length,6,"model metadata remains immutable");
`
	if output, err := exec.Command(node, "-e", string(data)+assertions).CombinedOutput(); err != nil {
		t.Fatalf("native effort query: %s", output)
	}
	probe, err := a.Probe()
	if err != nil || probe.Status != StatusPatched || len(probe.Candidates) != 0 {
		t.Fatalf("effort patch is not idempotent: %#v, %v", probe, err)
	}
}

func TestPreviousSpeedPatchNeedsEffortUpgrade(t *testing.T) {
	legacySource := filepath.Join(t.TempDir(), "legacy.asar")
	writeFixture(t, legacySource, []byte("let a=true,u=false,c={},d=a&&!u&&c!=null&&c?.requirements?.featureRequirements?.fast_mode!==!1;"+requestFixture))
	legacyDest := filepath.Join(t.TempDir(), "legacy-patched.asar")
	if _, err := PatchCopy(legacySource, legacyDest); err != nil {
		t.Fatal(err)
	}
	legacy, err := Open(legacyDest)
	if err != nil {
		t.Fatal(err)
	}
	data, err := legacy.ReadEntry(legacy.Entries()[0])
	if err != nil {
		t.Fatal(err)
	}
	for name, query := range map[string]string{
		"native gate": effortQueryFixture,
		"v5 API-only gate": strings.Replace(effortQueryFixture,
			"x=a&&d(rg,`1186680773`)", "x=a&&(t===`apikey`||d(rg,`1186680773`))", 1),
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "upgrade.asar")
			writeMultiFixture(t, path, map[string][]byte{
				"webview/assets/app.js":         data,
				"webview/assets/app-initial.js": []byte(query + effortSelectorFixture),
				"zz-payload.bin":                []byte("unchanged"),
			})
			a, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			probe, err := a.Probe()
			if err != nil || probe.Status != StatusPartial || len(probe.Candidates) != 1 {
				t.Fatalf("previous Speed patch must require exactly one effort repair: %#v, %v", probe, err)
			}
			dest := filepath.Join(t.TempDir(), "upgraded.asar")
			if _, err := PatchCopy(path, dest); err != nil {
				t.Fatal(err)
			}
			upgraded, err := Open(dest)
			if err != nil {
				t.Fatal(err)
			}
			probe, err = upgraded.Probe()
			if err != nil || probe.Status != StatusPatched || len(probe.Candidates) != 0 {
				t.Fatalf("effort upgrade is not idempotent: %#v, %v", probe, err)
			}
		})
	}
}

func TestEffortQueryWithoutCustomProviderParameterKeepsAPISupport(t *testing.T) {
	query := strings.Replace(effortQueryFixture, "isCustomModelProvider:o,", "", 1)
	entry := Entry{Path: "webview/assets/app-initial.js"}
	report, err := scanEffortRules(entry, []byte(query))
	if err != nil || len(report.edits) != 1 {
		t.Fatalf("legacy query must retain API support: %#v, %v", report, err)
	}
	edit := report.edits[0]
	patched := append([]byte(query[:edit.MatchStart]), edit.Replacement...)
	patched = append(patched, query[edit.MatchEnd:]...)
	report, err = scanEffortRules(entry, patched)
	if err != nil || report.patched != 1 || len(report.edits) != 0 {
		t.Fatalf("legacy API effort patch is not idempotent: %#v, %v", report, err)
	}
}

func TestCustomEffortGateVerifiesParameterBindings(t *testing.T) {
	complete := strings.Replace(effortQueryFixture, "x=a&&d(rg,`1186680773`)",
		"x=a&&(t===`apikey`||t==null&&o===!0||d(rg,`1186680773`))", 1)
	for name, query := range map[string]string{
		"different auth":     strings.Replace(complete, "||t==null", "||other==null", 1),
		"different provider": strings.Replace(complete, "&&o===!0", "&&other===!0", 1),
		"missing provider":   strings.Replace(complete, "isCustomModelProvider:o,", "", 1),
		"duplicate provider": strings.Replace(complete, "isCustomModelProvider:o,", "isCustomModelProvider:o,isCustomModelProvider:other,", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := scanEffortRules(Entry{Path: "webview/assets/app-initial.js"}, []byte(query)); err == nil {
				t.Fatal("inconsistent custom-provider gate accepted")
			}
		})
	}
	renamed := strings.ReplaceAll(complete, "isCustomModelProvider:o", "isCustomModelProvider:provider")
	renamed = strings.ReplaceAll(renamed, "&&o===!0", "&&provider===!0")
	report, err := scanEffortRules(Entry{Path: "webview/assets/app-initial.js"}, []byte(renamed))
	if err != nil || report.patched != 1 || len(report.edits) != 0 {
		t.Fatalf("bound custom-provider gate was not recognized: %#v, %v", report, err)
	}
}

func TestEffortQueryRejectsUnknownOrInconsistentGates(t *testing.T) {
	for name, query := range map[string]string{
		"different include parameter": strings.Replace(effortQueryFixture, "x=a&&", "x=other&&", 1),
		"different getter":            strings.Replace(effortQueryFixture, "&&d(rg,", "&&other(rg,", 1),
		"different selected gate":     strings.Replace(effortQueryFixture, "includeUltraReasoningEffort:x", "includeUltraReasoningEffort:other", 1),
		"unknown gate layout":         strings.Replace(effortQueryFixture, "x=a&&d(rg,`1186680773`)", "x=unknown()", 1),
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "app.asar")
			writeEffortFixture(t, path, query)
			a, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := a.Probe(); err == nil {
				t.Fatal("unsafe effort query accepted")
			}
		})
	}
}

func TestDuplicateNativeEffortQueriesAreRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.asar")
	writeMultiFixture(t, path, map[string][]byte{
		"webview/assets/app.js": []byte("let a=true,u=false,c={},d=a&&!u&&c!=null&&c?.requirements?.featureRequirements?.fast_mode!==!1;" + requestFixture),
		"webview/assets/a.js":   []byte(effortQueryFixture),
		"webview/assets/b.js":   []byte(effortQueryFixture),
		"zz-payload.bin":        []byte("unchanged"),
	})
	a, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Probe(); err == nil {
		t.Fatal("multiple native effort query targets were accepted")
	}
}

package asar

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Native descriptions explicitly list tier-specific variants, while older
// API fallback catalogs may still expose only priority for the base model.
const nativeTierCatalogFixture = "function nativeDescriptions(){return new Map([[`gpt-6.1-sol`,labels.sol],[`gpt-6.1-sol-ultrafast`,labels.sol],[`another-model`,labels.other],[`another-model-ultrafast`,labels.other],[`fast-only`,labels.fast]])}let descriptionId=`modelDescription.copyV1.sol61`;"

const nativeTierFunctionsFixture = "function tierKind(e,t){let n=t?.trim().toLowerCase();return e===`priority`||e===`fast`||n===`fast`?`fast`:e===`ultrafast`||n===`ultrafast`?`ultrafast`:null}" +
	"function fastTier(e){return e?.serviceTiers?.find(e=>tierKind(e.id,e.name)===`fast`)??null}" +
	"function findTier(e,t){return t==null?null:t===`fast`?fastTier(e):e?.serviceTiers?.find(e=>e.id===t)??null}" +
	"function optionsForModel(e){return buildOptions(e?.model,e?.serviceTiers)}"

func TestConfirmedUltrafastModelsWithoutNativeVariants(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("Node.js is required to verify native tier selection")
	}
	catalog := "function nativeDescriptions(){return new Map([[`gpt-6.1-sol`,labels.sol],[`gpt-6.1-sol-ultrafast`,labels.sol],[`gpt-5.6-sol`,labels.previous],[`gpt-6-astra`,labels.astra],[`gpt-6-sol`,labels.other]])}let descriptionId=`modelDescription.copyV1.sol61`;"
	source := filepath.Join(t.TempDir(), "app.asar")
	writeFixture(t, source, []byte("let a=true,u=false,c={requirements:{}},d=a&&!u&&c!=null&&c?.requirements?.featureRequirements?.fast_mode!==!1;"+requestFixture+catalog+nativeTierFunctionsFixture))
	a, err := Open(source)
	if err != nil {
		t.Fatal(err)
	}
	probe, err := a.Probe()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(probe.UltrafastModels, ","); got != "gpt-5.6-sol,gpt-6-astra,gpt-6.1-sol" {
		t.Fatalf("confirmed model capabilities not passed to the native catalog: %s", got)
	}
	dest := filepath.Join(t.TempDir(), "patched.asar")
	if _, err := PatchCopy(source, dest); err != nil {
		t.Fatal(err)
	}
	a, err = Open(dest)
	if err != nil {
		t.Fatal(err)
	}
	data, err := a.ReadEntry(a.Entries()[0])
	if err != nil {
		t.Fatal(err)
	}
	assertions := `
const assert=require("node:assert/strict");
function buildOptions(model,tiers){return [{value:null},...(tiers??[]).map(t=>({value:t.id}))]}
for(const model of ["gpt-6.1-sol","gpt-5.6-sol","gpt-6-astra"]){
 const info={model,serviceTiers:[{id:"priority",name:"Fast"}]};
 assert.deepEqual(optionsForModel(info).map(o=>o.value),[null,"priority","ultrafast"],model);
 assert.equal(findTier(info,"ultrafast").id,"ultrafast",model+" selection");
 assert.equal(findTier(info,"fast").id,"priority",model+" Fast mapping");
 assert.equal(info.serviceTiers.length,1,"source metadata is immutable");
}
for(const model of ["gpt-6-sol","gpt-5.6-sol-custom","unknown"]){
 const info={model,serviceTiers:[{id:"priority",name:"Fast"}]};
 assert.deepEqual(optionsForModel(info).map(o=>o.value),[null,"priority"],model);
 assert.equal(findTier(info,"ultrafast"),null);
}
`
	if output, err := exec.Command(node, "-e", string(data)+assertions).CombinedOutput(); err != nil {
		t.Fatalf("confirmed model tier controls: %s", output)
	}
	// Merely mentioning a confirmed model elsewhere must not modify its tiers.
	if got := nativeUltrafastModels([]byte("let x=`modelDescription.copyV1.example`,model=`gpt-5.6-sol`;")); len(got) != 0 {
		t.Fatalf("unstructured model name accepted: %v", got)
	}
}

func TestUltrafastOptionsAndSelectionUseCapabilityDeclarations(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js runtime test")
	}
	source := filepath.Join(t.TempDir(), "app.asar")
	content := "let a=true,u=false,c={requirements:{}},d=a&&!u&&c!=null&&c?.requirements?.featureRequirements?.fast_mode!==!1;" + requestFixture + nativeTierCatalogFixture + nativeTierFunctionsFixture
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
	assertions := `
const assert=require("node:assert/strict");
function buildOptions(name,tiers){return [{value:null},...(tiers??[]).map(t=>({value:t.id}))]}
const priority={id:"priority",name:"Fast"};
for(const model of ["gpt-6.1-sol","another-model"]){
 const info={model,serviceTiers:[priority]};
 assert.deepEqual(optionsForModel(info).map(o=>o.value),[null,"priority","ultrafast"],model+" available controls");
 assert.equal(findTier(info,"ultrafast").id,"ultrafast",model+" selected control");
 assert.equal(info.serviceTiers.length,1,"original model catalog must remain immutable");
}
assert.deepEqual(optionsForModel({model:"fast-only",serviceTiers:[priority]}).map(o=>o.value),[null,"priority"]);
assert.deepEqual(optionsForModel({model:"unknown",serviceTiers:[]}).map(o=>o.value),[null]);
assert.deepEqual(optionsForModel({model:"custom",serviceTiers:[priority],additionalSpeedTiers:["ultrafast"]}).map(o=>o.value),[null,"priority","ultrafast"]);
const explicit={id:"custom-ultra",name:"Ultrafast",description:"Provider-defined tier"};
const declared={model:"gpt-6.1-sol",serviceTiers:[priority,explicit]};
assert.deepEqual(optionsForModel(declared).map(o=>o.value),[null,"priority","custom-ultra"]);
assert.equal(findTier(declared,"custom-ultra"),explicit);
assert.equal(findTier({model:"fast-only",serviceTiers:[priority]},"ultrafast"),null);
`
	if output, err := exec.Command(node, "-e", string(data)+assertions).CombinedOutput(); err != nil {
		t.Fatalf("native Ultrafast model options: %s", output)
	}
}

func TestV2PatchWithUnreconciledCatalogIsPartial(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.asar")
	requests := strings.ReplaceAll(requestFixture, "!await r()", "!1")
	requests = strings.ReplaceAll(requests, "n!==`chatgpt`&&n!==`personalAccessToken`", "!/^(chatgpt|personalAccessToken|apikey)$/.test(n)")
	writeFixture(t, path, []byte("let d=!u&&(!a||!!c&&c?.requirements?.featureRequirements?.fast_mode!==!1);"+requests+nativeTierFunctionsFixture))
	a, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	probe, err := a.Probe()
	if err != nil {
		t.Fatal(err)
	}
	if probe.Status != StatusPartial {
		t.Fatalf("old tier resolver should need upgrade, got %s", probe.Status)
	}
}

func TestTierMetadataInSeparateAssetAndReprobeIsStable(t *testing.T) {
	source := filepath.Join(t.TempDir(), "app.asar")
	writeMultiFixture(t, source, map[string][]byte{
		"webview/assets/app.js":     []byte("let d=a&&!u&&c!=null&&c?.requirements?.featureRequirements?.fast_mode!==!1;" + requestFixture + nativeTierFunctionsFixture),
		"webview/assets/catalog.js": []byte(nativeTierCatalogFixture),
		"zz-payload.bin":            []byte("unchanged"),
	})
	dest := filepath.Join(t.TempDir(), "patched.asar")
	if _, err := PatchCopy(source, dest); err != nil {
		t.Fatal(err)
	}
	a, err := Open(dest)
	if err != nil {
		t.Fatal(err)
	}
	probe, err := a.Probe()
	if err != nil || probe.Status != StatusPatched || len(probe.Candidates) != 0 {
		t.Fatalf("patched catalog should not require another patch: %#v, %v", probe, err)
	}
	for _, entry := range a.Entries() {
		if entry.Path != "webview/assets/app.js" {
			continue
		}
		data, err := a.ReadEntry(entry)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), `["another-model","gpt-6.1-sol"]`) {
			t.Fatal("capability declarations in another asset were not included")
		}
	}
}

func TestUnrelatedOrMismatchedModelNamesDoNotDeclareUltrafast(t *testing.T) {
	for _, text := range []string{
		"new Map([[`model`,labels.same],[`model-ultrafast`,labels.same]])",
		"let descriptionId=`modelDescription.copyV1.example`;new Map([[`model`,labels.a],[`model-ultrafast`,labels.b]])",
		"let descriptionId=`modelDescription.copyV1.example`;let label=`model-ultrafast`;",
	} {
		if models := nativeUltrafastModels([]byte(text)); len(models) != 0 {
			t.Fatalf("unverified capability declaration accepted: %v", models)
		}
	}
}

func TestTierResolverWithDifferentModelVariableIsRejected(t *testing.T) {
	text := strings.Replace(nativeTierFunctionsFixture, "e?.serviceTiers?.find(e=>e.id===t)", "other?.serviceTiers?.find(e=>e.id===t)", 1)
	_, err := scanTierRules(Entry{Path: "unsupported.js"}, []byte(text), nil)
	if err == nil {
		t.Fatal("inconsistent tier resolver was accepted")
	}
}

func TestTierMenuAndResolverCanLiveInSeparateModules(t *testing.T) {
	source := filepath.Join(t.TempDir(), "app.asar")
	menuStart := strings.Index(nativeTierFunctionsFixture, "function optionsForModel(")
	writeMultiFixture(t, source, map[string][]byte{
		"webview/assets/app.js":         []byte("let d=a&&!u&&c!=null&&c?.requirements?.featureRequirements?.fast_mode!==!1;" + requestFixture),
		"webview/assets/app-initial.js": []byte(nativeTierFunctionsFixture[menuStart:]),
		"webview/assets/app-shared.js":  []byte(nativeTierFunctionsFixture[:menuStart]),
		"webview/assets/app-primary.js": []byte(nativeTierCatalogFixture),
		"zz-payload.bin":                []byte("unchanged"),
	})
	dest := filepath.Join(t.TempDir(), "patched.asar")
	if _, err := PatchCopy(source, dest); err != nil {
		t.Fatal(err)
	}
	a, err := Open(dest)
	if err != nil {
		t.Fatal(err)
	}
	probe, err := a.Probe()
	if err != nil || probe.Status != StatusPatched {
		t.Fatalf("split model-tier modules failed validation: %#v, %v", probe, err)
	}
	for _, entry := range a.Entries() {
		if entry.Path != "webview/assets/app-initial.js" && entry.Path != "webview/assets/app-shared.js" {
			continue
		}
		data, err := a.ReadEntry(entry)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), tierHelperMarker) {
			t.Fatalf("module has no local tier helper: %s", entry.Path)
		}
	}
}

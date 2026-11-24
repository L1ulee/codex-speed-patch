package asar

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

const catalogLaunchFixture = `let D="Codex Desktop",L="CODEX_APP_RUNTIME_CLI_PATH",B=["-c","features.code_mode_host=true"],U=[];function launch(){let e=U.flatMap(()=>[]);return 0===e.length?[...B,"app-server","--analytics-default-enabled"]:["app-server",...B,...e,"--analytics-default-enabled"]}`

func TestExternalCatalogLaunchPreservesFlagsAndSignedCLIEntryPoint(t *testing.T) {
	entry := Entry{Path: ".vite/build/app-server-host.js"}
	report, loaded, err := scanCatalogRules(entry, []byte(catalogLaunchFixture))
	if err != nil || loaded || len(report.edits) != 1 {
		t.Fatalf("launch patch: %#v, %v, %v", report, loaded, err)
	}
	edit := report.edits[0]
	patched := append([]byte(catalogLaunchFixture[:edit.MatchStart]), edit.Replacement...)
	patched = append(patched, catalogLaunchFixture[edit.MatchEnd:]...)
	again, loaded, err := scanCatalogRules(entry, patched)
	if err != nil || !loaded || len(again.edits) != 0 {
		t.Fatalf("launch patch is not idempotent: %#v, %v", again, err)
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(string(patched))
	program := `const vm=require("node:vm"),assert=require("node:assert/strict");
for(const platform of ["darwin","linux","win32"]){
 const scope={process:{platform},__dirname:"/tmp/test app/Contents/Resources/app.asar/.vite/build",require};
 vm.runInNewContext(SOURCE,scope);
 const args=Array.from(scope.launch());
 assert(args.includes("app-server")&&args.includes("--analytics-default-enabled"));
 assert(args.includes("features.code_mode_host=true"));
 const catalog=args.find(x=>x.startsWith("model_catalog_json="));
 if(platform==="darwin")assert.equal(catalog,'model_catalog_json="/tmp/test app/Contents/Resources/codex-speed-models.json"');
 else assert.equal(catalog,undefined);
 assert(!args.some(x=>x.includes("codex-cli")),"CLI executable selection remains native");
}`
	program = strings.Replace(program, "SOURCE", string(encoded), 1)
	if output, err := exec.Command(node, "-e", program).CombinedOutput(); err != nil {
		t.Fatalf("native startup arguments: %s", output)
	}
	broken := bytes.Replace(patched, []byte(CatalogFileName), []byte("other.json"), 1)
	if _, _, err := scanCatalogRules(entry, broken); err == nil {
		t.Fatal("inconsistent external-catalog loader was accepted")
	}
}

func TestExternalCatalogLaunchRejectsAmbiguousOrUnboundArguments(t *testing.T) {
	for _, text := range []string{
		catalogLaunchFixture + `;let copy=["-c","features.code_mode_host=true"];`,
		strings.Replace(catalogLaunchFixture, `...B,"app-server"`, `...other,"app-server"`, 1),
	} {
		if _, _, err := scanCatalogRules(Entry{Path: ".vite/build/app-server-host.js"}, []byte(text)); err == nil {
			t.Fatal("ambiguous startup arguments were accepted")
		}
	}
}

func TestSignedWorkerIsSelectedOnlyForMacAppServer(t *testing.T) {
	fixture := `let workers={git:{entry:"worker.js",serviceName:"Git worker"},"app-server-host":{entry:"app-server-host.js",serviceName:"App server host"}};var spawnWorker=function(e){let{entry:t,serviceName:r}=workers[e];return electron.utilityProcess.fork((0,paths.join)(__dirname,t),[],{serviceName:r})}`
	report, signed, err := scanWorkerRules(Entry{Path: ".vite/build/bootstrap.js"}, []byte(fixture))
	if err != nil || signed || len(report.edits) != 1 {
		t.Fatalf("signed utility launch: %#v, %v", report, err)
	}
	edit := report.edits[0]
	patched := append([]byte(fixture[:edit.MatchStart]), edit.Replacement...)
	patched = append(patched, fixture[edit.MatchEnd:]...)
	if _, signed, err := scanWorkerRules(Entry{}, patched); err != nil || !signed {
		t.Fatalf("patched utility launch not recognized: %v", err)
	}
	wrong := bytes.Replace(patched, []byte(`"app-server-host"===e`), []byte(`"app-server-host"===other`), 1)
	if _, _, err := scanWorkerRules(Entry{}, wrong); err == nil {
		t.Fatal("signed worker flag bound to a different parameter was accepted")
	}
	encoded, _ := json.Marshal(string(patched))
	program := `const vm=require("node:vm"),assert=require("node:assert/strict");
for(const platform of ["darwin","linux","win32"]){
 const switches=[];
 const electron={app:{commandLine:{appendSwitch:(key,value)=>switches.push({key,value})}},utilityProcess:{fork:(path,args,options)=>({path,args,options})}};
 const scope={process:{platform,resourcesPath:"/test app/Contents/Resources"},__dirname:"/runtime",paths:require("node:path"),electron,require:id=>id==="electron"?electron:require(id)};
 vm.runInNewContext(SOURCE,scope);
 for(const kind of ["git","app-server-host"]){
  const result=scope.spawnWorker(kind);
  assert.deepEqual(Object.keys(result.options),["serviceName"],"only Owl-supported fork options are supplied");
  assert.equal(switches.length,platform==="darwin"&&kind==="app-server-host"?1:0);
  assert(result.path.endsWith(kind==="git"?"worker.js":"app-server-host.js"));
 }
 if(platform==="darwin"){
  assert.equal(switches[0].key,"browser-subprocess-path");
  assert.equal(switches[0].value,"/test app/Contents/Frameworks/Codex Framework.framework/Helpers/Codex (Service).app/Contents/MacOS/Codex (Service)");
 }
}`
	program = strings.Replace(program, "SOURCE", string(encoded), 1)
	if output, err := exec.Command("node", "-e", program).CombinedOutput(); err != nil {
		t.Fatalf("signed utility selection: %s", output)
	}
}

package asar

import (
	"bytes"
	"fmt"
	"regexp"
)

const CatalogFileName = "codex-speed-models.json"
const catalogLaunchMarker = "/*codex-speed-external-catalog-v10*/"
const signedWorkerMarker = "/*codex-speed-signed-worker-v10*/"
const signedWorkerEnd = "/*codex-speed-signed-worker-end*/"

var catalogLaunchArguments = regexp.MustCompile(`(` + identifier + `)=\["-c","features\.code_mode_host=true"\]`)
var workerLaunch = regexp.MustCompile(`function\((` + identifier + `)\)\{let\{entry:(` + identifier + `),serviceName:(` + identifier + `)\}=(` + identifier + `)\[(` + identifier + `)\];return ` + identifier + `\.utilityProcess\.fork\(\(0,` + identifier + `\.join\)\(__dirname,(` + identifier + `)\),\[\],\{serviceName:(` + identifier + `)\}\)\}`)

// Owl rejects Electron's allowLoadingUnsignedLibraries option. Select the
// official service executable through Chromium's subprocess path instead.
func scanWorkerRules(entry Entry, data []byte) (ruleReport, bool, error) {
	var report ruleReport
	if !bytes.Contains(data, []byte(`"app-server-host":{entry:"app-server-host.js",serviceName:"App server host"}`)) {
		return report, false, nil
	}
	if bytes.Contains(data, []byte(signedWorkerMarker)) {
		start := bytes.Index(data, []byte(signedWorkerMarker))
		end := bytes.Index(data, []byte(signedWorkerEnd))
		if start < 0 || end < start || bytes.Count(data, []byte(signedWorkerMarker)) != 1 || bytes.Count(data, []byte(signedWorkerEnd)) != 1 {
			return report, false, fmt.Errorf("signed app-server worker binding differs in %s", entry.Path)
		}
		end += len(signedWorkerEnd)
		native := append(append([]byte(nil), data[:start]...), data[end:]...)
		nativeReport, _, err := scanWorkerRules(entry, native)
		if err != nil || len(nativeReport.edits) != 1 {
			return report, false, fmt.Errorf("signed worker parameters differ in %s", entry.Path)
		}
		edit := nativeReport.edits[0]
		expected := append(append(append([]byte(nil), native[:edit.MatchStart]...), edit.Replacement...), native[edit.MatchEnd:]...)
		if !bytes.Equal(data, expected) {
			return report, false, fmt.Errorf("signed worker selection differs in %s", entry.Path)
		}
		report.patched = 1
		return report, true, nil
	}
	matches := workerLaunch.FindAllSubmatchIndex(data, -1)
	if len(matches) != 1 {
		return report, false, fmt.Errorf("app-server utility launch is ambiguous in %s", entry.Path)
	}
	m := matches[0]
	value := func(group int) []byte { return data[m[group*2]:m[group*2+1]] }
	if !bytes.Equal(value(1), value(5)) || !bytes.Equal(value(2), value(6)) || !bytes.Equal(value(3), value(7)) {
		return report, false, fmt.Errorf("app-server utility parameters differ in %s", entry.Path)
	}
	region := data[m[0]:m[1]]
	start := bytes.IndexByte(region, '{') + 1
	selection := signedWorkerMarker + `if("darwin"===process.platform&&"app-server-host"===` + string(value(1)) + `)require("electron").app.commandLine.appendSwitch("browser-subprocess-path",require("node:path").join(process.resourcesPath,"../Frameworks/Codex Framework.framework/Helpers/Codex (Service).app/Contents/MacOS/Codex (Service)"));` + signedWorkerEnd
	report.add(entry, data, m[0], m[1], string(region[:start])+selection+string(region[start:]))
	return report, false, nil
}

// The app-server host must keep launching its signed CLI directly. Supply the
// model data through the CLI's supported config flag instead of changing it.
func scanCatalogRules(entry Entry, data []byte) (ruleReport, bool, error) {
	var report ruleReport
	if entry.Path != ".vite/build/app-server-host.js" {
		return report, false, nil
	}
	if !bytes.Contains(data, []byte(`"CODEX_APP_RUNTIME_CLI_PATH"`)) {
		return report, false, nil
	}
	if !bytes.Contains(data, []byte(`"--analytics-default-enabled"`)) {
		return report, false, fmt.Errorf("unsupported app-server catalog launch in %s", entry.Path)
	}
	addition := catalogLaunchMarker + `...("darwin"===process.platform?["-c","model_catalog_json="+JSON.stringify(require("node:path").resolve(__dirname,"../../../` + CatalogFileName + `"))]:[])`
	if bytes.Contains(data, []byte(catalogLaunchMarker)) {
		if bytes.Count(data, []byte(addition)) != 1 || bytes.Count(data, []byte(catalogLaunchMarker)) != 1 {
			return report, false, fmt.Errorf("external catalog launch differs in %s", entry.Path)
		}
		report.patched = 1
		return report, true, nil
	}
	matches := catalogLaunchArguments.FindAllSubmatchIndex(data, -1)
	if len(matches) != 1 {
		return report, false, fmt.Errorf("app-server catalog argument binding is ambiguous in %s", entry.Path)
	}
	m := matches[0]
	name := string(data[m[2]:m[3]])
	if !bytes.Contains(data, []byte(`...`+name+`,"app-server"`)) || !bytes.Contains(data, []byte(`"app-server",...`+name)) {
		return report, false, fmt.Errorf("app-server does not use the matched startup arguments in %s", entry.Path)
	}
	report.add(entry, data, m[0], m[1], string(data[m[0]:m[1]-1])+","+addition+"]")
	return report, false, nil
}

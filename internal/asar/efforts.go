package asar

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
)

var (
	effortQueryHeader = regexp.MustCompile(`\(\{([^{};]+)\},\{get:(` + identifier + `),queryClient:(` + identifier + `),scope:(` + identifier + `)\}\)=>\{`)
	effortGate        = regexp.MustCompile("(" + identifier + ")=(" + identifier + ")&&(" + identifier + ")\\((" + identifier + "),`1186680773`\\)")
	effortGatePatched = regexp.MustCompile("(" + identifier + ")=(" + identifier + ")&&\\((" + identifier + ")===`apikey`\\|\\|(" + identifier + ")\\((" + identifier + "),`1186680773`\\)\\)")
	effortGateCustom  = regexp.MustCompile("(" + identifier + ")=(" + identifier + ")&&\\((" + identifier + ")===`apikey`\\|\\|(" + identifier + ")==null&&(" + identifier + ")===!0\\|\\|(" + identifier + ")\\((" + identifier + "),`1186680773`\\)\\)")
)

func effortParameter(fields []byte, name string) string {
	pattern := regexp.MustCompile(`(?:^|,)` + regexp.QuoteMeta(name) + `:(` + identifier + `)(?:,|$)`)
	matches := pattern.FindAllSubmatch(fields, -1)
	if len(matches) != 1 {
		return ""
	}
	return string(matches[0][1])
}

// Restore native Ultra eligibility for API authentication and custom providers
// without an OpenAI identity. The native selector still applies the caller's
// include flag, preferences and model capabilities; other identities keep their gate.
func scanEffortRules(entry Entry, data []byte) (ruleReport, error) {
	var report ruleReport
	if strings.HasPrefix(entry.Path, ".vite/") || !bytes.Contains(data, []byte("includeUltraReasoningEffort")) {
		return report, nil
	}
	for _, header := range effortQueryHeader.FindAllSubmatchIndex(data, -1) {
		fields := data[header[2]:header[3]]
		auth := effortParameter(fields, "authMethod")
		include := effortParameter(fields, "includeUltraReasoningEffort")
		custom := effortParameter(fields, "isCustomModelProvider")
		if auth == "" || include == "" || effortParameter(fields, "enabledReasoningEfforts") == "" {
			continue
		}
		end := nextDeclaration(data, header[1])
		region := data[header[1]:end]
		if !bytes.Contains(region, []byte("sendRequest(`model/list`")) {
			continue
		}
		if len(region) > 10000 {
			return report, fmt.Errorf("unsupported native effort query layout in %s", entry.Path)
		}
		if bytes.Contains(fields, []byte("isCustomModelProvider:")) &&
			(custom == "" || bytes.Count(fields, []byte("isCustomModelProvider:")) != 1) {
			return report, fmt.Errorf("native effort custom-provider parameter is ambiguous in %s", entry.Path)
		}
		report.effortQueries++
		if report.effortQueries > 1 {
			return report, fmt.Errorf("multiple native effort queries in %s", entry.Path)
		}
		original := effortGate.FindAllSubmatchIndex(region, -1)
		patched := effortGatePatched.FindAllSubmatchIndex(region, -1)
		complete := effortGateCustom.FindAllSubmatchIndex(region, -1)
		if len(original)+len(patched)+len(complete) != 1 {
			return report, fmt.Errorf("native Ultra reasoning gate missing or ambiguous in %s", entry.Path)
		}
		var match []int
		if len(original) == 1 {
			match = original[0]
		} else if len(patched) == 1 {
			match = patched[0]
		} else {
			match = complete[0]
		}
		value := func(group int) string { return string(region[match[group*2]:match[group*2+1]]) }
		getterGroup := 3
		if len(patched) == 1 {
			getterGroup = 4
			if value(3) != auth {
				return report, fmt.Errorf("native effort gate authentication variable differs in %s", entry.Path)
			}
		} else if len(complete) == 1 {
			getterGroup = 6
			if value(3) != auth || value(4) != auth || custom == "" || value(5) != custom {
				return report, fmt.Errorf("native effort custom-provider gate variables differ in %s", entry.Path)
			}
		}
		getter := string(data[header[4]:header[5]])
		if value(2) != include || value(getterGroup) != getter {
			return report, fmt.Errorf("native effort gate input variables differ in %s", entry.Path)
		}
		selected := regexp.MustCompile(`includeUltraReasoningEffort:` + regexp.QuoteMeta(value(1)) + `(?:,|\})`)
		if !selected.Match(region) {
			return report, fmt.Errorf("native effort selector does not use the matched gate in %s", entry.Path)
		}
		if len(complete) == 1 || len(patched) == 1 && custom == "" {
			report.patched++
			continue
		}
		access := fmt.Sprintf("%s===`apikey`", auth)
		if custom != "" {
			access += fmt.Sprintf("||%s==null&&%s===!0", auth, custom)
		}
		replacement := fmt.Sprintf("%s=%s&&(%s||%s(%s,`1186680773`))", value(1), include, access, getter, value(getterGroup+1))
		report.add(entry, data, header[1]+match[0], header[1]+match[1], replacement)
	}
	return report, nil
}

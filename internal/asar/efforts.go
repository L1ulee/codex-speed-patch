package asar

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
)

const effortFeature = identifier + `(?:\.` + identifier + `)?`
const effortFlag = "(?:\"1186680773\"|`1186680773`)"
const effortAPI = "(?:\"apikey\"|`apikey`)"

var (
	effortQueryHeader      = regexp.MustCompile(`\(\{([^{};]+)\},\{get:(` + identifier + `),queryClient:(` + identifier + `),scope:(` + identifier + `)\}\)=>\{`)
	effortGate             = regexp.MustCompile("(" + identifier + ")=(" + identifier + ")&&(" + identifier + ")\\((" + effortFeature + ")," + effortFlag + "\\)")
	effortGatePatched      = regexp.MustCompile("(" + identifier + ")=(" + identifier + ")&&\\((" + identifier + ")===" + effortAPI + "\\|\\|(" + identifier + ")\\((" + effortFeature + ")," + effortFlag + "\\)\\)")
	effortGateCustom       = regexp.MustCompile("(" + identifier + ")=(" + identifier + ")&&\\((" + identifier + ")===" + effortAPI + "\\|\\|(" + identifier + ")==null&&(" + identifier + ")===!0\\|\\|(" + identifier + ")\\((" + effortFeature + ")," + effortFlag + "\\)\\)")
	selectedCustomProvider = regexp.MustCompile(`isCustomModelProvider:(` + identifier + `)(?:,|\})`)
)

func effortParameter(fields []byte, name string) string {
	if bytes.Count(fields, []byte(name+":")) != 1 {
		return ""
	}
	pattern := regexp.MustCompile(`(?:^|,)` + regexp.QuoteMeta(name) + `:(` + identifier + `)(?:=![01])?(?:,|$)`)
	match := pattern.FindSubmatch(fields)
	if match == nil {
		return ""
	}
	return string(match[1])
}

// Keep provider detection in the native query. Newer queries derive it from
// the cached config instead of accepting it as an argument.
func computedEffortProvider(region []byte) (string, error) {
	matches := selectedCustomProvider.FindAllSubmatch(region, -1)
	if len(matches) == 0 {
		return "", nil
	}
	if len(matches) != 1 {
		return "", fmt.Errorf("native effort provider selector is ambiguous")
	}
	custom := string(matches[0][1])
	binding := regexp.MustCompile(regexp.QuoteMeta(custom) + `=\(0,` + identifier + `\.cc\)\((` + identifier + `)(?:,(` + identifier + `))?\)`)
	bindings := binding.FindAllSubmatch(region, -1)
	if len(bindings) != 1 {
		return "", fmt.Errorf("native effort provider has no unique config binding")
	}
	config := string(bindings[0][1])
	if !bytes.Contains(region, []byte(config+"?.model_provider")) && !bytes.Contains(region, []byte(config+".model_provider")) {
		return "", fmt.Errorf("native effort provider does not use the query config")
	}
	if len(bindings[0][2]) > 0 {
		// The override belongs to this query only when model/list receives the
		// same value. Preserve the native detector and both of its arguments.
		provider := regexp.MustCompile(`modelProvider:(` + identifier + `)(?:,|\})`)
		requests := provider.FindAllSubmatch(region, -1)
		if len(requests) != 1 || !bytes.Equal(requests[0][1], bindings[0][2]) {
			return "", fmt.Errorf("native effort provider override differs from the model request")
		}
	}
	return custom, nil
}

// Restore the API/custom-provider path through the native Ultra gate while
// leaving include flags, preferences and model capabilities in its selector.
func scanEffortRules(entry Entry, data []byte) (ruleReport, error) {
	var report ruleReport
	if strings.HasPrefix(entry.Path, ".vite/") || !bytes.Contains(data, []byte("includeUltraReasoningEffort")) {
		return report, nil
	}
	for _, header := range effortQueryHeader.FindAllSubmatchIndex(data, -1) {
		fields := data[header[2]:header[3]]
		if !bytes.Contains(fields, []byte("authMethod:")) || !bytes.Contains(fields, []byte("enabledReasoningEfforts:")) ||
			!bytes.Contains(fields, []byte("includeUltraReasoningEffort:")) {
			continue
		}
		auth := effortParameter(fields, "authMethod")
		include := effortParameter(fields, "includeUltraReasoningEffort")
		if auth == "" || include == "" || effortParameter(fields, "enabledReasoningEfforts") == "" {
			return report, fmt.Errorf("unsupported native effort parameters in %s", entry.Path)
		}
		end, ok := javascriptBlockEnd(data, header[1]-1, min(len(data), header[1]+10000), 0)
		if !ok {
			return report, fmt.Errorf("unsupported native effort query layout in %s", entry.Path)
		}
		region := data[header[1]:end]
		if !bytes.Contains(region, []byte("sendRequest(`model/list`")) && !bytes.Contains(region, []byte(`sendRequest("model/list"`)) {
			continue
		}
		custom := effortParameter(fields, "isCustomModelProvider")
		if bytes.Contains(fields, []byte("isCustomModelProvider:")) {
			if custom == "" {
				return report, fmt.Errorf("native effort custom-provider parameter is ambiguous in %s", entry.Path)
			}
		} else {
			var err error
			custom, err = computedEffortProvider(region)
			if err != nil {
				return report, fmt.Errorf("%w in %s", err, entry.Path)
			}
		}
		report.effortQueries++
		if report.effortQueries > 1 {
			return report, fmt.Errorf("multiple native effort queries in %s", entry.Path)
		}
		report.effortModule = rspackScopeAt(data, header[0]).id
		original := effortGate.FindAllSubmatchIndex(region, -1)
		patched := effortGatePatched.FindAllSubmatchIndex(region, -1)
		complete := effortGateCustom.FindAllSubmatchIndex(region, -1)
		if len(original)+len(patched)+len(complete) != 1 {
			return report, fmt.Errorf("native Ultra reasoning gate missing or ambiguous in %s", entry.Path)
		}
		var match []int
		getterGroup := 3
		switch {
		case len(original) == 1:
			match = original[0]
		case len(patched) == 1:
			match, getterGroup = patched[0], 4
		default:
			match, getterGroup = complete[0], 6
		}
		value := func(group int) string { return string(region[match[group*2]:match[group*2+1]]) }
		if len(patched) == 1 && value(3) != auth ||
			len(complete) == 1 && (value(3) != auth || value(4) != auth || custom == "" || value(5) != custom) {
			return report, fmt.Errorf("native effort authentication variables differ in %s", entry.Path)
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

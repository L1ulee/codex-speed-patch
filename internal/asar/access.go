package asar

import (
	"bytes"
	"fmt"
	"regexp"
)

var (
	optionalSpeedAccess     = regexp.MustCompile(`\(` + accessAuthPatched.String() + `\|\|(` + identifier + `)\?\.authMethod==null&&(` + identifier + `)\?\.requiresAuth===!1\)`)
	optionalSpeedAccessJSON = regexp.MustCompile(`\(` + accessAuthJSONPatched.String() + `\|\|(` + identifier + `)\?\.authMethod==null&&(` + identifier + `)\?\.requiresAuth===!1\)`)
	accountBinding          = regexp.MustCompile(`\[(` + identifier + `),(` + identifier + `)\]=await Promise\.all\(\[[^;]{0,200}?\.getAccount\(`)
	accountAuthReturn       = regexp.MustCompile("(" + identifier + ")===`personalAccessToken`\\?(" + identifier + "):(" + identifier + ")\\((" + identifier + ")\\.account\\)")
	optionalAuthReturn      = regexp.MustCompile("(" + identifier + ")===`personalAccessToken`\\?(" + identifier + "):\\((" + identifier + ")\\.account==null&&(" + identifier + ")\\.requiresOpenaiAuth===!1\\?`apikey`:(" + identifier + ")\\((" + identifier + ")\\.account\\)\\)")
	accessAuthJSON          = regexp.MustCompile(`(` + identifier + `)\?\.authMethod===\"chatgpt\"\|\|(` + identifier + `)\?\.authMethod===\"personalAccessToken\"\|\|(` + identifier + `)\?\.authMethod===\"amazonBedrock\"`)
	accessAuthJSONPatched   = regexp.MustCompile(`/\^\(chatgpt\|personalAccessToken\|apikey\|amazonBedrock\)\$/\.test\((` + identifier + `)\?\.authMethod\)`)
)

func scanSpeedAccess(entry Entry, data []byte) (ruleReport, error) {
	var report ruleReport
	seen := make(map[int]bool)
	for _, marker := range regexp.MustCompile(`serviceTierAccess:`).FindAllIndex(data, -1) {
		start, end, ok := enclosingFunction(data, marker[0], false)
		if !ok || seen[start] {
			continue
		}
		seen[start] = true
		local, err := scanSpeedAccessRegion(entry, data, start, end)
		if err != nil {
			return report, err
		}
		report.ui += local.ui
		report.patched += local.patched
		report.edits = append(report.edits, local.edits...)
	}
	return report, nil
}

func scanSpeedAccessRegion(entry Entry, data []byte, start, end int) (ruleReport, error) {
	var report ruleReport
	region := data[start:end]
	original := accessAuth.FindAllSubmatchIndex(region, -1)
	originalJSON := accessAuthJSON.FindAllSubmatchIndex(region, -1)
	partial := accessAuthPatched.FindAllSubmatchIndex(region, -1)
	partialJSON := accessAuthJSONPatched.FindAllSubmatchIndex(region, -1)
	complete := optionalSpeedAccess.FindAllSubmatchIndex(region, -1)
	completeJSON := optionalSpeedAccessJSON.FindAllSubmatchIndex(region, -1)
	if len(original)+len(originalJSON)+len(partial)+len(partialJSON) > 1 || len(complete)+len(completeJSON) > 1 {
		return report, fmt.Errorf("multiple speed-access auth checks in %s", entry.Path)
	}
	if len(complete)+len(completeJSON) == 1 {
		var m []int
		partialMatch := partial
		if len(completeJSON) == 1 {
			m = completeJSON[0]
			partialMatch = partialJSON
		} else {
			m = complete[0]
		}
		if len(partialMatch) != 1 || partialMatch[0][0] < m[0] || partialMatch[0][1] > m[1] ||
			!bytes.Equal(region[m[2]:m[3]], region[m[4]:m[5]]) || !bytes.Equal(region[m[2]:m[3]], region[m[6]:m[7]]) {
			return report, fmt.Errorf("optional-auth Speed access variables differ in %s", entry.Path)
		}
		report.ui, report.patched = 1, 1
		return report, nil
	}
	var match []int
	if len(originalJSON) == 1 {
		match = originalJSON[0]
		if !bytes.Equal(region[match[2]:match[3]], region[match[4]:match[5]]) ||
			!bytes.Equal(region[match[2]:match[3]], region[match[6]:match[7]]) {
			return report, fmt.Errorf("speed-access variables differ in %s", entry.Path)
		}
	} else if len(original) == 1 {
		match = original[0]
		if !bytes.Equal(region[match[2]:match[3]], region[match[4]:match[5]]) {
			return report, fmt.Errorf("speed-access variables differ in %s", entry.Path)
		}
	} else if len(partialJSON) == 1 {
		match = partialJSON[0]
	} else if len(partial) == 1 {
		match = partial[0]
	} else {
		return report, nil
	}
	value := string(region[match[2]:match[3]])
	pattern := "chatgpt|personalAccessToken|apikey"
	if len(originalJSON)+len(partialJSON) == 1 {
		pattern += "|amazonBedrock"
	}
	replacement := fmt.Sprintf("(/^(%s)$/.test(%s?.authMethod)||%s?.authMethod==null&&%s?.requiresAuth===!1)", pattern, value, value, value)
	report.ui = 1
	report.add(entry, data, start+match[0], start+match[1], replacement)
	return report, nil
}

// This authentication reader belongs only to the Speed request policy. Treat
// an explicitly optional OpenAI account as API access without changing the
// app's global login state or bypassing config-requirements policy checks.
func scanSpeedAccountReader(entry Entry, data, policy []byte, scopeStart, scopeEnd int) (ruleReport, error) {
	var report ruleReport
	call := awaitCall.FindSubmatch(policy)
	if call == nil {
		return report, nil
	}
	declaration := []byte("async function " + string(call[1]) + "(")
	if scopeStart < 0 || scopeEnd > len(data) || scopeStart >= scopeEnd {
		scopeStart, scopeEnd = 0, len(data)
	}
	scope := data[scopeStart:scopeEnd]
	if bytes.Count(scope, declaration) != 1 {
		return report, nil
	}
	start := scopeStart + bytes.Index(scope, declaration)
	regionEnd := nextDeclaration(data, start+len(declaration))
	if regionEnd > scopeEnd {
		regionEnd = scopeEnd
	}
	region := data[start:regionEnd]
	if !bytes.Contains(region, []byte(".getAccount(")) {
		return report, nil
	}
	bindings := accountBinding.FindAllSubmatch(region, -1)
	original := accountAuthReturn.FindAllSubmatchIndex(region, -1)
	complete := optionalAuthReturn.FindAllSubmatchIndex(region, -1)
	if len(bindings) != 1 || len(original)+len(complete) != 1 {
		return report, fmt.Errorf("unsupported Speed account-reader layout in %s", entry.Path)
	}
	var match []int
	if len(complete) == 1 {
		match = complete[0]
	} else {
		match = original[0]
	}
	value := func(group int) string { return string(region[match[group*2]:match[group*2+1]]) }
	account, method := string(bindings[0][1]), string(bindings[0][2])
	if value(1) != method || value(2) != method {
		return report, fmt.Errorf("Speed account-reader method variable differs in %s", entry.Path)
	}
	if len(complete) == 1 {
		if value(3) != account || value(4) != account || value(6) != account {
			return report, fmt.Errorf("optional Speed account-reader result variable differs in %s", entry.Path)
		}
		report.patched = 1
		return report, nil
	}
	if value(4) != account {
		return report, fmt.Errorf("Speed account-reader result variable differs in %s", entry.Path)
	}
	replacement := fmt.Sprintf("%s===`personalAccessToken`?%s:(%s.account==null&&%s.requiresOpenaiAuth===!1?`apikey`:%s(%s.account))", method, method, account, account, value(3), account)
	report.add(entry, data, start+match[0], start+match[1], replacement)
	return report, nil
}

var (
	accountNullOriginal = regexp.MustCompile(`(?:var|let|const) (` + identifier + `)=(` + identifier + `)\.account;if\(null==(` + identifier + `)\)return null;`)
	accountNullPatched  = regexp.MustCompile(`(?:var|let|const) (` + identifier + `)=(` + identifier + `)\.account;if\(null==(` + identifier + `)\)return (` + identifier + `)\.requiresOpenaiAuth===!1\?"apikey":null;`)
)

// Follow the identity export imported by the Speed policy. Other account
// readers, including copies with an unrelated module ID, remain outside it.
func scanRspackAccountReaders(entry Entry, data []byte, references map[rspackReference]bool) (ruleReport, error) {
	var report ruleReport
	seen := make(map[rspackReference]bool)
	for _, module := range rspackScopes(data) {
		for reference := range references {
			if reference.module != module.id {
				continue
			}
			if seen[reference] {
				return report, fmt.Errorf("duplicate Speed identity module in %s", entry.Path)
			}
			seen[reference] = true
			region := data[module.start:module.end]
			export := regexp.MustCompile(`(?:\{|,)` + regexp.QuoteMeta(reference.export) + `:async function\(`)
			namedExport := regexp.MustCompile(`(?:\{|,)` + regexp.QuoteMeta(reference.export) + `:\(\)=>(` + identifier + `)`)
			if len(export.FindAllIndex(region, -1))+len(namedExport.FindAllIndex(region, -1)) != 1 ||
				bytes.Count(region, []byte(".getAccount(")) != 1 ||
				!bytes.Contains(region, []byte("personalAccessToken")) {
				return report, fmt.Errorf("unsupported Speed identity export in %s", entry.Path)
			}
			bindings := accountBinding.FindAllSubmatch(region, -1)
			original := accountNullOriginal.FindAllSubmatchIndex(region, -1)
			patched := accountNullPatched.FindAllSubmatchIndex(region, -1)
			if len(bindings) != 1 || len(original)+len(patched) != 1 {
				return report, fmt.Errorf("unsupported Speed identity account binding in %s", entry.Path)
			}
			var match []int
			if len(original) == 1 {
				match = original[0]
			} else {
				match = patched[0]
			}
			value := func(group int) []byte { return region[match[group*2]:match[group*2+1]] }
			if !bytes.Equal(value(1), value(3)) || !bytes.Equal(value(2), bindings[0][1]) ||
				len(patched) == 1 && !bytes.Equal(value(2), value(4)) {
				return report, fmt.Errorf("Speed identity result variable differs in %s", entry.Path)
			}
			references[reference] = true
			if len(patched) == 1 {
				report.patched++
				continue
			}
			originalText := region[match[0]:match[1]]
			replacement := string(bytes.TrimSuffix(originalText, []byte("null;"))) +
				string(value(2)) + `.requiresOpenaiAuth===!1?"apikey":null;`
			report.add(entry, data, module.start+match[0], module.start+match[1], replacement)
		}
	}
	return report, nil
}

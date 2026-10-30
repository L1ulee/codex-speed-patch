package asar

import (
	"bytes"
	"fmt"
	"regexp"
)

var (
	optionalSpeedAccess = regexp.MustCompile(`\(` + accessAuthPatched.String() + `\|\|(` + identifier + `)\?\.authMethod==null&&(` + identifier + `)\?\.requiresAuth===!1\)`)
	accountBinding      = regexp.MustCompile(`\[(` + identifier + `),(` + identifier + `)\]=await Promise\.all\(\[[^;]{0,200}?\.getAccount\(`)
	accountAuthReturn   = regexp.MustCompile("(" + identifier + ")===`personalAccessToken`\\?(" + identifier + "):(" + identifier + ")\\((" + identifier + ")\\.account\\)")
	optionalAuthReturn  = regexp.MustCompile("(" + identifier + ")===`personalAccessToken`\\?(" + identifier + "):\\((" + identifier + ")\\.account==null&&(" + identifier + ")\\.requiresOpenaiAuth===!1\\?`apikey`:(" + identifier + ")\\((" + identifier + ")\\.account\\)\\)")
)

func scanSpeedAccess(entry Entry, data []byte) (ruleReport, error) {
	var report ruleReport
	i := bytes.Index(data, []byte("serviceTierAccess:"))
	if i < 0 {
		return report, nil
	}
	start := bytes.LastIndex(data[:i], []byte("function "))
	if start < 0 {
		return report, nil
	}
	region := data[start:nextDeclaration(data, i)]
	original := accessAuth.FindAllSubmatchIndex(region, -1)
	partial := accessAuthPatched.FindAllSubmatchIndex(region, -1)
	complete := optionalSpeedAccess.FindAllSubmatchIndex(region, -1)
	if len(original)+len(partial) > 1 || len(complete) > 1 {
		return report, fmt.Errorf("multiple speed-access auth checks in %s", entry.Path)
	}
	if len(complete) == 1 {
		m := complete[0]
		if len(partial) != 1 || partial[0][0] < m[0] || partial[0][1] > m[1] ||
			!bytes.Equal(region[m[2]:m[3]], region[m[4]:m[5]]) || !bytes.Equal(region[m[2]:m[3]], region[m[6]:m[7]]) {
			return report, fmt.Errorf("optional-auth Speed access variables differ in %s", entry.Path)
		}
		report.ui, report.patched = 1, 1
		return report, nil
	}
	var match []int
	if len(original) == 1 {
		match = original[0]
		if !bytes.Equal(region[match[2]:match[3]], region[match[4]:match[5]]) {
			return report, fmt.Errorf("speed-access variables differ in %s", entry.Path)
		}
	} else if len(partial) == 1 {
		match = partial[0]
	} else {
		return report, nil
	}
	value := string(region[match[2]:match[3]])
	replacement := fmt.Sprintf("(/^(chatgpt|personalAccessToken|apikey)$/.test(%s?.authMethod)||%s?.authMethod==null&&%s?.requiresAuth===!1)", value, value, value)
	report.ui = 1
	report.add(entry, data, start+match[0], start+match[1], replacement)
	return report, nil
}

// This authentication reader belongs only to the Speed request policy. Treat
// an explicitly optional OpenAI account as API access without changing the
// app's global login state or bypassing config-requirements policy checks.
func scanSpeedAccountReader(entry Entry, data, policy []byte) (ruleReport, error) {
	var report ruleReport
	call := awaitCall.FindSubmatch(policy)
	if call == nil {
		return report, nil
	}
	declaration := []byte("async function " + string(call[1]) + "(")
	if bytes.Count(data, declaration) != 1 {
		return report, nil
	}
	start := bytes.Index(data, declaration)
	region := data[start:nextDeclaration(data, start+len(declaration))]
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

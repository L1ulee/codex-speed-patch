package asar

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
)

const tierHelperName = "__codexSpeedPatchModelTiersV3"
const tierHelperMarker = "/*codex-speed-model-tiers-v3*/"

var (
	ultrafastVariant   = regexp.MustCompile("\\[`([^`]+)-ultrafast`,(" + identifier + `\.` + identifier + `)\]`)
	modelDescription   = regexp.MustCompile("\\[`([^`]+)`," + identifier + `\.` + identifier + `\]`)
	tierOptions        = regexp.MustCompile(`function (` + identifier + `)\((` + identifier + `)\)\{return (` + identifier + `)\((` + identifier + `)\?\.model,(` + identifier + `)\?\.serviceTiers\)\}`)
	tierOptionsPatched = regexp.MustCompile(`function (` + identifier + `)\((` + identifier + `)\)\{return (` + identifier + `)\((` + identifier + `)\?\.model,` + tierHelperName + `\((` + identifier + `)\)\)\}`)
)

// Prefer paired native tier variants. These three operator-confirmed models
// also support Ultrafast when the packaged description map omits the variant.
// Require a structured base entry; never infer support from a model family.
func nativeUltrafastModels(data []byte) []string {
	if !bytes.Contains(data, []byte("modelDescription.copyV")) {
		return nil
	}
	var result []string
	for _, match := range ultrafastVariant.FindAllSubmatch(data, -1) {
		base := "[`" + string(match[1]) + "`," + string(match[2]) + "]"
		if bytes.Contains(data, []byte(base)) {
			result = append(result, string(match[1]))
		}
	}
	for _, match := range modelDescription.FindAllSubmatch(data, -1) {
		switch model := string(match[1]); model {
		case "gpt-6.1-sol", "gpt-5.6-sol", "gpt-6-astra":
			result = append(result, model)
		}
	}
	return sortedModels(result)
}

func sortedModels(models []string) []string {
	set := make(map[string]bool)
	for _, model := range models {
		set[model] = true
	}
	result := make([]string, 0, len(set))
	for model := range set {
		result = append(result, model)
	}
	sort.Strings(result)
	return result
}

func tierHelper(models []string) string {
	encoded, _ := json.Marshal(sortedModels(models))
	return tierHelperMarker + `function ` + tierHelperName + `(e){let t=e?.serviceTiers??[];if(t.some(e=>e.id==="ultrafast"||e.name?.trim().toLowerCase()==="ultrafast"))return t;return e?.additionalSpeedTiers?.includes("ultrafast")||` + string(encoded) + `.includes(e?.model)?[...t,{id:"ultrafast",name:"Ultrafast",description:""}]:t}`
}

func scanTierRules(entry Entry, data []byte, models []string) (ruleReport, error) {
	var report ruleReport
	original := tierOptions.FindAllSubmatchIndex(data, -1)
	patched := tierOptionsPatched.FindAllSubmatchIndex(data, -1)
	if len(original)+len(patched) > 1 {
		return report, fmt.Errorf("multiple native model-tier option readers in %s", entry.Path)
	}
	var match []int
	value := func(group int) string { return string(data[match[group*2]:match[group*2+1]]) }
	if len(patched) == 1 {
		match = patched[0]
	} else if len(original) == 1 {
		match = original[0]
	}
	if match != nil {
		if value(2) != value(4) || value(2) != value(5) {
			return report, fmt.Errorf("model-tier option variables differ in %s", entry.Path)
		}
		report.tierMenus++
		if len(patched) == 1 {
			report.patched++
		} else {
			replacement := fmt.Sprintf("function %s(%s){return %s(%s?.model,%s(%s))}", value(1), value(2), value(3), value(2), tierHelperName, value(2))
			report.add(entry, data, match[0], match[1], replacement)
		}
	}

	// Locate the tier resolver by its complete native calling convention.
	lookup := regexp.MustCompile(`function (` + identifier + `)\((` + identifier + `),(` + identifier + `)\)\{return (` + identifier + `)==null\?null:(` + identifier + ")===`fast`\\?(" + identifier + `)\((` + identifier + `)\):(` + identifier + `)\?\.serviceTiers\?\.find\((` + identifier + `)=>(` + identifier + `)\.id===(` + identifier + `)\)\?\?null\}`)
	lookupPatched := regexp.MustCompile(`function (` + identifier + `)\((` + identifier + `),(` + identifier + `)\)\{return (` + identifier + `)==null\?null:(` + identifier + ")===`fast`\\?(" + identifier + `)\((` + identifier + `)\):` + tierHelperName + `\((` + identifier + `)\)\?\.find\((` + identifier + `)=>(` + identifier + `)\.id===(` + identifier + `)\)\?\?null\}`)
	ms, ps := lookup.FindAllSubmatchIndex(data, -1), lookupPatched.FindAllSubmatchIndex(data, -1)
	if len(ms)+len(ps) > 1 {
		return report, fmt.Errorf("native model-tier resolver ambiguous in %s", entry.Path)
	}
	match = nil
	if len(ms) == 1 {
		match = ms[0]
	} else if len(ps) == 1 {
		match = ps[0]
	}
	if match != nil {
		if value(2) != value(7) || value(2) != value(8) || value(3) != value(4) || value(3) != value(5) || value(3) != value(11) || value(9) != value(10) {
			return report, fmt.Errorf("native model-tier resolver variables differ in %s", entry.Path)
		}
		report.tierLookups++
		if len(ps) == 1 {
			report.patched++
		} else {
			replacement := fmt.Sprintf("function %s(%s,%s){return %s==null?null:%s===`fast`?%s(%s):%s(%s)?.find(%s=>%s.id===%s)??null}", value(1), value(2), value(3), value(3), value(3), value(6), value(2), tierHelperName, value(2), value(9), value(9), value(3))
			report.add(entry, data, match[0], match[1], replacement)
		}
	}
	if report.tierMenus+report.tierLookups == 0 {
		return report, nil
	}
	// Modules have independent scopes. Newer builds put the menu and resolver
	// in different assets, so each participating module owns its own helper.
	helper := tierHelper(models)
	helperStart := bytes.Index(data, []byte(tierHelperMarker))
	if helperStart >= 0 {
		if bytes.Count(data, []byte(tierHelperMarker)) != 1 {
			return report, fmt.Errorf("native model-tier helper ambiguous in %s", entry.Path)
		}
		functionStart := helperStart + len(tierHelperMarker)
		if !bytes.HasPrefix(data[functionStart:], []byte("function "+tierHelperName+"(")) {
			return report, fmt.Errorf("unsupported model-tier helper declaration in %s", entry.Path)
		}
		end := bytes.Index(data[functionStart+len("function "):], []byte("function "))
		if end < 0 {
			return report, fmt.Errorf("model-tier helper boundary missing in %s", entry.Path)
		}
		helperEnd := functionStart + len("function ") + end
		if bytes.Equal(data[helperStart:helperEnd], []byte(helper)) {
			report.patched++
		} else {
			report.add(entry, data, helperStart, helperEnd, helper)
		}
	} else {
		if bytes.Contains(data, []byte(tierHelperName)) || len(report.edits) == 0 {
			return report, fmt.Errorf("model-tier helper missing or name collision in %s", entry.Path)
		}
		first := 0
		for i := range report.edits {
			if report.edits[i].MatchStart < report.edits[first].MatchStart {
				first = i
			}
		}
		report.edits[first].Replacement = append([]byte(helper), report.edits[first].Replacement...)
	}
	return report, nil
}

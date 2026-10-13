package asar

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
)

const identifier = `[A-Za-z_$][A-Za-z0-9_$]*`

var (
	savedGuard        = regexp.MustCompile(`\{try\{if\((!await (` + identifier + `)\(\)|!1)`)
	savedDefinition   = regexp.MustCompile(`^async function ` + identifier + `\(` + identifier + `,` + identifier + `,` + identifier + `,(` + identifier + `)\)`)
	authDenied        = regexp.MustCompile(`(` + identifier + ")!==`chatgpt`&&(" + identifier + ")!==`personalAccessToken`")
	authDeniedPatched = regexp.MustCompile(`!/\^\(chatgpt\|personalAccessToken\|apikey\)\$/\.test\((` + identifier + `)\)`)
	accessAuth        = regexp.MustCompile(`(` + identifier + ")\\?\\.authMethod===`chatgpt`\\|\\|(" + identifier + ")\\?\\.authMethod===`personalAccessToken`")
	accessAuthPatched = regexp.MustCompile(`/\^\(chatgpt\|personalAccessToken\|apikey\)\$/\.test\((` + identifier + `)\?\.authMethod\)`)
	awaitCall         = regexp.MustCompile(`await (` + identifier + `)\(`)
)

type ruleReport struct {
	ui, readers, policies int
	patched               int
	edits                 []Candidate
}

func (r *ruleReport) add(entry Entry, data []byte, start, end int, replacement string) {
	original := append([]byte(nil), data[start:end]...)
	repl := []byte(replacement)
	if len(repl) < len(original) {
		repl = append(repl, bytes.Repeat([]byte{' '}, len(original)-len(repl))...)
	}
	r.edits = append(r.edits, Candidate{Entry: entry, MatchStart: start, MatchEnd: end, Original: original, Replacement: repl})
}

// These markers are emitted by the packaged desktop functions. Restrict each
// rule to its logged operation; matching a generic authentication check is not
// sufficient evidence to modify it.
func functionRegion(data []byte, marker string) (int, int, error) {
	i := bytes.Index(data, []byte(marker))
	if i < 0 || bytes.Count(data, []byte(marker)) != 1 {
		return 0, 0, fmt.Errorf("non-unique function marker %q", marker)
	}
	start := bytes.LastIndex(data[:i], []byte("async function "))
	if start < 0 {
		return 0, 0, fmt.Errorf("function declaration missing for %q", marker)
	}
	end := nextDeclaration(data, i+len(marker))
	if end-start > 10000 {
		return 0, 0, fmt.Errorf("unsupported function layout for %q", marker)
	}
	return start, end, nil
}

func nextDeclaration(data []byte, start int) int {
	end := len(data)
	for _, delimiter := range []string{"}async function ", "}function ", "}var "} {
		if i := bytes.Index(data[start:], []byte(delimiter)); i >= 0 && start+i+1 < end {
			end = start + i + 1
		}
	}
	return end
}

func scanRules(entry Entry, data []byte) (ruleReport, error) {
	var r ruleReport
	for _, m := range gatePattern.FindAllSubmatchIndex(data, -1) {
		if !bytes.Equal(data[m[6]:m[7]], data[m[8]:m[9]]) {
			continue
		}
		auth, loading, value := string(data[m[2]:m[3]]), string(data[m[4]:m[5]]), string(data[m[6]:m[7]])
		r.ui++
		r.add(entry, data, m[0], m[1], fmt.Sprintf("!%s&&(!%s||!!%s&&%s?.requirements?.featureRequirements?.fast_mode!==!1)", loading, auth, value, value))
	}
	count := len(patchedGatePattern.FindAllIndex(data, -1)) + len(previousPatchedGatePattern.FindAllIndex(data, -1))
	r.ui += count
	r.patched += count

	// New desktop builds return separate fast/ultrafast permissions. Keep that
	// policy conversion intact, but include API-key authentication in its input.
	if i := bytes.Index(data, []byte("serviceTierAccess:")); i >= 0 {
		start := bytes.LastIndex(data[:i], []byte("function "))
		if start >= 0 {
			end := nextDeclaration(data, i)
			region := data[start:end]
			ms := accessAuth.FindAllSubmatchIndex(region, -1)
			ps := accessAuthPatched.FindAllIndex(region, -1)
			if len(ms)+len(ps) > 1 {
				return r, fmt.Errorf("multiple speed-access auth checks in %s", entry.Path)
			}
			if len(ms) == 1 {
				m := ms[0]
				if !bytes.Equal(region[m[2]:m[3]], region[m[4]:m[5]]) {
					return r, fmt.Errorf("speed-access variables differ")
				}
				value := string(region[m[2]:m[3]])
				r.ui++
				r.add(entry, data, start+m[0], start+m[1], "/^(chatgpt|personalAccessToken|apikey)$/.test("+value+"?.authMethod)")
			} else if len(ps) == 1 {
				r.ui++
				r.patched++
			}
		}
	}

	if bytes.Contains(data, []byte("Failed to read saved service tier for request")) {
		start, end, err := functionRegion(data, "Failed to read saved service tier for request")
		if err != nil {
			return r, err
		}
		region := data[start:end]
		if !bytes.Contains(region, []byte("config/read")) || !bytes.Contains(region, []byte("configRequirements/read")) || !bytes.Contains(region, []byte("service_tier")) {
			return r, fmt.Errorf("unsupported saved-tier reader in %s", entry.Path)
		}
		m := savedGuard.FindSubmatchIndex(region)
		if m == nil {
			return r, fmt.Errorf("saved-tier guard missing in %s", entry.Path)
		}
		definition := savedDefinition.FindSubmatch(region)
		if definition == nil || m[4] >= 0 && !bytes.Equal(definition[1], region[m[4]:m[5]]) {
			return r, fmt.Errorf("saved-tier authentication callback differs in %s", entry.Path)
		}
		r.readers++
		if string(region[m[2]:m[3]]) == "!1" {
			r.patched++
		} else {
			r.add(entry, data, start+m[2], start+m[3], "!1")
		}
	}

	if bytes.Contains(data, []byte("Failed to read service tier for request")) {
		start, end, err := functionRegion(data, "Failed to read service tier for request")
		if err != nil {
			return r, err
		}
		region := data[start:end]
		call := awaitCall.FindSubmatch(region)
		if call == nil || !bytes.Contains(region, []byte("service_tier")) {
			return r, fmt.Errorf("request-tier policy call missing in %s", entry.Path)
		}
		declaration := []byte("async function " + string(call[1]) + "(")
		if bytes.Count(data, declaration) != 1 {
			return r, fmt.Errorf("request-tier policy helper is not unique in %s", entry.Path)
		}
		helperStart := bytes.Index(data, declaration)
		helperEnd := nextDeclaration(data, helperStart+len(declaration))
		helper := data[helperStart:helperEnd]
		ms := authDenied.FindAllSubmatchIndex(helper, -1)
		ps := authDeniedPatched.FindAllIndex(helper, -1)
		if len(ms)+len(ps) != 1 {
			return r, fmt.Errorf("unsupported request-tier auth guard in %s", entry.Path)
		}
		r.policies++
		if len(ps) == 1 {
			r.patched++
		} else {
			m := ms[0]
			if !bytes.Equal(helper[m[2]:m[3]], helper[m[4]:m[5]]) {
				return r, fmt.Errorf("request-tier auth variables differ")
			}
			value := string(helper[m[2]:m[3]])
			r.add(entry, data, helperStart+m[0], helperStart+m[1], "!/^(chatgpt|personalAccessToken|apikey)$/.test("+value+")")
		}
	}
	return r, nil
}

func isJavaScript(entry Entry) bool {
	return !entry.Node.Unpacked && entry.Node.Size > 0 && strings.HasSuffix(strings.ToLower(entry.Path), ".js")
}

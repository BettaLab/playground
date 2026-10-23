package playground

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func ruleTestState(capacity, maxLength int) *State {
	return &State{
		nextID:            100,
		config:            &PlaygroundConfig{FilteredStreamRules: &FilteredStreamRulesConfig{Capacity: capacity, MaxExpressionLength: maxLength}},
		searchStreamRules: make(map[string]*SearchStreamRule),
		users:             make(map[string]*User),
	}
}

func mutateRules(t *testing.T, state *State, target, body string) (map[string]interface{}, int) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, target, bytes.NewBufferString(body))
	data, status := handleSearchStreamRuleMutation(req, state)
	var response map[string]interface{}
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatalf("decode response: %v (%s)", err, data)
	}
	return response, status
}

func responseSummary(t *testing.T, response map[string]interface{}) map[string]interface{} {
	t.Helper()
	return response["meta"].(map[string]interface{})["summary"].(map[string]interface{})
}

func TestFilteredStreamRuleConfigDefaultsAndValidation(t *testing.T) {
	defaults := (*PlaygroundConfig)(nil).GetFilteredStreamRulesConfig()
	if defaults.Capacity != 1000 || defaults.MaxExpressionLength != 1024 {
		t.Fatalf("unexpected defaults: %+v", defaults)
	}
	if err := validateConfig(&PlaygroundConfig{FilteredStreamRules: &FilteredStreamRulesConfig{Capacity: 0, MaxExpressionLength: 10}}); err == nil {
		t.Fatal("zero capacity was accepted")
	}
	if err := validateConfig(&PlaygroundConfig{FilteredStreamRules: &FilteredStreamRulesConfig{Capacity: 10, MaxExpressionLength: 0}}); err == nil {
		t.Fatal("zero expression length was accepted")
	}
}

func TestSearchStreamRuleLifecycleAndDeterministicReads(t *testing.T) {
	state := ruleTestState(2, 1024)

	response, status := mutateRules(t, state, "/2/tweets/search/stream/rules", `{"add":[{"value":"from:jmilei -is:retweet","tag":"account"},{"value":"lang:es","tag":"language"}]}`)
	if status != http.StatusOK || responseSummary(t, response)["created"] != float64(2) {
		t.Fatalf("unexpected add response: status=%d response=%v", status, response)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/2/tweets/search/stream/rules", nil)
	data, getStatus := handleStatefulOperation(nil, "/2/tweets/search/stream/rules", http.MethodGet, getReq, state, nil, nil, nil)
	if getStatus != http.StatusOK {
		t.Fatalf("GET status: %d", getStatus)
	}
	var getResponse struct {
		Data []SearchStreamRule `json:"data"`
		Meta struct {
			ResultCount int `json:"result_count"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(data, &getResponse); err != nil {
		t.Fatal(err)
	}
	if getResponse.Meta.ResultCount != 2 || getResponse.Data[0].ID != "100" || getResponse.Data[1].ID != "101" {
		t.Fatalf("unexpected deterministic GET: %+v", getResponse)
	}

	response, _ = mutateRules(t, state, "/2/tweets/search/stream/rules", `{"delete":{"ids":["100"]}}`)
	if responseSummary(t, response)["deleted"] != float64(1) {
		t.Fatalf("unexpected delete response: %v", response)
	}
	rules := state.GetSearchStreamRules()
	if len(rules) != 1 || rules[0].Value != "lang:es" {
		t.Fatalf("targeted delete removed unrelated rule: %+v", rules)
	}

	response, _ = mutateRules(t, state, "/2/tweets/search/stream/rules", `{"delete":{"values":["lang:es"]}}`)
	if responseSummary(t, response)["deleted"] != float64(1) || len(state.GetSearchStreamRules()) != 0 {
		t.Fatalf("delete by value failed: %v", response)
	}
}

func TestSearchStreamRuleDryRunDoesNotMutateOrConsumeIDs(t *testing.T) {
	state := ruleTestState(2, 1024)
	response, status := mutateRules(t, state, "/2/tweets/search/stream/rules?dry_run=true", `{"add":[{"value":"from:example","tag":"preview"}]}`)
	if status != http.StatusOK || len(state.GetSearchStreamRules()) != 0 || state.nextID != 100 {
		t.Fatalf("add dry run mutated state: status=%d nextID=%d response=%v", status, state.nextID, response)
	}
	if response["meta"].(map[string]interface{})["dry_run"] != true {
		t.Fatalf("dry run not identified: %v", response)
	}

	mutateRules(t, state, "/2/tweets/search/stream/rules", `{"add":[{"value":"from:example"}]}`)
	response, _ = mutateRules(t, state, "/2/tweets/search/stream/rules?dry_run=true", `{"delete":{"ids":["100"]}}`)
	if len(state.GetSearchStreamRules()) != 1 || responseSummary(t, response)["deleted"] != float64(1) {
		t.Fatalf("delete dry run mutated or misreported: %v", response)
	}
	mutateRules(t, state, "/2/tweets/search/stream/rules?delete_all=true&dry_run=true", ``)
	if len(state.GetSearchStreamRules()) != 1 {
		t.Fatal("delete_all dry run mutated state")
	}
	mutateRules(t, state, "/2/tweets/search/stream/rules?delete_all=true", ``)
	if len(state.GetSearchStreamRules()) != 0 {
		t.Fatal("delete_all did not clear state")
	}
}

func TestSearchStreamRuleValidationDuplicatesAndCapacity(t *testing.T) {
	state := ruleTestState(2, 20)
	response, status := mutateRules(t, state, "/2/tweets/search/stream/rules", `{"add":[]}`)
	if status != http.StatusBadRequest {
		t.Fatalf("empty add status=%d response=%v", status, response)
	}
	_, status = mutateRules(t, state, "/2/tweets/search/stream/rules", `{"add":[{"value":"from:"}]}`)
	if status != http.StatusOK || len(state.GetSearchStreamRules()) != 0 {
		t.Fatal("invalid expression was applied")
	}
	_, status = mutateRules(t, state, "/2/tweets/search/stream/rules", `{"add":[{"value":"from:one","tag":" "}]}`)
	if status != http.StatusOK || len(state.GetSearchStreamRules()) != 0 {
		t.Fatal("invalid tag was applied")
	}
	mutateRules(t, state, "/2/tweets/search/stream/rules", `{"add":[{"value":"from:one"},{"value":"from:two"}]}`)
	response, _ = mutateRules(t, state, "/2/tweets/search/stream/rules", `{"add":[{"value":"from:one"},{"value":"from:three"}]}`)
	if responseSummary(t, response)["not_created"] != float64(2) || len(state.GetSearchStreamRules()) != 2 {
		t.Fatalf("duplicate/capacity handling failed: %v", response)
	}
	mutateRules(t, state, "/2/tweets/search/stream/rules", `{"delete":{"values":["from:two"]}}`)
	mutateRules(t, state, "/2/tweets/search/stream/rules", `{"add":[{"value":"from:three"}]}`)
	if len(state.GetSearchStreamRules()) != 2 {
		t.Fatal("deletion did not free capacity")
	}

	unicodeState := ruleTestState(2, 3)
	response, _ = mutateRules(t, unicodeState, "/2/tweets/search/stream/rules", `{"add":[{"value":"ééé"}]}`)
	if responseSummary(t, response)["created"] != float64(1) {
		t.Fatalf("limit did not count Unicode characters: %v", response)
	}
}

func TestSearchStreamRuleCapacityIsAtomicUnderConcurrency(t *testing.T) {
	state := ruleTestState(2, 1024)
	var wait sync.WaitGroup
	for _, value := range []string{"from:one", "from:two", "from:three", "from:four"} {
		wait.Add(1)
		go func(value string) {
			defer wait.Done()
			state.AddSearchStreamRules([]SearchStreamRuleAddition{{Value: value}}, false)
		}(value)
	}
	wait.Wait()
	if len(state.GetSearchStreamRules()) != 2 {
		t.Fatalf("concurrent additions exceeded capacity: %d", len(state.GetSearchStreamRules()))
	}
}

func TestSearchStreamRuleMalformedAndMissingDeletes(t *testing.T) {
	state := ruleTestState(2, 1024)
	cases := []string{
		`not-json`,
		`{"add":[{"value":"from:one"}],"delete":{"ids":["1"]}}`,
		`{"delete":{}}`,
		`{"delete":{"ids":[]}}`,
		`{"unknown":true}`,
	}
	for _, body := range cases {
		if _, status := mutateRules(t, state, "/2/tweets/search/stream/rules", body); status != http.StatusBadRequest {
			t.Fatalf("body %q returned status %d", body, status)
		}
	}
	response, status := mutateRules(t, state, "/2/tweets/search/stream/rules", `{"delete":{"ids":["abc","999"]}}`)
	if status != http.StatusOK || responseSummary(t, response)["not_deleted"] != float64(2) {
		t.Fatalf("missing/invalid deletion not reported: %v", response)
	}
	if len(state.GetSearchStreamRules()) != 0 {
		t.Fatal("malformed operations mutated state")
	}
}

func TestSearchStreamRuleCountsUseConfiguredLimit(t *testing.T) {
	state := ruleTestState(7, 1024)
	mutateRules(t, state, "/2/tweets/search/stream/rules", `{"add":[{"value":"from:one"}]}`)
	req := httptest.NewRequest(http.MethodGet, "/2/tweets/search/stream/rules/counts", nil)
	data, status := handleStatefulOperation(nil, "/2/tweets/search/stream/rules/counts", http.MethodGet, req, state, nil, nil, nil)
	if status != http.StatusOK {
		t.Fatal(status)
	}
	var response struct {
		Data struct {
			CapPerClientApp     string `json:"cap_per_client_app"`
			CapPerProject       string `json:"cap_per_project"`
			ProjectRuleCount    string `json:"project_rules_count"`
			ClientAppRulesCount struct {
				RuleCount int `json:"rule_count"`
			} `json:"client_app_rules_count"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatal(err)
	}
	if response.Data.CapPerClientApp != "7" || response.Data.CapPerProject != "7" || response.Data.ProjectRuleCount != "1" || response.Data.ClientAppRulesCount.RuleCount != 1 {
		t.Fatalf("unexpected counts response: %+v", response)
	}
}

func TestSearchStreamRulesSurviveStateExport(t *testing.T) {
	state := ruleTestState(2, 1024)
	mutateRules(t, state, "/2/tweets/search/stream/rules", `{"add":[{"value":"from:example","tag":"persisted"}]}`)
	recorder := httptest.NewRecorder()
	HandleStateExport(state).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/state/export", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"search_stream_rules"`) || !strings.Contains(recorder.Body.String(), `"persisted"`) {
		t.Fatalf("rule missing from export: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var exported StateExport
	if err := json.Unmarshal(recorder.Body.Bytes(), &exported); err != nil {
		t.Fatal(err)
	}
	restored := ruleTestState(2, 1024)
	ImportStateFromFile(restored, &exported)
	rules := restored.GetSearchStreamRules()
	if len(rules) != 1 || rules[0].Value != "from:example" || rules[0].Tag != "persisted" {
		t.Fatalf("rule did not survive export/import: %+v", rules)
	}
}

func TestRuleChangeAffectsAccountRetweetMatching(t *testing.T) {
	state := ruleTestState(2, 1024)
	state.users["42"] = &User{ID: "42", Username: "jmilei"}
	original := &Tweet{ID: "1", AuthorID: "42"}
	retweet := &Tweet{ID: "2", AuthorID: "42", ReferencedTweets: []ReferencedTweet{{Type: "retweeted", ID: "9"}}}

	mutateRules(t, state, "/2/tweets/search/stream/rules", `{"add":[{"value":"from:jmilei -is:retweet","tag":"account"}]}`)
	matcher := NewRuleMatcher(state.GetSearchStreamRules())
	if !matcher.MatchTweet(original, state) || matcher.MatchTweet(retweet, state) {
		t.Fatal("account/repost rule matching did not reflect supported expression")
	}
	mutateRules(t, state, "/2/tweets/search/stream/rules", `{"delete":{"values":["from:jmilei -is:retweet"]}}`)
	if NewRuleMatcher(state.GetSearchStreamRules()).MatchTweet(original, state) {
		t.Fatal("deleted rule still matched later posts")
	}
}

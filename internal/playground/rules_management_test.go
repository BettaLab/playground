package playground

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
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

func listRules(t *testing.T, state *State, target string) (map[string]interface{}, int) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	data, status := handleSearchStreamRuleList(req, state)
	var response map[string]interface{}
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatalf("decode list response: %v (%s)", err, data)
	}
	return response, status
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

func ruleRoutingHandler(state *State) http.Handler {
	query := func(name, valueType string) Parameter {
		return Parameter{Name: name, In: "query", Schema: map[string]interface{}{"type": valueType}}
	}
	spec := &OpenAPISpec{Paths: map[string]PathItem{
		"/2/tweets/search/stream/rules": {
			Get: &Operation{OperationID: "getRules", Parameters: []Parameter{
				query("ids", "string"), query("max_results", "integer"), query("pagination_token", "string"),
			}, Responses: map[string]Response{}},
			Post: &Operation{OperationID: "updateRules", Parameters: []Parameter{
				query("dry_run", "boolean"), query("delete_all", "boolean"),
			}, Responses: map[string]Response{}},
		},
		"/2/tweets/search/stream/rules/counts": {
			Get: &Operation{OperationID: "getRuleCounts", Responses: map[string]Response{}},
		},
	}}
	return createUnifiedOpenAPIHandler(spec, state, NewExampleStore(), nil)
}

func TestSearchStreamRuleHTTPRoutingContractFields(t *testing.T) {
	state := ruleTestState(10, 1024)
	state.config.Auth = &AuthConfig{DisableValidation: true}
	handler := ruleRoutingHandler(state)

	post := httptest.NewRecorder()
	handler.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/2/tweets/search/stream/rules?dry_run=true", strings.NewReader(`{"add":[{"value":"from:preview"}]}`)))
	if post.Code != http.StatusOK {
		t.Fatalf("POST route status=%d body=%s", post.Code, post.Body.String())
	}
	var mutation map[string]interface{}
	if err := json.Unmarshal(post.Body.Bytes(), &mutation); err != nil {
		t.Fatal(err)
	}
	meta := mutation["meta"].(map[string]interface{})
	if _, exists := meta["dry_run"]; exists {
		t.Fatalf("POST exposed custom meta.dry_run: %v", mutation)
	}
	if len(meta) != 2 || mutation["data"] == nil {
		t.Fatalf("unexpected documented mutation field set: %v", mutation)
	}

	state.AddSearchStreamRules([]SearchStreamRuleAddition{{Value: "from:one"}, {Value: "from:two"}}, false)
	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/2/tweets/search/stream/rules?ids=100&max_results=1", nil))
	if get.Code != http.StatusOK {
		t.Fatalf("GET route status=%d body=%s", get.Code, get.Body.String())
	}
	var listing map[string]interface{}
	if err := json.Unmarshal(get.Body.Bytes(), &listing); err != nil {
		t.Fatal(err)
	}
	if strings.Join(listedRuleIDs(t, listing), ",") != "100" || len(listing) != 2 {
		t.Fatalf("GET route ignored filter or exposed custom fields: %v", listing)
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

func listedRuleIDs(t *testing.T, response map[string]interface{}) []string {
	t.Helper()
	items := response["data"].([]interface{})
	ids := make([]string, 0, len(items))
	for _, raw := range items {
		ids = append(ids, raw.(map[string]interface{})["id"].(string))
	}
	return ids
}

func TestSearchStreamRuleListingFiltersAndPaginatesWithoutDuplicates(t *testing.T) {
	state := ruleTestState(10, 1024)
	for _, value := range []string{"from:one", "from:two", "from:three", "from:four", "from:five"} {
		state.AddSearchStreamRules([]SearchStreamRuleAddition{{Value: value}}, false)
	}

	seen := map[string]bool{}
	target := "/2/tweets/search/stream/rules?max_results=2"
	for {
		response, status := listRules(t, state, target)
		if status != http.StatusOK {
			t.Fatalf("page status=%d response=%v", status, response)
		}
		for _, id := range listedRuleIDs(t, response) {
			if seen[id] {
				t.Fatalf("rule %s appeared on multiple pages", id)
			}
			seen[id] = true
		}
		meta := response["meta"].(map[string]interface{})
		next, exists := meta["next_token"]
		if !exists {
			break
		}
		token := next.(string)
		if len(token) != 16 {
			t.Fatalf("cursor does not have documented surface: %q", token)
		}
		target = "/2/tweets/search/stream/rules?max_results=2&pagination_token=" + token
	}
	if len(seen) != 5 {
		t.Fatalf("pagination omitted rules: %v", seen)
	}

	response, status := listRules(t, state, "/2/tweets/search/stream/rules?ids=104,100,999&max_results=1")
	if status != http.StatusOK || strings.Join(listedRuleIDs(t, response), ",") != "100" {
		t.Fatalf("filtered first page failed: status=%d response=%v", status, response)
	}
	if len(response["errors"].([]interface{})) != 1 {
		t.Fatalf("missing filtered ID was not reported: %v", response)
	}
	token := response["meta"].(map[string]interface{})["next_token"].(string)
	response, status = listRules(t, state, "/2/tweets/search/stream/rules?ids=104,100,999&max_results=1&pagination_token="+token)
	if status != http.StatusOK || strings.Join(listedRuleIDs(t, response), ",") != "104" {
		t.Fatalf("filtered final page failed: status=%d response=%v", status, response)
	}
	if _, exists := response["meta"].(map[string]interface{})["next_token"]; exists {
		t.Fatalf("final page exposed next_token: %v", response)
	}
}

func TestSearchStreamRuleListingValidatesParametersAndStaleCursors(t *testing.T) {
	state := ruleTestState(10, 1024)
	state.AddSearchStreamRules([]SearchStreamRuleAddition{{Value: "from:one"}, {Value: "from:two"}}, false)

	invalidTargets := []string{
		"/2/tweets/search/stream/rules?ids=",
		"/2/tweets/search/stream/rules?ids=abc",
		"/2/tweets/search/stream/rules?ids=12345678901234567890",
		"/2/tweets/search/stream/rules?max_results=0",
		"/2/tweets/search/stream/rules?max_results=1001",
		"/2/tweets/search/stream/rules?max_results=not-a-number",
		"/2/tweets/search/stream/rules?pagination_token=bad",
		"/2/tweets/search/stream/rules?pagination_token=",
		"/2/tweets/search/stream/rules?pagination_token=0000000000000000&pagination_token=1111111111111111",
	}
	tooManyIDs := make([]string, 1001)
	for index := range tooManyIDs {
		tooManyIDs[index] = strconv.Itoa(index + 1)
	}
	invalidTargets = append(invalidTargets,
		"/2/tweets/search/stream/rules?ids=1&ids=2",
		"/2/tweets/search/stream/rules?ids="+strings.Join(tooManyIDs, ","),
	)
	for _, target := range invalidTargets {
		response, status := listRules(t, state, target)
		if status != http.StatusBadRequest || response["type"] != "https://api.x.com/2/problems/invalid-request" {
			t.Fatalf("target %q status=%d response=%v", target, status, response)
		}
	}

	first, _ := listRules(t, state, "/2/tweets/search/stream/rules?max_results=1")
	token := first["meta"].(map[string]interface{})["next_token"].(string)
	state.AddSearchStreamRules([]SearchStreamRuleAddition{{Value: "from:three"}}, false)
	response, status := listRules(t, state, "/2/tweets/search/stream/rules?max_results=1&pagination_token="+token)
	if status != http.StatusBadRequest || response["detail"] != "is invalid or expired" {
		t.Fatalf("stale cursor accepted: status=%d response=%v", status, response)
	}
}

func TestSearchStreamRuleEmptyListingAndDefaultLimit(t *testing.T) {
	empty := ruleTestState(1001, 1024)
	response, status := listRules(t, empty, "/2/tweets/search/stream/rules")
	if status != http.StatusOK || len(listedRuleIDs(t, response)) != 0 {
		t.Fatalf("empty listing failed: status=%d response=%v", status, response)
	}
	meta := response["meta"].(map[string]interface{})
	if meta["result_count"] != float64(0) || len(meta) != 1 {
		t.Fatalf("unexpected empty metadata: %v", meta)
	}

	state := ruleTestState(1001, 1024)
	for index := 0; index < 1001; index++ {
		state.AddSearchStreamRules([]SearchStreamRuleAddition{{Value: fmt.Sprintf("from:user%d", index)}}, false)
	}
	response, status = listRules(t, state, "/2/tweets/search/stream/rules")
	if status != http.StatusOK || len(listedRuleIDs(t, response)) != 1000 {
		t.Fatalf("default page size failed: status=%d count=%d", status, len(listedRuleIDs(t, response)))
	}
	if _, exists := response["meta"].(map[string]interface{})["next_token"]; !exists {
		t.Fatal("default first page omitted next_token")
	}
}

func TestSearchStreamRuleDryRunDoesNotMutateOrConsumeIDs(t *testing.T) {
	state := ruleTestState(2, 1024)
	response, status := mutateRules(t, state, "/2/tweets/search/stream/rules?dry_run=true", `{"add":[{"value":"from:example","tag":"preview"}]}`)
	if status != http.StatusOK || len(state.GetSearchStreamRules()) != 0 || state.nextID != 100 {
		t.Fatalf("add dry run mutated state: status=%d nextID=%d response=%v", status, state.nextID, response)
	}
	if _, exists := response["meta"].(map[string]interface{})["dry_run"]; exists {
		t.Fatalf("dry run leaked a simulator-only response field: %v", response)
	}
	response, status = mutateRules(t, state, "/2/tweets/search/stream/rules?dry_run=true", `{"add":[{"value":"from:example","tag":"preview"}]}`)
	count, _, _ := state.SearchStreamRuleLimits()
	if status != http.StatusOK || count != 0 || state.nextID != 100 || responseSummary(t, response)["created"] != float64(1) {
		t.Fatalf("repeated dry run was not repeatable: status=%d nextID=%d response=%v", status, state.nextID, response)
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

func TestSearchStreamRuleExpressionValidationBoundary(t *testing.T) {
	for _, value := range []string{
		"from:jmilei -is:retweet",
		"from:jmilei (is:reply OR is:quote)",
		"from:jmilei lang:es has:media",
		`"quoted native phrase" OR from:jmilei`,
	} {
		if message := validateRuleValue(value, 1024); message != "" {
			t.Fatalf("supported expression %q rejected: %s", value, message)
		}
	}
	for _, value := range []string{"from:", "()", "OR from:jmilei", "from:jmilei OR", "from:jmilei (is:reply OR )", `"unterminated`} {
		if message := validateRuleValue(value, 1024); message == "" {
			t.Fatalf("obviously invalid expression %q accepted", value)
		}
	}
	// Unknown native operators remain outside the simulator's validation claim.
	if message := validateRuleValue("future_operator:value", 1024); message != "" {
		t.Fatalf("unknown native operator was over-restricted: %s", message)
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
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	countFields := raw["data"].(map[string]interface{})
	if len(raw) != 1 || len(countFields) != 4 {
		t.Fatalf("counts exposed unsupported fields: %v", raw)
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
